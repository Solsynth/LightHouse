package auth

import (
	"net/http"
	"strings"
)

// ExtractToken extracts a token from the request, checking (in order):
//  1. Query parameter "tk" (returns AuthKey)
//  2. Authorization header: Bearer only (type determined by JWT claims)
//     including X-Forwarded-Authorization and X-Original-Authorization
//  3. Cookie "AuthToken" (returns AuthKey)
func ExtractToken(r *http.Request) (TokenInfo, bool) {
	if tk := strings.TrimSpace(r.URL.Query().Get("tk")); tk != "" {
		return TokenInfo{Token: tk, Type: TokenTypeAuthKey}, true
	}

	authz := NormalizeAuthHeader(ExtractRawAuthHeader(r))
	if authz != "" {
		scheme, token, ok := splitAuthorizationHeader(authz)
		if ok && strings.ToLower(scheme) == "bearer" {
			tokenType := TokenTypeAuthKey
			if IsApiKeyToken(token) {
				tokenType = TokenTypeApiKey
			}
			return TokenInfo{Token: token, Type: tokenType}, true
		}
	}

	if cookie, err := r.Cookie("AuthToken"); err == nil {
		tk := strings.TrimSpace(cookie.Value)
		if tk != "" {
			return TokenInfo{Token: tk, Type: TokenTypeAuthKey}, true
		}
	}

	return TokenInfo{}, false
}

// ExtractRawAuthHeader returns the first non-empty auth header value from
// Authorization, X-Forwarded-Authorization, or X-Original-Authorization.
func ExtractRawAuthHeader(r *http.Request) string {
	if r == nil {
		return ""
	}

	if authz := strings.TrimSpace(r.Header.Get("Authorization")); authz != "" {
		return authz
	}
	if authz := strings.TrimSpace(r.Header.Get("X-Forwarded-Authorization")); authz != "" {
		return authz
	}
	if authz := strings.TrimSpace(r.Header.Get("X-Original-Authorization")); authz != "" {
		return authz
	}
	return ""
}

// NormalizeAuthHeader strips wrapping brackets and quotes from auth header values.
func NormalizeAuthHeader(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if len(value) >= 2 && value[0] == '[' && value[len(value)-1] == ']' {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	return value
}

func splitAuthorizationHeader(value string) (scheme string, token string, ok bool) {
	parts := strings.Fields(strings.TrimSpace(value))
	if len(parts) < 2 {
		return "", "", false
	}
	return parts[0], strings.TrimSpace(strings.Join(parts[1:], " ")), true
}
