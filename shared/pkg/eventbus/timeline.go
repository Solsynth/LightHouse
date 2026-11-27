package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The fleet's timeline protocol: a dedicated JetStream stream services
// publish account-visible milestones to, independent of the action-log
// progression channel. Every publisher picks a
// timeline.<service>.<event-type> subject and sends a TimelineEvent
// envelope; consumers (Stargate's persisted account timeline) ingest the
// whole timeline.> wildcard, so a new service joins by publishing — no
// consumer change.

// TimelineStream is the JetStream stream every timeline event lands on.
const TimelineStream = "timeline_events"

// TimelineSubjectPrefix heads every timeline subject.
const TimelineSubjectPrefix = "timeline."

// TimelineEvent is the wire envelope of the timeline_events stream. Event
// carries the fleet envelope (event_id/timestamp/event_type/stream_name);
// event_id doubles as the consumer-side dedupe key, so publishers must
// always build it with NewEvent.
type TimelineEvent struct {
	Event
	// AccountID is the account the milestone belongs to.
	AccountID string `json:"account_id"`
	// OccurredAt is when the milestone happened, not when it was published
	// (zero falls back to the envelope Timestamp).
	OccurredAt time.Time `json:"occurred_at,omitempty"`
	// Data is the service-specific payload the timeline surfaces verbatim.
	Data json.RawMessage `json:"data,omitempty"`
}

// PublishTimeline emits one timeline event on
// timeline.<service>.<eventType>. A nil Bus or one without a JetStream
// context is a no-op, matching the fleet's degraded-events convention.
func (b *Bus) PublishTimeline(ctx context.Context, service, eventType, accountID string, occurredAt time.Time, data any) error {
	if b == nil || b.Conn == nil || b.JS == nil {
		return nil
	}
	subject := TimelineSubjectPrefix + service + "." + eventType
	envelope := TimelineEvent{
		Event:      NewEvent(TimelineStream, subject),
		AccountID:  accountID,
		OccurredAt: occurredAt,
	}
	if envelope.OccurredAt.IsZero() {
		envelope.OccurredAt = envelope.Timestamp
	}
	if data != nil {
		raw, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("marshal timeline data: %w", err)
		}
		envelope.Data = raw
	}
	return b.PublishJetStream(ctx, subject, TimelineStream, envelope)
}

// SplitTimelineSubject splits timeline.<service>.<event-type...> into its
// service and event-type segments; ok=false for foreign subjects.
func SplitTimelineSubject(subject string) (service, eventType string, ok bool) {
	rest, found := strings.CutPrefix(subject, TimelineSubjectPrefix)
	if !found {
		return "", "", false
	}
	service, eventType, ok = strings.Cut(rest, ".")
	if !ok || service == "" || eventType == "" {
		return "", "", false
	}
	return service, eventType, true
}
