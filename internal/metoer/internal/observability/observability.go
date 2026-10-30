// Package observability ports DysonNetwork.Ring.DeliveryObservabilityService:
// fire-and-forget delivery record writers that never break the delivery
// path (each record opens its own DB scope; failures are logged, matching
// the C# try/catch around SaveChanges).
package observability

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/metoer/internal/model"
	"src.solsynth.dev/sosys/metoer/internal/store"
)

// Service records delivery outcomes.
type Service struct {
	st  *store.Store
	log *slog.Logger
}

// New builds the observability service.
func New(st *store.Store, log *slog.Logger) *Service {
	return &Service{st: st, log: log}
}

// RecordEmail records an email delivery outcome (RecordEmailAsync). Provider
// is always "smtp". Errors are logged and swallowed.
func (s *Service) RecordEmail(ctx context.Context, source string, outcome model.DeliveryOutcome, durationMilliseconds int64, err error) {
	// Fire-and-forget: detach from the caller's cancellation so the record
	// lands even when the request/queue context is already done.
	ctx = context.WithoutCancel(ctx)
	rec := &model.EmailDeliveryRecord{
		Source:             source,
		Provider:           "smtp",
		Outcome:            outcome,
		DurationMilliseconds: durationMilliseconds,
		Error:              truncateErr(err),
		ModelBase:          model.ModelBase{Id: uuid.New(), CreatedAt: model.NowTime(), UpdatedAt: model.NowTime()},
	}
	if rerr := s.st.RecordEmailDelivery(ctx, rec); rerr != nil {
		s.log.Error("failed to record email delivery outcome", "error", rerr)
	}
}

// RecordNotification records a notification delivery outcome
// (RecordNotificationAsync). Errors are logged and swallowed.
func (s *Service) RecordNotification(ctx context.Context, n *model.Notification, provider string, outcome model.DeliveryOutcome, durationMilliseconds int64, err error, subscriptionID *uuid.UUID) {
	// Fire-and-forget: detach from the caller's cancellation so the record
	// lands even when the request/queue context is already done.
	ctx = context.WithoutCancel(ctx)
	var notificationID *uuid.UUID
	if n != nil {
		id := n.Id
		notificationID = &id
	}
	rec := &model.NotificationDeliveryRecord{
		NotificationId:      notificationID,
		SubscriptionId:      subscriptionID,
		Topic:               nTopic(n),
		AppId:               nAppID(n),
		PushType:            nPushType(n),
		Provider:            provider,
		Outcome:             outcome,
		DurationMilliseconds: durationMilliseconds,
		Error:               truncateErr(err),
		ModelBase:           model.ModelBase{Id: uuid.New(), CreatedAt: model.NowTime(), UpdatedAt: model.NowTime()},
	}
	if rerr := s.st.RecordNotificationDelivery(ctx, rec); rerr != nil {
		s.log.Error("failed to record notification delivery outcome", "error", rerr)
	}
}

// RecordNotificationSend records a send request (RecordNotificationSendAsync).
func (s *Service) RecordNotificationSend(ctx context.Context, n *model.Notification, source string) {
	// Fire-and-forget: detach from the caller's cancellation so the record
	// lands even when the request/queue context is already done.
	ctx = context.WithoutCancel(ctx)
	rec := &model.NotificationSendRecord{
		Topic:     nTopic(n),
		AppId:     nAppID(n),
		PushType:  nPushType(n),
		Source:    source,
		ModelBase: model.ModelBase{Id: uuid.New(), CreatedAt: model.NowTime(), UpdatedAt: model.NowTime()},
	}
	if rerr := s.st.RecordNotificationSend(ctx, rec); rerr != nil {
		s.log.Error("failed to record notification send", "error", rerr)
	}
}

// MarkSopDeliveryRead promotes Held sop delivery records to Success
// (MarkSopDeliveryReadAsync). Errors are logged and swallowed.
func (s *Service) MarkSopDeliveryRead(ctx context.Context, subscriptionID uuid.UUID, notificationIDs []uuid.UUID) {
	// Fire-and-forget: detach from the caller's cancellation so the read
	// receipt lands even when the SSE client has already disconnected.
	ctx = context.WithoutCancel(ctx)
	if _, err := s.st.MarkSopDeliveryRead(ctx, subscriptionID, notificationIDs, time.Now().UTC()); err != nil {
		s.log.Error("failed to mark SOP notification delivery as read", "error", err)
	}
}

func truncateErr(err error) *string {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if len(msg) > 4096 {
		msg = msg[:4096]
	}
	return &msg
}

func nTopic(n *model.Notification) string {
	if n == nil {
		return ""
	}
	return n.Topic
}

func nAppID(n *model.Notification) *string {
	if n == nil {
		return nil
	}
	return n.AppId
}

func nPushType(n *model.Notification) *string {
	if n == nil {
		return nil
	}
	return n.PushType
}
