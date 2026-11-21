package bangumisync

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/bangumi"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/config"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/store"
	platformapp "src.solsynth.dev/sosys/lighthouse/shared/platform/app"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/jobs"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

func oauthTestInstance(t *testing.T) (*gin.Engine, *instance, uuid.UUID, *mockOAuth) {
	t.Helper()
	mock := newMockOAuth(t)

	dbPath := filepath.Join(t.TempDir(), "bangumi.db") + "?_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.CatalogItem{},
		&model.Collection{},
		&model.CollectionItem{},
		&model.ExternalSyncConnection{},
		&model.ExternalOAuthState{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	cfg := &config.Config{}
	cfg.Security.MaxTake = 100
	cfg.Bangumi.ClientID = "client-id"
	cfg.Bangumi.ClientSecret = "client-secret"
	cfg.Bangumi.RedirectURL = "https://pulsar.example/api/bangumi/oauth/callback"
	cfg.Bangumi.AuthorizeURL = "https://bgm.tv/oauth/authorize"
	cfg.Bangumi.TokenURL = mock.server.URL + "/oauth/access_token"
	cfg.Bangumi.APIBaseURL = mock.server.URL
	cfg.Bangumi.UserAgent = "PulsarTest/1.0"
	cfg.Bangumi.PostAuthRedirect = "https://solian.example/settings/pulsar"
	cfg.Bangumi.ConflictPolicy = "remote"
	cfg.Bangumi.SyncIntervalMinutes = -1

	scheduler := jobs.New(context.Background(), slog.Default())
	env := &platformapp.Env{DB: db, Jobs: scheduler, Log: slog.Default()}
	coreServices := &core.Services{Env: env, Config: cfg, Store: store.New(db)}

	engine := gin.New()
	account := uuid.New()
	engine.Use(func(c *gin.Context) {
		c.Set(authn.ContextKeyUser, &gen.DyAccount{Id: account.String()})
		c.Next()
	})
	host := &module.Host[core.Services]{Core: coreServices, Log: slog.Default(), Engine: engine}
	host.API = engine.Group("/api")
	inst := &instance{disableAutoSync: true}
	if err := inst.Configure(context.Background(), host); err != nil {
		t.Fatalf("configure: %v", err)
	}
	// The client is built by Configure; replace its interval so the test does
	// not spend half a second between mock requests.
	inst.client = bangumi.NewClient(bangumi.Config{
		APIBaseURL:   mock.server.URL,
		TokenURL:     mock.server.URL + "/oauth/access_token",
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		RedirectURL:  "https://pulsar.example/api/bangumi/oauth/callback",
		UserAgent:    "PulsarTest/1.0",
		Interval:     time.Millisecond,
		HTTPClient:   mock.server.Client(),
	})
	t.Cleanup(func() { _ = inst.Stop(context.Background()) })
	return engine, inst, account, mock
}

type mockOAuth struct {
	server *httptest.Server
}

func newMockOAuth(t *testing.T) *mockOAuth {
	t.Helper()
	mock := &mockOAuth{}
	mock.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/oauth/access_token":
			_ = r.ParseForm()
			if got := r.Form.Get("grant_type"); got != "authorization_code" {
				t.Errorf("grant_type = %q", got)
			}
			if got := r.Form.Get("code"); got != "good-code" {
				t.Errorf("code = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access-value",
				"refresh_token": "refresh-value",
				"expires_in":    3600,
				"token_type":    "Bearer",
				"user_id":       42,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v0/me":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":       42,
				"username": "alice",
				"nickname": "Alice",
				"avatar":   map[string]string{"large": "https://img.example/a.jpg"},
			})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/collections"):
			_ = json.NewEncoder(w).Encode(map[string]any{"total": 0, "limit": 100, "offset": 0, "data": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(mock.server.Close)
	return mock
}

func callAPI(t *testing.T, engine *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader = http.NoBody
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

func TestOAuthLinkCallbackAndUnlink(t *testing.T) {
	engine, inst, account, _ := oauthTestInstance(t)

	recorder := callAPI(t, engine, http.MethodPost, "/api/bangumi/oauth/start", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("start status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	var start oauthStartResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &start); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if !strings.Contains(start.AuthorizeURL, "state="+url.QueryEscape(start.State)) {
		t.Fatalf("authorize url = %q, state = %q", start.AuthorizeURL, start.State)
	}

	recorder = callAPI(t, engine, http.MethodGet, "/api/bangumi/oauth/callback?state="+url.QueryEscape(start.State)+"&code=good-code", nil)
	if recorder.Code != http.StatusFound {
		t.Fatalf("callback status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatalf("location: %v", err)
	}
	if location.Query().Get("bangumi") != "linked" {
		t.Fatalf("location = %s", location)
	}

	conn, err := inst.core.Store.GetSyncConnection(context.Background(), account, provider)
	if err != nil {
		t.Fatalf("connection: %v", err)
	}
	if conn.RemoteUsername == nil || *conn.RemoteUsername != "alice" {
		t.Fatalf("remote username = %v", conn.RemoteUsername)
	}
	if conn.AccessToken == nil || *conn.AccessToken != "access-value" {
		t.Fatalf("access token = %v", conn.AccessToken)
	}
	if conn.RefreshToken == nil || *conn.RefreshToken != "refresh-value" {
		t.Fatalf("refresh token = %v", conn.RefreshToken)
	}
	if conn.TokenExpiresAt == nil || !conn.TokenExpiresAt.After(time.Now()) {
		t.Fatalf("token expiry = %v", conn.TokenExpiresAt)
	}

	// The state is single use.
	recorder = callAPI(t, engine, http.MethodGet, "/api/bangumi/oauth/callback?state="+url.QueryEscape(start.State)+"&code=good-code", nil)
	if recorder.Code != http.StatusFound {
		t.Fatalf("replay status = %d", recorder.Code)
	}
	location, _ = url.Parse(recorder.Header().Get("Location"))
	if location.Query().Get("bangumi") != "error" {
		t.Fatalf("replay location = %s", location)
	}

	recorder = callAPI(t, engine, http.MethodGet, "/api/bangumi/connection", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("connection status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	var view connectionResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode connection: %v", err)
	}
	if !view.Linked || view.RemoteUsername != "alice" || view.Nickname != "Alice" {
		t.Fatalf("connection view = %+v", view)
	}

	recorder = callAPI(t, engine, http.MethodDelete, "/api/bangumi/connection", nil)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("unlink status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	recorder = callAPI(t, engine, http.MethodGet, "/api/bangumi/connection", nil)
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode unlinked: %v", err)
	}
	if view.Linked {
		t.Fatalf("still linked after unlink: %+v", view)
	}
}
