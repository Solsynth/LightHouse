package syncyomi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protowire"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/config"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/modules/synckeys"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/store"
	platformapp "src.solsynth.dev/sosys/lighthouse/shared/platform/app"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
)

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.SyncAPIKey{},
		&model.SyncYomiBlob{},
		&model.CatalogItem{},
		&model.Creator{},
		&model.CatalogCreator{},
		&model.Scrobble{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func testEngine(t *testing.T) (*gin.Engine, *store.Store, *gorm.DB) {
	t.Helper()
	db := testDB(t)
	st := store.New(db)
	services := &core.Services{
		Env:    &platformapp.Env{DB: db},
		Config: &config.Config{},
		Store:  st,
	}
	inst := &instance{
		core: services,
		log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	engine := httpx.New(httpx.Options{}).Engine
	group := engine.Group("/api/sync", synckeys.RequireAPIKey(st, "syncyomi"))
	group.GET("/content", inst.getContent)
	group.PUT("/content", inst.putContent)
	group.GET("/status", inst.getStatus)
	return engine, st, db
}

func buildTachibkPayload(t *testing.T, gzipped bool) []byte {
	t.Helper()

	var ch1 []byte
	ch1 = protowire.AppendTag(ch1, 1, protowire.BytesType)
	ch1 = protowire.AppendString(ch1, "/manga/1/ch1")
	ch1 = protowire.AppendTag(ch1, 2, protowire.BytesType)
	ch1 = protowire.AppendString(ch1, "Chapter 1")
	ch1 = protowire.AppendTag(ch1, 4, protowire.VarintType)
	ch1 = protowire.AppendVarint(ch1, 1) // read = true
	ch1 = protowire.AppendTag(ch1, 6, protowire.VarintType)
	ch1 = protowire.AppendVarint(ch1, 18) // lastPageRead
	ch1 = protowire.AppendTag(ch1, 11, protowire.VarintType)
	ch1 = protowire.AppendVarint(ch1, 45000) // readDuration ms

	var ch2 []byte
	ch2 = protowire.AppendTag(ch2, 1, protowire.BytesType)
	ch2 = protowire.AppendString(ch2, "/manga/1/ch2")
	ch2 = protowire.AppendTag(ch2, 2, protowire.BytesType)
	ch2 = protowire.AppendString(ch2, "Chapter 2")
	ch2 = protowire.AppendTag(ch2, 4, protowire.VarintType)
	ch2 = protowire.AppendVarint(ch2, 0) // unread

	var hist []byte
	hist = protowire.AppendTag(hist, 1, protowire.BytesType)
	hist = protowire.AppendString(hist, "/manga/1/ch1")
	hist = protowire.AppendTag(hist, 2, protowire.VarintType)
	hist = protowire.AppendVarint(hist, uint64(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).UnixMilli()))

	var manga []byte
	manga = protowire.AppendTag(manga, 1, protowire.VarintType)
	manga = protowire.AppendVarint(manga, 123456) // source
	manga = protowire.AppendTag(manga, 2, protowire.BytesType)
	manga = protowire.AppendString(manga, "/manga/1")
	manga = protowire.AppendTag(manga, 3, protowire.BytesType)
	manga = protowire.AppendString(manga, "Solo Leveling")
	manga = protowire.AppendTag(manga, 5, protowire.BytesType)
	manga = protowire.AppendString(manga, "Chugong")
	manga = protowire.AppendTag(manga, 16, protowire.BytesType)
	manga = protowire.AppendBytes(manga, ch1)
	manga = protowire.AppendTag(manga, 16, protowire.BytesType)
	manga = protowire.AppendBytes(manga, ch2)
	manga = protowire.AppendTag(manga, 19, protowire.BytesType)
	manga = protowire.AppendBytes(manga, hist)

	var backup []byte
	backup = protowire.AppendTag(backup, 1, protowire.BytesType)
	backup = protowire.AppendBytes(backup, manga)

	if !gzipped {
		return backup
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(backup); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestSyncKeyAuthAndScopes(t *testing.T) {
	engine, st, _ := testEngine(t)
	accountID := uuid.New()
	ctx := context.Background()

	// Unauthenticated request fails.
	req := httptest.NewRequest(http.MethodGet, "/api/sync/status", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without key, got %d", rec.Code)
	}

	// Wrong scope is rejected with 403.
	_, otherScopeKey, err := st.CreateSyncAPIKey(ctx, accountID, "Komga only", []string{"komga"})
	if err != nil {
		t.Fatalf("create scoped key: %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/sync/status", nil)
	req.Header.Set("X-API-Token", otherScopeKey)
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for wrong scope, got %d", rec.Code)
	}

	// Valid key with syncyomi scope works via X-API-Token, Bearer, and ?apikey=.
	keyRow, rawKey, err := st.CreateSyncAPIKey(ctx, accountID, "TachiyomiSY", []string{"syncyomi"})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}

	for _, tc := range []struct {
		name  string
		path  string
		setup func(*http.Request)
	}{
		{"X-API-Token", "/api/sync/status", func(r *http.Request) { r.Header.Set("X-API-Token", rawKey) }},
		{"Bearer", "/api/sync/status", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+rawKey) }},
		{"QueryParam", "/api/sync/status?apikey=" + rawKey, func(r *http.Request) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			tc.setup(r)
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("%s: expected 200, got %d body=%s", tc.name, w.Code, w.Body.String())
			}
		})
	}

	// Revoked key is rejected.
	if err := st.RevokeSyncAPIKey(ctx, accountID, keyRow.ID); err != nil {
		t.Fatalf("revoke key: %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/sync/status", nil)
	req.Header.Set("X-API-Token", rawKey)
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 after revocation, got %d", rec.Code)
	}
}

func TestSyncYomiContentRoundTripAndScrobbleProjection(t *testing.T) {
	engine, st, db := testEngine(t)
	accountID := uuid.New()
	_, rawKey, err := st.CreateSyncAPIKey(context.Background(), accountID, "Komikku", nil)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}

	// Initial GET returns 404.
	req := httptest.NewRequest(http.MethodGet, "/api/sync/content", nil)
	req.Header.Set("X-API-Token", rawKey)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 initially, got %d", rec.Code)
	}

	// PUT gzipped .tachibk payload.
	payload := buildTachibkPayload(t, true)
	req = httptest.NewRequest(http.MethodPut, "/api/sync/content", bytes.NewReader(payload))
	req.Header.Set("X-API-Token", rawKey)
	req.Header.Set("Content-Type", "application/octet-stream")
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on PUT, got %d body=%s", rec.Code, rec.Body.String())
	}
	etag1 := rec.Header().Get("ETag")
	if etag1 == "" {
		t.Fatalf("expected ETag header on PUT")
	}

	// GET with If-None-Match returns 304.
	req = httptest.NewRequest(http.MethodGet, "/api/sync/content", nil)
	req.Header.Set("X-API-Token", rawKey)
	req.Header.Set("If-None-Match", etag1)
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("expected 304 on matching If-None-Match, got %d", rec.Code)
	}

	// GET without If-None-Match returns exact bytes.
	req = httptest.NewRequest(http.MethodGet, "/api/sync/content", nil)
	req.Header.Set("X-API-Token", rawKey)
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), payload) {
		t.Fatalf("expected exact payload back, code=%d len=%d", rec.Code, rec.Body.Len())
	}

	// PUT with stale If-Match returns 412.
	req = httptest.NewRequest(http.MethodPut, "/api/sync/content", bytes.NewReader(payload))
	req.Header.Set("X-API-Token", rawKey)
	req.Header.Set("If-Match", `"uuid=stale"`)
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("expected 412 on stale If-Match, got %d", rec.Code)
	}

	// PUT with matching If-Match succeeds and does not duplicate scrobbles.
	req = httptest.NewRequest(http.MethodPut, "/api/sync/content", bytes.NewReader(payload))
	req.Header.Set("X-API-Token", rawKey)
	req.Header.Set("If-Match", etag1)
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on matching If-Match, got %d", rec.Code)
	}

	// Verify 1 book scrobble + 1 catalog item were projected (unread ch2 skipped, re-PUT deduplicated).
	var scrobbles []model.Scrobble
	if err := db.Where("account_id = ?", accountID).Find(&scrobbles).Error; err != nil {
		t.Fatalf("query scrobbles: %v", err)
	}
	if len(scrobbles) != 1 {
		t.Fatalf("expected 1 scrobble, got %d", len(scrobbles))
	}
	if scrobbles[0].Domain != model.DomainBook || scrobbles[0].Title != "Solo Leveling" {
		t.Fatalf("unexpected scrobble: %+v", scrobbles[0])
	}
	if scrobbles[0].Caption == nil || *scrobbles[0].Caption != "Chapter 1" {
		t.Fatalf("expected caption 'Chapter 1', got %v", scrobbles[0].Caption)
	}
	if scrobbles[0].ListenedSeconds != 45 {
		t.Fatalf("expected 45s duration, got %d", scrobbles[0].ListenedSeconds)
	}

	// Verify /status reports has_content = true.
	req = httptest.NewRequest(http.MethodGet, "/api/sync/status", nil)
	req.Header.Set("X-API-Token", rawKey)
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	var statusResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &statusResp); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if statusResp["has_content"] != true {
		t.Fatalf("expected has_content=true, got %v", statusResp)
	}
}
