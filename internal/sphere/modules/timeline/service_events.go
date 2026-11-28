package timeline

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
	timelinebus "src.solsynth.dev/sosys/lighthouse/shared/pkg/eventbus"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/eventbus"
)

// feedServiceEventTypes are the fleet timeline event types mixed into the
// explorable feed. Profile/name changes stay in the account timeline: they are
// not discovery material, and scrobbles are too high-volume for a feed row.
var feedServiceEventTypes = []string{
	"realm.created",
	"realm.joined",
	"work.reviewed",
	"work.review_deleted",
	"work.status_updated",
}

// serviceFeedEvents loads the folded timeline events that belong in one feed
// page, rendered as `service.event` rows. It never fails the feed: a database
// error logs and yields no rows.
func (s *service) serviceFeedEvents(ctx context.Context, cursor *time.Time, take int) []model.TimelineEvent {
	if s.events == nil {
		return nil
	}
	events, err := s.events.ListTimelineServiceEventsBefore(ctx, feedServiceEventTypes, cursor, take)
	if err != nil {
		s.log.Warn("timeline: read service events", "error", err)
		return nil
	}
	out := make([]model.TimelineEvent, 0, len(events))
	for i := range events {
		event := events[i]
		created := model.Time(event.OccurredAt.UTC())
		out = append(out, model.TimelineEvent{
			ID:                 event.ID.String(),
			Type:               "service.event",
			ResourceIdentifier: event.ID.String(),
			// Everything a client needs travels in `data`: the frozen SDK
			// models (Solian) carry no `meta` field.
			Data: map[string]any{
				"source":     event.Source,
				"event_type": event.EventType,
				"account_id": event.AccountID.String(),
				"payload":    json.RawMessage(event.Payload),
			},
			CreatedAt: &created,
			UpdatedAt: &created,
		})
	}
	return out
}

// consumeTimelineEvents folds the fleet `timeline_events` stream into this
// service's own timeline_service_events table so the feed can join it without
// a cross-service read. It runs until ctx is cancelled.
func (s *service) consumeTimelineEvents(ctx context.Context, bus *eventbus.Client) {
	if bus == nil || !bus.Enabled() || s.events == nil {
		return
	}
	err := bus.Consume(ctx, timelinebus.TimelineStream, timelinebus.TimelineSubjectPrefix+">", "sphere_timeline_events", func(raw []byte) error {
		parsed, ok := timelinebus.ParseTimelineEvent(raw)
		if !ok {
			return nil
		}
		accountID, err := uuid.Parse(parsed.AccountID)
		if err != nil {
			return nil
		}
		if _, err := s.events.InsertTimelineServiceEvent(ctx, store.TimelineServiceEvent{
			AccountID:  accountID,
			EventType:  parsed.EventType,
			Source:     parsed.Service,
			SourceID:   parsed.EventID,
			OccurredAt: parsed.OccurredAt,
			Payload:    parsed.Data,
		}); err != nil {
			s.log.Warn("timeline: store service event", "event_id", parsed.EventID, "error", err)
			return err
		}
		return nil
	})
	if err != nil && ctx.Err() == nil {
		s.log.Warn("timeline: service event consumer stopped", "error", err)
	}
}
