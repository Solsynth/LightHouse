// Package eventbus wraps the shared fleet event bus (NATS/JetStream) for
// Lighthouse services: domain events on the C# wire envelope, and the
// `websocket_push` channel Blade's gateway consumes.
package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"src.solsynth.dev/sosys/go/pkg/eventbus"
	gen "src.solsynth.dev/sosys/go/proto"
)

// wsDefaultNamespace is WebSocketNamespaces.Solian: an unmatched namespace
// delivers nowhere, so an empty one resolves to the first-party tenant.
const wsDefaultNamespace = "dev.solsynth.solian"

// Client bundles the event bus connection and the push subject.
type Client struct {
	Bus      *eventbus.Bus
	Conn     *nats.Conn
	Subject  string
	Stream   string
	Log      *slog.Logger
	disabled bool
}

// Connect dials NATS. A failed or empty target returns a disabled client, so a
// deployment without NATS keeps serving HTTP and gRPC.
func Connect(target, pushSubject, pushStream string, log *slog.Logger) (*Client, error) {
	if log == nil {
		log = slog.Default()
	}
	if strings.TrimSpace(pushSubject) == "" {
		pushSubject = "websocket_push"
	}
	client := &Client{Subject: pushSubject, Stream: pushStream, Log: log, disabled: true}
	if strings.TrimSpace(target) == "" {
		return client, nil
	}
	conn, err := nats.Connect(target, nats.Name("lighthouse"), nats.Timeout(5*time.Second))
	if err != nil {
		return nil, err
	}
	bus, err := eventbus.New(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	client.Conn = conn
	client.Bus = bus
	client.disabled = false
	return client, nil
}

// Enabled reports whether the bus is connected.
func (c *Client) Enabled() bool { return c != nil && !c.disabled && c.Bus != nil }

// Close shuts down the connection.
func (c *Client) Close() {
	if c != nil && c.Conn != nil {
		c.Conn.Close()
	}
}

// Publish publishes a domain event on a JetStream subject, ensuring the stream
// covers it first. A disabled bus is a no-op.
func (c *Client) Publish(ctx context.Context, subject, stream string, payload any) error {
	if !c.Enabled() {
		return nil
	}
	if stream == "" {
		stream = subject + "_stream"
	}
	return c.Bus.PublishJetStream(ctx, subject, stream, payload)
}

// Consume runs a durable JetStream consumer until ctx is cancelled. A disabled
// bus blocks until ctx is cancelled so callers can start it unconditionally.
func (c *Client) Consume(ctx context.Context, stream, subject, consumer string, handler func([]byte) error) error {
	if !c.Enabled() {
		<-ctx.Done()
		return nil
	}
	return c.Bus.Consume(ctx, stream, subject, consumer, handler)
}

// PublishWS pushes one packet to a single account.
func (c *Client) PublishWS(ctx context.Context, accountID, event string, payload any) error {
	return c.PublishWSToAccounts(ctx, []string{accountID}, event, payload, "")
}

// PublishWSToAccounts fans one packet out to several accounts.
func (c *Client) PublishWSToAccounts(ctx context.Context, accountIDs []string, event string, payload any, namespace string) error {
	if !c.Enabled() {
		return nil
	}
	ids := normalizeRecipients(accountIDs)
	if len(ids) == 0 {
		return nil
	}
	body, err := BuildWSPushEnvelope(ids, event, payload, namespace)
	if err != nil {
		return err
	}
	return c.Conn.Publish(c.Subject, body)
}

// BuildWSPushEnvelope renders the body published on the websocket_push
// subject. Blade unmarshals the body, base64-decodes `packet` and
// protojson-decodes it into a DyWebSocketPacket.
func BuildWSPushEnvelope(ids []string, event string, payload any, namespace string) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal websocket payload: %w", err)
	}
	packet, err := protojson.Marshal(&gen.DyWebSocketPacket{
		Type:         event,
		Data:         data,
		ErrorMessage: wrapperspb.String(""),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal websocket packet: %w", err)
	}
	return json.Marshal(wsPushPayload{
		Namespace:         resolveNamespace(namespace),
		Target:            "account",
		IDs:               ids,
		ExcludedDeviceIDs: []string{},
		Packet:            packet,
	})
}

// wsPushPayload is the websocket_push envelope Blade's gateway consumes
// (wsgateway.natsPushEvent). Field names and the base64 `packet` byte[] are the
// wire contract shared with the C# RemoteWebSocketService and Metoer.
type wsPushPayload struct {
	Namespace         string   `json:"namespace"`
	Target            string   `json:"target"`
	IDs               []string `json:"ids"`
	ExcludedDeviceIDs []string `json:"excluded_device_ids"`
	Packet            []byte   `json:"packet"`
}

func resolveNamespace(namespace string) string {
	if strings.TrimSpace(namespace) == "" {
		return wsDefaultNamespace
	}
	return namespace
}

// normalizeRecipients drops blank ids and de-duplicates in first-seen order.
func normalizeRecipients(accountIDs []string) []string {
	out := make([]string, 0, len(accountIDs))
	seen := make(map[string]struct{}, len(accountIDs))
	for _, id := range accountIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
