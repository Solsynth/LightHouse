package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/cache"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

const (
	// ContextKeyAuth is the gin.Context key for *AuthResult.
	ContextKeyAuth = "auth_result"
	// ContextKeyToken is the gin.Context key for TokenInfo.
	ContextKeyToken = "auth_token"
)

// AuthMiddleware returns a Gin middleware that authenticates every request.
// Sessions are cached to avoid hitting the auth gRPC service on every request.
// On success it stores the AuthResult and TokenInfo in the context (use GetAuth to retrieve),
// hydrates the account profile, and touches last_seen_at.
// On failure it aborts with 401.
func AuthMiddleware(authenticator TokenAuthenticator, c cache.CacheService, profiles gen.DyProfileServiceClient) gin.HandlerFunc {
	cached := NewCachedTokenAuthenticator(authenticator, c)

	return func(ctx *gin.Context) {
		result, tokenInfo, err := authenticateContext(ctx, cached)
		if err != nil {
			ctx.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
			return
		}
		WithAuth(ctx, result, tokenInfo)

		// Hydrate profile + touch last-seen (best-effort, non-fatal)
		_ = HydrateAndTouch(ctx.Request.Context(), c, profiles, result)

		ctx.Next()
	}
}

// OptionalAuthMiddleware is like AuthMiddleware but does not abort on auth failure.
// If a valid token is provided the result is stored in context; otherwise the request proceeds unauthenticated.
func OptionalAuthMiddleware(authenticator TokenAuthenticator, c cache.CacheService, profiles gen.DyProfileServiceClient) gin.HandlerFunc {
	cached := NewCachedTokenAuthenticator(authenticator, c)

	return func(ctx *gin.Context) {
		result, tokenInfo, err := authenticateContext(ctx, cached)
		if err == nil {
			WithAuth(ctx, result, tokenInfo)

			// Hydrate profile + touch last-seen (best-effort, non-fatal)
			_ = HydrateAndTouch(ctx.Request.Context(), c, profiles, result)
		}
		ctx.Next()
	}
}

// WithAuth stores the AuthResult and TokenInfo in the gin.Context.
func WithAuth(c *gin.Context, result *AuthResult, token TokenInfo) {
	c.Set(ContextKeyAuth, result)
	c.Set(ContextKeyToken, token)
}

// GetAuth retrieves the AuthResult and TokenInfo from the gin.Context.
// Returns false if the values are not set (i.e. request was not authenticated).
func GetAuth(c *gin.Context) (*AuthResult, TokenInfo, bool) {
	resultRaw, ok1 := c.Get(ContextKeyAuth)
	tokenRaw, ok2 := c.Get(ContextKeyToken)
	if !ok1 || !ok2 {
		return nil, TokenInfo{}, false
	}
	result, ok := resultRaw.(*AuthResult)
	if !ok {
		return nil, TokenInfo{}, false
	}
	token, ok := tokenRaw.(TokenInfo)
	if !ok {
		return nil, TokenInfo{}, false
	}
	return result, token, true
}

// MustAuth is like GetAuth but panics if auth is not present.
// Use only in handlers behind AuthMiddleware.
func MustAuth(c *gin.Context) (*AuthResult, TokenInfo) {
	result, token, ok := GetAuth(c)
	if !ok {
		panic("auth: no auth result in context; ensure AuthMiddleware is applied")
	}
	return result, token
}

func authenticateContext(c *gin.Context, authenticator TokenAuthenticator) (*AuthResult, TokenInfo, error) {
	tokenInfo, ok := ExtractToken(c.Request)
	if !ok || strings.TrimSpace(tokenInfo.Token) == "" {
		return nil, TokenInfo{}, &AuthError{Message: "no token was provided"}
	}

	result, err := AuthenticateRequest(c.Request.Context(), authenticator, c.Request)
	if err != nil {
		return nil, TokenInfo{}, err
	}
	return result, tokenInfo, nil
}

// RequireSudo returns a Gin middleware that fails closed unless the
// authenticated session is currently elevated. Apply it after AuthMiddleware.
//
// Sessions are cached for an hour, so elevation is never read from the cached
// session: the auth service is asked directly (with an 8-second timeout).
// On failure it aborts with 403 AUTH_SUDO_REQUIRED, or 503 when elevation
// cannot be determined.
func RequireSudo(validator SudoValidator) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if validator == nil {
			ctx.AbortWithStatusJSON(http.StatusServiceUnavailable, sudoUnavailableError())
			return
		}

		result, _, ok := GetAuth(ctx)
		if !ok || result == nil {
			ctx.AbortWithStatusJSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		sessionID := result.Session.GetId()
		if sessionID == "" {
			ctx.AbortWithStatusJSON(http.StatusServiceUnavailable, sudoUnavailableError())
			return
		}

		sudoCtx, cancel := context.WithTimeout(ctx.Request.Context(), 8*time.Second)
		defer cancel()

		valid, factorTypes, err := validator.ValidateSudo(sudoCtx, sessionID)
		if err != nil {
			ctx.AbortWithStatusJSON(http.StatusServiceUnavailable, sudoUnavailableError())
			return
		}
		if !valid {
			ctx.AbortWithStatusJSON(http.StatusForbidden, SudoRequiredError(factorTypes))
			return
		}

		ctx.Next()
	}
}

// SudoRequiredError builds the 403 body for a request that is not elevated.
// Its detail carries the comma-joined factor type names the client may use,
// mirroring the Stargate AUTH_SUDO_REQUIRED response.
func SudoRequiredError(factorTypes []string) *errs.ApiError {
	apiErr := errs.New("AUTH_SUDO_REQUIRED", "This action requires re-authentication.", http.StatusForbidden)
	if len(factorTypes) > 0 {
		detail := strings.Join(factorTypes, ",")
		apiErr.Detail = &detail
	}
	return apiErr
}

func sudoUnavailableError() *errs.ApiError {
	return errs.New("SERVICE_UNAVAILABLE", "Elevation could not be verified.", http.StatusServiceUnavailable)
}

// AuthError represents an authentication failure.
type AuthError struct {
	Message string
}

func (e *AuthError) Error() string {
	return e.Message
}
