package nats

import (
	"encoding/json"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	gen "src.solsynth.dev/sosys/go/proto"
)

// TestBuildWSPushEnvelopeMultiRecipient decodes the body the way Blade's
// gateway does (wsgateway.SubscribeWebSocketPushes): unmarshal into the
// envelope, base64-decode `packet` via the []byte field, protojson-decode it,
// then hand `ids` to SendPacketToAccountsExcept.
func TestBuildWSPushEnvelopeMultiRecipient(t *testing.T) {
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	body, err := buildWSPushEnvelope(
		[]string{"u1", "u2", "u3"},
		"account.status.updated",
		map[string]any{"account_id": "u1", "timestamp": at},
		"",
	)
	if err != nil {
		t.Fatalf("build envelope: %v", err)
	}

	// Raw shape first: the envelope keys are snake_case and `packet` is a
	// base64 string, not an object.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal raw envelope: %v", err)
	}
	for _, key := range []string{"namespace", "target", "ids", "excluded_device_ids", "packet"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("envelope is missing %q: %s", key, body)
		}
	}
	var packetString string
	if err := json.Unmarshal(raw["packet"], &packetString); err != nil {
		t.Fatalf("packet is not a base64 string: %v", err)
	}

	var envelope wsPushPayload
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if envelope.Namespace != "dev.solsynth.solian" {
		t.Fatalf("namespace = %q, want the Solian tenant", envelope.Namespace)
	}
	if envelope.Target != "account" {
		t.Fatalf("target = %q, want account", envelope.Target)
	}
	if len(envelope.IDs) != 3 || envelope.IDs[0] != "u1" || envelope.IDs[1] != "u2" || envelope.IDs[2] != "u3" {
		t.Fatalf("ids = %v, want the whole recipient list in order", envelope.IDs)
	}
	if envelope.ExcludedDeviceIDs == nil || len(envelope.ExcludedDeviceIDs) != 0 {
		t.Fatalf("excluded_device_ids = %v, want an empty array", envelope.ExcludedDeviceIDs)
	}

	var decoded gen.DyWebSocketPacket
	if err := protojson.Unmarshal(envelope.Packet, &decoded); err != nil {
		t.Fatalf("Blade cannot decode the packet: %v", err)
	}
	if decoded.GetType() != "account.status.updated" {
		t.Fatalf("packet type = %q", decoded.GetType())
	}
	if decoded.GetErrorMessage().GetValue() != "" {
		t.Fatalf("errorMessage = %q, want empty", decoded.GetErrorMessage().GetValue())
	}
	var data map[string]any
	if err := json.Unmarshal(decoded.GetData(), &data); err != nil {
		t.Fatalf("packet data is not JSON: %v", err)
	}
	if data["account_id"] != "u1" {
		t.Fatalf("packet data account_id = %v", data["account_id"])
	}
	if data["timestamp"] != "2026-10-26T12:00:00Z" {
		t.Fatalf("packet data timestamp = %v", data["timestamp"])
	}
}

// TestNormalizeRecipients pins the C# fan-out de-duplication: blank ids are
// dropped, duplicates collapse, first-seen order is preserved.
func TestNormalizeRecipients(t *testing.T) {
	got := normalizeRecipients([]string{"u1", " ", "u2", "u1", ""})
	if len(got) != 2 || got[0] != "u1" || got[1] != "u2" {
		t.Fatalf("normalizeRecipients = %v", got)
	}
	if ids := normalizeRecipients([]string{"", "  "}); len(ids) != 0 {
		t.Fatalf("blank ids must publish nothing, got %v", ids)
	}
}

// TestResolveIdleSince pins the four payload shapes the fleet relays on
// `status.idle` (C# ResolveIdleSince): true, false, {is_idle:true},
// {is_idle:false}, plus an absent payload defaulting to idle.
func TestResolveIdleSince(t *testing.T) {
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		data string
		idle bool
	}{
		{"true", `true`, true},
		{"false", `false`, false},
		{"object true", `{"is_idle":true}`, true},
		{"object false", `{"is_idle":false}`, false},
		{"null data", ``, true},
		{"unrelated object", `{"foo":1}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			since := ResolveIdleSince(json.RawMessage(tc.data), at)
			if !tc.idle {
				if since != nil {
					t.Fatalf("want not idle, got %v", since)
				}
				return
			}
			if since == nil {
				t.Fatal("want idle since the event timestamp, got nil")
			}
			if !since.Equal(at) {
				t.Fatalf("idle since = %v, want %v", since, at)
			}
		})
	}

	// The timestamp is normalized to UTC so the wire value does not depend on
	// the publisher's zone.
	local := at.In(time.FixedZone("UTC+8", 8*3600))
	since := ResolveIdleSince(json.RawMessage(`{"is_idle":true}`), local)
	if since == nil || since.Location() != time.UTC {
		t.Fatalf("idle since must be UTC, got %v", since)
	}
}

// TestParseWebSocketPacket pins the relayed packet shape Blade publishes on
// websocket_passport: {type, data}.
func TestParseWebSocketPacket(t *testing.T) {
	packetType, data, err := ParseWebSocketPacket([]byte(`{"type":"status.idle","data":{"is_idle":true}}`))
	if err != nil {
		t.Fatalf("parse packet: %v", err)
	}
	if packetType != "status.idle" {
		t.Fatalf("type = %q", packetType)
	}
	if string(data) != `{"is_idle":true}` {
		t.Fatalf("data = %s", data)
	}
	if _, _, err := ParseWebSocketPacket([]byte("not json")); err == nil {
		t.Fatal("malformed packet bytes must error")
	}
}

// TestWebSocketConnectionEventPayload pins Blade's natsWebSocketConnectionEvent
// wire shape as decoded by the connection consumers.
func TestWebSocketConnectionEventPayload(t *testing.T) {
	payload := `{"event_id":"11111111-1111-1111-1111-111111111111",` +
		`"timestamp":"2026-10-26T12:00:00.123456789Z",` +
		`"stream_name":"websocket_connections","event_type":"disconnected",` +
		`"account_id":"22222222-2222-2222-2222-222222222222","device_id":"d1",` +
		`"is_offline":true}`

	var ev WebSocketConnectionEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.AccountID != "22222222-2222-2222-2222-222222222222" || ev.DeviceID != "d1" {
		t.Fatalf("account/device = %q/%q", ev.AccountID, ev.DeviceID)
	}
	if ev.EventType != "disconnected" || ev.StreamName != "websocket_connections" {
		t.Fatalf("event_type/stream = %q/%q", ev.EventType, ev.StreamName)
	}
	if !ev.IsOffline {
		t.Fatal("is_offline must decode")
	}
	if !ev.Timestamp.Equal(time.Date(2026, 10, 26, 12, 0, 0, 123456789, time.UTC)) {
		t.Fatalf("timestamp = %v", ev.Timestamp)
	}
}
