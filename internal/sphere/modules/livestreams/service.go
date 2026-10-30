package livestreams

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/filejson"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// LiveStreamStatus mirrors the C# enum.
const (
	statusPending = 0
	statusActive  = 1
	statusEnded   = 2
	statusError   = 3
)

// LiveStreamType mirrors the C# enum.
const (
	typeRegular     = 0
	typeInteractive = 1
)

// LiveStreamVisibility mirrors the C# enum.
const (
	visibilityPublic   = 0
	visibilityUnlisted = 1
	visibilityPrivate  = 2
)

// LiveStreamAwardAttitude mirrors the C# enum.
const (
	awardNeutral  = 0
	awardPositive = 1
	awardNegative = 2
)

type service struct {
	core  *core.Services
	db    *gorm.DB
	store *store.Store
	log   *slog.Logger
	ports *module.PortRegistry
	auth  *authn.Service
	lk    *livekitClient
}

func newService(c *core.Services, registry *module.PortRegistry, log *slog.Logger) *service {
	if log == nil {
		log = slog.Default()
	}
	cfg := c.Config.LiveStream
	return &service{
		core:  c,
		db:    c.DB,
		store: store.New(c.DB),
		log:   log,
		ports: registry,
		auth:  c.Auth,
		lk:    newLiveKitClient(cfg.Endpoint, cfg.APIKey, cfg.APISecret, cfg.PlaybackURL),
	}
}

// ---- ports ----------------------------------------------------------------

func (s *service) filesPort() (ports.Files, bool) {
	if s.ports == nil {
		return nil, false
	}
	return module.Get[ports.Files](s.ports, ports.PortFiles)
}

func (s *service) accountsPort() (ports.Accounts, bool) {
	if s.ports == nil {
		return nil, false
	}
	return module.Get[ports.Accounts](s.ports, ports.PortAccounts)
}

func (s *service) paymentsPort() (ports.Payments, bool) {
	if s.ports == nil {
		return nil, false
	}
	return module.Get[ports.Payments](s.ports, ports.PortPayments)
}

// ---- mapping --------------------------------------------------------------

func streamToModel(entity *store.LiveStreamEntity, publisher *store.PublisherEntity) model.LiveStream {
	stream := model.LiveStream{
		ID:                     entity.ID.String(),
		Title:                  entity.Title,
		Description:            entity.Description,
		Slug:                   entity.Slug,
		Type:                   entity.Type,
		Visibility:             entity.Visibility,
		Status:                 entity.Status,
		RoomName:               entity.RoomName,
		IngressID:              entity.IngressID,
		IngressStreamKey:       entity.IngressStreamKey,
		EgressID:               entity.EgressID,
		HLSEgressID:            entity.HLSEgressID,
		HLSPlaylistPath:        entity.HLSPlaylistPath,
		HLSStartedAt:           store.TimePtr(entity.HLSStartedAt),
		StartedAt:              store.TimePtr(entity.StartedAt),
		EndedAt:                store.TimePtr(entity.EndedAt),
		TotalDurationSeconds:   entity.TotalDurationSeconds,
		ViewerCount:            entity.ViewerCount,
		PeakViewerCount:        entity.PeakViewerCount,
		TotalAwardScore:        entity.TotalAwardScore,
		DistributedAwardAmount: entity.DistributedAwardAmount,
		Thumbnail:              filejson.Value(entity.Thumbnail),
		Metadata:               store.JSONValue(entity.Metadata),
		PublisherID:            store.UUIDPtrString(entity.PublisherID),
		CreatedAt:              store.TimePtr(&entity.CreatedAt),
		UpdatedAt:              store.TimePtr(&entity.UpdatedAt),
		DeletedAt:              store.DeletedPtr(entity.DeletedAt),
	}
	if publisher != nil {
		mapped := store.PublisherToModel(publisher)
		stream.Publisher = &mapped
	}
	return stream
}

func chatToModel(entity *store.LiveStreamChatMessageEntity) model.LiveStreamChatMessage {
	return model.LiveStreamChatMessage{
		ID:           entity.ID.String(),
		LiveStreamID: entity.LiveStreamID.String(),
		SenderID:     entity.SenderID.String(),
		SenderName:   entity.SenderName,
		Content:      entity.Content,
		TimeoutUntil: store.TimePtr(entity.TimeoutUntil),
		CreatedAt:    store.TimePtr(&entity.CreatedAt),
		UpdatedAt:    store.TimePtr(&entity.UpdatedAt),
		DeletedAt:    store.DeletedPtr(entity.DeletedAt),
	}
}

func awardToModel(entity *store.LiveStreamAwardEntity) model.LiveStreamAward {
	return model.LiveStreamAward{
		ID:           entity.ID.String(),
		Amount:       entity.Amount,
		Attitude:     entity.Attitude,
		Message:      entity.Message,
		LiveStreamID: entity.LiveStreamID.String(),
		AccountID:    entity.AccountID.String(),
		SenderName:   entity.SenderName,
		CreatedAt:    store.TimePtr(&entity.CreatedAt),
		UpdatedAt:    store.TimePtr(&entity.UpdatedAt),
		DeletedAt:    store.DeletedPtr(entity.DeletedAt),
	}
}

// sanitizeForPublic mirrors LiveStreamController.SanitizeForPublic: the HLS
// path becomes a playback URL and the ingest/egress credentials are removed.
// It mutates the wire copy, never the stored row.
func (s *service) sanitizeForPublic(stream *model.LiveStream) {
	stream.HLSPlaylistPath = s.lk.buildHLSPlaylistURL(stream.HLSPlaylistPath)
	stream.IngressStreamKey = nil
	stream.IngressID = nil
	stream.EgressID = nil
}

// ---- publisher membership -------------------------------------------------

func (s *service) memberRole(ctx context.Context, publisherID, accountID uuid.UUID) (int, bool) {
	var member store.PublisherMemberEntity
	err := s.db.WithContext(ctx).
		Where("publisher_id = ? AND account_id = ?", publisherID, accountID).
		First(&member).Error
	if err != nil {
		return 0, false
	}
	return member.Role, true
}

func (s *service) isEditor(ctx context.Context, publisherID *uuid.UUID, accountID uuid.UUID) bool {
	if publisherID == nil {
		return false
	}
	role, ok := s.memberRole(ctx, *publisherID, accountID)
	return ok && role >= int(model.PublisherRoleEditor)
}

func (s *service) userPublishers(ctx context.Context, accountID uuid.UUID) ([]store.PublisherEntity, error) {
	var publishers []store.PublisherEntity
	err := s.db.WithContext(ctx).
		Where("id IN (?)", s.db.Model(&store.PublisherMemberEntity{}).
			Select("publisher_id").
			Where("account_id = ?", accountID)).
		Order("created_at ASC").
		Find(&publishers).Error
	return publishers, err
}

// ---- queries --------------------------------------------------------------

func (s *service) getByID(ctx context.Context, id uuid.UUID) (*store.LiveStreamEntity, *store.PublisherEntity, error) {
	var entity store.LiveStreamEntity
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&entity).Error; err != nil {
		return nil, nil, err
	}
	var publisher *store.PublisherEntity
	if entity.PublisherID != nil {
		var row store.PublisherEntity
		if err := s.db.WithContext(ctx).Where("id = ?", *entity.PublisherID).First(&row).Error; err == nil {
			publisher = &row
		}
	}
	return &entity, publisher, nil
}

func (s *service) loadPublishers(ctx context.Context, streams []store.LiveStreamEntity) map[uuid.UUID]store.PublisherEntity {
	ids := make([]uuid.UUID, 0, len(streams))
	for _, stream := range streams {
		if stream.PublisherID != nil {
			ids = append(ids, *stream.PublisherID)
		}
	}
	result := make(map[uuid.UUID]store.PublisherEntity)
	if len(ids) == 0 {
		return result
	}
	var rows []store.PublisherEntity
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return result
	}
	for _, row := range rows {
		result[row.ID] = row
	}
	return result
}

func (s *service) getActive(ctx context.Context, limit, offset int) ([]model.LiveStream, error) {
	var rows []store.LiveStreamEntity
	err := s.db.WithContext(ctx).
		Where("status = ? AND visibility = ?", statusActive, visibilityPublic).
		Order("total_award_score * 10 + viewer_count DESC").
		Order("started_at DESC").
		Limit(limit).Offset(offset).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	publishers := s.loadPublishers(ctx, rows)
	out := make([]model.LiveStream, 0, len(rows))
	for i := range rows {
		var pub *store.PublisherEntity
		if rows[i].PublisherID != nil {
			if row, ok := publishers[*rows[i].PublisherID]; ok {
				copied := row
				pub = &copied
			}
		}
		out = append(out, streamToModel(&rows[i], pub))
	}
	return out, nil
}

func (s *service) getByPublisher(ctx context.Context, publisherID uuid.UUID, limit, offset int) ([]model.LiveStream, error) {
	var rows []store.LiveStreamEntity
	err := s.db.WithContext(ctx).
		Where("publisher_id = ?", publisherID).
		Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	var pub *store.PublisherEntity
	var publisherRow store.PublisherEntity
	if err := s.db.WithContext(ctx).Where("id = ?", publisherID).First(&publisherRow).Error; err == nil {
		pub = &publisherRow
	}
	out := make([]model.LiveStream, 0, len(rows))
	for i := range rows {
		out = append(out, streamToModel(&rows[i], pub))
	}
	return out, nil
}

// ---- lifecycle ------------------------------------------------------------

func (s *service) createStream(ctx context.Context, publisherID uuid.UUID, req createRequest, thumbnail *model.CloudFileReferenceObject) (*model.LiveStream, error) {
	roomName := "livestream_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if s.lk.configured() {
		if err := s.lk.createRoom(ctx, roomName, nil); err != nil {
			return nil, err
		}
	}
	streamType := typeRegular
	if req.Type != nil {
		streamType = *req.Type
	}
	visibility := visibilityPublic
	if req.Visibility != nil {
		visibility = *req.Visibility
	}
	entity := &store.LiveStreamEntity{
		ID:          uuid.New(),
		Title:       req.Title,
		Description: req.Description,
		Slug:        req.Slug,
		Type:        streamType,
		Visibility:  visibility,
		Status:      statusPending,
		RoomName:    roomName,
		PublisherID: &publisherID,
		Metadata:    store.ToJSON(req.Metadata),
		Thumbnail:   store.ToJSON(thumbnail),
	}
	if thumbnail == nil {
		entity.Thumbnail = nil
	}
	if err := s.db.WithContext(ctx).Create(entity).Error; err != nil {
		return nil, err
	}
	s.log.Info("created livestream", "id", entity.ID, "publisher_id", publisherID)
	stream := streamToModel(entity, nil)
	return &stream, nil
}

func (s *service) startStreaming(ctx context.Context, id uuid.UUID, identity, name string, createIngress, enableTranscoding, useWhip bool) (*ingressResult, error) {
	entity, _, err := s.getByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if entity.Status != statusPending && entity.Status != statusEnded {
		return nil, errors.New("livestream is not in a startable state")
	}
	var result *ingressResult
	if createIngress {
		result, err = s.lk.createIngress(ctx, entity.RoomName, identity, name, derefString(entity.Title), enableTranscoding, useWhip)
		if err != nil {
			return nil, err
		}
		entity.IngressID = &result.IngressID
		entity.IngressStreamKey = &result.StreamKey
	}
	now := time.Now().UTC()
	entity.Status = statusActive
	entity.StartedAt = &now
	if err := s.db.WithContext(ctx).Save(entity).Error; err != nil {
		return nil, err
	}
	_ = s.lk.broadcast(ctx, entity.RoomName, "stream_started", map[string]any{
		"livestream_id": entity.ID.String(),
		"title":         derefString(entity.Title),
		"started_at":    now.Format(time.RFC3339),
	})
	s.log.Info("started livestream", "id", id, "ingress", createIngress, "whip", useWhip)
	return result, nil
}

func (s *service) startInAppStreaming(ctx context.Context, id uuid.UUID) error {
	entity, _, err := s.getByID(ctx, id)
	if err != nil {
		return err
	}
	if entity.Status != statusPending && entity.Status != statusEnded {
		return errors.New("livestream is not in a startable state")
	}
	now := time.Now().UTC()
	entity.Status = statusActive
	entity.StartedAt = &now
	if err := s.db.WithContext(ctx).Save(entity).Error; err != nil {
		return err
	}
	_ = s.lk.broadcast(ctx, entity.RoomName, "stream_started", map[string]any{
		"livestream_id": entity.ID.String(),
		"title":         derefString(entity.Title),
		"started_at":    now.Format(time.RFC3339),
	})
	return nil
}

func (s *service) startEgressStream(ctx context.Context, id uuid.UUID, rtmpURLs []string, filePath *string) (string, error) {
	entity, _, err := s.getByID(ctx, id)
	if err != nil {
		return "", err
	}
	if entity.Status != statusActive {
		return "", errors.New("livestream is not active")
	}
	egressID, err := s.lk.startRoomCompositeEgress(ctx, entity.RoomName, rtmpURLs, derefString(filePath), "")
	if err != nil {
		return "", err
	}
	entity.EgressID = &egressID
	if err := s.db.WithContext(ctx).Save(entity).Error; err != nil {
		return "", err
	}
	return egressID, nil
}

func (s *service) startHLSEgress(ctx context.Context, id uuid.UUID, playlistName string, segmentDuration uint32, segmentCount int, layout *string) (*hlsEgressResult, error) {
	entity, _, err := s.getByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if entity.Status != statusActive {
		return nil, errors.New("livestream is not active")
	}
	if entity.HLSEgressID != nil && *entity.HLSEgressID != "" {
		return nil, errors.New("HLS egress is already active for this stream")
	}
	filePath := "hls/" + entity.ID.String() + "/" + playlistName
	result, err := s.lk.startRoomCompositeHLSEgress(ctx, entity.RoomName, playlistName, filePath, segmentDuration, derefString(layout))
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	entity.HLSEgressID = &result.EgressID
	entity.HLSStartedAt = &now
	if err := s.db.WithContext(ctx).Save(entity).Error; err != nil {
		return nil, err
	}
	return result, nil
}

func (s *service) stopHLSStream(ctx context.Context, id uuid.UUID) error {
	entity, _, err := s.getByID(ctx, id)
	if err != nil {
		return err
	}
	if entity.HLSEgressID != nil && *entity.HLSEgressID != "" {
		if err := s.lk.stopEgress(ctx, *entity.HLSEgressID); err != nil {
			s.log.Warn("failed to stop HLS egress; clearing it anyway", "egress_id", *entity.HLSEgressID, "error", err)
		}
		entity.HLSEgressID = nil
	}
	return s.db.WithContext(ctx).Save(entity).Error
}

func (s *service) stopStreamEgress(ctx context.Context, id uuid.UUID) error {
	entity, _, err := s.getByID(ctx, id)
	if err != nil {
		return err
	}
	if entity.EgressID != nil && *entity.EgressID != "" {
		if err := s.lk.stopEgress(ctx, *entity.EgressID); err != nil {
			s.log.Warn("failed to stop egress; clearing it anyway", "egress_id", *entity.EgressID, "error", err)
		}
		entity.EgressID = nil
	}
	return s.db.WithContext(ctx).Save(entity).Error
}

func (s *service) endStream(ctx context.Context, id uuid.UUID) error {
	entity, _, err := s.getByID(ctx, id)
	if err != nil {
		return err
	}
	if entity.IngressID != nil && *entity.IngressID != "" {
		if err := s.lk.deleteIngress(ctx, *entity.IngressID); err != nil {
			s.log.Warn("failed to delete ingress; clearing it anyway", "ingress_id", *entity.IngressID, "error", err)
		}
		entity.IngressID = nil
	}
	if entity.EgressID != nil && *entity.EgressID != "" {
		if err := s.lk.stopEgress(ctx, *entity.EgressID); err != nil {
			s.log.Warn("failed to stop egress; clearing it anyway", "egress_id", *entity.EgressID, "error", err)
		}
		entity.EgressID = nil
	}
	if entity.HLSEgressID != nil && *entity.HLSEgressID != "" {
		if err := s.lk.stopEgress(ctx, *entity.HLSEgressID); err != nil {
			s.log.Warn("failed to stop HLS egress; clearing it anyway", "egress_id", *entity.HLSEgressID, "error", err)
		}
		entity.HLSEgressID = nil
	}
	now := time.Now().UTC()
	entity.Status = statusEnded
	entity.EndedAt = &now
	if entity.StartedAt != nil {
		entity.TotalDurationSeconds += int64(now.Sub(*entity.StartedAt).Seconds())
	}
	if err := s.db.WithContext(ctx).Save(entity).Error; err != nil {
		return err
	}
	s.distributeAwards(ctx, entity)
	_ = s.lk.broadcast(ctx, entity.RoomName, "stream_ended", map[string]any{
		"livestream_id": entity.ID.String(),
		"ended_at":      now.Format(time.RFC3339),
	})
	s.log.Info("ended livestream", "id", id)
	return nil
}

func (s *service) deleteStream(ctx context.Context, id uuid.UUID) error {
	entity, _, err := s.getByID(ctx, id)
	if err != nil {
		return err
	}
	if entity.Status == statusActive {
		if entity.IngressID != nil && *entity.IngressID != "" {
			if err := s.lk.deleteIngress(ctx, *entity.IngressID); err != nil {
				s.log.Warn("failed to delete ingress during stream deletion", "ingress_id", *entity.IngressID, "error", err)
			}
		}
		if entity.EgressID != nil && *entity.EgressID != "" {
			if err := s.lk.stopEgress(ctx, *entity.EgressID); err != nil {
				s.log.Warn("failed to stop egress during stream deletion", "egress_id", *entity.EgressID, "error", err)
			}
		}
		if entity.HLSEgressID != nil && *entity.HLSEgressID != "" {
			if err := s.lk.stopEgress(ctx, *entity.HLSEgressID); err != nil {
				s.log.Warn("failed to stop HLS egress during stream deletion", "egress_id", *entity.HLSEgressID, "error", err)
			}
		}
		if err := s.lk.deleteRoom(ctx, entity.RoomName); err != nil {
			s.log.Warn("failed to delete room during stream deletion", "room", entity.RoomName, "error", err)
		}
	}
	return s.db.WithContext(ctx).Delete(&store.LiveStreamEntity{}, "id = ?", id).Error
}

// distributeAwards mirrors LiveStreamService.DistributeAwardsAsync. The C#
// service moved the streamer's 90% share with
// RemotePaymentService.CreateTransactionWithAccount; the Lighthouse payments
// port only exposes order creation, so the transfer cannot be performed here.
// The method records the shortfall and leaves DistributedAwardAmount alone
// rather than pretending the payout happened.
func (s *service) distributeAwards(ctx context.Context, stream *store.LiveStreamEntity) {
	if stream.PublisherID == nil {
		s.log.Warn("cannot distribute awards: livestream has no publisher", "id", stream.ID)
		return
	}
	var publisher store.PublisherEntity
	if err := s.db.WithContext(ctx).Where("id = ?", *stream.PublisherID).First(&publisher).Error; err != nil || publisher.AccountID == nil {
		s.log.Warn("cannot distribute awards: publisher not found", "id", stream.ID)
		return
	}
	var total float64
	if err := s.db.WithContext(ctx).Model(&store.LiveStreamAwardEntity{}).
		Where("live_stream_id = ? AND attitude = ?", stream.ID, awardPositive).
		Select("COALESCE(SUM(amount), 0)").Scan(&total).Error; err != nil {
		s.log.Error("cannot sum livestream awards", "id", stream.ID, "error", err)
		return
	}
	if total <= 0 || total-stream.DistributedAwardAmount <= 0 {
		return
	}
	s.log.Warn("livestream award distribution is not implemented: the payments port exposes no account transfer",
		"livestream_id", stream.ID, "streamer_account_id", publisher.AccountID, "amount_due", total-stream.DistributedAwardAmount)
}

// confirmAward mirrors LiveStreamService.ConfirmAwardAsync.
func (s *service) confirmAward(ctx context.Context, streamID, accountID uuid.UUID, amount float64, message *string) error {
	entity, _, err := s.getByID(ctx, streamID)
	if err != nil {
		return err
	}
	senderName := "Anonymous"
	if accounts, ok := s.accountsPort(); ok {
		if account, err := accounts.GetAccount(ctx, accountID.String()); err == nil && account != nil {
			switch {
			case account.GetNick() != "":
				senderName = account.GetNick()
			case account.GetName() != "":
				senderName = account.GetName()
			}
		}
	}
	award := &store.LiveStreamAwardEntity{
		ID:           uuid.New(),
		Amount:       amount,
		Attitude:     awardPositive,
		Message:      message,
		LiveStreamID: streamID,
		AccountID:    accountID,
		SenderName:   senderName,
	}
	if err := s.db.WithContext(ctx).Create(award).Error; err != nil {
		return err
	}
	entity.TotalAwardScore += amount
	if err := s.db.WithContext(ctx).Save(entity).Error; err != nil {
		return err
	}
	_ = s.lk.broadcast(ctx, entity.RoomName, "stream_awarded", map[string]any{
		"livestream_id":     streamID.String(),
		"sender_id":         accountID.String(),
		"sender_name":       senderName,
		"amount":            formatAmount(amount),
		"highlight_seconds": int(amount * 2),
		"message":           derefString(message),
	})
	return nil
}

// cleanupIngresses is LiveStreamIngressCleanupJob: streams that started more
// than five minutes ago with an ingress and no participants are ended.
func (s *service) cleanupIngresses(ctx context.Context) error {
	threshold := time.Now().UTC().Add(-5 * time.Minute)
	var stale []store.LiveStreamEntity
	if err := s.db.WithContext(ctx).
		Where("status = ? AND ingress_id IS NOT NULL AND started_at IS NOT NULL AND started_at < ?", statusActive, threshold).
		Find(&stale).Error; err != nil {
		return err
	}
	if len(stale) == 0 {
		return nil
	}
	cleaned := 0
	for i := range stale {
		stream := &stale[i]
		participants, err := s.lk.listParticipants(ctx, stream.RoomName)
		if err != nil {
			s.log.Warn("failed to inspect room during ingress cleanup", "id", stream.ID, "error", err)
			continue
		}
		if participants > 0 {
			continue
		}
		now := time.Now().UTC()
		stream.Status = statusEnded
		stream.EndedAt = &now
		if stream.StartedAt != nil {
			stream.TotalDurationSeconds += int64(now.Sub(*stream.StartedAt).Seconds())
		}
		_ = s.lk.broadcast(ctx, stream.RoomName, "stream_ended", map[string]any{
			"livestream_id": stream.ID.String(),
			"ended_at":      now.Format(time.RFC3339),
		})
		if stream.IngressID != nil && *stream.IngressID != "" {
			if err := s.lk.deleteIngress(ctx, *stream.IngressID); err != nil {
				s.log.Warn("failed to delete unused ingress", "ingress_id", *stream.IngressID, "id", stream.ID, "error", err)
			}
		}
		stream.IngressID = nil
		stream.IngressStreamKey = nil
		cleaned++
	}
	if err := s.db.WithContext(ctx).Save(&stale).Error; err != nil {
		return err
	}
	s.log.Info("cleaned up stale livestreams", "count", cleaned)
	return nil
}

// ---- payment order consumer ----------------------------------------------

// consumePaymentEvents handles wallet order confirmations for
// livestreams.award orders, the same listener the C# service registered.
func (s *service) consumePaymentEvents(ctx context.Context) {
	err := s.core.NATS.Consume(ctx, "payment_events", "payment_orders", "sphere_livestream_awards", func(raw []byte) error {
		var event struct {
			ProductIdentifier string         `json:"product_identifier"`
			Meta              map[string]any `json:"meta"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			s.log.Warn("failed to decode payment event", "error", err)
			return nil
		}
		if event.ProductIdentifier != "livestreams.award" || event.Meta == nil {
			return nil
		}
		streamID, err := uuid.Parse(stringValue(event.Meta["livestream_id"]))
		if err != nil {
			return nil
		}
		accountID, err := uuid.Parse(stringValue(event.Meta["account_id"]))
		if err != nil {
			return nil
		}
		amount, err := parseAmount(stringValue(event.Meta["amount"]))
		if err != nil {
			return nil
		}
		var message *string
		if value, ok := event.Meta["message"]; ok && value != nil {
			text := stringValue(value)
			message = &text
		}
		return s.confirmAward(ctx, streamID, accountID, amount, message)
	})
	if err != nil && ctx.Err() == nil {
		s.log.Warn("payment event consumer stopped", "error", err)
	}
}

// ---- helpers --------------------------------------------------------------

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return strings.Trim(string(raw), `"`)
	}
}

func parseAmount(value string) (float64, error) {
	var amount float64
	if err := json.Unmarshal([]byte(value), &amount); err != nil {
		return 0, err
	}
	return amount, nil
}

func formatAmount(amount float64) string {
	if math.IsNaN(amount) || math.IsInf(amount, 0) {
		return "0"
	}
	return strconv.FormatFloat(amount, 'f', -1, 64)
}
