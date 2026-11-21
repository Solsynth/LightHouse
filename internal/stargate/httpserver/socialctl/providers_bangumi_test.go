package socialctl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"src.solsynth.dev/sosys/stargate/config"
)

func bangumiTestConfig(serverURL string) *config.Config {
	cfg := config.Default()
	cfg.SiteUrl = "https://id.example.com"
	cfg.Oidc.Bangumi.ClientId = "client-id"
	cfg.Oidc.Bangumi.ClientSecret = "client-secret"
	cfg.Oidc.Bangumi.AuthorizeUrl = serverURL + "/oauth/authorize"
	cfg.Oidc.Bangumi.TokenUrl = serverURL + "/oauth/access_token"
	cfg.Oidc.Bangumi.ApiBaseUrl = serverURL
	cfg.Oidc.Bangumi.UserAgent = "StargateTest/1.0"
	return cfg
}

func TestBangumiAuthorizationURL(t *testing.T) {
	provider, err := newProvider("bangumi", testDeps(bangumiTestConfig("https://bgm.example")))
	if err != nil {
		t.Fatalf("newProvider: %v", err)
	}
	if !providerAvailable("bangumi", testDeps(bangumiTestConfig("https://bgm.example"))) {
		t.Fatal("configured Bangumi provider is unavailable")
	}
	raw, err := provider.authorizationURL(context.Background(), "state-1", "")
	if err != nil {
		t.Fatalf("authorizationURL: %v", err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Path != "/oauth/authorize" {
		t.Fatalf("path = %q", parsed.Path)
	}
	query := parsed.Query()
	if query.Get("client_id") != "client-id" {
		t.Fatalf("client_id = %q", query.Get("client_id"))
	}
	if query.Get("response_type") != "code" || query.Get("state") != "state-1" {
		t.Fatalf("query = %v", query)
	}
	if query.Get("redirect_uri") != "https://id.example.com/auth/callback/bangumi" {
		t.Fatalf("redirect_uri = %q", query.Get("redirect_uri"))
	}
}

func TestBangumiProcessCallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/access_token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
			}
			if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "code-1" {
				t.Errorf("form = %v", r.Form)
			}
			if r.Form.Get("redirect_uri") != "https://id.example.com/auth/callback/bangumi" {
				t.Errorf("redirect_uri = %q", r.Form.Get("redirect_uri"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access-value",
				"refresh_token": "refresh-value",
				"expires_in":    3600,
				"token_type":    "Bearer",
			})
		case "/v0/me":
			if r.Header.Get("Authorization") != "Bearer access-value" {
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":       42,
				"username": "alice",
				"nickname": "Alice",
				"avatar":   map[string]string{"large": "https://img.example/alice.jpg"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := bangumiTestConfig(server.URL)
	provider, err := newProvider("bangumi", testDeps(cfg))
	if err != nil {
		t.Fatalf("newProvider: %v", err)
	}
	user, err := provider.processCallback(context.Background(), &callbackData{Code: "code-1"})
	if err != nil {
		t.Fatalf("processCallback: %v", err)
	}
	if user.UserId != "42" || user.PreferredUsername != "alice" || user.DisplayName != "Alice" {
		t.Fatalf("user = %+v", user)
	}
	if user.ProfilePictureUrl != "https://img.example/alice.jpg" {
		t.Fatalf("picture = %q", user.ProfilePictureUrl)
	}
	if user.AccessToken != "access-value" || user.RefreshToken != "refresh-value" {
		t.Fatalf("tokens = %+v", user)
	}
	if user.TokenExpiresAt.Before(time.Now().Add(50 * time.Minute)) {
		t.Fatalf("expiry = %v", user.TokenExpiresAt)
	}
	meta := user.toMetadata()
	expiry, ok := meta["token_expires_at"].(string)
	if !ok || !strings.Contains(expiry, "T") {
		t.Fatalf("token_expires_at = %v", meta["token_expires_at"])
	}
	if meta["preferred_username"] != "alice" {
		t.Fatalf("meta = %v", meta)
	}
}
