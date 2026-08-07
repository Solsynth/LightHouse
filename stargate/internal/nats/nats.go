// Package nats wires the NATS connection and JetStream event publishing for
// auth.session.revoked and websocket_push streams. It builds on the shared
// fleet event bus (src.solsynth.dev/sosys/go/pkg/eventbus).
package nats

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	eb "src.solsynth.dev/sosys/go/pkg/eventbus"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/config"
)

// Client bundles the shared event bus connection and service configuration.
type Client struct {
	*eb.Bus
	cfg *config.Config
}

// Connect dials NATS via the shared event bus and ensures the JetStream
// streams exist.
func Connect(ctx context.Context, cfg *config.Config) (*Client, error) {
	if cfg.NATS.Target == "" {
		return nil, nil
	}
	b, err := eb.Connect(cfg.NATS.Target, nats.Timeout(10*time.Second))
	if err != nil {
		return nil, err
	}
	c := &Client{Bus: b, cfg: cfg}
	if err := c.ensureStreams(ctx); err != nil {
		b.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) ensureStreams(ctx context.Context) error {
	if err := c.EnsureStream(ctx, c.cfg.NATS.SessionEventsStream, []string{c.cfg.NATS.SessionEventsSubject}); err != nil {
		return fmt.Errorf("session events stream: %w", err)
	}
	if err := c.EnsureStream(ctx, c.cfg.NATS.WebsocketPushStream, []string{c.cfg.NATS.WebsocketPushSubject}); err != nil {
		return fmt.Errorf("websocket push stream: %w", err)
	}
	return nil
}

// sessionRevokedPayload mirrors the AuthSessionRevokedEvent wire shape.
type sessionRevokedPayload struct {
	eb.Event
	SessionID string    `json:"session_id"`
	AccountID string    `json:"account_id"`
	ClientID  *string   `json:"client_id,omitempty"`
	DeviceID  *string   `json:"device_id,omitempty"`
	RevokedAt time.Time `json:"revoked_at"`
}

// PublishSessionRevoked publishes auth.session.revoked events to JetStream.
func (c *Client) PublishSessionRevoked(ctx context.Context, events []auth.SessionRevokedEvent) error {
	if c == nil || c.Conn == nil {
		return nil
	}
	for _, ev := range events {
		payload, err := json.Marshal(sessionRevokedPayload{
			Event: eb.Event{
				EventID:    uuid.NewString(),
				Timestamp:  ev.RevokedAt,
				StreamName: c.cfg.NATS.SessionEventsStream,
				EventType:  "session_revoked",
			},
			SessionID: ev.SessionID,
			AccountID: ev.AccountID,
			ClientID:  ev.ClientID,
			DeviceID:  ev.DeviceID,
			RevokedAt: ev.RevokedAt,
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
	if c == nil || c.Conn == nil {
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
// (System.Text.Json snake_case keys via InfraObjectCoder; NodaTime Instant
// as ISO-8601 UTC). Passport publishes it on the subject accounts.activated
// once activation requirements are satisfied (e.g. required entry tests
// passed); the old Padlock consumer set activated_at + the verified group,
// which Stargate now does.
type AccountActivatedEvent struct {
	AccountID   string    `json:"account_id"`
	ActivatedAt time.Time `json:"activated_at"`
}

// AccountTestPassedPermissionGroupEvent mirrors the C#
// AccountTestPassedPermissionGroupEvent wire shape. Passport publishes it on
// accounts.tests.permission-group-granted when a passed test is configured
// with granted_permission_group_key; the old Padlock consumer granted the
// group membership, which Stargate now does.
type AccountTestPassedPermissionGroupEvent struct {
	AccountID          string    `json:"account_id"`
	TestID             string    `json:"test_id"`
	AttemptID          string    `json:"attempt_id"`
	PermissionGroupKey string    `json:"permission_group_key"`
	GrantedAt          time.Time `json:"granted_at"`
}

// ProfileFieldUpdatedEvent mirrors the Passport ProfileFieldUpdatedEvent
// wire shape (snake_case keys via InfraObjectCoder; NodaTime Instant as
// ISO-8601 UTC). Passport publishes it on accounts.profile_updated whenever
// a Passport-owned feature mutates a denormalized account_profiles field
// that moved to Stargate (last-seen touches, XP deltas, social-credit
// recomputes, active badge and verification changes). ActiveBadge/
// Verification are the raw jsonb payloads (or null to clear).
type ProfileFieldUpdatedEvent struct {
	AccountID       string          `json:"account_id"`
	LastSeenAt      *time.Time      `json:"last_seen_at"`
	Experience      *int            `json:"experience"`
	ExperienceDelta *int            `json:"experience_delta"`
	SocialCredits   *float64        `json:"social_credits"`
	ActiveBadge     json.RawMessage `json:"active_badge"`
	Verification    json.RawMessage `json:"verification"`
}

// LastActiveEvent mirrors the C# LastActiveEvent wire shape (snake_case
// keys via InfraObjectCoder; NodaTime Instant as ISO-8601 UTC). The fleet's
// DysonTokenAuthHandler publishes it on accounts.last_active for every
// authenticated request (throttled per account); Stargate applies profile
// last_seen_at + session last_granted_at/keep-alive.
type LastActiveEvent struct {
	AccountID string    `json:"account_id"`
	SessionID string    `json:"session_id"`
	SeenAt    time.Time `json:"seen_at"`
}

// ConsumeAccountEvents runs a durable JetStream consumer for one subject on
// the account_events stream, mirroring the C# EventBusBackgroundService
// defaults (JetStream, DeliverPolicy New). handler returns nil to ack;
// returning an error leaves the message unacked for redelivery
// (at-least-once). The consumer is created if missing and the loop blocks
// until ctx is cancelled.
func (c *Client) ConsumeAccountEvents(ctx context.Context, subject, consumerName string, handler func(payload []byte) error) error {
	if c == nil || c.Conn == nil {
		return nil
	}
	return c.Consume(ctx, accountEventsStream, subject, consumerName, handler)
}
