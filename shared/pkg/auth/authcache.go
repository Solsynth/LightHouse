package auth

import "fmt"

const (
	sessionCachePrefix  = "auth:session:"
	profileCachePrefix  = "auth:profile:"
	lastSeenTouchPrefix = "auth:last_seen_touch:"
)

// SessionCacheKey returns the cache key for an auth session.
// After cache normalization, the Redis key becomes "dyson:auth:session:{id}".
func SessionCacheKey(sessionID string) string {
	return fmt.Sprintf("%s%s", sessionCachePrefix, sessionID)
}

// ProfileCacheKey returns the cache key for an account profile.
// After cache normalization, the Redis key becomes "dyson:auth:profile:{id}".
func ProfileCacheKey(accountID string) string {
	return fmt.Sprintf("%s%s", profileCachePrefix, accountID)
}

// LastSeenTouchKey returns the throttle key for last-seen updates.
// After cache normalization, the Redis key becomes "dyson:auth:last_seen_touch:{id}".
func LastSeenTouchKey(accountID string) string {
	return fmt.Sprintf("%s%s", lastSeenTouchPrefix, accountID)
}
