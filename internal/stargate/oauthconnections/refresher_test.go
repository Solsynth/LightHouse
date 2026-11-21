package oauthconnections

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/config"
	"src.solsynth.dev/sosys/stargate/model"
)

type fakeStore struct {
	called  bool
	access  string
	refresh string
	meta    map[string]any
}

func (f *fakeStore) UpdateConnectionTokensAndMeta(_ context.Context, _ uuid.UUID, accessToken, refreshToken string, meta map[string]any) error {
	f.called = true
	f.access = accessToken
	f.refresh = refreshToken
	f.meta = meta
	return nil
}

func bangumiConnection(expires time.Time, refreshToken string) *model.Connection {
	return &model.Connection{
		Id:           uuid.NewString(),
		Provider:     providerBangumi,
		AccessToken:  "old-access",
		RefreshToken: refreshToken,
		Meta: map[string]any{
			expiryMetaKey:        expires.UTC().Format(time.RFC3339),
			"preferred_username": "alice",
		},
	}
}

func TestEnsureFreshRefreshesExpiringBangumiToken(t *testing.T) {
	var form map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		form = map[string]string{
			"grant_type":    r.Form.Get("grant_type"),
			"client_id":     r.Form.Get("client_id"),
			"client_secret": r.Form.Get("client_secret"),
			"refresh_token": r.Form.Get("refresh_token"),
			"redirect_uri":  r.Form.Get("redirect_uri"),
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.SiteUrl = "https://id.example.com"
	cfg.Oidc.Bangumi.ClientId = "client-id"
	cfg.Oidc.Bangumi.ClientSecret = "client-secret"
	cfg.Oidc.Bangumi.TokenUrl = server.URL
	store := &fakeStore{}
	service := New(cfg, store, nil)

	conn := bangumiConnection(time.Now().Add(-time.Minute), "old-refresh")
	refreshed, err := service.EnsureFresh(context.Background(), conn)
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if !store.called {
		t.Fatal("store was not updated")
	}
	if refreshed.AccessToken != "new-access" || refreshed.RefreshToken != "new-refresh" {
		t.Fatalf("tokens = %+v", refreshed)
	}
	if store.meta["preferred_username"] != "alice" {
		t.Fatalf("meta lost profile fields: %+v", store.meta)
	}
	expiry, ok := tokenExpiry(store.meta)
	if !ok || !expiry.After(time.Now()) {
		t.Fatalf("expiry = %v (%v)", expiry, ok)
	}
	want := map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     "client-id",
		"client_secret": "client-secret",
		"refresh_token": "old-refresh",
		"redirect_uri":  "https://id.example.com/auth/callback/bangumi",
	}
	for key, value := range want {
		if form[key] != value {
			t.Fatalf("%s = %q, want %q", key, form[key], value)
		}
	}
}

func TestEnsureFreshSkipsHealthyConnection(t *testing.T) {
	store := &fakeStore{}
	service := New(config.Default(), store, nil)
	conn := bangumiConnection(time.Now().Add(time.Hour), "refresh")
	if _, err := service.EnsureFresh(context.Background(), conn); err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if store.called {
		t.Fatal("healthy token was refreshed")
	}
}

func TestEnsureFreshWithoutRefreshToken(t *testing.T) {
	store := &fakeStore{}
	service := New(config.Default(), store, nil)
	conn := bangumiConnection(time.Now().Add(-time.Minute), "")
	if _, err := service.EnsureFresh(context.Background(), conn); err == nil {
		t.Fatal("expected an error for a missing refresh token")
	}
	if store.called {
		t.Fatal("store changed without a refresh token")
	}
}
