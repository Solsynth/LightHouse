package bangumisync

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/config"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/store"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

func connectionTestInstance(t *testing.T, link func(context.Context, uuid.UUID) (*stargateLink, error)) (*gin.Engine, *instance, uuid.UUID) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.CatalogItem{},
		&model.Collection{},
		&model.CollectionItem{},
		&model.ExternalSyncConnection{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	cfg := &config.Config{}
	cfg.Security.MaxTake = 100
	cfg.Bangumi.ConflictPolicy = "remote"
	coreServices := &core.Services{Config: cfg, Store: store.New(db)}
	account := uuid.New()
	inst := &instance{
		core:            coreServices,
		log:             slog.Default(),
		syncing:         map[uuid.UUID]bool{},
		disableAutoSync: true,
		linkOverride:    link,
	}

	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set(authn.ContextKeyUser, &gen.DyAccount{Id: account.String()})
		c.Next()
	})
	host := &module.Host[core.Services]{Core: coreServices, Log: slog.Default(), Engine: engine}
	host.API = engine.Group("/api")
	inst.registerRoutes(host.API)
	return engine, inst, account
}

func callRoute(t *testing.T, engine *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader = http.NoBody
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func TestConnectionReadsStargateLinkAndCreatesLocalPreferences(t *testing.T) {
	username := "alice"
	link := func(_ context.Context, accountID uuid.UUID) (*stargateLink, error) {
		return &stargateLink{
			remote: &model.ExternalSyncConnection{
				AccountID:      accountID,
				Provider:       provider,
				RemoteUserID:   "42",
				RemoteUsername: &username,
				Meta:           model.ToJSON(map[string]any{"nickname": "Alice", "avatar_url": "https://img.example/a.jpg"}),
			},
			accessToken: "access-value",
			username:    username,
		}, nil
	}
	engine, inst, account := connectionTestInstance(t, link)

	rec := callRoute(t, engine, http.MethodGet, "/api/bangumi/connection", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var view connectionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !view.Linked || view.RemoteUsername != "alice" || view.Nickname != "Alice" || !view.Configured {
		t.Fatalf("view = %+v", view)
	}
	if view.ConflictPolicy != "remote" {
		t.Fatalf("policy = %q", view.ConflictPolicy)
	}
	stored, err := inst.core.Store.GetSyncConnection(context.Background(), account, provider)
	if err != nil {
		t.Fatalf("local row: %v", err)
	}
	if !stored.SyncEnabled {
		t.Fatal("new local row should default to sync enabled")
	}

	enabled := false
	policy := "local"
	rec = callRoute(t, engine, http.MethodPatch, "/api/bangumi/connection", map[string]any{
		"sync_enabled":    enabled,
		"conflict_policy": policy,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if view.SyncEnabled || view.ConflictPolicy != "local" {
		t.Fatalf("patched view = %+v", view)
	}
}

func TestConnectionNotLinked(t *testing.T) {
	engine, _, _ := connectionTestInstance(t, func(context.Context, uuid.UUID) (*stargateLink, error) {
		return nil, errNotLinked
	})
	rec := callRoute(t, engine, http.MethodGet, "/api/bangumi/connection", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var view connectionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.Linked {
		t.Fatalf("view = %+v", view)
	}

	rec = callRoute(t, engine, http.MethodPost, "/api/bangumi/sync", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("sync status = %d, body %s", rec.Code, rec.Body.String())
	}
	rec = callRoute(t, engine, http.MethodPatch, "/api/bangumi/connection", map[string]any{"sync_enabled": false})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("patch status = %d, body %s", rec.Code, rec.Body.String())
	}
}
