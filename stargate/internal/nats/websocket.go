// Websocket connection events. Blade's gateway publishes these on core NATS
// (`websocket_connected` / `websocket_disconnected`) and Passport used to
// consume them to keep per-account idle state and to broadcast status
// changes; the passport endpoint packets arrive on `websocket_passport`.
package nats

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nats-io/nats.go"
)

// websocketConnectionsStream is the JetStream stream Passport declared for
// the connection events (WebSocketConnectedEvent.StreamName).
const websocketConnectionsStream = "websocket_connections"

// WebSocketConnectionEvent mirrors Blade's natsWebSocketConnectionEvent and
// the C# WebSocketConnectedEvent / WebSocketDisconnectedEvent wire shape:
// snake_case keys, RFC3339Nano timestamp, `is_offline` only on disconnect.
type WebSocketConnectionEvent struct {
	EventID    string    `json:"event_id"`
	Timestamp  time.Time `json:"timestamp"`
	StreamName string    `json:"stream_name"`
	EventType  string    `json:"event_type"`
	AccountID  string    `json:"account_id"`
	DeviceID   string    `json:"device_id"`
	// IsOffline is true when the disconnect took the account's last device
	// offline. A device hand-off between two connections is not offline.
	IsOffline bool `json:"is_offline"`
}

// WebSocketPacketEvent mirrors Blade's natsWebSocketPacketEvent and the C#
// WebSocketPacketEvent: a relayed client packet, `packet_bytes` base64-coded
// as a JSON byte array.
type WebSocketPacketEvent struct {
	EventID     string    `json:"event_id"`
	Timestamp   time.Time `json:"timestamp"`
	StreamName  string    `json:"stream_name"`
	AccountID   string    `json:"account_id"`
	DeviceID    string    `json:"device_id"`
	PacketBytes []byte    `json:"packet_bytes"`
}

// websocketPacket is the relayed `WebSocketPacket`: the subtype (`status.idle`
// here) and its raw data payload.
type websocketPacket struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// ParseWebSocketPacket decodes a relayed client packet and reports its type
// and data. Malformed bytes are an error, which the consumer acks and moves
// on from.
func ParseWebSocketPacket(raw []byte) (string, json.RawMessage, error) {
	var packet websocketPacket
	if err := json.Unmarshal(raw, &packet); err != nil {
		return "", nil, err
	}
	return packet.Type, packet.Data, nil
}

// ResolveIdleSince mirrors the C# ResolveIdleSince: it reads a `status.idle`
// payload into "idle since" (a timestamp) or nil (not idle).
//
//	absent data  → since (a bare status.idle signal means "idle as of now")
//	true         → since
//	false        → nil
//	{is_idle:b}  → since when b, nil otherwise
//	anything else→ since (the C# default arm)
func ResolveIdleSince(data json.RawMessage, since time.Time) *time.Time {
	at := since.UTC()
	if len(data) == 0 {
		return &at
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return &at
	}
	switch typed := value.(type) {
	case bool:
		if typed {
			return &at
		}
		return nil
	case map[string]any:
		if flag, ok := typed["is_idle"].(bool); ok {
			if flag {
				return &at
			}
			return nil
		}
		return &at
	default:
		return &at
	}
}

// ConsumeWebSocketConnections runs a durable JetStream consumer on the
// websocket_connections stream, mirroring Passport's listener options
// (JetStream, DeliverPolicy New, MaxRetries 3). The loop blocks until ctx is
// cancelled.
func (c *Client) ConsumeWebSocketConnections(ctx context.Context, subject, consumerName string, handler func(payload []byte) error) error {
	if c == nil || c.Conn == nil {
		return nil
	}
	return c.Consume(ctx, websocketConnectionsStream, subject, consumerName, handler)
}

// SubscribeWebSocketPackets listens on a core-NATS (non-JetStream) subject,
// the way Passport's WebSocketPacketEvent listener did: relayed client
// packets are fire-and-forget, so redelivery would only duplicate presence
// transitions. The subscription is drained when ctx is cancelled.
func (c *Client) SubscribeWebSocketPackets(ctx context.Context, subject string, handler func(payload []byte)) error {
	if c == nil || c.Conn == nil {
		return nil
	}
	sub, err := c.Conn.Subscribe(subject, func(msg *nats.Msg) {
		handler(msg.Data)
	})
	if err != nil {
		return err
	}
	<-ctx.Done()
	return sub.Unsubscribe()
}
