package events

import (
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

// TestPushEnvelopeIsBladeDecodable pins the websocket_push wire contract: the
// body is JSON with `packet` as a base64 string (encoding/json's []byte rule),
// and the decoded bytes are UTF-8 protojson. Blade decodes exactly that way, so
// a second base64 layer inside marshalPacket silently dropped every push.
func TestPushEnvelopeIsBladeDecodable(t *testing.T) {
	data := []byte(`{"id":"n1","topic":"messages.new","title":"hi","content":"there"}`)
	body, err := json.Marshal(wsPushPayload{
		Namespace: ResolveNamespace(""),
		Target:    "account",
		Ids:       []string{"acct-1"},
		Packet:    marshalPacket(CreatePacket("notifications.new", data)),
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	// Mirror Blade's SubscribeWebSocketPushes decoding.
	var event wsPushPayload
	if err := json.Unmarshal(body, &event); err != nil {
		t.Fatalf("blade cannot unmarshal envelope: %v", err)
	}
	var packet gen.DyWebSocketPacket
	if err := protojson.Unmarshal(event.Packet, &packet); err != nil {
		t.Fatalf("blade cannot decode packet: %v (raw=%q)", err, string(event.Packet))
	}

	if packet.GetType() != "notifications.new" {
		t.Fatalf("packet type = %q", packet.GetType())
	}
	var payload map[string]any
	if err := json.Unmarshal(packet.GetData(), &payload); err != nil {
		t.Fatalf("packet data is not JSON: %v", err)
	}
	if payload["id"] != "n1" {
		t.Fatalf("packet data id = %v", payload["id"])
	}
	if event.Namespace != Solian {
		t.Fatalf("namespace = %q, want %q", event.Namespace, Solian)
	}
}
