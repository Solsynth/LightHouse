// Package authn authenticates inbound requests the way the C# fleet's
// DysonTokenAuthHandler does: the bearer token (or AuthToken cookie, or ?tk=)
// is handed to the auth service over gRPC, which answers with the session and
// account behind it. No Lighthouse service validates JWTs locally, so a token
// revoked in Stargate stops working here immediately.
package authn

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/auth"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/cache"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

// Context keys used by the handlers. They mirror the C# HttpContext.Items
// names so the port reads the same way.
const (
	ContextKeyUser       = "current_user"
	ContextKeySession    = "current_session"
	ContextKeyTokenType  = "current_token_type"
	ContextKeyPermission = "permission_granted"
)

// Options configures the authenticator.
type Options struct {
	// Target is the auth service's gRPC endpoint (Stargate's DyAuthService).
	Target string
	// UseTLS dials with TLS. Fleet certificates are self-signed, so CA
	// validation is skipped like every other service in the fleet.
	UseTLS bool
	// Cache is the validated-session cache. Nil disables caching.
	Cache cache.CacheService
	// CacheTTL is how long a validated session is reused. Zero means one hour,
	// matching the C# handler.
	CacheTTL time.Duration
	// TouchInterval throttles last-seen write-backs. Zero disables them.
	TouchInterval time.Duration
	Log           *slog.Logger
}

// Service is the request authenticator and the outbound clients it needs.
type Service struct {
	authenticator auth.TokenAuthenticator
	cache         cache.CacheService
	profiles      gen.DyProfileServiceClient
	permissions   gen.DyPermissionServiceClient
	conn          *grpc.ClientConn
	log           *slog.Logger
}

// New dials the auth service and builds the authenticator. When target is
// empty the service still exists but authenticates nothing, so a deployment
// without Stargate serves anonymous routes only.
func New(opts Options) (*Service, error) {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.CacheTTL <= 0 {
		opts.CacheTTL = time.Hour
	}
	service := &Service{cache: opts.Cache, log: opts.Log}
	target := strings.TrimSpace(opts.Target)
	if target == "" {
		return service, nil
	}
	creds := credentials.TransportCredentials(insecure.NewCredentials())
	if opts.UseTLS {
		// Fleet certificates are self-signed, so CA validation is skipped like
		// every other service in the fleet.
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true})
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, err
	}
	service.conn = conn
	service.profiles = gen.NewDyProfileServiceClient(conn)
	service.permissions = gen.NewDyPermissionServiceClient(conn)
	authenticator, err := auth.NewGrpcTokenAuthenticator(auth.GrpcAuthDialConfig{
		Target:        target,
		UseTLS:        opts.UseTLS,
		TLSSkipVerify: true,
	})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	service.authenticator = authenticator
	return service, nil
}

// Close releases the outbound connection.
func (s *Service) Close() {
	if s != nil && s.conn != nil {
		_ = s.conn.Close()
	}
}

// Configured reports whether an auth service target was supplied.
func (s *Service) Configured() bool {
	return s != nil && s.authenticator != nil
}

// Middleware authenticates requests that carry a token and lets anonymous
// requests through. Handlers that require a user call RequireAuth.
func (s *Service) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.Configured() {
			c.Next()
			return
		}
		tokenInfo, ok := auth.ExtractToken(c.Request)
		if !ok || strings.TrimSpace(tokenInfo.Token) == "" {
			c.Next()
			return
		}
		result, err := auth.AuthenticateRequest(c.Request.Context(), s.authenticator, c.Request)
		if err != nil {
			// An invalid token is not a 401 on anonymous routes: the C# scheme
			// fails authentication but lets the request continue, and only
			// [Authorize] actions reject it.
			c.Next()
			return
		}
		s.setAuth(c, result, tokenInfo)
		if s.cache != nil && s.profiles != nil {
			_ = auth.HydrateAndTouch(c.Request.Context(), s.cache, s.profiles, result)
		}
		c.Next()
	}
}

func (s *Service) setAuth(c *gin.Context, result *auth.AuthResult, tokenInfo auth.TokenInfo) {
	c.Set(ContextKeyUser, result.Account)
	c.Set(ContextKeySession, result.Session)
	c.Set(ContextKeyTokenType, tokenInfo.Type)
	c.Set(ContextKeyPermission, result)
}

// RequireAuth rejects unauthenticated requests with the canonical 401
// ApiError. It must run after Middleware.
func RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if CurrentUser(c) == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		c.Next()
	}
}

// RequireInteractive rejects API-key (bot) tokens on interactive-only routes.
func RequireInteractive() gin.HandlerFunc {
	return func(c *gin.Context) {
		if CurrentTokenType(c) == auth.TokenTypeApiKey {
			c.AbortWithStatusJSON(http.StatusForbidden,
				errs.Forbidden("This action is not available to API keys."))
			return
		}
		c.Next()
	}
}

// RequirePermission gates a route on a permission node, mirroring
// [AskPermission]. Superusers bypass the check, like the C# middleware.
func (s *Service) RequirePermission(key string) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := CurrentUser(c)
		if user == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		if user.GetIsSuperuser() {
			c.Next()
			return
		}
		if s.permissions == nil {
			// No permission service: fail closed rather than grant.
			c.AbortWithStatusJSON(http.StatusServiceUnavailable,
				errs.New("SERVICE_UNAVAILABLE", "The permission service is unavailable.", http.StatusServiceUnavailable))
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
		defer cancel()
		resp, err := s.permissions.HasPermission(ctx, &gen.DyHasPermissionRequest{
			Actor: user.GetId(),
			Key:   key,
		})
		if err != nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable,
				errs.New("SERVICE_UNAVAILABLE", "The permission service is unavailable.", http.StatusServiceUnavailable))
			return
		}
		if !resp.GetHasPermission() {
			c.AbortWithStatusJSON(http.StatusForbidden,
				errs.Forbidden("You do not have the permission to perform this action."))
			return
		}
		c.Next()
	}
}

// RequireAuthOrAPIKey is RequireAuth for routes that also accept bot tokens.
func RequireAuthOrAPIKey() gin.HandlerFunc { return RequireAuth() }

// CurrentUser returns the authenticated account, or nil.
func CurrentUser(c *gin.Context) *gen.DyAccount {
	value, ok := c.Get(ContextKeyUser)
	if !ok {
		return nil
	}
	account, _ := value.(*gen.DyAccount)
	return account
}

// CurrentSession returns the authenticated session, or nil.
func CurrentSession(c *gin.Context) *gen.DyAuthSession {
	value, ok := c.Get(ContextKeySession)
	if !ok {
		return nil
	}
	session, _ := value.(*gen.DyAuthSession)
	return session
}

// CurrentTokenType returns the token type of the authenticated request.
func CurrentTokenType(c *gin.Context) auth.TokenType {
	value, ok := c.Get(ContextKeyTokenType)
	if !ok {
		return ""
	}
	tokenType, _ := value.(auth.TokenType)
	return tokenType
}

// AccountID returns the authenticated account id, or "".
func AccountID(c *gin.Context) string {
	if user := CurrentUser(c); user != nil {
		return user.GetId()
	}
	return ""
}

// IsSuperuser reports whether the authenticated account is a superuser.
func IsSuperuser(c *gin.Context) bool {
	user := CurrentUser(c)
	return user != nil && user.GetIsSuperuser()
}
