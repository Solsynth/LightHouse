package core

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/config"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/store"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/app"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

// Services holds the Pulsar core dependency graph.
type Services struct {
	*app.Env
	Config *config.Config
	Store  *store.Store
}

// PublishSessionUpdate pushes live playback session changes to WebSocket clients via Blade.
func (s *Services) PublishSessionUpdate(ctx context.Context, accountID uuid.UUID, session *model.ActiveSession) {
	if s.NATS == nil || !s.NATS.Enabled() {
		return
	}
	// Broadcast event compatible with Blade & Stargate's activity listeners
	payload := map[string]any{
		"account_id": accountID.String(),
		"session":    session,
	}
	if err := s.NATS.PublishWS(ctx, accountID.String(), "account.presence.activities.updated", payload); err != nil {
		if s.Log != nil {
			s.Log.Warn("failed to push session update via websocket", "account_id", accountID, "error", err)
		}
	}
}

// PublishScrobbleCreated pushes an event onto NATS JetStream when a scrobble is written.
func (s *Services) PublishScrobbleCreated(ctx context.Context, accountID uuid.UUID, scrobble *model.Scrobble) {
	if s.NATS == nil || !s.NATS.Enabled() {
		return
	}
	subject := "pulsar.scrobble.created"
	if err := s.NATS.Publish(ctx, subject, "pulsar_scrobbles", scrobble); err != nil {
		if s.Log != nil {
			s.Log.Warn("failed to publish scrobble event", "scrobble_id", scrobble.ID, "error", err)
		}
	}
}

// ResolveAccount resolves an identifier (UUID or username) to an account UUID.
func (s *Services) ResolveAccount(ctx context.Context, identifier string) (uuid.UUID, error) {
	identifier = strings.TrimSpace(identifier)
	if id, err := uuid.Parse(identifier); err == nil {
		return id, nil
	}

	// Try lookup via Stargate Account gRPC service
	if s.Clients != nil && s.Clients.Account != nil {
		resp, err := s.Clients.Account.SearchAccount(ctx, &gen.DySearchAccountRequest{Query: identifier})
		if err == nil && resp != nil {
			for _, acc := range resp.GetAccounts() {
				if strings.EqualFold(acc.GetName(), identifier) || strings.EqualFold(acc.GetNick(), identifier) {
					if accID, err := uuid.Parse(acc.GetId()); err == nil {
						return accID, nil
					}
				}
			}
		}
	}

	return uuid.Nil, gormErrNotFound
}

type errNotFound string

func (e errNotFound) Error() string { return string(e) }

const gormErrNotFound = errNotFound("account not found")
