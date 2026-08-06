// Package nats wires the NATS connection and JetStream event publishing for
// auth.session.revoked and websocket_push streams.
package nats

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/config"
)

// Client bundles the NATS connection and JetStream streams.
type Client struct {
	Conn    *nats.Conn
	JS      jetstream.JetStream
	cfg     *config.Config
	session jetstream.Stream
	wsPush  jetstream.Stream
}

// Connect dials NATS and ensures the JetStream streams exist.
func Connect(ctx context.Context, cfg *config.Config) (*Client, error) {
	if cfg.NATS.Target == "" {
		return nil, nil
	}
	nc, err := nats.Connect(cfg.NATS.Target, nats.Timeout(10*time.Second))
	if err != nil {
		return nil, fmt.Errorf("connect nats: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream: %w", err)
	}
	c := &Client{Conn: nc, JS: js, cfg: cfg}
	if err := c.ensureStreams(ctx); err != nil {
		nc.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) ensureStreams(ctx context.Context) error {
	sessionStream, err := c.JS.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      c.cfg.NATS.SessionEventsStream,
		Subjects:  []string{c.cfg.NATS.SessionEventsSubject},
		Retention: jetstream.LimitsPolicy,
	})
	if err != nil {
		return fmt.Errorf("session events stream: %w", err)
	}
	wsStream, err := c.JS.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      c.cfg.NATS.WebsocketPushStream,
		Subjects:  []string{c.cfg.NATS.WebsocketPushSubject},
		Retention: jetstream.LimitsPolicy,
	})
	if err != nil {
		return fmt.Errorf("websocket push stream: %w", err)
	}
	c.session = sessionStream
	c.wsPush = wsStream
	return nil
}

// Close shuts down the connection.
func (c *Client) Close() {
	if c != nil && c.Conn != nil {
		c.Conn.Close()
	}
}

// sessionRevokedPayload mirrors the AuthSessionRevokedEvent wire shape.
type sessionRevokedPayload struct {
	EventID    string    `json:"event_id"`
	Timestamp  time.Time `json:"timestamp"`
	StreamName string    `json:"stream_name"`
	EventType  string    `json:"event_type"`
	SessionID  string    `json:"session_id"`
	AccountID  string    `json:"account_id"`
	ClientID   *string   `json:"client_id,omitempty"`
	DeviceID   *string   `json:"device_id,omitempty"`
	RevokedAt  time.Time `json:"revoked_at"`
}

// PublishSessionRevoked publishes auth.session.revoked events to JetStream.
func (c *Client) PublishSessionRevoked(ctx context.Context, events []auth.SessionRevokedEvent) error {
	if c == nil || c.Conn == nil || c.session == nil {
		return nil
	}
	for _, ev := range events {
		payload, err := json.Marshal(sessionRevokedPayload{
			EventID:    uuid.NewString(),
			Timestamp:  ev.RevokedAt,
			StreamName: c.cfg.NATS.SessionEventsStream,
			EventType:  "session_revoked",
			SessionID:  ev.SessionID,
			AccountID:  ev.AccountID,
			ClientID:   ev.ClientID,
			DeviceID:   ev.DeviceID,
			RevokedAt:  ev.RevokedAt,
		})
		if err != nil {
			return err
		}
		if _, err := c.JS.Publish(ctx, c.cfg.NATS.SessionEventsSubject, payload); err != nil {
			return err
		}
	}
	return nil
}

// wsPushPayload is the websocket.push envelope.
type wsPushPayload struct {
	Target  string `json:"target"`
	Event   string `json:"event"`
	Payload any    `json:"payload"`
}

// PublishWS publishes a websocket push envelope to the websocket_push stream.
func (c *Client) PublishWS(ctx context.Context, target string, event string, payload any) error {
	if c == nil || c.Conn == nil || c.wsPush == nil {
		return nil
	}
	data, err := json.Marshal(wsPushPayload{Target: target, Event: event, Payload: payload})
	if err != nil {
		return err
	}
	_, err = c.JS.Publish(ctx, c.cfg.NATS.WebsocketPushSubject, data)
	return err
}

// accountEventsStream is the C# fleet's shared stream for account domain
// events (EventBase.StreamName of the account events, e.g.
// AccountActivatedEvent).
const accountEventsStream = "account_events"

// AccountActivatedEvent mirrors the C# AccountActivatedEvent wire shape
// (System.Text.Json PascalCase keys; NodaTime Instant as ISO-8601 UTC).
// Passport publishes it on the subject accounts.activated once activation
// requirements are satisfied (e.g. required entry tests passed); the old
// Padlock consumer set activated_at + the verified group, which Stargate now
// does.
type AccountActivatedEvent struct {
	AccountID   string    `json:"AccountId"`
	ActivatedAt time.Time `json:"ActivatedAt"`
}

// AccountTestPassedPermissionGroupEvent mirrors the C#
// AccountTestPassedPermissionGroupEvent wire shape. Passport publishes it on
// accounts.tests.permission-group-granted when a passed test is configured
// with granted_permission_group_key; the old Padlock consumer granted the
// group membership, which Stargate now does.
type AccountTestPassedPermissionGroupEvent struct {
	AccountID          string    `json:"AccountId"`
	TestID             string    `json:"TestId"`
	AttemptID          string    `json:"AttemptId"`
	PermissionGroupKey string    `json:"PermissionGroupKey"`
	GrantedAt          time.Time `json:"GrantedAt"`
}

// ConsumeAccountEvents runs a durable JetStream consumer for one subject on
// the account_events stream, mirroring the C# EventBusBackgroundService
// defaults (JetStream, DeliverPolicy New). handler returns nil to ack;
// returning an error leaves the message unacked for redelivery
// (at-least-once). The consumer is created if missing and the loop blocks
// until ctx is cancelled.
func (c *Client) ConsumeAccountEvents(ctx context.Context, subject, consumerName string, handler func(payload []byte) error) error {
	if c == nil || c.Conn == nil || c.JS == nil {
		return nil // NATS disabled
	}
	// Union subjects: the C# fleet created account_events with its own
	// subject list; replacing it would drop coverage for its other events.
	subjects := []string{subject}
	if stream, err := c.JS.Stream(ctx, accountEventsStream); err == nil {
		if info, err := stream.Info(ctx); err == nil {
			seen := map[string]bool{subject: true}
			for _, s := range info.Config.Subjects {
				if !seen[s] {
					seen[s] = true
					subjects = append(subjects, s)
				}
			}
		}
	}
	if _, err := c.JS.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      accountEventsStream,
		Subjects:  subjects,
		Retention: jetstream.LimitsPolicy,
	}); err != nil {
		return fmt.Errorf("ensure %s stream: %w", accountEventsStream, err)
	}
	cons, err := c.JS.CreateOrUpdateConsumer(ctx, accountEventsStream, jetstream.ConsumerConfig{
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
