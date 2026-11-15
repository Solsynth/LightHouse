package reviews

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/config"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/store"
	platformapp "src.solsynth.dev/sosys/lighthouse/shared/platform/app"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

// testInstance mounts the module's routes behind a user injected into the
// context, which is the one thing authn.RequireAuth looks for.
func testInstance(t *testing.T) (*gin.Engine, *instance, *gorm.DB, uuid.UUID) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("sqlite open: %v", err)
	}
	if err := db.AutoMigrate(
		&model.CatalogItem{},
		&model.Review{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	cfg := &config.Config{}
	cfg.Security.MaxTake = 100
	coreServices := &core.Services{
		Env:    &platformapp.Env{DB: db},
		Store:  store.New(db),
		Config: cfg,
	}
	inst := &instance{core: coreServices}

	engine := gin.New()
	account := uuid.New()
	engine.Use(func(c *gin.Context) {
		c.Set(authn.ContextKeyUser, &gen.DyAccount{Id: account.String()})
		c.Next()
	})
	host := &module.Host[core.Services]{Core: coreServices}
	api := engine.Group("/api")
	host.API = api
	host.Engine = engine

	if err := inst.Configure(nil, host); err != nil {
		t.Fatalf("configure: %v", err)
	}
	return engine, inst, db, account
}

func call(t *testing.T, engine *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
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

func seedCatalogItem(t *testing.T, db *gorm.DB) uuid.UUID {
	t.Helper()

	item := &model.CatalogItem{
		ID:       uuid.New(),
		Domain:   model.DomainMusic,
		Provider: "test",
		Title:    "Test Track",
	}
	if err := db.Create(item).Error; err != nil {
		t.Fatalf("seed catalog item: %v", err)
	}
	return item.ID
}

// TestReviewUpsertListStats walks the writes a client uses for reviews: the
// upsert that is created then updated in place, the public list for the item,
// and the aggregate rating.
func TestReviewUpsertListStats(t *testing.T) {
	engine, _, db, account := testInstance(t)
	catalogID := seedCatalogItem(t, db)

	// First send creates; the same review re-sent updates, it never appends.
	first := call(t, engine, http.MethodPut, "/api/reviews", map[string]any{
		"catalog_id": catalogID,
		"rating":     8,
		"body":       "A quiet favourite.",
	})
	if first.Code != http.StatusCreated {
		t.Fatalf("first review status = %d, body %s", first.Code, first.Body.String())
	}
	repeat := call(t, engine, http.MethodPut, "/api/reviews", map[string]any{
		"catalog_id": catalogID,
		"rating":     9,
	})
	if repeat.Code != http.StatusOK {
		t.Fatalf("repeat review status = %d, body %s", repeat.Code, repeat.Body.String())
	}

	var updated struct {
		ID        uuid.UUID `json:"id"`
		Rating    *int      `json:"rating"`
		Body      *string   `json:"body"`
		AccountID uuid.UUID `json:"account_id"`
	}
	if err := json.Unmarshal(repeat.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode repeat: %v", err)
	}
	if updated.Rating == nil || *updated.Rating != 9 {
		t.Fatalf("expected rating 9 after update, got %v", updated.Rating)
	}
	if updated.AccountID != account {
		t.Fatalf("review owned by %s, expected %s", updated.AccountID, account)
	}

	// The list endpoint is public and shows one review for the item; the total
	// rides in the X-Total header, the body is the bare array.
	listed := call(t, engine, http.MethodGet, "/api/reviews?catalog_id="+catalogID.String(), nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list status = %d, body %s", listed.Code, listed.Body.String())
	}
	var items []struct {
		Rating *int `json:"rating"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if listed.Header().Get("X-Total") != "1" || len(items) != 1 {
		t.Fatalf("expected one review, got total=%q len=%d", listed.Header().Get("X-Total"), len(items))
	}
	if items[0].Rating == nil || *items[0].Rating != 9 {
		t.Fatalf("expected listed rating 9, got %v", items[0].Rating)
	}

	// The aggregate is over every rating of the item.
	stats := call(t, engine, http.MethodGet, "/api/reviews/stats?catalog_id="+catalogID.String(), nil)
	if stats.Code != http.StatusOK {
		t.Fatalf("stats status = %d, body %s", stats.Code, stats.Body.String())
	}
	var summary struct {
		Average float64 `json:"average"`
		Count   int64   `json:"count"`
	}
	if err := json.Unmarshal(stats.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if summary.Count != 1 || summary.Average != 9 {
		t.Fatalf("expected one rating averaging 9, got %+v", summary)
	}

	// Deleting the review empties the item's review list.
	deleted := call(t, engine, http.MethodDelete, "/api/reviews/"+updated.ID.String(), nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body %s", deleted.Code, deleted.Body.String())
	}
	after := call(t, engine, http.MethodGet, "/api/reviews?catalog_id="+catalogID.String(), nil)
	if err := json.Unmarshal(after.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode list after delete: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no reviews after delete, got %d", len(items))
	}
}

// TestReviewValidation keeps empty reviews and out-of-scale ratings from being
// stored, and reviews of missing catalog items from dangling.
func TestReviewValidation(t *testing.T) {
	engine, _, db, _ := testInstance(t)

	if rec := call(t, engine, http.MethodPut, "/api/reviews", map[string]any{
		"catalog_id": seedCatalogItem(t, db),
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty review status = %d, body %s", rec.Code, rec.Body.String())
	}

	if rec := call(t, engine, http.MethodPut, "/api/reviews", map[string]any{
		"catalog_id": seedCatalogItem(t, db),
		"rating":     11,
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("rating 11 status = %d, body %s", rec.Code, rec.Body.String())
	}

	if rec := call(t, engine, http.MethodPut, "/api/reviews", map[string]any{
		"catalog_id": uuid.New(),
		"rating":     5,
	}); rec.Code != http.StatusNotFound {
		t.Fatalf("missing catalog item status = %d, body %s", rec.Code, rec.Body.String())
	}
}
