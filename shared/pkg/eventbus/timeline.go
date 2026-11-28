package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
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

// BuildTimelineEvent constructs the envelope for one milestone:
// event_type is the timeline.<service>.<eventType> subject, event_id the
// dedupe key consumers persist.
func BuildTimelineEvent(service, eventType, accountID string, occurredAt time.Time, data any) (TimelineEvent, error) {
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
			return TimelineEvent{}, fmt.Errorf("marshal timeline data: %w", err)
		}
		envelope.Data = raw
	}
	return envelope, nil
}

// PublishTimelineEvent publishes a prebuilt envelope (BuildTimelineEvent) on
// its timeline.<service>.<eventType> subject. A nil Bus or one without a
// JetStream context is a no-op, matching the fleet's degraded-events
// convention.
func (b *Bus) PublishTimelineEvent(ctx context.Context, event TimelineEvent) error {
	if b == nil || b.Conn == nil || b.JS == nil {
		return nil
	}
	if event.EventType == "" {
		return fmt.Errorf("timeline event needs an event type")
	}
	return b.PublishJetStream(ctx, event.EventType, TimelineStream, event)
}

// PublishTimeline emits one timeline event on
// timeline.<service>.<eventType>.
func (b *Bus) PublishTimeline(ctx context.Context, service, eventType, accountID string, occurredAt time.Time, data any) error {
	envelope, err := BuildTimelineEvent(service, eventType, accountID, occurredAt, data)
	if err != nil {
		return err
	}
	return b.PublishTimelineEvent(ctx, envelope)
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

// ParsedTimelineEvent is a decoded timeline envelope.
type ParsedTimelineEvent struct {
	EventID    string
	Service    string
	EventType  string
	AccountID  string
	OccurredAt time.Time
	Data       json.RawMessage
}

// ParseTimelineEvent decodes one timeline_events message. It reports ok=false
// for a foreign subject, a malformed envelope, a missing event/account id, or
// an unparseable account id — consumers ack-and-drop those.
func ParseTimelineEvent(payload []byte) (ParsedTimelineEvent, bool) {
	var envelope struct {
		EventID    string          `json:"event_id"`
		EventType  string          `json:"event_type"`
		AccountID  string          `json:"account_id"`
		OccurredAt time.Time       `json:"occurred_at"`
		Timestamp  time.Time       `json:"timestamp"`
		Data       json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return ParsedTimelineEvent{}, false
	}
	service, eventType, ok := SplitTimelineSubject(envelope.EventType)
	if !ok || envelope.EventID == "" || envelope.AccountID == "" {
		return ParsedTimelineEvent{}, false
	}
	if _, err := uuid.Parse(envelope.AccountID); err != nil {
		return ParsedTimelineEvent{}, false
	}
	occurred := envelope.OccurredAt
	if occurred.IsZero() {
		occurred = envelope.Timestamp
	}
	data := envelope.Data
	if len(data) == 0 {
		data = payload
	}
	return ParsedTimelineEvent{
		EventID:    envelope.EventID,
		Service:    service,
		EventType:  eventType,
		AccountID:  envelope.AccountID,
		OccurredAt: occurred,
		Data:       data,
	}, true
}
