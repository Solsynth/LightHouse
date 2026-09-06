package nats

import (
	"encoding/json"
	"testing"
	"time"
)

// TestAccountActivatedEventPayload pins the C# wire contract: Passport
// publishes AccountActivatedEvent serialized with System.Text.Json
// (snake_case keys via InfraObjectCoder) and NodaTime Instant as ISO-8601
// UTC. The consumer must parse this exact shape.
func TestAccountActivatedEventPayload(t *testing.T) {
	payload := `{"event_id":"11111111-1111-1111-1111-111111111111",` +
		`"timestamp":"2026-08-06T06:29:50.123456Z",` +
		`"event_type":"accounts.activated",` +
		`"stream_name":"account_events",` +
		`"account_id":"22222222-2222-2222-2222-222222222222",` +
		`"activated_at":"2026-08-06T06:29:50.123456Z"}`

	var ev AccountActivatedEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.AccountID != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("AccountID = %q", ev.AccountID)
	}
	want := time.Date(2026, 8, 6, 6, 29, 50, 123456000, time.UTC)
	if !ev.ActivatedAt.Equal(want) {
		t.Fatalf("ActivatedAt = %v, want %v", ev.ActivatedAt, want)
	}
}

// TestAccountTestPassedPermissionGroupEventPayload pins the C# wire shape of
// the test-passed permission-group grant event.
func TestAccountTestPassedPermissionGroupEventPayload(t *testing.T) {
	payload := `{"event_id":"11111111-1111-1111-1111-111111111111",` +
		`"timestamp":"2026-08-06T06:29:50.123456Z",` +
		`"event_type":"accounts.tests.permission-group-granted",` +
		`"stream_name":"account_events",` +
		`"account_id":"22222222-2222-2222-2222-222222222222",` +
		`"test_id":"33333333-3333-3333-3333-333333333333",` +
		`"attempt_id":"44444444-4444-4444-4444-444444444444",` +
		`"permission_group_key":"community-member",` +
		`"granted_at":"2026-08-06T06:29:50.123456Z"}`

	var ev AccountTestPassedPermissionGroupEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.AccountID != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("AccountID = %q", ev.AccountID)
	}
	if ev.TestID != "33333333-3333-3333-3333-333333333333" {
		t.Fatalf("TestID = %q", ev.TestID)
	}
	if ev.AttemptID != "44444444-4444-4444-4444-444444444444" {
		t.Fatalf("AttemptID = %q", ev.AttemptID)
	}
	if ev.PermissionGroupKey != "community-member" {
		t.Fatalf("PermissionGroupKey = %q", ev.PermissionGroupKey)
	}
	want := time.Date(2026, 8, 6, 6, 29, 50, 123456000, time.UTC)
	if !ev.GrantedAt.Equal(want) {
		t.Fatalf("GrantedAt = %v, want %v", ev.GrantedAt, want)
	}
}

// TestActionLogTriggeredEventPayload pins the C# wire contract: Stargate
// publishes ActionLogTriggeredEvent with snake_case keys (InfraObjectCoder)
// and an ISO-8601 UTC occurred_at. Passport deserializes this into
// ActionLogTriggeredEvent to drive ProgressionService counting.
func TestActionLogTriggeredEventPayload(t *testing.T) {
	payload := `{"event_id":"11111111-1111-1111-1111-111111111111",` +
		`"timestamp":"2026-08-06T06:29:50Z",` +
		`"event_type":"action_logs.posts.create",` +
		`"stream_name":"action_log_events",` +
		`"action_log_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",` +
		`"account_id":"22222222-2222-2222-2222-222222222222",` +
		`"action":"posts.create",` +
		`"meta":{"id":"session:id"},"session_id":"33333333-3333-3333-3333-333333333333",` +
		`"occurred_at":"2026-08-06T06:29:50Z"}`

	var ev ActionLogTriggeredEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.ActionLogID != "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" {
		t.Fatalf("ActionLogID = %q", ev.ActionLogID)
	}
	if ev.AccountID != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("AccountID = %q", ev.AccountID)
	}
	if ev.Action != "posts.create" {
		t.Fatalf("Action = %q", ev.Action)
	}
	if ev.Meta["id"] != "session:id" {
		t.Fatalf("Meta = %v", ev.Meta)
	}
	if ev.SessionID == nil || *ev.SessionID != "33333333-3333-3333-3333-333333333333" {
		t.Fatalf("SessionID = %v", ev.SessionID)
	}
	want := time.Date(2026, 8, 6, 6, 29, 50, 0, time.UTC)
	if !ev.OccurredAt.Equal(want) {
		t.Fatalf("OccurredAt = %v, want %v", ev.OccurredAt, want)
	}
}

// TestShouldPublishActionLog guards the tracked-action set against drift with
// DysonNetwork.Shared.Queue.ProgressionActionLogRegistry.TrackedActions: a
// tracked action must publish, an untracked one must be dropped, and the set
// size must stay in sync (38 actions today).
func TestShouldPublishActionLog(t *testing.T) {
	tracked := []string{
		"accounts.profile.update", "accounts.auth_factors.create",
		"login", "accounts.active", "stellar.support.month",
		"developer.sessions.revoke", "posts.create", "posts.react",
		"chat.use", "publishers.create", "realms.join",
	}
	for _, action := range tracked {
		if !ShouldPublishActionLog(action) {
			t.Errorf("expected %q to be tracked", action)
		}
	}
	untracked := []string{"accounts.recovery", "posts.update", "admin.contacts.verify"}
	for _, action := range untracked {
		if ShouldPublishActionLog(action) {
			t.Errorf("expected %q to be untracked", action)
		}
	}
	if len(trackedActionLogs) != 38 {
		t.Fatalf("trackedActionLogs has %d entries, want 38 (match C# ProgressionActionLogRegistry.TrackedActions)", len(trackedActionLogs))
	}
}
