package accountevents

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ─────────────────────────── harnesses ───────────────────────────

func newTimelineStore(t *testing.T) *Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&TimelineEventEntity{}); err != nil {
		t.Fatalf("migrate timeline events: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS ux_account_timeline_events_source
		 ON account_timeline_events (source, source_id)`).Error; err != nil {
		t.Fatalf("apply source index: %v", err)
	}
	return NewStore(db)
}

// stubConsumer captures the handler each source registered so a test can feed
// payloads without a broker.
type stubConsumer struct {
	handlers map[string]func(payload []byte) error
}

func (s *stubConsumer) Consume(_ context.Context, _, subject, _ string, handler func(payload []byte) error) error {
	if s.handlers == nil {
		s.handlers = map[string]func(payload []byte) error{}
	}
	s.handlers[subject] = handler
	return nil
}

func feed(t *testing.T, consumer *stubConsumer, store *Store, subject string, payload any) error {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	// The single timeline source registers under the timeline.> wildcard;
	// resolve its handler whatever concrete subject the test names.
	handler, ok := consumer.handlers[subject]
	if !ok {
		if len(consumer.handlers) != 1 {
			t.Fatalf("no handler registered for %q", subject)
		}
		for _, registered := range consumer.handlers {
			handler = registered
		}
	}
	return handler(raw)
}

func countEvents(t *testing.T, store *Store, accountID uuid.UUID, eventType string) int {
	t.Helper()
	events, err := store.ListTimelineEvents(context.Background(), accountID)
	if err != nil {
		t.Fatalf("list timeline events: %v", err)
	}
	count := 0
	for _, event := range events {
		if event.EventType == eventType {
			count++
		}
	}
	return count
}

func startIngest(t *testing.T, consumer *stubConsumer, store *Store, sources []TimelineEventSource) {
	t.Helper()
	StartTimelineEventIngest(context.Background(), consumer, store, nil, sources)
	// The ingesters register from goroutines; wait until every subject is
	// captured so feeding a payload cannot race the registration.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(consumer.handlers) < len(sources) {
		time.Sleep(time.Millisecond)
	}
	if len(consumer.handlers) < len(sources) {
		t.Fatalf("ingest registered %d of %d consumers", len(consumer.handlers), len(sources))
	}
}

// ─────────────────────────── ingest tests ───────────────────────────

// envelope builds a fleet timeline protocol payload the way
// eventbus.Bus.PublishTimeline writes it.
func envelope(service, eventType, accountID string, occurredAt time.Time, data string) map[string]any {
	payload := map[string]any{
		"event_id":    uuid.NewString(),
		"event_type":  "timeline." + service + "." + eventType,
		"stream_name": "timeline_events",
		"account_id":  accountID,
		"occurred_at": occurredAt.UTC(),
		"timestamp":   time.Now().UTC(),
	}
	if data != "" {
		payload["data"] = json.RawMessage(data)
	}
	return payload
}

// TestIngestFoldsTimelineEnvelope pins the protocol mapping: one
// timeline_events message becomes a persisted row keyed by the envelope
// event_id, typed and sourced from the subject segments, carrying the
// envelope data verbatim and the envelope occurred_at as its timestamp.
func TestIngestFoldsTimelineEnvelope(t *testing.T) {
	store := newTimelineStore(t)
	consumer := &stubConsumer{}
	startIngest(t, consumer, store, TimelineEventSources())

	account := uuid.NewString()
	occurred := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	if err := feed(t, consumer, store, "timeline.stargate.realm.created",
		envelope("stargate", "realm.created", account, occurred, `{"slug":"lighthouse"}`)); err != nil {
		t.Fatalf("handler delivery: %v", err)
	}

	id, _ := uuid.Parse(account)
	events, err := store.ListTimelineEvents(context.Background(), id)
	if err != nil {
		t.Fatalf("list timeline events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	event := events[0]
	if event.EventType != "realm.created" || event.Source != "stargate" {
		t.Fatalf("event = %+v, want realm.created from stargate", event)
	}
	if !time.Time(event.CreatedAt).Equal(occurred) {
		t.Fatalf("created_at = %v, want the envelope occurred_at", event.CreatedAt)
	}
	var data struct {
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(event.Payload, &data); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if data.Slug != "lighthouse" {
		t.Fatalf("payload.slug = %q, want lighthouse", data.Slug)
	}
}

// TestIngestDropsForeignAndBadPayloads pins the ack-and-drop paths: foreign
// event types, missing ids, malformed envelopes and unparseable account ids
// never become timeline rows; the valid control case still lands.
func TestIngestDropsForeignAndBadPayloads(t *testing.T) {
	store := newTimelineStore(t)
	consumer := &stubConsumer{}
	startIngest(t, consumer, store, TimelineEventSources())

	account := uuid.NewString()
	drops := []map[string]any{
		// A foreign event_type (the old action-log channel, for example).
		{"event_id": uuid.NewString(), "event_type": "action_logs.realms.create", "account_id": account},
		// Missing the event id: no dedupe key.
		{"event_type": "timeline.stargate.realm.created", "account_id": account},
		// Missing the account: not attributable.
		{"event_id": uuid.NewString(), "event_type": "timeline.stargate.realm.created"},
		// A malformed account id.
		{"event_id": uuid.NewString(), "event_type": "timeline.stargate.realm.created", "account_id": "not-a-uuid"},
	}
	for _, payload := range drops {
		if err := feed(t, consumer, store, "timeline.stargate.realm.created", payload); err != nil {
			t.Fatalf("drop-case handler: %v", err)
		}
	}
	if err := feed(t, consumer, store, "timeline.stargate.realm.created", "not-json"); err != nil {
		t.Fatalf("malformed handler: %v", err)
	}
	if err := feed(t, consumer, store, "timeline.stargate.realm.created",
		envelope("stargate", "realm.created", account, time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC), "")); err != nil {
		t.Fatalf("valid handler: %v", err)
	}

	id, _ := uuid.Parse(account)
	if count := countEvents(t, store, id, "realm.created"); count != 1 {
		t.Fatalf("realm.created events = %d, want only the valid one", count)
	}
}

// TestTimelineRecorderPersistsWithoutBroker pins the in-process write path:
// Stargate's own events land in the table even with no NATS client (Events
// nil), and the stream echo of the same envelope dedupes on the shared
// event id instead of inserting a second row.
func TestTimelineRecorderPersistsWithoutBroker(t *testing.T) {
	store := newTimelineStore(t)
	recorder := &TimelineRecorder{Store: store}
	account := uuid.NewString()
	occurred := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	data := map[string]any{"old_name": "before", "new_name": "after"}
	if err := recorder.Record(context.Background(), "stargate", "name.changed", account, occurred, data); err != nil {
		t.Fatalf("record: %v", err)
	}

	id, _ := uuid.Parse(account)
	events, err := store.ListTimelineEvents(context.Background(), id)
	if err != nil {
		t.Fatalf("list timeline events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1 (recorded without a broker)", len(events))
	}
	event := events[0]
	if event.EventType != "name.changed" || event.Source != "stargate" {
		t.Fatalf("event = %+v, want name.changed from stargate", event)
	}
	if !time.Time(event.CreatedAt).Equal(occurred) {
		t.Fatalf("created_at = %v, want the recorded occurred_at", event.CreatedAt)
	}
	var payload struct {
		NewName string `json:"new_name"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.NewName != "after" {
		t.Fatalf("payload.new_name = %q, want after", payload.NewName)
	}

	// The recorder's echo carries the same event id; ingesting it is a no-op.
	consumer := &stubConsumer{}
	startIngest(t, consumer, store, TimelineEventSources())
	echo := map[string]any{
		"event_id":    event.SourceId,
		"event_type":  "timeline.stargate.name.changed",
		"account_id":  account,
		"occurred_at": occurred,
		"data":        data,
	}
	if err := feed(t, consumer, store, "timeline.stargate.name.changed", echo); err != nil {
		t.Fatalf("echo handler: %v", err)
	}
	if count := countEvents(t, store, id, "name.changed"); count != 1 {
		t.Fatalf("name.changed events = %d, want 1 (the echo must dedupe)", count)
	}
}

// TestIngestDeduplicatesRedeliveries pins the idempotency contract: a
// redelivered message with the same envelope event_id inserts nothing.
func TestIngestDeduplicatesRedeliveries(t *testing.T) {
	store := newTimelineStore(t)
	consumer := &stubConsumer{}
	startIngest(t, consumer, store, TimelineEventSources())

	payload := envelope("pulsar", "scrobble.created", uuid.NewString(),
		time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC), "")
	for range 2 {
		if err := feed(t, consumer, store, "timeline.pulsar.scrobble.created", payload); err != nil {
			t.Fatalf("handler delivery: %v", err)
		}
	}
	id, _ := uuid.Parse(payload["account_id"].(string))
	if count := countEvents(t, store, id, "scrobble.created"); count != 1 {
		t.Fatalf("scrobble.created events = %d, want 1 (redelivery must dedupe)", count)
	}
}

// TestIngestMapsServiceSubjects pins subject-derived typing: the same parser
// stores pulsar review and scrobble events under their own event types,
// including the delete action as its own event.
func TestIngestMapsServiceSubjects(t *testing.T) {
	store := newTimelineStore(t)
	consumer := &stubConsumer{}
	startIngest(t, consumer, store, TimelineEventSources())

	account := uuid.NewString()
	if err := feed(t, consumer, store, "timeline.pulsar.work.reviewed",
		envelope("pulsar", "work.reviewed", account, time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC), `{"id":"r1"}`)); err != nil {
		t.Fatalf("review handler: %v", err)
	}
	if err := feed(t, consumer, store, "timeline.pulsar.work.review_deleted",
		envelope("pulsar", "work.review_deleted", account, time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC), `{"id":"r1"}`)); err != nil {
		t.Fatalf("review delete handler: %v", err)
	}

	id, _ := uuid.Parse(account)
	if count := countEvents(t, store, id, "work.reviewed"); count != 1 {
		t.Fatalf("work.reviewed events = %d, want 1", count)
	}
	if count := countEvents(t, store, id, "work.review_deleted"); count != 1 {
		t.Fatalf("work.review_deleted events = %d, want 1", count)
	}
}

// ─────────────────────────── timeline merge test ───────────────────────────

// TestTimelineMergesPersistedEvents pins the merge: persisted service events
// sit in the same list as statuses and activities, sorted newest first, and
// carry the TimelineServiceEvent discriminator with the event payload.
func TestTimelineMergesPersistedEvents(t *testing.T) {
	store := newTimelineStore(t)
	account := uuid.New()
	now := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)

	if _, err := store.InsertTimelineEvent(context.Background(), timelineEventRow{
		AccountID: account, EventType: "profile.updated", Source: "stargate",
		SourceID: "log-old", OccurredAt: now.Add(-time.Hour),
		Payload: json.RawMessage(`{"action":"accounts.profile.update"}`),
	}); err != nil {
		t.Fatalf("insert old event: %v", err)
	}
	if _, err := store.InsertTimelineEvent(context.Background(), timelineEventRow{
		AccountID: account, EventType: "work.reviewed", Source: "pulsar",
		SourceID: "rev-1", OccurredAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("insert new event: %v", err)
	}

	service := NewService(Deps{
		Store:    store,
		Accounts: profileStub{},
		Now:      func() time.Time { return now },
	})
	items, total, err := service.Timeline(context.Background(), account, 0, 10, true)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	if items[0].EventType != TimelineServiceEvent || items[0].Event == nil ||
		items[0].Event.EventType != "work.reviewed" {
		t.Fatalf("items[0] = %+v, want the newest event first", items[0])
	}
	if items[1].Event == nil || items[1].Event.EventType != "profile.updated" {
		t.Fatalf("items[1] = %+v, want the profile.updated event", items[1])
	}
	var decoded struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(items[1].Event.Payload, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if decoded.Action != "accounts.profile.update" {
		t.Fatalf("payload.action = %q, want accounts.profile.update", decoded.Action)
	}
}
