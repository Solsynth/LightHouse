package analytics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// errInvalidEvent is returned for events missing their identity fields.
var errInvalidEvent = errors.New("analytics: kind, resource and event are required")

// enqueue validates and buffers one event. The send never blocks: when the
// writer cannot keep up, the event is dropped and counted for the next flush.
// Classification runs on the flush goroutine, off the request path.
func (h *handler) enqueue(event ports.AnalyticsEvent) error {
	event.Kind = clip(strings.TrimSpace(event.Kind), kindLimit)
	event.ResourceID = clip(strings.TrimSpace(event.ResourceID), resourceLimit)
	event.Event = clip(strings.TrimSpace(event.Event), eventLimit)
	if event.Kind == "" || event.ResourceID == "" || event.Event == "" {
		return errInvalidEvent
	}
	select {
	case h.events <- event:
		return nil
	default:
		h.dropped.Add(1)
		return nil
	}
}

// flush drains the buffer into the event table in batches. A failed batch is
// logged and dropped; the events still buffered stay queued for the next run,
// so a short database outage costs at most one batch.
func (h *handler) flush(ctx context.Context) error {
	if h.db == nil {
		return nil
	}
	h.flushMu.Lock()
	defer h.flushMu.Unlock()
	var firstErr error
	for {
		batch := h.drainBatch()
		if len(batch) == 0 {
			break
		}
		if err := h.db.WithContext(ctx).CreateInBatches(batch, h.batchSize).Error; err != nil {
			h.log.Error("analytics: flush failed", "events", len(batch), "error", err)
			firstErr = err
			break
		}
	}
	if dropped := h.dropped.Swap(0); dropped > 0 {
		h.log.Warn("analytics: events dropped, buffer full", "events", dropped)
	}
	return firstErr
}

// drainBatch converts up to batchSize buffered events into rows.
func (h *handler) drainBatch() []store.AnalyticsEventEntity {
	batch := make([]store.AnalyticsEventEntity, 0, h.batchSize)
	for len(batch) < h.batchSize {
		select {
		case event := <-h.events:
			batch = append(batch, h.toEntity(event))
		default:
			return batch
		}
	}
	return batch
}

// toEntity classifies the visitor and freezes one event row.
func (h *handler) toEntity(event ports.AnalyticsEvent) store.AnalyticsEventEntity {
	visitor := event.Visitor
	info := classifyClient(visitor.UserAgent)
	entity := store.AnalyticsEventEntity{
		ID:          uuid.New(),
		Kind:        clip(event.Kind, kindLimit),
		ResourceID:  clip(event.ResourceID, resourceLimit),
		Event:       clip(event.Event, eventLimit),
		VisitorID:   clip(strings.TrimSpace(visitor.VisitorID), 36),
		IPHash:      hashIP(visitor.IP, h.ipPepper),
		UserAgent:   clip(visitor.UserAgent, uaLimit),
		ClientClass: info.Class,
		ClientName:  clip(info.Name, clientNameLimit),
		Platform:    clip(info.Platform, platformLimit),
		IsMobile:    info.Mobile,
		Referrer:    clip(visitor.Referrer, referrerLimit),
		CreatedAt:   time.Now().UTC(),
	}
	if id, err := uuid.Parse(strings.TrimSpace(visitor.AccountID)); err == nil && id != uuid.Nil {
		entity.AccountID = &id
	}
	return entity
}

// retentionSweep deletes expired events in bounded batches so the delete never
// holds a long transaction. A negative retention keeps events forever.
func (h *handler) retentionSweep(ctx context.Context) error {
	if h.db == nil || h.retention <= 0 {
		return nil
	}
	cutoff := time.Now().UTC().Add(-h.retention)
	for {
		result := h.db.WithContext(ctx).Exec(
			`DELETE FROM analytics_events WHERE id IN (SELECT id FROM analytics_events WHERE created_at < ? LIMIT ?)`,
			cutoff, h.batchSize)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected < int64(h.batchSize) {
			return nil
		}
	}
}

// hashIP stores a truncated keyed SHA-256 digest: enough to count distinct
// addresses, not enough to recover the address without the pepper.
func hashIP(ip, pepper string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(ip + "|" + pepper))
	return hex.EncodeToString(sum[:16])
}

// clip keeps a value within limit bytes on a UTF-8 boundary and replaces
// invalid sequences so the text column always accepts it.
func clip(value string, limit int) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && value[cut]&0xC0 == 0x80 {
		cut--
	}
	return value[:cut]
}
