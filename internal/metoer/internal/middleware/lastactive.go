package middleware

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	eb "src.solsynth.dev/sosys/go/pkg/eventbus"

	"src.solsynth.dev/sosys/go/pkg/auth"
	"src.solsynth.dev/sosys/go/pkg/cache"
)

// lastActiveStream/subject mirror LastActiveEvent (StreamName
// "account_events", Type "accounts.last_active").
const (
	lastActiveStream  = "account_events"
	lastActiveSubject = "accounts.last_active"
	lastActiveThrottle = time.Minute
)

// lastActivePayload mirrors the C# LastActiveEvent wire shape exactly:
// EventBase (PascalCase) + AccountId/SessionId/SeenAt (PascalCase, NodaTime
// ISO instant). Stargate's consumer unmarshals the PascalCase fields.
type lastActivePayload struct {
	EventID    string    `json:"EventId"`
	Timestamp  time.Time `json:"Timestamp"`
	EventType  string    `json:"EventType"`
	StreamName string    `json:"StreamName"`
	AccountID  string    `json:"AccountId"`
	SessionID  string    `json:"SessionId"`
	SeenAt     time.Time `json:"SeenAt"`
}

// LastActivePublisher mirrors the C# TouchProfileLastSeenAsync: a Redis
// flag (auth:last_seen_touch:{accountId}, 1m) throttles a JetStream
// accounts.last_active publish on account_events.
type LastActivePublisher struct {
	Bus   *eb.Bus
	Cache cache.CacheService
	Log   *slog.Logger
}

// Touch publishes the throttled last-active event for the authenticated
// result (best-effort, mirrors the C# swallow-everything semantics).
func (p *LastActivePublisher) Touch(ctx context.Context, result *auth.AuthResult) {
	if p == nil || result == nil || result.Account == nil || result.Account.Id == "" {
		return
	}
	accountID := result.Account.Id
	throttleKey := auth.LastSeenTouchKey(accountID)

	if p.Cache != nil {
		has, err := p.Cache.HasFlag(ctx, throttleKey)
		if err == nil && has {
			return
		}
		if err != nil && p.Log != nil {
			p.Log.Debug("last_seen throttle check failed", "account_id", accountID, "error", err)
		}
	}

	sessionID := ""
	if result.Session != nil {
		sessionID = result.Session.Id
	}
	if _, err := uuid.Parse(sessionID); err != nil {
		sessionID = ""
	}

	now := time.Now().UTC()
	payload := lastActivePayload{
		EventID:    uuid.NewString(),
		Timestamp:  now,
		EventType:  lastActiveSubject,
		StreamName: lastActiveStream,
		AccountID:  accountID,
		SessionID:  sessionID,
		SeenAt:     now,
	}

	if p.Bus != nil {
		if err := p.Bus.PublishJetStream(ctx, lastActiveSubject, lastActiveStream, payload); err != nil && p.Log != nil {
			p.Log.Debug("failed to publish last_active", "account_id", accountID, "error", err)
		}
	}

	if p.Cache != nil {
		if err := p.Cache.SetFlag(ctx, throttleKey, lastActiveThrottle); err != nil && p.Log != nil {
			p.Log.Debug("failed to set last_seen throttle", "account_id", accountID, "error", err)
		}
	}
}
