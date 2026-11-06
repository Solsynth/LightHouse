package middleware

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/stargate/model"
)

// presetScopes are the scopes that describe identity, not a permission: the
// scope filter skips them when matching a permission key. Mirrors
// PermissionScopeGate.PresetScopes (case-insensitive).
var presetScopes = map[string]struct{}{
	"openid":  {},
	"profile": {},
	"email":   {},
	"*":       {},
}

// MatchedScope returns the scope that permits permissionKey, if any.
//
// A token scope is a filter over the permissions the account already holds,
// never a grant: "*" matches every key (it removes the narrowing) but the
// caller must still run the permission lookup. Preset scopes permit nothing on
// their own, an exact key matches itself, and "prefix.*" matches keys under
// prefix. An empty scope list matches nothing.
func MatchedScope(scopes []string, permissionKey string) (string, bool) {
	if strings.TrimSpace(permissionKey) == "" {
		return "", false
	}
	for _, rawScope := range scopes {
		scope := strings.TrimSpace(rawScope)
		if scope == "" {
			continue
		}
		if strings.EqualFold(scope, "*") {
			return "*", true
		}
		if _, preset := presetScopes[strings.ToLower(scope)]; preset {
			continue
		}
		if strings.EqualFold(scope, permissionKey) {
			return scope, true
		}
		if strings.HasSuffix(scope, ".*") {
			prefix := scope[:len(scope)-1]
			if strings.HasPrefix(strings.ToLower(permissionKey), strings.ToLower(prefix)) {
				return scope, true
			}
		}
	}
	return "", false
}

// TokenScopeAllows reports whether scopes permit permissionKey. It is the
// filter half of a permission gate: true only narrows nothing, it never
// authorizes.
func TokenScopeAllows(scopes []string, permissionKey string) bool {
	_, ok := MatchedScope(scopes, permissionKey)
	return ok
}

// ScopeAllowsContext reports whether the session in ctx permits key.
//
// A token scope narrows what its session may do; it never grants. OAuth
// sessions are filtered by the scopes the app was granted ("*" matches every
// key and so removes the narrowing — the permission check still decides).
// Login, OIDC and API-key sessions carry no app grant and are not filtered,
// mirroring PermissionScopeGate in the C# fleet.
func ScopeAllowsContext(ctx context.Context, key string) bool {
	session := CurrentSession(ctx)
	if session == nil || session.Type != model.SessionTypeOAuth {
		return true
	}
	return TokenScopeAllows(session.Scopes, key)
}

// ScopeAllows is ScopeAllowsContext for a gin handler.
func ScopeAllows(c *gin.Context, key string) bool {
	return ScopeAllowsContext(c.Request.Context(), key)
}
