package accountevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm/clause"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/eventbus"
	"src.solsynth.dev/sosys/stargate/model"
)

// ───────────────────────── persisted timeline events ─────────────────────────

// timelineEventRow is the parsed, account-scoped shape an ingest source hands
// to the store.
type timelineEventRow struct {
	AccountID  uuid.UUID
	EventType  string
	Source     string
	SourceID   string
	OccurredAt time.Time
	Payload    json.RawMessage
}

func timelineEventFromEntity(entity *TimelineEventEntity) *TimelineEvent {
	return &TimelineEvent{
		Id:        entity.ID.String(),
		CreatedAt: model.Time(entity.CreatedAt),
		EventType: entity.EventType,
		Source:    entity.Source,
		SourceId:  entity.SourceID,
		Payload:   json.RawMessage(entity.Payload),
	}
}

// InsertTimelineEvent persists one ingested event. CreatedAt is the event's
// own occurred-at instant, not the ingestion time, so the timeline orders
// rows by when they happened. The (source, source_id) unique index makes the
// JetStream at-least-once delivery idempotent: a redelivered message inserts
// nothing. Duplicate reports inserted=false with a nil error.
func (s *Store) InsertTimelineEvent(ctx context.Context, row timelineEventRow) (bool, error) {
	switch {
	case row.AccountID == uuid.Nil:
		return false, errors.New("timeline event needs an account id")
	case row.EventType == "":
		return false, errors.New("timeline event needs an event type")
	case row.Source == "" || row.SourceID == "":
		return false, errors.New("timeline event needs a source and source id")
	}
	occurred := row.OccurredAt
	if occurred.IsZero() {
		occurred = time.Now().UTC()
	}
	entity := TimelineEventEntity{
		ID:        uuid.New(),
		AccountID: row.AccountID,
		EventType: row.EventType,
		Source:    row.Source,
		SourceID:  row.SourceID,
		Payload:   datatypes.JSON(row.Payload),
	}
	entity.CreatedAt = occurred
	entity.UpdatedAt = occurred
	result := s.DB.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&entity)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// ListTimelineEvents returns the account's persisted timeline events, newest
// first. Service.Timeline merges them with statuses and presence activities.
func (s *Store) ListTimelineEvents(ctx context.Context, accountID uuid.UUID) ([]TimelineEvent, error) {
	var entities []TimelineEventEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ? AND deleted_at IS NULL", accountID).
		Order("created_at DESC").
		Find(&entities).Error; err != nil {
		return nil, err
	}
	events := make([]TimelineEvent, 0, len(entities))
	for i := range entities {
		events = append(events, *timelineEventFromEntity(&entities[i]))
	}
	return events, nil
}

// ───────────────────────────── JetStream ingest ─────────────────────────────

// EventConsumer is the JetStream consume surface the ingester needs.
// *nats.Client (Stargate) and *eventbus.Client (the shared platform) both
// implement it.
type EventConsumer interface {
	Consume(ctx context.Context, stream, subject, consumerName string, handler func(payload []byte) error) error
}

// TimelineEventSource is one JetStream subject folded into the persisted
// timeline log. Parse maps a message to an account-scoped row; ok=false acks
// and drops the message (an unrelated action, a malformed payload).
type TimelineEventSource struct {
	Stream   string
	Subject  string
	Consumer string
	Parse    func(payload []byte) (timelineEventRow, bool)
}

// TimelineRecorder is the write path Stargate's own modules use for timeline
// events: it persists the row in-process (so the timeline works even when no
// message broker is configured) and echoes the same envelope onto the fleet
// stream for other consumers. The accountevents ingest consumer receives that
// echo and dedupes it on the shared event_id, so exactly one row lands.
//
// It is published as the module's `timeline` port.
type TimelineRecorder struct {
	Store *Store
	// Events is the fleet bus. Nil — NATS disabled — skips the echo; the row
	// is still recorded.
	Events *eventbus.Bus
}

// Record persists one account timeline event and publishes its envelope.
func (r *TimelineRecorder) Record(ctx context.Context, service, eventType, accountID string, occurredAt time.Time, data any) error {
	if r == nil || r.Store == nil {
		return nil
	}
	envelope, err := eventbus.BuildTimelineEvent(service, eventType, accountID, occurredAt, data)
	if err != nil {
		return err
	}
	source, storedType, ok := eventbus.SplitTimelineSubject(envelope.EventType)
	if !ok {
		return fmt.Errorf("timeline event has an invalid subject %q", envelope.EventType)
	}
	accountUUID, err := uuid.Parse(envelope.AccountID)
	if err != nil {
		return fmt.Errorf("timeline event has an invalid account id: %w", err)
	}
	payload := envelope.Data
	if len(payload) == 0 {
		encoded, err := json.Marshal(envelope)
		if err != nil {
			return fmt.Errorf("marshal timeline envelope: %w", err)
		}
		payload = encoded
	}
	if _, err := r.Store.InsertTimelineEvent(ctx, timelineEventRow{
		AccountID:  accountUUID,
		EventType:  storedType,
		Source:     source,
		SourceID:   envelope.EventID,
		OccurredAt: envelope.OccurredAt,
		Payload:    payload,
	}); err != nil {
		return err
	}
	return r.Events.PublishTimelineEvent(ctx, envelope)
}

// parseTimelineEnvelope folds one timeline_events message into a row via the
// shared protocol parser. The subject (mirrored in the envelope event_type,
// timeline.<service>.<event>) names the source and event type; the envelope
// event_id is the dedupe key. Anything that is not a timeline event, or
// lacks an account/event id, acks-and-drops.
func parseTimelineEnvelope(payload []byte) (timelineEventRow, bool) {
	parsed, ok := eventbus.ParseTimelineEvent(payload)
	if !ok {
		return timelineEventRow{}, false
	}
	accountID, err := uuid.Parse(parsed.AccountID)
	if err != nil {
		return timelineEventRow{}, false
	}
	return timelineEventRow{
		AccountID:  accountID,
		EventType:  parsed.EventType,
		Source:     parsed.Service,
		SourceID:   parsed.EventID,
		OccurredAt: parsed.OccurredAt,
		Payload:    parsed.Data,
	}, true
}

// TimelineEventSources lists the JetStream subjects folded into the
// persisted timeline log: the fleet timeline protocol wildcard. A service
// joins by publishing on timeline.<service>.<event> — no consumer change.
func TimelineEventSources() []TimelineEventSource {
	return []TimelineEventSource{
		{
			Stream:   eventbus.TimelineStream,
			Subject:  eventbus.TimelineSubjectPrefix + ">",
			Consumer: "stargate_account_timeline",
			Parse:    parseTimelineEnvelope,
		},
	}
}

// StartTimelineEventIngest runs one durable consumer per source, persisting
// every mapped event. It returns a stop function that cancels the consumers
// and waits for their loops to exit.
func StartTimelineEventIngest(ctx context.Context, consumer EventConsumer, store *Store, log *slog.Logger, sources []TimelineEventSource) func() {
	if log == nil {
		log = slog.Default()
	}
	ingestCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for _, source := range sources {
		source := source
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := consumer.Consume(ingestCtx, source.Stream, source.Subject, source.Consumer, func(payload []byte) error {
				row, ok := source.Parse(payload)
				if !ok {
					return nil
				}
				inserted, err := store.InsertTimelineEvent(ingestCtx, row)
				if err != nil {
					// Store errors stay unacked for redelivery, mirroring the
					// fleet's at-least-once consumer convention.
					log.Warn("timeline ingest: store event",
						"subject", source.Subject, "source_id", row.SourceID, "error", err)
					return err
				}
				if inserted {
					log.Debug("timeline ingest: stored event",
						"subject", source.Subject, "account_id", row.AccountID,
						"event_type", row.EventType)
				}
				return nil
			})
			if err != nil && ingestCtx.Err() == nil {
				log.Warn("timeline ingest consumer stopped", "subject", source.Subject, "error", err)
			}
		}()
	}
	return func() {
		cancel()
		wg.Wait()
	}
}
