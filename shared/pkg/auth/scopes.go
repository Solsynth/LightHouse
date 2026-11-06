package auth

import "strings"

// PresetScopes are the scopes that describe identity, not a permission: the
// scope gate skips them when matching a permission key. Mirrors
// PermissionScopeGate.PresetScopes (case-insensitive).
var PresetScopes = map[string]struct{}{
	"openid":  {},
	"profile": {},
	"email":   {},
	"*":       {},
}

// MatchedScope returns the scope that permits permissionKey, if any.
//
// A token scope is a filter over the permissions the account already holds,
// never a grant: "*" matches every key (it removes the narrowing) but the
// caller must still run the permission-service check. Unknown preset scopes
// (openid/profile/email) permit nothing on their own, an exact key matches
// itself, and "prefix.*" matches keys under prefix. An empty scope list
// matches nothing.
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
		if _, preset := PresetScopes[strings.ToLower(scope)]; preset {
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

// ScopeAllows reports whether scopes permit permissionKey. It is the filter
// half of a permission gate: true only narrows nothing, it never authorizes.
func ScopeAllows(scopes []string, permissionKey string) bool {
	_, ok := MatchedScope(scopes, permissionKey)
	return ok
}
