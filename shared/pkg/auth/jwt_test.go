package auth

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func makeJWT(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	payloadEnc := base64.RawURLEncoding.EncodeToString(payload)
	sig := base64.RawURLEncoding.EncodeToString([]byte("fake-sig"))
	return header + "." + payloadEnc + "." + sig
}

func TestExtractSessionIDFromJWT_JTI(t *testing.T) {
	token := makeJWT(map[string]any{"jti": "session-abc", "sub": "user-1"})
	got := ExtractSessionIDFromJWT(token)
	if got != "session-abc" {
		t.Errorf("expected 'session-abc', got %q", got)
	}
}

func TestExtractSessionIDFromJWT_SID(t *testing.T) {
	token := makeJWT(map[string]any{"sid": "session-xyz"})
	got := ExtractSessionIDFromJWT(token)
	if got != "session-xyz" {
		t.Errorf("expected 'session-xyz', got %q", got)
	}
}

func TestExtractSessionIDFromJWT_JTIPriority(t *testing.T) {
	token := makeJWT(map[string]any{"jti": "jti-val", "sid": "sid-val"})
	got := ExtractSessionIDFromJWT(token)
	if got != "jti-val" {
		t.Errorf("expected 'jti-val' (jti takes priority), got %q", got)
	}
}

func TestExtractSessionIDFromJWT_NoClaims(t *testing.T) {
	token := makeJWT(map[string]any{"sub": "user-1"})
	got := ExtractSessionIDFromJWT(token)
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestExtractSessionIDFromJWT_InvalidToken(t *testing.T) {
	tests := []string{
		"",
		"not-a-jwt",
		"only.two",
		"too.many.parts.here",
	}
	for _, token := range tests {
		got := ExtractSessionIDFromJWT(token)
		if got != "" {
			t.Errorf("ExtractSessionIDFromJWT(%q) = %q, want empty", token, got)
		}
	}
}

func TestExtractScopesFromJWT_ScopeClaim(t *testing.T) {
	token := makeJWT(map[string]any{"scope": "read write admin"})
	got := ExtractScopesFromJWT(token)
	if len(got) != 3 {
		t.Fatalf("expected 3 scopes, got %d: %v", len(got), got)
	}
	if got[0] != "read" || got[1] != "write" || got[2] != "admin" {
		t.Errorf("unexpected scopes: %v", got)
	}
}

func TestExtractScopesFromJWT_ScpClaim(t *testing.T) {
	token := makeJWT(map[string]any{"scp": "profile email"})
	got := ExtractScopesFromJWT(token)
	if len(got) != 2 {
		t.Fatalf("expected 2 scopes, got %d: %v", len(got), got)
	}
	if got[0] != "profile" || got[1] != "email" {
		t.Errorf("unexpected scopes: %v", got)
	}
}

func TestExtractScopesFromJWT_Dedup(t *testing.T) {
	token := makeJWT(map[string]any{"scope": "read read write"})
	got := ExtractScopesFromJWT(token)
	if len(got) != 2 {
		t.Fatalf("expected 2 deduplicated scopes, got %d: %v", len(got), got)
	}
}

func TestExtractScopesFromJWT_NoScopes(t *testing.T) {
	token := makeJWT(map[string]any{"sub": "user-1"})
	got := ExtractScopesFromJWT(token)
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestExtractScopesFromJWT_InvalidToken(t *testing.T) {
	got := ExtractScopesFromJWT("not.a.jwt")
	if got != nil {
		t.Errorf("expected nil for invalid token, got %v", got)
	}
}

func TestTokenHash(t *testing.T) {
	h1 := TokenHash("my-token")
	h2 := TokenHash("my-token")
	h3 := TokenHash("other-token")

	if h1 != h2 {
		t.Errorf("same token should produce same hash: %q != %q", h1, h2)
	}
	if h1 == h3 {
		t.Errorf("different tokens should produce different hashes: %q == %q", h1, h3)
	}
	if len(h1) != 64 {
		t.Errorf("SHA256 hex should be 64 chars, got %d", len(h1))
	}
}

func TestIsApiKeyToken_ApiKeyType(t *testing.T) {
	token := makeJWT(map[string]any{"type": "api_key", "sub": "user-1"})
	if !IsApiKeyToken(token) {
		t.Error("expected true for api_key type")
	}
}

func TestIsApiKeyToken_UserType(t *testing.T) {
	token := makeJWT(map[string]any{"type": "user", "sub": "user-1"})
	if IsApiKeyToken(token) {
		t.Error("expected false for user type")
	}
}

func TestIsApiKeyToken_NoTypeClaim(t *testing.T) {
	token := makeJWT(map[string]any{"sub": "user-1"})
	if IsApiKeyToken(token) {
		t.Error("expected false when type claim is missing")
	}
}

func TestIsApiKeyToken_InvalidToken(t *testing.T) {
	tests := []string{"", "not-a-jwt", "only.two"}
	for _, token := range tests {
		if IsApiKeyToken(token) {
			t.Errorf("IsApiKeyToken(%q) should be false for invalid token", token)
		}
	}
}
