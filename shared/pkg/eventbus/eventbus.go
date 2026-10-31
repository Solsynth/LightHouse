// Package eventbus is the Go counterpart of the shared .NET event bus
// (NeTo, DysonNetwork.Shared.EventBus): a thin NATS/JetStream wrapper every
// Go service in the fleet uses to publish and consume events.
//
// Wire shapes mirror the C# EventBase envelope (event_id, timestamp,
// event_type, stream_name); see Event.
//
// A nil *Bus (or one with a nil Conn) is safe to call: every method no-ops,
// matching the fleet convention of running with events disabled.
package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Event is the Go counterpart of DysonNetwork.Shared.EventBus.EventBase: the
// envelope fields carried by every fleet event. Embed it in domain events to
// inherit the wire shape. NeTo's EventBase defaults StreamName to
// "{EventType}_stream"; callers that want that convention can pass
// eventType + "_stream" to NewEvent.
type Event struct {
	EventID    string    `json:"event_id,omitempty"`
	Timestamp  time.Time `json:"timestamp,omitempty"`
	EventType  string    `json:"event_type,omitempty"`
	StreamName string    `json:"stream_name,omitempty"`
}

// NewEvent builds an Event with a fresh EventID and the current UTC time.
func NewEvent(streamName, eventType string) Event {
	return Event{
		EventID:    uuid.NewString(),
		Timestamp:  time.Now().UTC(),
		EventType:  eventType,
		StreamName: streamName,
	}
}

// Bus bundles the NATS connection and its JetStream context.
type Bus struct {
	Conn *nats.Conn
	JS   jetstream.JetStream
}

// Connect dials NATS with the given options and prepares the JetStream
// context. Streams are not created here; call EnsureStream for the streams
// the service owns.
func Connect(url string, opts ...nats.Option) (*Bus, error) {
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return nil, fmt.Errorf("connect nats: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream: %w", err)
	}
	return &Bus{Conn: nc, JS: js}, nil
}

// New wraps an existing NATS connection. Returns (nil, nil) when conn is nil.
func New(conn *nats.Conn) (*Bus, error) {
	if conn == nil {
		return nil, nil
	}
	js, err := jetstream.New(conn)
	if err != nil {
		return nil, err
	}
	return &Bus{Conn: conn, JS: js}, nil
}

// Close shuts down the connection.
func (b *Bus) Close() {
	if b != nil && b.Conn != nil {
		b.Conn.Close()
	}
}

// Publish marshals v as JSON and publishes it to a core-NATS subject.
func (b *Bus) Publish(subject string, v any) error {
	if b == nil || b.Conn == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	return b.Conn.Publish(subject, data)
}

// PublishJetStream publishes JSON to a JetStream subject, ensuring the
// stream exists first (existing subjects are preserved).
func (b *Bus) PublishJetStream(ctx context.Context, subject, stream string, v any) error {
	if b == nil || b.Conn == nil || b.JS == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	if err := b.EnsureStream(ctx, stream, []string{subject}); err != nil {
		return err
	}
	if _, err := b.JS.Publish(ctx, subject, data); err != nil {
		return fmt.Errorf("publish %s to %s: %w", subject, stream, err)
	}
	return nil
}

// EnsureStream creates a JetStream stream if missing, or updates it to also
// cover the given subjects — always preserving subjects other processes
// registered (the C# fleet shares streams like account_events).
func (b *Bus) EnsureStream(ctx context.Context, stream string, subjects []string) error {
	if b == nil || b.Conn == nil || b.JS == nil {
		return nil
	}
	covered := make(map[string]struct{}, len(subjects))
	for _, s := range subjects {
		covered[s] = struct{}{}
	}
	if cur, err := b.JS.Stream(ctx, stream); err == nil {
		if info, err := cur.Info(ctx); err == nil {
			for _, s := range info.Config.Subjects {
				covered[s] = struct{}{}
			}
		}
	}
	union := make([]string, 0, len(covered))
	for s := range covered {
		union = append(union, s)
	}
	sort.Strings(union)
	if _, err := b.JS.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      stream,
		Subjects:  union,
		Retention: jetstream.LimitsPolicy,
	}); err != nil {
		return fmt.Errorf("ensure %s stream: %w", stream, err)
	}
	return nil
}

// Consume runs a durable JetStream consumer for one subject on the stream,
// mirroring the C# EventBusBackgroundService defaults (JetStream, DeliverPolicy
// New). handler returns nil to ack; returning an error leaves the message
// unacked for redelivery (at-least-once). The consumer is created if missing
// and the loop blocks until ctx is cancelled.
func (b *Bus) Consume(ctx context.Context, stream, subject, consumerName string, handler func([]byte) error) error {
	if b == nil || b.Conn == nil || b.JS == nil {
		return nil // NATS disabled
	}
	if err := b.EnsureStream(ctx, stream, []string{subject}); err != nil {
		return err
	}
	cons, err := b.JS.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Name:          consumerName,
		FilterSubject: subject,
		DeliverPolicy: jetstream.DeliverNewPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return fmt.Errorf("create %s consumer: %w", consumerName, err)
	}
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		if err := handler(msg.Data()); err != nil {
			_ = msg.Nak()
			return
		}
		_ = msg.Ack()
	})
	if err != nil {
		return fmt.Errorf("start %s consumer: %w", consumerName, err)
	}
	<-ctx.Done()
	cc.Stop()
	return nil
}
