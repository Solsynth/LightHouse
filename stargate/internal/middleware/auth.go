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
	"github.com/jackc/pgx/v5/pgconn"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/errs"
	"src.solsynth.dev/sosys/stargate/internal/model"
)

type contextKey int

const (
	ctxKeyCurrentUser contextKey = iota
	ctxKeyCurrentSession
	ctxKeyCurrentTokenType
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

// LastSeenToucher debounced-updates account_profiles.last_seen_at, mirroring
// Padlock's FlushBufferService/LastActiveFlushHandler.
type LastSeenToucher struct {
	db interface {
		Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	}
	log *slog.Logger

	mu     sync.Mutex
	queue  map[string]time.Time
	stop   chan struct{}
	done   chan struct{}
	closed bool
}

// NewLastSeenToucher starts the flush loop (5s flush interval).
func NewLastSeenToucher(db interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}, log *slog.Logger) *LastSeenToucher {
	t := &LastSeenToucher{db: db, log: log, queue: map[string]time.Time{}, stop: make(chan struct{}), done: make(chan struct{})}
	go t.loop()
	return t
}

// Enqueue records a last-seen update for the account.
func (t *LastSeenToucher) Enqueue(accountID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.queue[accountID] = time.Now().UTC()
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
	t.queue = map[string]time.Time{}
	t.mu.Unlock()
	now := time.Now().UTC()
	for accountID := range queue {
		if _, err := t.db.Exec(context.Background(),
			`UPDATE account_profiles SET last_seen_at = $1, updated_at = $1 WHERE account_id = $2`,
			now, accountID); err != nil {
			t.log.Warn("flush last_seen", "account", accountID, "error", err)
		}
	}
}

// AuthDeps bundles what the auth middleware needs.
type AuthDeps struct {
	Token   *auth.TokenAuthService
	Toucher *LastSeenToucher
	Log     *slog.Logger
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
		if !valid || session == nil {
			// Mirror DysonTokenAuthHandler: an invalid token leaves the
			// request unauthenticated but does NOT reject anonymous routes
			// (login/challenge start must work even with a stale token
			// attached). RequireAuth and handlers checking CurrentUser
			// emit the 401 where auth is actually mandatory.
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
			deps.Toucher.Enqueue(session.Account.Id)
		}
		c.Next()
	}
}

// RequireAuth rejects unauthenticated requests with a 401 ApiError.
func RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if CurrentUser(c.Request.Context()) == nil {
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
