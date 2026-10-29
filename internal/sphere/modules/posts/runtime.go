package posts

import (
	"context"
	"encoding/json"
	"fmt"
	rand "math/rand/v2"
	"strconv"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

func (h *handler) startJobs(host *module.Host[core.Services]) {
	jobs := host.Core.Jobs
	if jobs == nil {
		return
	}
	jobs.Every("posts_view_flush", time.Minute, h.flushViews)
	jobs.Every("posts_interest_flush", time.Minute, h.flushInterest)
	jobs.Every("posts_scheduled_publish", 15*time.Second, h.publishScheduledPosts)
	jobs.Cron("posts_sponsor_auction", "0 0 * * * ?", h.runSponsorAuction)
	jobs.Every("posts_index_backfill", 10*time.Minute, h.backfillPostIndices)
	jobs.Now("posts_index_backfill_initial", h.backfillPostIndices)
}

// ---------------------------------------------------------------------------
// Event consumers
// ---------------------------------------------------------------------------

func (h *handler) startConsumers(ctx context.Context) {
	bus := h.core.NATS
	if bus == nil {
		return
	}
	go func() {
		err := bus.Consume(ctx, "payment_events", "payment_orders", "sphere_posts_payment_handler", func(data []byte) error {
			return h.handlePaymentEvent(ctx, data)
		})
		if err != nil && ctx.Err() == nil {
			h.log.Warn("posts: payment consumer stopped", "error", err)
		}
	}()
	go func() {
		err := bus.Consume(ctx, "account_events", "account.deleted", "sphere_posts_account_deleted_handler", func(data []byte) error {
			return h.handleAccountDeleted(ctx, data)
		})
		if err != nil && ctx.Err() == nil {
			h.log.Warn("posts: account-deleted consumer stopped", "error", err)
		}
	}()
}

type paymentOrderEvent struct {
	OrderID           string         `json:"order_id"`
	WalletID          string         `json:"wallet_id"`
	AccountID         string         `json:"account_id"`
	AppIdentifier     *string        `json:"app_identifier"`
	ProductIdentifier *string        `json:"product_identifier"`
	Status            int            `json:"status"`
	Meta              map[string]any `json:"meta"`
}

func (h *handler) handlePaymentEvent(ctx context.Context, data []byte) error {
	var event paymentOrderEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}
	if event.ProductIdentifier == nil || event.Meta == nil {
		return nil
	}
	switch *event.ProductIdentifier {
	case awardProductIdentifier:
		postID, err := metaUUID(event.Meta, "post_id")
		if err != nil {
			return err
		}
		accountID, err := metaUUID(event.Meta, "account_id")
		if err != nil {
			return err
		}
		amount, err := metaAmount(event.Meta)
		if err != nil {
			return err
		}
		attitude := metaInt(event.Meta, "attitude")
		message := metaString(event.Meta, "message")
		h.log.Info("posts: handling award order", "order", event.OrderID, "post", postID)
		return h.settleAward(ctx, postID, accountID, amount, attitude, message)
	case sponsorProductIdentifier:
		postID, err := metaUUID(event.Meta, "post_id")
		if err != nil {
			return err
		}
		accountID, err := metaUUID(event.Meta, "account_id")
		if err != nil {
			return err
		}
		amount, err := metaAmount(event.Meta)
		if err != nil {
			return err
		}
		h.log.Info("posts: handling sponsor order", "order", event.OrderID, "post", postID)
		return h.confirmSponsorBid(ctx, postID, accountID, amount)
	}
	return nil
}

func metaUUID(meta map[string]any, key string) (uuid.UUID, error) {
	raw, ok := meta[key]
	if !ok {
		return uuid.Nil, fmt.Errorf("missing %s in order meta", key)
	}
	switch value := raw.(type) {
	case string:
		return uuid.Parse(value)
	default:
		return uuid.Nil, fmt.Errorf("invalid %s in order meta", key)
	}
}

func metaAmount(meta map[string]any) (float64, error) {
	raw, ok := meta["amount"]
	if !ok {
		return 0, fmt.Errorf("missing amount in order meta")
	}
	switch value := raw.(type) {
	case string:
		return strconv.ParseFloat(value, 64)
	case float64:
		return value, nil
	case json.Number:
		return value.Float64()
	default:
		return 0, fmt.Errorf("invalid amount in order meta")
	}
}

func metaInt(meta map[string]any, key string) int {
	switch value := meta[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case string:
		parsed, _ := strconv.Atoi(value)
		return parsed
	default:
		return 0
	}
}

func metaString(meta map[string]any, key string) *string {
	switch value := meta[key].(type) {
	case string:
		if value == "" {
			return nil
		}
		return &value
	default:
		return nil
	}
}

type accountDeletedEvent struct {
	AccountID string `json:"account_id"`
}

func (h *handler) handleAccountDeleted(ctx context.Context, data []byte) error {
	var event accountDeletedEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}
	accountID, err := uuid.Parse(event.AccountID)
	if err != nil {
		return err
	}
	var publisherIDs []uuid.UUID
	query := `SELECT p.id FROM publishers p
		WHERE p.deleted_at IS NULL
		AND EXISTS (SELECT 1 FROM publisher_members m WHERE m.publisher_id = p.id)
		AND NOT EXISTS (
			SELECT 1 FROM publisher_members m
			WHERE m.publisher_id = p.id AND m.account_id <> ?
		)`
	if err := h.db.WithContext(ctx).Raw(query, accountID).Scan(&publisherIDs).Error; err != nil {
		return err
	}
	if len(publisherIDs) == 0 {
		return nil
	}
	now := time.Now().UTC()
	return h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&store.PostEntity{}).Where("publisher_id IN ?", publisherIDs).
			Update("deleted_at", now).Error; err != nil {
			return err
		}
		return tx.Model(&store.PublisherEntity{}).Where("id IN ?", publisherIDs).
			Update("deleted_at", now).Error
	})
}

// ---------------------------------------------------------------------------
// Scheduled publishing
// ---------------------------------------------------------------------------

const scheduledPublishBatchSize = 100

func (h *handler) publishScheduledPosts(ctx context.Context) error {
	now := time.Now().UTC()
	var dueIDs []uuid.UUID
	if err := h.db.WithContext(ctx).Model(&store.PostEntity{}).
		Where("publish_notified_at IS NULL").
		Where("fediverse_uri IS NULL").
		Where("drafted_at IS NULL").
		Where("published_at IS NOT NULL AND published_at <= ?", now).
		Order("published_at ASC").
		Limit(scheduledPublishBatchSize).
		Pluck("id", &dueIDs).Error; err != nil {
		return err
	}
	published := 0
	for _, id := range dueIDs {
		result := h.db.WithContext(ctx).Model(&store.PostEntity{}).
			Where("id = ? AND publish_notified_at IS NULL", id).
			Where("fediverse_uri IS NULL AND drafted_at IS NULL").
			Where("published_at IS NOT NULL AND published_at <= ?", now).
			Updates(map[string]any{"publish_notified_at": now, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			continue
		}
		published++
	}
	if published > 0 {
		h.log.Info("posts: published scheduled posts", "count", published)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Sponsor auction
// ---------------------------------------------------------------------------

func (h *handler) runSponsorAuction(ctx context.Context) error {
	now := time.Now().UTC()
	hourStart := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, time.UTC)
	hourEnd := hourStart.Add(time.Hour)

	var existing int64
	if err := h.db.WithContext(ctx).Model(&store.PostSponsorPlacementEntity{}).
		Where("valid_from = ?", hourStart).Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}

	type candidate struct {
		PostID      uuid.UUID `gorm:"column:post_id"`
		TotalAmount float64   `gorm:"column:total_amount"`
	}
	var candidates []candidate
	if err := h.db.WithContext(ctx).Model(&store.PostSponsorBidEntity{}).
		Select("post_id, sum(amount) AS total_amount").
		Where("expires_at > ?", now).
		Group("post_id").Scan(&candidates).Error; err != nil {
		return err
	}
	if len(candidates) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.PostID)
	}
	var validIDs []uuid.UUID
	if err := h.db.WithContext(ctx).Model(&store.PostEntity{}).
		Where("id IN ?", ids).
		Where("visibility = ?", int(model.VisibilityPublic)).
		Where("(shadowban_reason IS NULL OR shadowban_reason = ?)", int(model.ShadowbanNone)).
		Where("publisher_id NOT IN (SELECT id FROM publishers WHERE shadowban_reason IS NOT NULL AND shadowban_reason <> 0)").
		Pluck("id", &validIDs).Error; err != nil {
		return err
	}
	valid := map[uuid.UUID]struct{}{}
	for _, id := range validIDs {
		valid[id] = struct{}{}
	}
	var validCandidates []candidate
	totalWeight := 0.0
	for _, candidate := range candidates {
		if _, ok := valid[candidate.PostID]; !ok {
			continue
		}
		validCandidates = append(validCandidates, candidate)
		totalWeight += candidate.TotalAmount
	}
	if len(validCandidates) == 0 || totalWeight <= 0 {
		return nil
	}
	roll := rand.Float64() * totalWeight
	cumulative := 0.0
	winner := validCandidates[0]
	for _, candidate := range validCandidates {
		cumulative += candidate.TotalAmount
		if roll < cumulative {
			winner = candidate
			break
		}
	}
	placement := store.PostSponsorPlacementEntity{
		ID:          uuid.New(),
		PostID:      winner.PostID,
		TotalAmount: winner.TotalAmount,
		ValidFrom:   hourStart,
		ValidUntil:  hourEnd,
	}
	if err := h.db.WithContext(ctx).Create(&placement).Error; err != nil {
		return err
	}
	h.upsertAggregatedStats(ctx, winner.PostID)
	h.log.Info("posts: sponsor auction winner", "post", winner.PostID, "amount", winner.TotalAmount)
	return nil
}

func (h *handler) upsertAggregatedStats(ctx context.Context, postID uuid.UUID) {
	now := time.Now().UTC()
	var bids []store.PostSponsorBidEntity
	h.db.WithContext(ctx).Where("post_id = ? AND expires_at > ?", postID, now).Find(&bids)
	var placed int64
	h.db.WithContext(ctx).Model(&store.PostSponsorPlacementEntity{}).
		Where("post_id = ? AND valid_from <= ? AND valid_until > ?", postID, now, now).Count(&placed)
	total := 0.0
	for _, bid := range bids {
		total += bid.Amount
	}
	var stats store.PostAggregatedStatsEntity
	err := h.db.WithContext(ctx).Where("post_id = ?", postID).First(&stats).Error
	if isNotFound(err) {
		stats = store.PostAggregatedStatsEntity{ID: uuid.New(), PostID: postID}
		h.db.WithContext(ctx).Create(&stats)
	}
	stats.ActiveBidTotal = total
	stats.ActiveBidCount = len(bids)
	stats.IsCurrentlyPlaced = placed > 0
	if err := h.db.WithContext(ctx).Save(&stats).Error; err != nil {
		h.log.Warn("posts: update aggregated stats failed", "error", err)
	}
}
