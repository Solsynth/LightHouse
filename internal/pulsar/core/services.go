package core

import (
	"context"
	"strings"
	"time"

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

// PublishSessionUpdate pushes live playback session changes to WebSocket
// clients via Blade. The actor receives the update for its own UI; friends
// need it too because their presence strips and toasts are invalidated by the
// same packet.
func (s *Services) PublishSessionUpdate(ctx context.Context, accountID uuid.UUID, session *model.ActiveSession) {
	if s.NATS == nil || !s.NATS.Enabled() {
		return
	}
	payload := map[string]any{
		"account_id": accountID.String(),
		"session":    session,
	}
	if err := s.NATS.PublishWS(ctx, accountID.String(), "account.presence.activities.updated", payload); err != nil {
		if s.Log != nil {
			s.Log.Warn("failed to push session update via websocket", "account_id", accountID, "error", err)
		}
	}
	s.publishSessionToFriends(ctx, accountID, payload)
}

// publishSessionToFriends resolves the social graph in Stargate and fans the
// same packet out to each friend. Missing relationship RPCs only disable the
// realtime fan-out; the account's own push above still lands.
func (s *Services) publishSessionToFriends(ctx context.Context, accountID uuid.UUID, payload map[string]any) {
	if s.Clients == nil || s.Clients.Profile == nil {
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	resp, err := s.Clients.Profile.ListFriends(callCtx, &gen.DyListRelationshipSimpleRequest{
		RelationIdentifier: &gen.DyListRelationshipSimpleRequest_AccountId{
			AccountId: accountID.String(),
		},
	})
	if err != nil {
		if s.Log != nil {
			s.Log.Warn("failed to resolve friends for presence push", "account_id", accountID, "error", err)
		}
		return
	}
	if len(resp.GetAccountsId()) == 0 {
		return
	}
	if err := s.NATS.PublishWSToAccounts(
		callCtx,
		resp.GetAccountsId(),
		"account.presence.activities.updated",
		payload,
		"",
	); err != nil && s.Log != nil {
		s.Log.Warn("failed to push session update to friends", "account_id", accountID, "error", err)
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
