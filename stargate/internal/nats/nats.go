// Package nats wires the NATS connection and JetStream event publishing for
// auth.session.revoked and websocket_push streams. It builds on the shared
// fleet event bus (src.solsynth.dev/sosys/go/pkg/eventbus).
package nats

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/wrapperspb"

	eb "src.solsynth.dev/sosys/go/pkg/eventbus"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/model"
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
	if err := c.EnsureStream(ctx, actionLogEventsStream, []string{actionLogsSubjectPrefix + ">"}); err != nil {
		return fmt.Errorf("action log events stream: %w", err)
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

// wsPushPayload is the websocket_push envelope Blade's gateway consumes
// (wsgateway.natsPushEvent). Field names and the base64 `packet` byte[] are
// the wire contract shared with the C# RemoteWebSocketService and Metoer.
type wsPushPayload struct {
	Namespace         string   `json:"namespace"`
	Target            string   `json:"target"`
	IDs               []string `json:"ids"`
	ExcludedDeviceIDs []string `json:"excluded_device_ids"`
	Packet            []byte   `json:"packet"`
}

// wsDefaultNamespace is WebSocketNamespaces.Solian: an unmatched namespace
// delivers nowhere, so an empty one resolves to the first-party tenant.
const wsDefaultNamespace = "dev.solsynth.solian"

// resolveWSNamespace mirrors WebSocketNamespaces.Resolve (empty → Solian).
func resolveWSNamespace(namespace string) string {
	if strings.TrimSpace(namespace) == "" {
		return wsDefaultNamespace
	}
	return strings.TrimSpace(namespace)
}

// PublishWS publishes an account-targeted websocket push on the subject
// Blade's gateway subscribes to.
//
// The envelope is not the flat {target,event,payload} shape this used to
// send: Blade decodes `packet` as a protojson DyWebSocketPacket and reads
// `ids` to pick recipients, so a mismatched subject or shape is dropped
// without an error on either side.
func (c *Client) PublishWS(ctx context.Context, target string, event string, payload any) error {
	return c.PublishWSNamespace(ctx, target, event, payload, "")
}

// PublishWSNamespace is PublishWS with an explicit tenant namespace.
func (c *Client) PublishWSNamespace(ctx context.Context, target, event string, payload any, namespace string) error {
	return c.publishWSPush(ctx, []string{target}, event, payload, namespace)
}

// PublishWSToAccounts fans one packet out to several accounts, mirroring the
// C# RemoteWebSocketService.PushWebSocketPacketToUsers: the same envelope as
// PublishWSNamespace with the whole recipient list in `ids`. Blank and
// duplicate ids are dropped, and an empty list publishes nothing.
func (c *Client) PublishWSToAccounts(ctx context.Context, accountIDs []string, event string, payload any, namespace string) error {
	return c.publishWSPush(ctx, accountIDs, event, payload, namespace)
}

func (c *Client) publishWSPush(ctx context.Context, accountIDs []string, event string, payload any, namespace string) error {
	if c == nil || c.Conn == nil {
		return nil
	}
	ids := normalizeRecipients(accountIDs)
	if len(ids) == 0 {
		return nil
	}
	body, err := buildWSPushEnvelope(ids, event, payload, namespace)
	if err != nil {
		return err
	}
	_, err = c.JS.Publish(ctx, c.cfg.NATS.WebsocketPushSubject, body)
	return err
}

// buildWSPushEnvelope renders the body published on the `websocket_push`
// subject. Splitting it out keeps the wire contract testable without a
// JetStream connection: Blade unmarshals the body, base64-decodes `packet`
// and protojson-decodes it into a DyWebSocketPacket.
func buildWSPushEnvelope(ids []string, event string, payload any, namespace string) ([]byte, error) {
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
	// json.Marshal base64-encodes this []byte, which is the layer Blade undoes.
	body, err := json.Marshal(wsPushPayload{
		Namespace:         resolveWSNamespace(namespace),
		Target:            "account",
		IDs:               ids,
		ExcludedDeviceIDs: []string{},
		Packet:            packet,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal websocket push envelope: %w", err)
	}
	return body, nil
}

// normalizeRecipients mirrors the C# push helpers: blank ids are dropped and
// the list is de-duplicated (StringComparer.Ordinal) in first-seen order.
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

// actionLogEventsStream is the C# fleet's shared stream for progression
// action log events (ActionLogTriggeredEvent.StreamName). Passport's
// ProgressionService consumes it on action_logs.* to advance achievements
// and quests. It is fixed in the C# contract, so it is hardcoded here like
// the account_events stream.
const actionLogEventsStream = "action_log_events"

// actionLogsSubjectPrefix mirrors ActionLogTriggeredEvent.SubjectPrefix.
const actionLogsSubjectPrefix = "action_logs."

// trackedActionLogs mirrors DysonNetwork.Shared.Queue.ProgressionActionLogRegistry.TrackedActions.
// Only these actions emit an ActionLogTriggeredEvent; the C# registry is the
// source of truth — keep the two sets in sync when a progression trigger is
// added or removed.
var trackedActionLogs = map[string]struct{}{
	"accounts.profile.update":              {},
	"accounts.auth_factors.create":         {},
	"accounts.auth_factors.enable":         {},
	"accounts.auth_factors.disable":        {},
	"accounts.auth_factors.delete":         {},
	"accounts.auth_factors.reset_password": {},
	"login":                                {},
	"accounts.active":                      {},
	"stellar.support.month":                {},
	"developer.sessions.revoke":            {},
	"developer.devices.revoke":             {},
	"developer.devices.rename":             {},
	"developer.apps.deauthorize":           {},
	"relationships.friends.request":        {},
	"relationships.friends.accept":         {},
	"relationships.friends.established":    {},
	"relationships.block":                  {},
	"relationships.unblock":                {},
	"relationships.mute":                   {},
	"relationships.unmute":                 {},
	"relationships.close_friend.add":       {},
	"relationships.close_friend.remove":    {},
	"accounts.profile.avatar":              {},
	"accounts.profile.complete":            {},
	"accounts.connection.link":             {},
	"accounts.push.enable":                 {},
	"posts.create":                         {},
	"posts.create.topical":                 {},
	"posts.featured":                       {},
	"posts.react":                          {},
	"posts.bookmark":                       {},
	"posts.boost":                          {},
	"chat.use":                             {},
	"chatrooms.join":                       {},
	"publishers.create":                    {},
	"publishers.members.join":              {},
	"realms.create":                        {},
	"realms.join":                          {},
}

// ShouldPublishActionLog reports whether a freshly stored action log should
// emit an ActionLogTriggeredEvent for the Passport progression consumer.
func ShouldPublishActionLog(action string) bool {
	_, ok := trackedActionLogs[action]
	return ok
}

// ActionLogTriggeredEvent mirrors the C# ActionLogTriggeredEvent wire shape
// (snake_case keys via InfraObjectCoder; NodaTime Instant as ISO-8601 UTC).
// Padlock's ActionLogService published it on action_logs.<action> after
// storing a tracked log; Passport deserializes this into
// ActionLogTriggeredEvent to drive progression counting. EventType and
// StreamName are recomputed by the C# side from Action, so only the
// envelope fields plus the domain fields are sent.
type ActionLogTriggeredEvent struct {
	eb.Event
	ActionLogID string         `json:"action_log_id"`
	AccountID   string         `json:"account_id"`
	Action      string         `json:"action"`
	Meta        map[string]any `json:"meta"`
	SessionID   *string        `json:"session_id,omitempty"`
	OccurredAt  time.Time      `json:"occurred_at"`
}

// PublishActionLogTriggered broadcasts a stored action log on the
// action_log_events stream (subject action_logs.<action>) for the tracked
// actions Passport's progression counts. Untracked actions are dropped, and
// a disabled/absent NATS connection is a no-op.
func (c *Client) PublishActionLogTriggered(ctx context.Context, logID, accountID, action string, meta map[string]any, sessionID *string, occurredAt time.Time) error {
	if c == nil || c.Conn == nil {
		return nil
	}
	if !ShouldPublishActionLog(action) {
		return nil
	}
	subject := actionLogsSubjectPrefix + action
	payload, err := json.Marshal(ActionLogTriggeredEvent{
		Event: eb.Event{
			EventID:    uuid.NewString(),
			Timestamp:  occurredAt.UTC(),
			StreamName: actionLogEventsStream,
			EventType:  subject,
		},
		ActionLogID: logID,
		AccountID:   accountID,
		Action:      action,
		Meta:        meta,
		SessionID:   sessionID,
		OccurredAt:  occurredAt.UTC(),
	})
	if err != nil {
		return err
	}
	_, err = c.JS.Publish(ctx, subject, payload)
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

// AccountRemovalConfirmedEvent mirrors the C# AccountRemovalConfirmedEvent
// wire shape. Passport publishes it on accounts.removal.confirmed once an
// account-removal spell is confirmed, after which the account row is gone and
// every relationship row naming it is orphaned.
type AccountRemovalConfirmedEvent struct {
	AccountID   string    `json:"account_id"`
	SpellID     string    `json:"spell_id"`
	ConfirmedAt time.Time `json:"confirmed_at"`
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

// AccountPresenceActivitiesUpdatedEvent mirrors the C#
// AccountPresenceActivitiesUpdatedEvent wire shape. Passport published it on
// the account_events stream next to the friends-only websocket push, and
// Messager consumes it to refresh the chat presence of everyone sharing a
// room with the account.
type AccountPresenceActivitiesUpdatedEvent struct {
	eb.Event
	AccountID  string                   `json:"account_id"`
	Activities []model.PresenceActivity `json:"activities"`
}

// PublishPresenceActivitiesUpdated publishes the account's live activities on
// account.presence.activities.updated (stream account_events). A nil event
// list is published as an empty array so consumers can clear stale presence.
func (c *Client) PublishPresenceActivitiesUpdated(ctx context.Context, accountID string, activities []model.PresenceActivity, at time.Time) error {
	if c == nil || c.Conn == nil {
		return nil
	}
	if activities == nil {
		activities = []model.PresenceActivity{}
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	eventType := "account.presence.activities.updated"
	// account_events is the C# fleet's shared stream; PublishJetStream
	// ensures it exists (a fresh deployment has no Passport to create it).
	return c.PublishJetStream(ctx, eventType, accountEventsStream, AccountPresenceActivitiesUpdatedEvent{
		Event: eb.Event{
			EventID:    uuid.NewString(),
			Timestamp:  at.UTC(),
			StreamName: accountEventsStream,
			EventType:  eventType,
		},
		AccountID:  accountID,
		Activities: activities,
	})
}
