package authctl

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/go/pkg/cache"
	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/go/pkg/handoff"
	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
)

// Cross-domain session hand-off.
//
// The console's sign-in page is shared, but a session cookie is not: a cookie
// carries one Domain attribute, and a browser refuses one that does not match
// the request host (RFC 6265 §4.1.2.3). An app on another registrable domain
// (dx.solsynth.dev against id.solian.app) therefore cannot receive the session
// by cookie however first-party it is.
//
// Instead the console hands the app a single-use code, and the app's own server
// trades that code for a token pair it stores in its own cookie. The protocol —
// code storage, single-use redemption, PKCE, target policy — lives in
// src.solsynth.dev/sosys/go/pkg/handoff, so every Solar Network app speaks one
// version of it. This file is only the Stargate half: resolving its own session,
// and minting from a redeemed grant.

// handoffClients adapts the configured clients to the SDK's lookup.
func (h *handler) handoffClients(clientID string) (*handoff.Client, error) {
	configured := h.d.Cfg.FindHandoffClient(clientID)
	if configured == nil {
		return nil, nil
	}
	return &handoff.Client{
		ID:                   configured.Id,
		Secret:               configured.Secret,
		AllowedRedirectHosts: configured.RedirectHosts,
	}, nil
}

// handoffClientForHost finds the single client registered for a host. The host
// is already required to match a client's registered hosts, so an ambiguous
// match means two clients claim one domain — a configuration error Validate
// cannot see (hosts may be subdomain-overlapping), and one that must not be
// resolved by picking arbitrarily.
func (h *handler) handoffClientForHost(host string) (*handoff.Client, error) {
	var match *handoff.Client
	for _, configured := range h.d.Cfg.Auth.HandoffClients {
		if !handoff.HostAllowed(host, configured.RedirectHosts) {
			continue
		}
		if match != nil {
			return nil, errs.New(
				"AUTH_HANDOFF_AMBIGUOUS_HOST",
				"More than one hand-off client is registered for this host.",
				http.StatusInternalServerError,
			)
		}
		match = &handoff.Client{
			ID:                   configured.Id,
			Secret:               configured.Secret,
			AllowedRedirectHosts: configured.RedirectHosts,
		}
	}
	return match, nil
}

// registerHandoff mounts the hand-off endpoints.
//
// Nothing is mounted unless at least one client is configured: an unmounted
// feature should not answer "unknown client" to a deployment that never turned
// it on.
//
// The issuer resolves a request with no client_id from the host the app wants
// to land on (ClientForHost), so the console never has to carry an app's id.
func registerHandoff(authGroup *gin.RouterGroup, h *handler) {
	if len(h.d.Cfg.Auth.HandoffClients) == 0 {
		return
	}
	var cache cache.CacheService
	if h.d.Redis != nil {
		cache = h.d.Redis.Cache
	}
	handoff.MountIssue(authGroup, handoff.IssueEndpointDeps{
		Issuer: &handoff.Issuer{
			Cache: cache, Clients: h.handoffClients, ClientForHost: h.handoffClientForHost,
			CodeTTL: h.d.Cfg.HandoffCodeLifetime(),
		},
		Session: h.handoffSession,
		// The session handed off is the caller's, so the route is exactly as
		// protected as the rest of the authenticated auth surface.
		Middleware: []gin.HandlerFunc{middleware.RequireAuth(), middleware.RequireInteractive()},
		// The cookie's Secure flag is the authority on whether https is
		// required: a deployment that has not enabled it must still be able to
		// hand a session to an http app on its own infrastructure.
		RequireHTTPS: new(h.d.Cfg.Auth.CookieSecure),
	})
	handoff.MountRedeem(authGroup, handoff.RedeemEndpointDeps{
		Redeemer: &handoff.Redeemer{Cache: cache, Clients: h.handoffClients},
		Mint:     h.handoffMint,
	})
	handoff.MountRenew(authGroup, handoff.RenewEndpointDeps{
		Clients: h.handoffClients,
		Renew:   h.handoffRenew,
	})
}

// handoffSession resolves the session being handed off.
//
// The issue endpoint is mounted behind RequireAuth, so the session is the
// caller's own and the request body cannot name one. Only the id, account and
// scopes leave here.
func (h *handler) handoffSession(c *gin.Context) (handoff.SessionRef, bool) {
	ctx := c.Request.Context()
	session := middleware.CurrentSession(ctx)
	user := middleware.CurrentUser(ctx)
	if session == nil || user == nil {
		return handoff.SessionRef{}, false
	}
	// A bot token's session was never a person signing in, so it is not a
	// sign-in to hand to an app.
	if middleware.CurrentTokenType(ctx) == auth.TokenTypeApiKey {
		return handoff.SessionRef{}, false
	}
	return handoff.SessionRef{
		SessionID: session.Id,
		AccountID: user.Id,
		Scopes:    session.Scopes,
	}, true
}

// handoffMint turns a redeemed grant into the token pair the relying app stores
// in its own cookie. The code only ever named a session; minting stays here.
//
// The pair belongs to the session the person signed in with, so revoking that
// session (security console, password change, admin action) revokes the app's
// session too: a hand-off does not leave a second foothold behind.
func (h *handler) handoffMint(ctx context.Context, grant *handoff.Grant) (*handoff.RedeemWireResponse, error) {
	sessionID, err := uuid.Parse(grant.SessionID)
	if err != nil {
		return nil, errs.BadRequest("AUTH_SESSION_INVALID", "The handed-off session identifier is invalid.")
	}
	session, err := h.d.Store.GetSessionWithAccount(ctx, sessionID)
	if err != nil {
		return nil, handoff.NewError(handoff.CodeInvalidGrant, "The session behind this code is no longer available.")
	}
	if session.AccountId != grant.AccountID {
		return nil, errs.BadRequest("AUTH_SESSION_MISMATCH", "The session does not belong to the handed-off account.")
	}
	pair, err := h.d.Auth.CreateTokenPair(ctx, session)
	if err != nil {
		return nil, err
	}
	return handoffPairResponse(pair), nil
}

// handoffRenew re-issues a pair for the app's own refresh token, so a hand-off
// session outlives the short access-token lifetime instead of forcing a
// re-login. The token is Stargate's, minted for the session the hand-off used,
// so the existing rotation semantics (epoch bump, grace window, revocation)
// apply unchanged.
func (h *handler) handoffRenew(ctx context.Context, grant handoff.RefreshGrant) (*handoff.RedeemWireResponse, error) {
	pair, _, err := h.d.Auth.RefreshSessionAndIssueTokens(ctx, grant.RefreshToken)
	if err != nil {
		return nil, handoff.NewError(handoff.CodeInvalidGrant, "The refresh token is invalid or has expired.")
	}
	return handoffPairResponse(pair), nil
}

// handoffPairResponse maps a token pair onto the shared wire shape.
func handoffPairResponse(pair *auth.TokenPair) *handoff.RedeemWireResponse {
	return &handoff.RedeemWireResponse{
		Token:            pair.AccessToken,
		RefreshToken:     pair.RefreshToken,
		ExpiresIn:        secondsUntil(pair.AccessTokenExpiresAt),
		RefreshExpiresIn: secondsUntil(pair.RefreshTokenExpiresAt),
	}
}

// secondsUntil converts an expiry instant into the wire's seconds-from-now,
// clamped at zero so an already-expired instant never reports negative.
func secondsUntil(at time.Time) int64 {
	if seconds := int64(time.Until(at).Seconds()); seconds > 0 {
		return seconds
	}
	return 0
}
