package nats

import (
	"encoding/json"
	"testing"
	"time"
)

// TestAccountActivatedEventPayload pins the C# wire contract: Passport
// publishes AccountActivatedEvent serialized with System.Text.Json
// (PascalCase keys) and NodaTime Instant as ISO-8601 UTC. The consumer must
// parse this exact shape.
func TestAccountActivatedEventPayload(t *testing.T) {
	payload := `{"EventId":"11111111-1111-1111-1111-111111111111",` +
		`"Timestamp":"2026-08-06T06:29:50.123456Z",` +
		`"EventType":"accounts.activated",` +
		`"StreamName":"account_events",` +
		`"AccountId":"22222222-2222-2222-2222-222222222222",` +
		`"ActivatedAt":"2026-08-06T06:29:50.123456Z"}`

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
	payload := `{"EventId":"11111111-1111-1111-1111-111111111111",` +
		`"Timestamp":"2026-08-06T06:29:50.123456Z",` +
		`"EventType":"accounts.tests.permission-group-granted",` +
		`"StreamName":"account_events",` +
		`"AccountId":"22222222-2222-2222-2222-222222222222",` +
		`"TestId":"33333333-3333-3333-3333-333333333333",` +
		`"AttemptId":"44444444-4444-4444-4444-444444444444",` +
		`"PermissionGroupKey":"community-member",` +
		`"GrantedAt":"2026-08-06T06:29:50.123456Z"}`

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
