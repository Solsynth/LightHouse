// Package middleware provides the auth, permission and support middleware
// mirroring Padlock's DysonTokenAuthHandler, LocalPermissionMiddleware and
// friends.
package middleware

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/internal/store"
	"src.solsynth.dev/sosys/stargate/internal/model"
)

type contextKey int

const (
	ctxKeyCurrentUser contextKey = iota
	ctxKeyCurrentSession
	ctxKeyCurrentTokenType
	// ctxKeyAuthRejectReason carries the token rejection reason set by Auth
	// so RequireAuth can emit a precise error code (e.g. TOKEN_EXPIRED).
	ctxKeyAuthRejectReason
)

// CurrentUser extracts the authenticated account from the context.
func CurrentUser(ctx context.Context) *model.Account {
	if v, ok := ctx.Value(ctxKeyCurrentUser).(*model.Account); ok {
		return v
	}
	return nil
}

// CurrentSession extracts the authenticated session from the context.
func CurrentSession(ctx context.Context) *model.AuthSession {
	if v, ok := ctx.Value(ctxKeyCurrentSession).(*model.AuthSession); ok {
		return v
	}
	return nil
}

// CurrentTokenType extracts the token type (AuthKey/ApiKey/OidcKey).
func CurrentTokenType(ctx context.Context) auth.TokenType {
	if v, ok := ctx.Value(ctxKeyCurrentTokenType).(auth.TokenType); ok {
		return v
	}
	return auth.TokenTypeUnknown
}

// LastSeenToucher debounced-updates account_profiles.last_seen_at plus the
// session's last_granted_at/keep-alive, mirroring Padlock's
// FlushBufferService/LastActiveFlushHandler (last-active now lands on
// Stargate's tables).
type LastSeenToucher struct {
	st   *store.Store
	log  *slog.Logger

	mu     sync.Mutex
	queue  map[string]touchEntry
	stop   chan struct{}
	done   chan struct{}
	closed bool
}

type touchEntry struct {
	accountID string
	sessionID string
}

// NewLastSeenToucher starts the flush loop (5s flush interval).
func NewLastSeenToucher(st *store.Store, log *slog.Logger) *LastSeenToucher {
	t := &LastSeenToucher{st: st, log: log, queue: map[string]touchEntry{}, stop: make(chan struct{}), done: make(chan struct{})}
	go t.loop()
	return t
}

// Enqueue records a last-seen update for the account + session.
func (t *LastSeenToucher) Enqueue(accountID, sessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.queue[accountID] = touchEntry{accountID: accountID, sessionID: sessionID}
}

// Close stops the flush loop.
func (t *LastSeenToucher) Close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	close(t.stop)
	t.mu.Unlock()
	<-t.done
}

func (t *LastSeenToucher) loop() {
	defer close(t.done)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-t.stop:
			t.flush()
			return
		case <-ticker.C:
			t.flush()
		}
	}
}

func (t *LastSeenToucher) flush() {
	t.mu.Lock()
	if len(t.queue) == 0 {
		t.mu.Unlock()
		return
	}
	queue := t.queue
	t.queue = map[string]touchEntry{}
	t.mu.Unlock()
	now := time.Now().UTC()
	for _, entry := range queue {
		if err := t.st.TouchLastActive(context.Background(), entry.accountID, entry.sessionID, now); err != nil {
			t.log.Warn("flush last_active", "account", entry.accountID, "session", entry.sessionID, "error", err)
		}
	}
}

// TokenRenewer rotates an expired access token using its refresh token,
// returning the fresh pair plus the rotated session.
type TokenRenewer interface {
	RefreshSessionAndIssueTokens(ctx context.Context, refreshToken string) (*auth.TokenPair, *model.AuthSession, error)
}

// AuthDeps bundles what the auth middleware needs.
type AuthDeps struct {
	Token        *auth.TokenAuthService
	Renewer      TokenRenewer
	Toucher      *LastSeenToucher
	CookieDomain string
	CookieSecure bool
	Log          *slog.Logger
}

// Auth returns a middleware that authenticates requests, mirroring
// DysonTokenAuthHandler. Routes without a token pass through; handlers that
// require auth call RequireAuth.
func Auth(deps AuthDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenInfo := auth.ExtractToken(c.Request)
		if tokenInfo == nil || tokenInfo.Token == "" {
			c.Next()
			return
		}
		ip := ClientIP(c.Request)
		valid, session, message, _ := deps.Token.AuthenticateToken(c.Request.Context(), tokenInfo.Token, ip)

		// Auto-renew: an expired access token whose session is still valid is
		// transparently rotated via the RefreshToken cookie, so clients with a
		// valid session never see TOKEN_EXPIRED. OIDC and API-key tokens are
		// excluded (see TokenAuthService.AutoRenewable).
		if (!valid || session == nil) && message == auth.MsgTokenExpired && deps.Renewer != nil {
			if refreshToken, err := c.Cookie("RefreshToken"); err == nil && strings.TrimSpace(refreshToken) != "" {
				if deps.Token.AutoRenewable(tokenInfo.Token, refreshToken) {
					pair, renewed, rerr := deps.Renewer.RefreshSessionAndIssueTokens(c.Request.Context(), refreshToken)
					if rerr == nil && pair != nil && renewed != nil {
						setAuthCookies(c, pair, deps.CookieDomain, deps.CookieSecure)
						valid, session = true, renewed
						deps.Log.Debug("auto-renewed expired access token", "session", renewed.Id)
					} else {
						deps.Log.Debug("auto-renew failed", "error", rerr)
					}
				}
			}
		}

		if !valid || session == nil {
			// Mirror DysonTokenAuthHandler: an invalid token leaves the
			// request unauthenticated but does NOT reject anonymous routes
			// (login/challenge start must work even with a stale token
			// attached). RequireAuth and handlers checking CurrentUser
			// emit the 401 where auth is actually mandatory.
			ctx := context.WithValue(c.Request.Context(), ctxKeyAuthRejectReason, message)
			c.Request = c.Request.WithContext(ctx)
			deps.Log.Debug("token rejected", "reason", message)
			c.Next()
			return
		}
		// API-key tokens are typed ApiKey regardless of extraction path.
		tokenType := tokenInfo.Type
		if auth.IsApiKeyTokenString(tokenInfo.Token) {
			tokenType = auth.TokenTypeApiKey
		}
		ctx := context.WithValue(c.Request.Context(), ctxKeyCurrentUser, session.Account)
		ctx = context.WithValue(ctx, ctxKeyCurrentSession, session)
		ctx = context.WithValue(ctx, ctxKeyCurrentTokenType, tokenType)
		c.Request = c.Request.WithContext(ctx)
		if deps.Toucher != nil && session.Account != nil {
			deps.Toucher.Enqueue(session.Account.Id, session.Id)
		}
		c.Next()
	}
}

// setAuthCookies mirrors Padlock's SetAuthCookies (HttpOnly, Secure,
// SameSite=Lax, domain from AuthToken:CookieDomain).
func setAuthCookies(c *gin.Context, pair *auth.TokenPair, domain string, secure bool) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("AuthToken", pair.AccessToken, int(pair.AccessTokenExpiresAt.Sub(time.Now()).Seconds()), "/", domain, secure, true)
	c.SetCookie("RefreshToken", pair.RefreshToken, int(pair.RefreshTokenExpiresAt.Sub(time.Now()).Seconds()), "/", domain, secure, true)
}

// RequireAuth rejects unauthenticated requests with a 401 ApiError. A token
// whose JWT exp has passed is rejected with the distinct TOKEN_EXPIRED code
// (clients refresh and retry); every other rejection is the canonical
// UNAUTHORIZED.
func RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if CurrentUser(c.Request.Context()) == nil {
			if reason, _ := c.Request.Context().Value(ctxKeyAuthRejectReason).(string); reason == auth.MsgTokenExpired {
				c.AbortWithStatusJSON(http.StatusUnauthorized, errs.New("TOKEN_EXPIRED", auth.MsgTokenExpired, http.StatusUnauthorized))
				return
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		c.Next()
	}
}

// RequireInteractive rejects API-key (bot) tokens on interactive-only routes.
func RequireInteractive() gin.HandlerFunc {
	return func(c *gin.Context) {
		if CurrentTokenType(c.Request.Context()) == auth.TokenTypeApiKey {
			c.AbortWithStatusJSON(http.StatusForbidden, errs.Forbidden("Interactive session required."))
			return
		}
		c.Next()
	}
}

// ClientIP resolves the client IP honoring X-Forwarded-For (mirrors
// GetClientIpAddress with KnownProxies trusted).
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for _, part := range parts {
			ip := strings.TrimSpace(part)
			if ip != "" && !isKnownProxy(ip) {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isKnownProxy(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	if parsed.IsLoopback() {
		return true
	}
	return false
}

// AccountIDOf parses an account UUID from a string.
func AccountIDOf(s string) uuid.UUID {
	id, _ := uuid.Parse(s)
	return id
}
