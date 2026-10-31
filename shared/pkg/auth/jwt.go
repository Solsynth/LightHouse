package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// IsApiKeyToken checks if a JWT token represents an API key (bot token)
// by inspecting the "type" claim. Returns true if type == "api_key".
func IsApiKeyToken(token string) bool {
	claims, ok := parseJWTPayload(token)
	if !ok {
		return false
	}
	t, _ := claims["type"].(string)
	return t == "api_key"
}

// ExtractSessionIDFromJWT parses a JWT (without signature validation) and returns
// the session ID from the "jti" or "sid" claim. Returns empty string if the token
// is not a valid JWT or has no session claims.
func ExtractSessionIDFromJWT(token string) string {
	claims, ok := parseJWTPayload(token)
	if !ok {
		return ""
	}
	if jti, _ := claims["jti"].(string); jti != "" {
		return jti
	}
	if sid, _ := claims["sid"].(string); sid != "" {
		return sid
	}
	return ""
}

// ExtractScopesFromJWT parses a JWT (without signature validation) and returns
// the scopes from the "scope" or "scp" claim. Scopes can be space-separated
// strings in the claim value.
func ExtractScopesFromJWT(token string) []string {
	claims, ok := parseJWTPayload(token)
	if !ok {
		return nil
	}

	var raw string
	if v, _ := claims["scope"].(string); v != "" {
		raw = v
	} else if v, _ := claims["scp"].(string); v != "" {
		raw = v
	}
	if raw == "" {
		return nil
	}

	seen := make(map[string]struct{})
	var scopes []string
	for _, part := range strings.Fields(raw) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, exists := seen[part]; !exists {
			seen[part] = struct{}{}
			scopes = append(scopes, part)
		}
	}
	return scopes
}

// TokenHash returns the hex-encoded SHA256 hash of the token string.
// Used as a cache key fallback for non-JWT tokens.
func TokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// parseJWTPayload decodes the payload segment of a JWT without validating
// the signature. Returns the claims map and true on success.
func parseJWTPayload(token string) (map[string]any, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, false
	}
	return claims, true
}
