package oidcctl

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/stargate/internal/config"
)

// TestDiscoveryDocumentAdvertisesStargateEndpoints pins the OIDC discovery
// contract: the provider endpoints must use the /stargate service prefix the
// deployed gateway routes (Padlock is fully replaced; the legacy /padlock
// prefix 404s, which broke token/device-code fetches).
func TestDiscoveryDocumentAdvertisesStargateEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Default()
	cfg.BaseUrl = "https://api.solian.app"
	cfg.SiteUrl = "https://island.solian.app"
	s := &service{cfg: cfg, issuer: "https://nt.solian.app"}

	e := gin.New()
	e.GET("/.well-known/openid-configuration", s.handleConfiguration)

	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/.well-known/openid-configuration", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	endpoints := map[string]string{
		"token_endpoint":                "https://api.solian.app/stargate/auth/open/token",
		"userinfo_endpoint":             "https://api.solian.app/stargate/auth/open/userinfo",
		"device_authorization_endpoint": "https://api.solian.app/stargate/auth/open/device/code",
	}
	for key, want := range endpoints {
		got, _ := doc[key].(string)
		if got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
		if strings.Contains(got, "/padlock/") {
			t.Errorf("%s still advertises the legacy /padlock prefix: %q", key, got)
		}
	}
	if got, _ := doc["jwks_uri"].(string); got != "https://api.solian.app/.well-known/jwks" {
		t.Errorf("jwks_uri = %q", got)
	}
	if got, _ := doc["issuer"].(string); got != "https://nt.solian.app" {
		t.Errorf("issuer = %q", got)
	}
}

func TestScopesFromClaimsSplitsOAuthScopeString(t *testing.T) {
	got := scopesFromClaims(map[string]any{"scope": "openid profile email"})
	for _, scope := range []string{"openid", "profile", "email"} {
		if _, ok := got[scope]; !ok {
			t.Errorf("scope %q missing from %#v", scope, got)
		}
	}
	if len(got) != 3 {
		t.Fatalf("scope count = %d, want 3", len(got))
	}
}

func TestScopesFromClaimsKeepsLegacyArrayCompatibility(t *testing.T) {
	got := scopesFromClaims(map[string]any{"scope": []any{"openid", "profile"}})
	for _, scope := range []string{"openid", "profile"} {
		if _, ok := got[scope]; !ok {
			t.Errorf("legacy scope %q missing from %#v", scope, got)
		}
	}
	if len(got) != 2 {
		t.Fatalf("legacy scope count = %d, want 2", len(got))
	}
}
