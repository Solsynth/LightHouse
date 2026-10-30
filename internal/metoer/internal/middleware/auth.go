// Package middleware provides the auth and permission middleware mirroring
// the C# DysonTokenAuthHandler + RemotePermissionMiddleware.
package middleware

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/go/pkg/auth"
	"src.solsynth.dev/sosys/go/pkg/cache"
	"src.solsynth.dev/sosys/go/pkg/errs"
)

// AuthDeps bundles what the auth middleware needs.
type AuthDeps struct {
	// Authenticator validates tokens against DyAuthService (Stargate).
	Authenticator auth.TokenAuthenticator
	// Cache is the shared Redis cache service (session/profile/throttle).
	Cache cache.CacheService
	// Profiles hydrates account profiles (DyProfileService, Stargate).
	Profiles gen.DyProfileServiceClient
	// LastActive publishes throttled accounts.last_active events.
	LastActive *LastActivePublisher
	Log        *slog.Logger
}

// Auth returns a middleware that authenticates requests, mirroring
// DysonTokenAuthHandler. Routes without a token pass through; handlers that
// require auth call RequireAuth. On success it stores the AuthResult via
// pkg/auth.WithAuth, hydrates the profile (cached 5m) and publishes the
// throttled accounts.last_active event (the C# handler's
// HydrateProfileAsync + TouchProfileLastSeenAsync — the touch is an event
// publish, NOT a gRPC UpdateProfile).
func Auth(deps AuthDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		req := c.Request
		tokenInfo, ok := auth.ExtractToken(req)
		if !ok {
			c.Next()
			return
		}
		result, err := auth.AuthenticateRequest(c.Request.Context(), deps.Authenticator, req)
		if err != nil {
			// C# handler: failed auth leaves the request anonymous.
			if deps.Log != nil {
				deps.Log.Debug("request authentication failed; continuing anonymous", "error", err)
			}
			c.Next()
			return
		}
		auth.WithAuth(c, result, tokenInfo)

		// Profile hydration (cached 5m) + last-active publish (throttled
		// 1m), both best-effort like the C# handler.
		_ = auth.HydrateProfile(c.Request.Context(), deps.Cache, deps.Profiles, result.Account)
		if deps.LastActive != nil {
			deps.LastActive.Touch(c.Request.Context(), result)
		}
		c.Next()
	}
}

// RequireAuth rejects unauthenticated requests with a 401 ApiError.
func RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		result, _, ok := auth.GetAuth(c)
		if !ok || result == nil || result.Account == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		c.Next()
	}
}

// CurrentUser extracts the authenticated account from the request context
// (nil when anonymous).
func CurrentUser(c *gin.Context) *gen.DyAccount {
	result, _, ok := auth.GetAuth(c)
	if !ok || result == nil || result.Account == nil {
		return nil
	}
	return result.Account
}

// CurrentSession extracts the authenticated session (nil when anonymous).
func CurrentSession(c *gin.Context) *gen.DyAuthSession {
	result, _, ok := auth.GetAuth(c)
	if !ok || result == nil || result.Session == nil {
		return nil
	}
	return result.Session
}
