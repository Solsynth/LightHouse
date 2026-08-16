package push

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sideshow/apns2"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/metoer/internal/events"
	"src.solsynth.dev/sosys/metoer/internal/grpcclient"
	"src.solsynth.dev/sosys/metoer/internal/model"
	"src.solsynth.dev/sosys/metoer/internal/observability"
	"src.solsynth.dev/sosys/metoer/internal/store"
)

// Invalid FCM/APNs error strings (case-insensitive), mirroring the C#
// InvalidFcmErrors / InvalidApnsErrors sets.
var invalidFcmErrors = newSet(strings.ToLower("InvalidRegistration"), "notregistered", "registration-token-not-registered", "unregistered")
var invalidApnsErrors = newSet(strings.ToLower("BadDeviceToken"), "unregistered")

func newSet(items ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(items))
	for _, item := range items {
		out[item] = struct{}{}
	}
	return out
}

// Enqueuer enqueues a push notification to the pusher_queue (wired to
// queue.Service.EnqueuePushNotification; kept as a func to avoid an import
// cycle queue ↔ push).
type Enqueuer func(ctx context.Context, notification *model.Notification, userID uuid.UUID, excludedWebSocketDeviceIDs []string, isSavable bool) error

// Service is the push delivery engine (PushService).
type Service struct {
	apps          *PushService
	st            *store.Store
	enqueuer      Enqueuer
	streams       *SopStreams
	replay        *SopNotificationReplayBuffer
	ws            *events.WebSocketService
	obs           *observability.Service
	logs          *grpcclient.ActionLogService
	prefs         *preferences
	httpClient    *http.Client
	removalBuffer *FlushBuffer[PushSubRemovalRequest]
	log           *slog.Logger
}

// preferences is a thin wrapper for the preference store calls the service
// needs (keeps the constructor small).
type preferences struct {
	st *store.Store
}

// Get loads the account's preference level for a topic (default Normal).
func (p *preferences) Get(ctx context.Context, accountID uuid.UUID, topic string) (model.NotificationPreferenceLevel, error) {
	pref, err := p.st.GetPreference(ctx, accountID, topic)
	if err != nil {
		return model.NotificationPreferenceNormal, err
	}
	if pref == nil {
		return model.NotificationPreferenceNormal, nil
	}
	return pref.Preference, nil
}

// GetMany loads preference levels for a set of accounts and one topic
// (missing → Normal).
func (p *preferences) GetMany(ctx context.Context, accounts []uuid.UUID, topic string) (map[uuid.UUID]model.NotificationPreferenceLevel, error) {
	return p.st.GetPreferencesByTopics(ctx, accounts, topic)
}

// New builds the push service. enqueuer may be nil (queue disabled).
func New(
	apps *PushService,
	st *store.Store,
	enqueuer Enqueuer,
	streams *SopStreams,
	replay *SopNotificationReplayBuffer,
	ws *events.WebSocketService,
	obs *observability.Service,
	logs *grpcclient.ActionLogService,
	httpClient *http.Client,
	log *slog.Logger,
) *Service {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Service{
		apps:          apps,
		st:            st,
		enqueuer:      enqueuer,
		streams:       streams,
		replay:        replay,
		ws:            ws,
		obs:           obs,
		logs:          logs,
		prefs:         &preferences{st: st},
		httpClient:    httpClient,
		removalBuffer: NewFlushBuffer[PushSubRemovalRequest](),
		log:           log,
	}
}

// FlushRemovalBuffer drains the invalid-token removal buffer
// (PushSubFlushHandler), deleting the queued subscriptions.
func (s *Service) FlushRemovalBuffer(ctx context.Context) error {
	return s.removalBuffer.Flush(ctx, func(ctx context.Context, items []PushSubRemovalRequest) error {
		seen := map[uuid.UUID]struct{}{}
		var ids []uuid.UUID
		for _, item := range items {
			if _, ok := seen[item.SubId]; ok {
				continue
			}
			seen[item.SubId] = struct{}{}
			ids = append(ids, item.SubId)
		}
		count, err := s.st.DeleteSubscriptionsByIds(ctx, ids)
		if err != nil {
			return err
		}
		s.log.Info("removed invalid push notification tokens", "count", count)
		return nil
	})
}

// SetEnqueuer wires the queue publisher after construction (main wires
// queue.Service once both exist).
func (s *Service) SetEnqueuer(enqueuer Enqueuer) { s.enqueuer = enqueuer }

// ResolveAppId mirrors PushService.ResolveAppId.
func (s *Service) ResolveAppId(appId string, useDefaultIfMissing bool) string {
	return s.apps.ResolveAppId(appId, useDefaultIfMissing)
}

// GetDefaultAppId mirrors PushService.GetDefaultAppId.
func (s *Service) GetDefaultAppId() string { return s.apps.GetDefaultAppId() }

// UnsubscribeDevice mirrors PushService.UnsubscribeDevice.
func (s *Service) UnsubscribeDevice(ctx context.Context, deviceId string) error {
	return s.st.DeleteSubscriptionsByDevice(ctx, deviceId)
}

// SubscribeDevice mirrors PushService.SubscribeDevice.
func (s *Service) SubscribeDevice(ctx context.Context, deviceId, deviceToken string, deviceName *string, provider model.PushProvider, account *gen.DyAccount, isActivated bool, appId string) (*model.PushSubscription, error) {
	now := time.Now().UTC()
	accountID, err := uuid.Parse(account.Id)
	if err != nil {
		return nil, fmt.Errorf("parse account id: %w", err)
	}
	appId = s.ResolveAppId(appId, true)
	if err := s.validateAppId(appId); err != nil {
		return nil, err
	}

	if isActivated {
		if err := s.st.DeactivateSubscriptions(ctx, accountID, deviceId, provider, now); err != nil {
			return nil, err
		}
	}

	existing, err := s.st.GetExistingSubscription(ctx, accountID, deviceId, provider)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if existing != nil {
		existing.DeviceId = deviceId
		existing.DeviceToken = deviceToken
		existing.Provider = provider
		existing.IsActivated = isActivated
		existing.LastUsedAt = model.TimePtr(now)
		existing.UpdatedAt = model.NowTime()
		existing.DeviceName = deviceName
		existing.AppId = &appId
		if err := s.st.UpdateSubscription(ctx, existing); err != nil {
			return nil, err
		}
		return existing, nil
	}

	subscription := &model.PushSubscription{
		ModelBase:   model.ModelBase{Id: uuid.New(), CreatedAt: model.NowTime(), UpdatedAt: model.NowTime()},
		DeviceId:    deviceId,
		DeviceToken: deviceToken,
		Provider:    provider,
		IsActivated: isActivated,
		AccountId:   accountID,
		AppId:       &appId,
		DeviceName:  deviceName,
		LastUsedAt:  model.TimePtr(now),
	}
	if err := s.st.InsertSubscription(ctx, subscription); err != nil {
		return nil, err
	}

	existingCount, err := s.st.CountSubscriptions(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if existingCount <= 1 {
		s.logs.CreateActionLog(accountID.String(), "accounts.push.enable", map[string]string{
			"provider": strings.ToLower(provider.String()),
		})
	}

	return subscription, nil
}

// RegisterSopToken mirrors PushService.RegisterSopToken.
func (s *Service) RegisterSopToken(ctx context.Context, deviceId string, deviceName *string, account *gen.DyAccount, appId string) (string, *model.PushSubscription, error) {
	token := fmt.Sprintf("%s%s", newSopToken(), newSopToken())
	subscription, err := s.SubscribeDevice(ctx, deviceId, token, deviceName, model.PushProviderSop, account, true, appId)
	if err != nil {
		return "", nil, err
	}
	return token, subscription, nil
}

// newSopToken mirrors the C# token builder: two hex-encoded GUID byte
// arrays, lowercase, concatenated (64 hex chars).
func newSopToken() string {
	bytes := uuid.New()
	return strings.ToLower(strings.ReplaceAll(bytes.String(), "-", ""))
}

// GetSopSubscriptionByToken mirrors PushService.GetSopSubscriptionByToken.
func (s *Service) GetSopSubscriptionByToken(ctx context.Context, token string) (*model.PushSubscription, error) {
	sub, err := s.st.GetSopSubscriptionByToken(ctx, token)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return sub, nil
}

// GetCurrentDeviceSubscriptions mirrors PushService.GetCurrentDeviceSubscriptions.
func (s *Service) GetCurrentDeviceSubscriptions(ctx context.Context, accountID uuid.UUID, deviceId string) ([]*model.PushSubscription, error) {
	return s.st.GetCurrentDeviceSubscriptions(ctx, accountID, deviceId)
}

// GetCurrentDeviceActiveSubscription mirrors PushService.GetCurrentDeviceActiveSubscription.
func (s *Service) GetCurrentDeviceActiveSubscription(ctx context.Context, accountID uuid.UUID, deviceId string) (*model.PushSubscription, error) {
	subscriptions, err := s.st.GetCurrentDeviceSubscriptions(ctx, accountID, deviceId)
	if err != nil {
		return nil, err
	}
	var best *model.PushSubscription
	for _, sub := range subscriptions {
		if !sub.IsActivated {
			continue
		}
		if best == nil {
			best = sub
			continue
		}
		// OrderByDescending(Provider == Sop).ThenByDescending(UpdatedAt)
		bestSop := best.Provider == model.PushProviderSop
		subSop := sub.Provider == model.PushProviderSop
		if subSop != bestSop {
			if subSop {
				best = sub
			}
			continue
		}
		if sub.UpdatedAt.Time().After(best.UpdatedAt.Time()) {
			best = sub
		}
	}
	return best, nil
}

// SubscribeSopStream mirrors PushService.SubscribeSopStream.
func (s *Service) SubscribeSopStream(accountID uuid.UUID, deviceId string) (uuid.UUID, *SopStream) {
	return s.streams.Subscribe(accountID, deviceId)
}

// UnsubscribeSopStream mirrors PushService.UnsubscribeSopStream.
func (s *Service) UnsubscribeSopStream(accountID, streamID uuid.UUID) {
	s.streams.Unsubscribe(accountID, streamID)
}

// GetConnectedSopWebSocketDeviceIds mirrors
// PushService.GetConnectedSopWebSocketDeviceIds.
func (s *Service) GetConnectedSopWebSocketDeviceIds(accountID uuid.UUID) map[string]struct{} {
	return s.streams.ConnectedDeviceIds(accountID)
}

// ErrEmptyNotification mirrors the C# ArgumentException thrown by
// SendNotification when title/subtitle/content are all null (ASP.NET Core
// gRPC maps it to InvalidArgument).
var ErrEmptyNotification = errors.New("Unable to send notification that is completely empty.")

// ErrUnknownAppId is returned when a send or subscription targets an app id
// that is not configured (the allowlist guard; ResolveAppSenders would
// otherwise silently fall back to the default app's senders).
var ErrUnknownAppId = errors.New("App id is not configured")

// validateAppId enforces the configured-app allowlist: a non-empty app id
// that names no configured app is rejected instead of silently delivering
// through the default app's senders.
func (s *Service) validateAppId(appId string) error {
	if appId != "" && !s.apps.IsAppConfigured(appId) {
		return fmt.Errorf("%w: %s", ErrUnknownAppId, appId)
	}
	return nil
}

// SendNotification mirrors PushService.SendNotification.
func (s *Service) SendNotification(ctx context.Context, accountID uuid.UUID, topic string, title, subtitle, content *string, meta map[string]any, actionUri *string, isSilent, save bool, appId, pushType string) error {
	if meta == nil {
		meta = map[string]any{}
	}
	if title == nil && subtitle == nil && content == nil {
		return ErrEmptyNotification
	}
	if actionUri != nil {
		meta["action_uri"] = *actionUri
	}

	appId = s.ResolveAppId(appId, true)
	if err := s.validateAppId(appId); err != nil {
		return err
	}

	preference, err := s.prefs.Get(ctx, accountID, topic)
	if err != nil {
		return err
	}
	if preference == model.NotificationPreferenceReject {
		return nil
	}

	now := model.NowTime()
	notification := &model.Notification{
		ModelBase: model.ModelBase{Id: uuid.New(), CreatedAt: now, UpdatedAt: now},
		Topic:     topic,
		Title:     title,
		Subtitle:  subtitle,
		Content:   content,
		Meta:      meta,
		Priority:  10,
		AppId:     new(appId),
		PushType:  strPtrOrNil(pushType),
		AccountId: accountID,
	}

	if save {
		if err := s.st.SaveNotification(ctx, notification); err != nil {
			return err
		}
	}

	if !isSilent && preference == model.NotificationPreferenceNormal {
		if s.enqueuer == nil {
			return errors.New("push queue unavailable: enqueuer not configured")
		}
		if err := s.enqueuer(ctx, notification, accountID, nil, save); err != nil {
			return fmt.Errorf("enqueue push notification: %w", err)
		}
	}
	return nil
}

// DeliverPushNotification mirrors PushService.DeliverPushNotification.
func (s *Service) DeliverPushNotification(ctx context.Context, notification *model.Notification, excludedWebSocketDeviceIds []string, isSavable bool) error {
	s.obs.RecordNotificationSend(ctx, notification, "queue")
	connectedSopDeviceIds := s.GetConnectedSopWebSocketDeviceIds(notification.AccountId)

	appID := s.ResolveAppId(notification.AppIdValue(), true)
	defaultApp := s.GetDefaultAppId()
	subscriptions, err := s.st.ListActivatedSubscriptions(ctx, notification.AccountId, &appID, new(defaultApp))
	if err != nil {
		return err
	}

	if ShouldQueueSopReplay(isSavable, subscriptions) {
		if err := s.replay.AppendNotification(ctx, notification); err != nil {
			s.log.Warn("failed to append SOP replay notification", "notification_id", notification.Id, "error", err)
		}
	}

	s.streams.Broadcast(notification.AccountId, notification)

	s.log.Info("delivering push notification", "topic", notification.Topic, "meta", notification.Meta)

	if len(subscriptions) == 0 {
		s.log.Info("no push subscriptions found for account", "account_id", notification.AccountId)
		return s.SendWebSocketNotification(ctx, notification, excludedWebSocketDeviceIds)
	}

	connectedWebSocketDeviceIds, err := s.getConnectedWebsocketDeviceIds(ctx, subscriptions)
	if err != nil {
		s.log.Warn("unable to get websocket connection status for devices", "error", err)
		connectedWebSocketDeviceIds = map[string]struct{}{}
	}
	s.recordSkippedWebSocketDeliveries(ctx, notification, subscriptions, connectedWebSocketDeviceIds)

	var remaining []*model.PushSubscription
	for _, sub := range subscriptions {
		if _, ok := connectedWebSocketDeviceIds[NormalizeSopDeviceId(sub.DeviceId)]; !ok {
			remaining = append(remaining, sub)
		}
	}
	subscriptionByDevice := s.selectSubscriptionsByDevice(remaining, connectedSopDeviceIds, notification)

	s.recordSopDeliveryHolds(ctx, notification, subscriptions, connectedWebSocketDeviceIds)

	websocketExclusions := buildWebSocketExclusions(connectedSopDeviceIds, excludedWebSocketDeviceIds)
	if err := s.SendWebSocketNotification(ctx, notification, websocketExclusions); err != nil {
		return err
	}

	var wg sync.WaitGroup
	deliveryErrs := make(chan error, len(subscriptionByDevice))
	for _, sub := range subscriptionByDevice {
		wg.Add(1)
		go func(sub *model.PushSubscription) {
			defer wg.Done()
			if err := s.SendPushNotification(ctx, sub, notification); err != nil {
				deliveryErrs <- fmt.Errorf("device %s: %w", sub.DeviceId, err)
			}
		}(sub)
	}
	wg.Wait()
	close(deliveryErrs)
	var errs []error
	for err := range deliveryErrs {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// MarkNotificationsViewed mirrors PushService.MarkNotificationsViewed.
func (s *Service) MarkNotificationsViewed(ctx context.Context, notifications []*model.Notification, now time.Time) error {
	var ids []uuid.UUID
	for _, n := range notifications {
		if n.ViewedAt == nil {
			ids = append(ids, n.Id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return s.st.MarkNotificationsViewed(ctx, ids, now)
}

// MarkAllNotificationsViewed mirrors PushService.MarkAllNotificationsViewed.
func (s *Service) MarkAllNotificationsViewed(ctx context.Context, accountID uuid.UUID, appId string) error {
	resolved := s.ResolveAppId(appId, true)
	defaultApp := s.GetDefaultAppId()
	return s.st.MarkAllNotificationsViewed(ctx, accountID, &resolved, new(defaultApp), time.Now().UTC())
}

// SendNotificationBatch mirrors PushService.SendNotificationBatch.
func (s *Service) SendNotificationBatch(ctx context.Context, notification *model.Notification, accounts []uuid.UUID, save bool, excludedWebSocketDeviceIds []string) error {
	appID := s.ResolveAppId(notification.AppIdValue(), true)
	if err := s.validateAppId(appID); err != nil {
		return err
	}
	defaultApp := s.GetDefaultAppId()

	preferences, err := s.prefs.GetMany(ctx, accounts, notification.Topic)
	if err != nil {
		return err
	}
	var recipients []uuid.UUID
	for _, account := range accounts {
		if preferences[account] != model.NotificationPreferenceReject {
			recipients = append(recipients, account)
		}
	}

	if save {
		now := model.NowTime()
		var rows []*model.Notification
		for _, accountID := range recipients {
			rows = append(rows, &model.Notification{
				ModelBase: model.ModelBase{Id: uuid.New(), CreatedAt: now, UpdatedAt: now},
				Topic:     notification.Topic,
				Title:     notification.Title,
				Subtitle:  notification.Subtitle,
				Content:   notification.Content,
				Meta:      notification.Meta,
				Priority:  notification.Priority,
				AccountId: accountID,
				AppId:     notification.AppId,
				PushType:  notification.PushType,
			})
		}
		if len(rows) != 0 {
			if err := s.st.BatchInsertNotifications(ctx, rows); err != nil {
				return err
			}
		}
	}

	s.log.Info("delivering notification in batch", "topic", notification.Topic, "notification_id", notification.Id, "meta", notification.Meta)

	var deliveryErrors []error
	for _, account := range recipients {
		notification.AccountId = account
		if preferences[account] == model.NotificationPreferenceSilent {
			continue
		}

		s.obs.RecordNotificationSend(ctx, notification, "batch")
		connectedSopDeviceIds := s.GetConnectedSopWebSocketDeviceIds(notification.AccountId)
		subscriptions, err := s.st.ListActivatedSubscriptions(ctx, account, &appID, new(defaultApp))
		if err != nil {
			return err
		}

		if ShouldQueueSopReplay(save, subscriptions) {
			if err := s.replay.AppendNotification(ctx, notification); err != nil {
				s.log.Warn("failed to append SOP replay notification", "notification_id", notification.Id, "error", err)
			}
		}

		s.streams.Broadcast(notification.AccountId, notification)

		if len(subscriptions) == 0 {
			if err := s.SendWebSocketNotification(ctx, notification, excludedWebSocketDeviceIds); err != nil {
				return err
			}
			continue
		}

		connectedWebSocketDeviceIds, err := s.getConnectedWebsocketDeviceIds(ctx, subscriptions)
		if err != nil {
			s.log.Warn("unable to get websocket connection status for devices", "error", err)
			connectedWebSocketDeviceIds = map[string]struct{}{}
		}
		s.recordSkippedWebSocketDeliveries(ctx, notification, subscriptions, connectedWebSocketDeviceIds)

		var remaining []*model.PushSubscription
		for _, sub := range subscriptions {
			if _, ok := connectedWebSocketDeviceIds[NormalizeSopDeviceId(sub.DeviceId)]; !ok {
				remaining = append(remaining, sub)
			}
		}
		subscriptionByDevice := s.selectSubscriptionsByDevice(remaining, connectedSopDeviceIds, notification)

		s.recordSopDeliveryHolds(ctx, notification, subscriptions, connectedWebSocketDeviceIds)

		websocketExclusions := buildWebSocketExclusions(connectedSopDeviceIds, excludedWebSocketDeviceIds)
		if err := s.SendWebSocketNotification(ctx, notification, websocketExclusions); err != nil {
			return err
		}

		var wg sync.WaitGroup
		deliveryErrs := make(chan error, len(subscriptionByDevice))
		for _, sub := range subscriptionByDevice {
			wg.Add(1)
			go func(sub *model.PushSubscription) {
				defer wg.Done()
				if err := s.SendPushNotification(ctx, sub, notification); err != nil {
					deliveryErrs <- fmt.Errorf("account %s device %s: %w", account, sub.DeviceId, err)
				}
			}(sub)
		}
		wg.Wait()
		close(deliveryErrs)
		for err := range deliveryErrs {
			deliveryErrors = append(deliveryErrors, err)
		}
	}
	return errors.Join(deliveryErrors...)
}

// SendPushNotification mirrors PushService.SendPushNotificationAsync.
func (s *Service) SendPushNotification(ctx context.Context, subscription *model.PushSubscription, notification *model.Notification) error {
	startedAt := time.Now()
	outcome := model.DeliveryOutcomeFailure
	var sendErr error

	senders := s.apps.ResolveAppSenders(subscription.AppIdValue())
	s.log.Debug("pushing notification", "topic", notification.Topic, "notification_id", notification.Id, "device_id", subscription.DeviceId)

	switch subscription.Provider {
	case model.PushProviderGoogle:
		if senders == nil || senders.FCM == nil {
			sendErr = errors.New("Firebase Cloud Messaging is not initialized.")
			break
		}
		body := ""
		if notification.Subtitle != nil || notification.Content != nil {
			subtitle := ""
			if notification.Subtitle != nil {
				subtitle = *notification.Subtitle
			}
			content := ""
			if notification.Content != nil {
				content = *notification.Content
			}
			body = strings.TrimSpace(strings.Join([]string{subtitle, content}, "\n"))
		}
		title := ""
		if notification.Title != nil {
			title = *notification.Title
		}
		fcmResult, err := senders.FCM.SendAsync(ctx, fcmPayload(subscription.DeviceToken, title, body))
		if err != nil {
			sendErr = err
			break
		}
		if fcmResult.StatusCode == 404 || fcmResult.StatusCode == 410 || isInvalidFcmTokenError(fcmResult.Error) {
			s.enqueueRemoval(subscription)
			outcome = model.DeliveryOutcomeInvalidToken
		} else if fcmResult.Error != "" {
			sendErr = fmt.Errorf("Notification pushed failed (%d) %s", fcmResult.StatusCode, fcmResult.Error)
		}

	case model.PushProviderApple:
		apnsTopicKey := "Alert"
		if isVoipPush(notification) {
			apnsTopicKey = "Alert"
		} else if notification.PushType != nil {
			apnsTopicKey = *notification.PushType
		}
		apnsPushTopic := ""
		if senders != nil {
			apnsPushTopic = topicsLookup(senders.Topics, apnsTopicKey)
			if apnsPushTopic == "" {
				apnsPushTopic = senders.DefaultApnsTopic
			}
		}
		appleApns, ok := senders.apnsFor(apnsPushTopic)
		if senders == nil || strings.TrimSpace(apnsPushTopic) == "" || !ok {
			sendErr = errors.New("Apple Push Notification Service is not initialized.")
			break
		}

		alertDict := map[string]any{}
		if notification.Title != nil {
			alertDict["title"] = *notification.Title
		}
		if notification.Subtitle != nil {
			alertDict["subtitle"] = *notification.Subtitle
		}
		if notification.Content != nil {
			alertDict["body"] = *notification.Content
		}

		payload := applePayload(apnsPushTopic, notification.Topic, alertDict, notification.Meta, notification.Priority)

		apnResult, err := appleApns.SendAsync(subscription.DeviceToken, payload, notification.Id.String(), notification.Priority, apns2.PushTypeAlert)
		if err != nil {
			sendErr = err
			break
		}
		if apnResult.StatusCode == 404 || apnResult.StatusCode == 410 || isInvalidApnsTokenError(apnResult.Error) {
			s.enqueueRemoval(subscription)
			outcome = model.DeliveryOutcomeInvalidToken
		} else if apnResult.Error != "" {
			sendErr = fmt.Errorf("Notification pushed failed (%d) %s", apnResult.StatusCode, apnResult.Error)
		}

	case model.PushProviderAppk:
		appkTopic := ""
		if senders != nil {
			appkTopic = topicsLookup(senders.Topics, "VoIP")
		}
		appkApns, ok := senders.apnsFor(appkTopic)
		if senders == nil || strings.TrimSpace(appkTopic) == "" || !ok {
			sendErr = errors.New("Apple PushKit is not initialized.")
			break
		}

		appkPayload := appkPayload(notification)

		appkResult, err := appkApns.SendAsync(subscription.DeviceToken, appkPayload, notification.Id.String(), notification.Priority, apns2.PushTypeVOIP)
		if err != nil {
			sendErr = err
			break
		}
		if appkResult.StatusCode == 404 || appkResult.StatusCode == 410 || isInvalidApnsTokenError(appkResult.Error) {
			s.enqueueRemoval(subscription)
			outcome = model.DeliveryOutcomeInvalidToken
		} else if appkResult.Error != "" {
			sendErr = fmt.Errorf("Notification pushed failed (%d) %s", appkResult.StatusCode, appkResult.Error)
		}

	case model.PushProviderSop:
		return nil

	case model.PushProviderUnifiedPush:
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, subscription.DeviceToken, nil)
		if err != nil {
			sendErr = err
			break
		}
		req.Header.Set("TTL", "60")
		urgency := "normal"
		if notification.Priority >= 5 {
			urgency = "high"
		}
		req.Header.Set("Urgency", urgency)

		unifiedPushResult, err := s.httpClient.Do(req)
		if err != nil {
			sendErr = err
			break
		}
		_ = unifiedPushResult.Body.Close()
		if unifiedPushResult.StatusCode == http.StatusNotFound || unifiedPushResult.StatusCode == http.StatusGone {
			s.enqueueRemoval(subscription)
			outcome = model.DeliveryOutcomeInvalidToken
		} else if unifiedPushResult.StatusCode < 200 || unifiedPushResult.StatusCode >= 300 {
			sendErr = fmt.Errorf("Notification push failed (%d) %s", unifiedPushResult.StatusCode, unifiedPushResult.Status)
		}

	default:
		sendErr = fmt.Errorf("Push provider not supported: %d", subscription.Provider)
	}

	if outcome == model.DeliveryOutcomeFailure && sendErr == nil {
		outcome = model.DeliveryOutcomeSuccess
	}

	if sendErr != nil {
		s.log.Error("failed to push notification",
			"notification_id", notification.Id, "device_id", subscription.DeviceId,
			"provider", subscription.Provider.String(), "error", sendErr)
	}

	if subscription.Provider != model.PushProviderSop {
		s.obs.RecordNotification(ctx, notification, subscription.Provider.ProviderName(), outcome, time.Since(startedAt).Milliseconds(), sendErr, &subscription.Id)
	}

	if outcome == model.DeliveryOutcomeSuccess {
		s.log.Info("successfully pushed notification",
			"notification_id", notification.Id, "device_id", subscription.DeviceId, "provider", subscription.Provider)
	}
	return sendErr
}

func (s *Service) enqueueRemoval(subscription *model.PushSubscription) {
	s.removalBuffer.Enqueue(PushSubRemovalRequest{SubId: subscription.Id})
}

// SendWebSocketNotification mirrors PushService.SendWebSocketNotificationAsync.
func (s *Service) SendWebSocketNotification(ctx context.Context, notification *model.Notification, excludedDeviceIds []string) error {
	startedAt := time.Now()
	packetData := marshalNotificationPacket(notification)
	err := s.ws.PushWebSocketPacket(ctx, notification.AccountId.String(), "notifications.new", packetData, excludedDeviceIds, s.ResolveAppId(notification.AppIdValue(), true))
	if err != nil {
		s.obs.RecordNotification(ctx, notification, "websocket", model.DeliveryOutcomeFailure, time.Since(startedAt).Milliseconds(), err, nil)
		return err
	}
	s.obs.RecordNotification(ctx, notification, "websocket", model.DeliveryOutcomeSuccess, time.Since(startedAt).Milliseconds(), nil, nil)
	return nil
}

func (s *Service) getConnectedWebsocketDeviceIds(ctx context.Context, subscriptions []*model.PushSubscription) (map[string]struct{}, error) {
	var deviceIds []string
	seen := map[string]struct{}{}
	for _, sub := range subscriptions {
		id := NormalizeSopDeviceId(sub.DeviceId)
		if strings.TrimSpace(id) == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		deviceIds = append(deviceIds, id)
	}
	connected, err := s.ws.GetConnectedWebsocketDeviceIds(ctx, deviceIds, "")
	if err != nil {
		return nil, err
	}
	out := map[string]struct{}{}
	for _, id := range connected {
		out[id] = struct{}{}
	}
	return out, nil
}

func (s *Service) recordSkippedWebSocketDeliveries(ctx context.Context, notification *model.Notification, subscriptions []*model.PushSubscription, connected map[string]struct{}) {
	for _, sub := range subscriptions {
		if _, ok := connected[NormalizeSopDeviceId(sub.DeviceId)]; ok {
			s.obs.RecordNotification(ctx, notification, sub.Provider.ProviderName(), model.DeliveryOutcomeSkipped, 0, nil, &sub.Id)
		}
	}
}

func (s *Service) recordSopDeliveryHolds(ctx context.Context, notification *model.Notification, subscriptions []*model.PushSubscription, connected map[string]struct{}) {
	for _, sub := range subscriptions {
		if sub.Provider == model.PushProviderSop {
			if _, ok := connected[NormalizeSopDeviceId(sub.DeviceId)]; !ok {
				s.obs.RecordNotification(ctx, notification, "sop", model.DeliveryOutcomeHeld, 0, nil, &sub.Id)
			}
		}
	}
}

// selectSubscriptionsByDevice mirrors PushService.SelectSubscriptionsByDevice.
func (s *Service) selectSubscriptionsByDevice(subscriptions []*model.PushSubscription, connectedSopDeviceIds map[string]struct{}, notification *model.Notification) map[string]*model.PushSubscription {
	hasAnyAppk := isVoipPush(notification)
	if hasAnyAppk {
		hasAnyAppk = false
		for _, sub := range subscriptions {
			if sub.Provider == model.PushProviderAppk {
				hasAnyAppk = true
				break
			}
		}
	}

	result := map[string]*model.PushSubscription{}
	for _, sub := range subscriptions {
		if !isProviderCompatibleWithNotification(sub.Provider, notification, hasAnyAppk) {
			continue
		}
		key := NormalizeSopDeviceId(sub.DeviceId)
		current, ok := result[key]
		if !ok || getSubscriptionPriority(sub, connectedSopDeviceIds, notification) > getSubscriptionPriority(current, connectedSopDeviceIds, notification) {
			result[key] = sub
		}
	}
	return result
}

// getSubscriptionPriority mirrors PushService.GetSubscriptionPriority.
func getSubscriptionPriority(subscription *model.PushSubscription, connectedSopDeviceIds map[string]struct{}, notification *model.Notification) int {
	switch subscription.Provider {
	case model.PushProviderSop:
		if _, ok := connectedSopDeviceIds[NormalizeSopDeviceId(subscription.DeviceId)]; ok {
			return 5
		}
		return 0
	case model.PushProviderAppk:
		if isVoipPush(notification) {
			return 4
		}
		return 0
	case model.PushProviderGoogle:
		return 2
	case model.PushProviderUnifiedPush:
		return 1
	case model.PushProviderApple:
		if isVoipPush(notification) {
			return 3
		}
		return 4
	default:
		return 0
	}
}

// isProviderCompatibleWithNotification mirrors the C# helper.
func isProviderCompatibleWithNotification(provider model.PushProvider, notification *model.Notification, hasAnyAppk bool) bool {
	if !isVoipPush(notification) {
		return provider != model.PushProviderAppk
	}
	if hasAnyAppk && provider == model.PushProviderApple {
		return false
	}
	return true
}

// isVoipPush mirrors PushService.IsVoipPush.
func isVoipPush(notification *model.Notification) bool {
	return notification.PushType != nil && strings.EqualFold(*notification.PushType, "VoIP")
}

// ShouldQueueSopReplay mirrors PushService.ShouldQueueSopReplay.
func ShouldQueueSopReplay(isSavable bool, subscriptions []*model.PushSubscription) bool {
	if isSavable {
		return false
	}
	for _, sub := range subscriptions {
		if sub.Provider == model.PushProviderSop {
			return true
		}
	}
	return false
}

// buildWebSocketExclusions mirrors PushService.BuildWebSocketExclusions.
func buildWebSocketExclusions(deviceIds map[string]struct{}, excludedWebSocketDeviceIds []string) []string {
	exclusions := map[string]struct{}{}
	for _, id := range excludedWebSocketDeviceIds {
		exclusions[id] = struct{}{}
	}
	for id := range deviceIds {
		exclusions[id] = struct{}{}
	}
	if len(exclusions) == 0 {
		return nil
	}
	out := make([]string, 0, len(exclusions))
	for id := range exclusions {
		out = append(out, id)
	}
	return out
}

func isInvalidFcmTokenError(err string) bool {
	_, ok := invalidFcmErrors[strings.ToLower(strings.TrimSpace(err))]
	return ok
}

func isInvalidApnsTokenError(err string) bool {
	_, ok := invalidApnsErrors[strings.ToLower(strings.TrimSpace(err))]
	return ok
}

// apnsFor returns the APNs sender for a topic (missing → nil).
func (a *AppSenders) apnsFor(topic string) (*ApnSender, bool) {
	if a == nil {
		return nil, false
	}
	sender, ok := a.ApnsByTopic[topic]
	return sender, ok
}

// topicsLookup returns the topic value for key, falling back to a
// case-insensitive match (configs write "alert"/"voip", the C# lookups use
// "Alert"/"VoIP").
func topicsLookup(topics map[string]string, key string) string {
	if v, ok := topics[key]; ok {
		return v
	}
	lower := strings.ToLower(key)
	for k, v := range topics {
		if strings.ToLower(k) == lower {
			return v
		}
	}
	return ""
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
