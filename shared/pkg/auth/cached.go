package auth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/cache"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

const sessionCacheTTL = time.Hour

// CachedTokenAuthenticator wraps a TokenAuthenticator with session caching.
// Validated sessions are cached for 1 hour, keyed by JWT session ID or token hash.
type CachedTokenAuthenticator struct {
	inner TokenAuthenticator
	cache cache.CacheService
}

// NewCachedTokenAuthenticator wraps an authenticator with session caching.
func NewCachedTokenAuthenticator(inner TokenAuthenticator, c cache.CacheService) *CachedTokenAuthenticator {
	return &CachedTokenAuthenticator{inner: inner, cache: c}
}

func (a *CachedTokenAuthenticator) Authenticate(ctx context.Context, tokenInfo TokenInfo, r *http.Request) (*AuthResult, error) {
	if a.inner == nil {
		return nil, errors.New("token authenticator is not configured")
	}

	sessionID := ExtractSessionIDFromJWT(tokenInfo.Token)
	if sessionID == "" {
		sessionID = TokenHash(tokenInfo.Token)
	}

	cacheKey := SessionCacheKey(sessionID)

	if a.cache != nil {
		// Check cache first
		var cachedSession gen.DyAuthSession
		found, err := a.cache.Get(ctx, cacheKey, &cachedSession)
		if err == nil && found && cachedSession.Account != nil {
			// Apply any token scopes from JWT
			if scopes := ExtractScopesFromJWT(tokenInfo.Token); len(scopes) > 0 {
				cachedSession.Scopes = scopes
			}
			return &AuthResult{
				Account: cachedSession.Account,
				Session: &cachedSession,
			}, nil
		}
	}

	// Cache miss — validate via underlying authenticator
	result, err := a.inner.Authenticate(ctx, tokenInfo, r)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("token authenticator returned nil result")
	}

	// Cache the validated session
	if a.cache != nil && result.Session != nil {
		_ = a.cache.Set(ctx, cacheKey, result.Session, sessionCacheTTL)
	}

	// Apply any token scopes from JWT
	if scopes := ExtractScopesFromJWT(tokenInfo.Token); len(scopes) > 0 {
		if result.Session != nil {
			result.Session.Scopes = scopes
		}
	}

	return result, nil
}
