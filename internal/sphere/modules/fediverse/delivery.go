package fediverse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// Delivery statuses mirror the C# DeliveryStatus enum.
const (
	deliveryPending          = 0
	deliveryProcessing       = 1
	deliverySent             = 2
	deliveryFailed           = 3
	deliveryExhaustedRetries = 4
)

type deliveryService struct {
	s *server
}

func newDeliveryService(s *server) *deliveryService { return &deliveryService{s: s} }

// enqueue records the delivery and attempts it immediately, exactly like
// ActivityPubDeliveryService.EnqueueActivityDeliveryAsync.
func (d *deliveryService) enqueue(ctx context.Context, activityType string, activity map[string]any,
	actorURI, inboxURI, activityID string, signActor *store.PublisherEntity) (bool, error) {
	if activityID == "" {
		activityID = getString(activity, "id")
	}
	if activityID == "" {
		activityID = uuid.NewString()
	}
	raw, err := json.Marshal(activity)
	if err != nil {
		return false, err
	}
	payload := string(raw)
	delivery := &store.ActivityPubDeliveryEntity{
		ID:              uuid.New(),
		ActivityID:      activityID,
		ActivityType:    activityType,
		InboxURI:        inboxURI,
		ActorURI:        actorURI,
		Status:          deliveryPending,
		ActivityPayload: &payload,
	}
	if err := d.s.db.WithContext(ctx).Create(delivery).Error; err != nil {
		return false, err
	}

	attemptCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	now := time.Now().UTC()
	delivery.Status = deliveryProcessing
	delivery.LastAttemptAt = &now
	_ = d.s.db.WithContext(ctx).Save(delivery).Error

	status, body, retryAfter, err := d.send(attemptCtx, raw, inboxURI, actorURI, signActor)
	if err == nil && status >= 200 && status < 300 {
		sent := time.Now().UTC()
		delivery.Status = deliverySent
		delivery.SentAt = &sent
		delivery.ResponseStatusCode = strconv.Itoa(status)
		_ = d.s.db.WithContext(ctx).Save(delivery).Error
		d.s.log.Info("delivered activity", "type", activityType, "id", activityID, "inbox", inboxURI, "status", status)
		return true, nil
	}

	reason := body
	if err != nil {
		reason = err.Error()
	}
	delivery.ErrorMessage = &reason
	if status > 0 {
		delivery.ResponseStatusCode = strconv.Itoa(status)
	}
	if shouldRetryStatus(status) && delivery.RetryCount < d.maxRetries() {
		delivery.Status = deliveryFailed
		delivery.RetryCount++
		next := nextRetryAt(delivery.RetryCount, retryAfter)
		delivery.NextRetryAt = &next
		_ = d.s.db.WithContext(ctx).Save(delivery).Error
		d.s.log.Warn("delivery failed; scheduled retry", "type", activityType, "id", activityID,
			"inbox", inboxURI, "status", status, "retry", delivery.RetryCount, "next", next)
		return true, nil
	}
	delivery.Status = deliveryExhaustedRetries
	_ = d.s.db.WithContext(ctx).Save(delivery).Error
	d.s.log.Error("delivery exhausted retries", "type", activityType, "id", activityID, "inbox", inboxURI, "status", status)
	return true, nil
}

// send signs and POSTs one activity document.
func (d *deliveryService) send(ctx context.Context, raw []byte, inboxURI, actorURI string, signActor *store.PublisherEntity) (int, string, time.Duration, error) {
	if err := validateOutboundURL(inboxURI); err != nil {
		return 0, "", 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, inboxURI, bytes.NewReader(raw))
	if err != nil {
		return 0, "", 0, err
	}
	req.Header.Set("Content-Type", "application/activity+json")
	req.Header.Set("Accept", "application/activity+json, application/ld+json")
	req.Header.Set("User-Agent", "DysonNetwork/1.0 (Lighthouse)")
	if err := d.s.signRequestAs(ctx, req, raw, signActor); err != nil {
		return 0, "", 0, fmt.Errorf("sign delivery: %w", err)
	}
	resp, err := d.s.http.Do(req)
	if err != nil {
		return 0, "", 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	text := string(body)
	if len(text) > 500 {
		text = text[:500]
	}
	return resp.StatusCode, text, parseRetryAfter(resp.Header.Get("Retry-After")), nil
}

func (d *deliveryService) maxRetries() int {
	maxRetries := d.s.core.Config.ActivityPubDelivery.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 5
	}
	return maxRetries
}

// retryFailed is DeliveryRetryJob: failed deliveries whose next_retry_at has
// passed are re-attempted, and exhausted ones move to the dead letters.
func (d *deliveryService) retryFailed(ctx context.Context) error {
	now := time.Now().UTC()
	var deliveries []store.ActivityPubDeliveryEntity
	if err := d.s.db.WithContext(ctx).
		Where("status = ? AND next_retry_at IS NOT NULL AND next_retry_at <= ?", deliveryFailed, now).
		Order("next_retry_at ASC").
		Limit(500).
		Find(&deliveries).Error; err != nil {
		return err
	}
	if len(deliveries) == 0 {
		return nil
	}
	for i := range deliveries {
		delivery := &deliveries[i]
		if delivery.RetryCount >= d.maxRetries() {
			d.moveToDeadLetter(ctx, delivery, "Max retries exceeded")
			continue
		}
		raw := []byte("{}")
		if delivery.ActivityPayload != nil {
			raw = []byte(*delivery.ActivityPayload)
		}
		var signActor *store.PublisherEntity
		if actor, err := d.s.discovery.actorByURI(ctx, delivery.ActorURI); err == nil {
			signActor = actor
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		status, body, retryAfter, err := d.send(attemptCtx, raw, delivery.InboxURI, delivery.ActorURI, signActor)
		cancel()
		attempted := time.Now().UTC()
		delivery.LastAttemptAt = &attempted
		if err == nil && status >= 200 && status < 300 {
			delivery.Status = deliverySent
			delivery.SentAt = &attempted
			delivery.ResponseStatusCode = strconv.Itoa(status)
			delivery.ErrorMessage = nil
			delivery.NextRetryAt = nil
		} else {
			reason := body
			if err != nil {
				reason = err.Error()
			}
			delivery.ErrorMessage = &reason
			if status > 0 {
				delivery.ResponseStatusCode = strconv.Itoa(status)
			}
			delivery.RetryCount++
			if delivery.RetryCount >= d.maxRetries() {
				delivery.Status = deliveryExhaustedRetries
				delivery.NextRetryAt = nil
			} else {
				delivery.Status = deliveryFailed
				next := nextRetryAt(delivery.RetryCount, retryAfter)
				delivery.NextRetryAt = &next
			}
		}
		if err := d.s.db.WithContext(ctx).Save(delivery).Error; err != nil {
			d.s.log.Warn("failed to save delivery retry", "id", delivery.ID, "error", err)
		}
	}
	return nil
}

// moveToDeadLetter mirrors DeliveryDeadLetterService.MoveToDeadLetterAsync.
func (d *deliveryService) moveToDeadLetter(ctx context.Context, delivery *store.ActivityPubDeliveryEntity, reason string) {
	payload := "{}"
	if delivery.ActivityPayload != nil {
		payload = *delivery.ActivityPayload
	}
	errorText := reason
	dead := &store.DeliveryDeadLetterEntity{
		ID:              uuid.New(),
		DeliveryID:      delivery.ID,
		InboxURI:        delivery.InboxURI,
		ActorURI:        delivery.ActorURI,
		ActivityType:    delivery.ActivityType,
		ActivityPayload: payload,
		ErrorMessage:    &errorText,
		RetryCount:      delivery.RetryCount,
		CreatedAt:       delivery.CreatedAt,
		FailedAt:        time.Now().UTC(),
	}
	if err := d.s.db.WithContext(ctx).Create(dead).Error; err != nil {
		d.s.log.Error("failed to create dead letter", "delivery_id", delivery.ID, "error", err)
		return
	}
	if err := d.s.db.WithContext(ctx).Delete(delivery).Error; err != nil {
		d.s.log.Error("failed to delete exhausted delivery", "delivery_id", delivery.ID, "error", err)
	}
	d.s.log.Warn("moved delivery to dead letter queue", "delivery_id", delivery.ID, "error", reason)
}

// cleanup is DeliveryCleanupJob: seven-day retention for terminal deliveries
// and thirty-day retention for dead letters.
func (d *deliveryService) cleanup(ctx context.Context) error {
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour)
	var stale []store.ActivityPubDeliveryEntity
	if err := d.s.db.WithContext(ctx).
		Where("status IN ? AND created_at < ?", []int{deliverySent, deliveryExhaustedRetries}, cutoff).
		Order("created_at ASC").
		Limit(1000).
		Find(&stale).Error; err != nil {
		return err
	}
	for i := range stale {
		_ = d.s.db.WithContext(ctx).Delete(&stale[i]).Error
	}
	if len(stale) > 0 {
		d.s.log.Info("cleaned up old ActivityPub deliveries", "count", len(stale))
	}
	deadCutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)
	var oldDead []store.DeliveryDeadLetterEntity
	if err := d.s.db.WithContext(ctx).Where("failed_at < ?", deadCutoff).Find(&oldDead).Error; err != nil {
		return err
	}
	for i := range oldDead {
		_ = d.s.db.WithContext(ctx).Delete(&oldDead[i]).Error
	}
	if len(oldDead) > 0 {
		d.s.log.Info("purged old dead letters", "count", len(oldDead))
	}
	return nil
}

// healthCheck is DeliveryHealthCheckJob: it reports queue health and flags
// pending rows that are older than an hour.
func (d *deliveryService) healthCheck(ctx context.Context) error {
	counts := map[int]int64{}
	for _, status := range []int{deliveryPending, deliveryProcessing, deliverySent, deliveryFailed, deliveryExhaustedRetries} {
		var count int64
		if err := d.s.db.WithContext(ctx).Model(&store.ActivityPubDeliveryEntity{}).
			Where("status = ?", status).Count(&count).Error; err != nil {
			return err
		}
		counts[status] = count
	}
	var deadLetters int64
	if err := d.s.db.WithContext(ctx).Model(&store.DeliveryDeadLetterEntity{}).Count(&deadLetters).Error; err != nil {
		return err
	}
	var pendingOld int64
	if err := d.s.db.WithContext(ctx).Model(&store.ActivityPubDeliveryEntity{}).
		Where("status = ? AND created_at < ?", deliveryPending, time.Now().UTC().Add(-time.Hour)).
		Count(&pendingOld).Error; err != nil {
		return err
	}
	d.s.log.Info("activitypub delivery health",
		"delivered", counts[deliverySent],
		"failed", counts[deliveryFailed],
		"pending", counts[deliveryPending]+counts[deliveryProcessing],
		"dead_letters", deadLetters,
		"pending_older_than_hour", pendingOld,
	)
	if deadLetters > 100 {
		d.s.log.Warn("high dead letter count", "count", deadLetters)
	}
	if pendingOld > 0 {
		d.s.log.Warn("pending deliveries older than an hour", "count", pendingOld)
	}
	return nil
}

// ---- follow helpers -------------------------------------------------------

func (d *deliveryService) sendFollow(ctx context.Context, publisherID uuid.UUID, targetActorURI string) bool {
	var local store.PublisherEntity
	if err := d.s.db.WithContext(ctx).Where("id = ? AND uri IS NOT NULL", publisherID).First(&local).Error; err != nil {
		return false
	}
	target, err := d.s.discovery.actorByURI(ctx, targetActorURI)
	if err != nil || target.InboxURI == nil {
		if target, err = d.s.discovery.getOrCreateActor(ctx, targetActorURI, "", nil); err != nil || target.InboxURI == nil {
			return false
		}
	}
	activityID := *local.URI + "/follows/" + uuid.NewString()
	activity := map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       activityID,
		"type":     "Follow",
		"actor":    *local.URI,
		"object":   targetActorURI,
	}

	var existing store.PublisherSubscriptionEntity
	err = d.s.db.WithContext(ctx).
		Where("follower_publisher_id = ? AND publisher_id = ?", local.ID, target.ID).
		First(&existing).Error
	now := time.Now().UTC()
	if errors.Is(err, errNotFound) {
		existing = store.PublisherSubscriptionEntity{
			ID:                  uuid.New(),
			AccountID:           local.AccountID,
			FollowerPublisherID: &local.ID,
			PublisherID:         target.ID,
			State:               int(model.PublisherSubscriptionStatePending),
			FollowedAt:          &now,
		}
		if createErr := d.s.db.WithContext(ctx).Create(&existing).Error; createErr != nil {
			return false
		}
	} else if err == nil {
		existing.State = int(model.PublisherSubscriptionStatePending)
		existing.EndedAt = nil
		_ = d.s.db.WithContext(ctx).Save(&existing).Error
	} else {
		return false
	}

	ok, _ := d.enqueue(ctx, "Follow", activity, *local.URI, *target.InboxURI, activityID, &local)
	return ok
}

func (d *deliveryService) sendUnfollow(ctx context.Context, publisherID uuid.UUID, targetActorURI string) bool {
	var local store.PublisherEntity
	if err := d.s.db.WithContext(ctx).Where("id = ? AND uri IS NOT NULL", publisherID).First(&local).Error; err != nil {
		return false
	}
	target, err := d.s.discovery.actorByURI(ctx, targetActorURI)
	if err != nil || target.InboxURI == nil {
		return false
	}
	var relationship store.PublisherSubscriptionEntity
	if err := d.s.db.WithContext(ctx).
		Where("follower_publisher_id = ? AND publisher_id = ?", local.ID, target.ID).
		First(&relationship).Error; err != nil {
		return false
	}
	activityID := *local.URI + "/undo/" + uuid.NewString()
	activity := map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       activityID,
		"type":     "Undo",
		"actor":    *local.URI,
		"object": map[string]any{
			"type":   "Follow",
			"actor":  *local.URI,
			"object": targetActorURI,
		},
	}
	ok, _ := d.enqueue(ctx, "Undo", activity, *local.URI, *target.InboxURI, activityID, &local)
	if ok {
		_ = d.s.db.WithContext(ctx).Delete(&relationship).Error
	}
	return ok
}

// sendAccept answers a remote Follow.
func (d *deliveryService) sendAccept(ctx context.Context, signActor *store.PublisherEntity, actorURI, inboxURI, followActorURI string) {
	activityID := actorURI + "/accepts/" + uuid.NewString()
	activity := map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       activityID,
		"type":     "Accept",
		"actor":    actorURI,
		"object": map[string]any{
			"type":   "Follow",
			"actor":  followActorURI,
			"object": actorURI,
		},
	}
	_, _ = d.enqueue(ctx, "Accept", activity, actorURI, inboxURI, activityID, signActor)
}

// ---- retry math -----------------------------------------------------------

func shouldRetryStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func parseRetryAfter(header string) time.Duration {
	if header == "" {
		return 0
	}
	if seconds, err := strconv.ParseFloat(header, 64); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(header); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return 0
}

// nextRetryAt mirrors CalculateNextRetryAt: exponential backoff capped at 15
// minutes plus up to 30% jitter, or the server's Retry-After when supplied.
func nextRetryAt(retryCount int, retryAfter time.Duration) time.Time {
	const maxDelay = 900.0
	var delay float64
	if retryAfter > 0 {
		delay = math.Min(maxDelay, math.Max(1, retryAfter.Seconds()))
	} else {
		delay = math.Min(maxDelay, math.Pow(2, float64(retryCount-1)))
	}
	jitter := rand.Float64() * 0.3 * delay
	return time.Now().UTC().Add(time.Duration((delay + jitter) * float64(time.Second)))
}

