package broadcast

import (
	"encoding/json"
	"testing"
	"time"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// TestStatusUpdatedPayloadShape pins the packet data
// HandleWebSocketStatusChanged builds: snake_case account_id/status/device_id/
// timestamp, with the assembled status (device list included, since the
// audience is the account's friends).
func TestStatusUpdatedPayloadShape(t *testing.T) {
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	payload := StatusUpdatedPayload{
		AccountID: "22222222-2222-2222-2222-222222222222",
		Status:    statusFixture(),
		DeviceID:  "device-1",
		Timestamp: at,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"account_id", "status", "device_id", "timestamp"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("payload is missing %q: %s", key, body)
		}
	}
	if len(decoded) != 4 {
		t.Fatalf("payload has unexpected keys: %s", body)
	}

	var timestamp string
	if err := json.Unmarshal(decoded["timestamp"], &timestamp); err != nil {
		t.Fatalf("timestamp is not a string: %v", err)
	}
	if timestamp != "2026-10-26T12:00:00Z" {
		t.Fatalf("timestamp = %q, want RFC3339 UTC", timestamp)
	}

	var status map[string]any
	if err := json.Unmarshal(decoded["status"], &status); err != nil {
		t.Fatalf("status is not an object: %v", err)
	}
	// Enums are integers and the read-time presence fields are present, like
	// the C# serializer the Dart SDK parses.
	if status["type"] != float64(model.StatusTypeBusy) {
		t.Fatalf("status.type = %v", status["type"])
	}
	if status["attitude"] != float64(model.StatusAttitudeNeutral) {
		t.Fatalf("status.attitude = %v", status["attitude"])
	}
	if status["is_online"] != true || status["is_idle"] != true {
		t.Fatalf("status presence flags = %v/%v", status["is_online"], status["is_idle"])
	}
	if status["idle_since"] != "2026-08-06T06:29:50Z" {
		t.Fatalf("status.idle_since = %v", status["idle_since"])
	}
}

// TestActivitiesUpdatedPayloadShape pins the packet data
// BroadcastPresenceActivitiesUpdated builds: account_id/activities/timestamp,
// with an empty (never null) activity array once the last one ends.
func TestActivitiesUpdatedPayloadShape(t *testing.T) {
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

	body, err := json.Marshal(ActivitiesUpdatedPayload{
		AccountID: "22222222-2222-2222-2222-222222222222",
		Activities: []model.PresenceActivity{{
			Id:         "33333333-3333-3333-3333-333333333333",
			Type:       new("listening"),
			Tags:       []model.PresenceTag{{Slug: "lofi"}},
			Visibility: model.PresenceVisibilityFriends,
			AccountId:  "22222222-2222-2222-2222-222222222222",
		}},
		Timestamp: at,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"account_id", "activities", "timestamp"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("payload is missing %q: %s", key, body)
		}
	}
	var activities []map[string]any
	if err := json.Unmarshal(decoded["activities"], &activities); err != nil {
		t.Fatalf("activities is not an array: %v", err)
	}
	if len(activities) != 1 || activities[0]["type"] != "listening" {
		t.Fatalf("activities = %v", activities)
	}
	if activities[0]["visibility"] != float64(model.PresenceVisibilityFriends) {
		t.Fatalf("visibility = %v, want the integer enum", activities[0]["visibility"])
	}

	empty, err := json.Marshal(ActivitiesUpdatedPayload{
		AccountID:  "22222222-2222-2222-2222-222222222222",
		Activities: []model.PresenceActivity{},
		Timestamp:  at,
	})
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	var emptyDecoded map[string]json.RawMessage
	if err := json.Unmarshal(empty, &emptyDecoded); err != nil {
		t.Fatalf("unmarshal empty: %v", err)
	}
	if string(emptyDecoded["activities"]) != "[]" {
		t.Fatalf("activities = %s, want []", emptyDecoded["activities"])
	}
}
