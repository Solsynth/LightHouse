package catalog

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
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
func testInstance(t *testing.T) (*gin.Engine, *instance, *gorm.DB) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("sqlite open: %v", err)
	}
	if err := db.AutoMigrate(
		&model.CatalogItem{},
		&model.Creator{},
		&model.CatalogCreator{},
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
	engine.Use(func(c *gin.Context) {
		c.Set(authn.ContextKeyUser, &gen.DyAccount{Id: "00000000-0000-0000-0000-000000000001"})
		c.Next()
	})
	host := &module.Host[core.Services]{Core: coreServices}
	api := engine.Group("/api")
	host.API = api
	host.Engine = engine

	if err := inst.Configure(nil, host); err != nil {
		t.Fatalf("configure: %v", err)
	}
	return engine, inst, db
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

// TestCreatorBrowse walks the organization a client gets from creators:
// creating an item with credits, listing the creators, and pulling every
// item by a creator id.
func TestCreatorBrowse(t *testing.T) {
	engine, _, _ := testInstance(t)

	recorder := call(t, engine, http.MethodPost, "/api/catalog", map[string]any{
		"domain": model.DomainMusic,
		"title":  "Shelter",
		"creators": []map[string]any{
			{"name": "Porter Robinson", "role": model.RoleArtist},
			{"name": "Porter Robinson", "role": model.RoleArtist},
		},
	})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body %s", recorder.Code, recorder.Body.String())
	}

	recorder = call(t, engine, http.MethodPost, "/api/catalog", map[string]any{
		"domain": model.DomainMusic,
		"title":  "Language",
		"creators": []map[string]any{
			{"name": "Porter Robinson", "role": model.RoleArtist},
		},
	})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create 2 status = %d, body %s", recorder.Code, recorder.Body.String())
	}

	// The same name and role dedupes to one creator with two items.
	recorder = call(t, engine, http.MethodGet, "/api/creators?q=porter", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list creators status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	var creators []struct {
		Name      string `json:"name"`
		Domain    string `json:"domain"`
		ItemCount int64  `json:"item_count"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &creators); err != nil {
		t.Fatalf("decode creators: %v", err)
	}
	if len(creators) != 1 || creators[0].Name != "Porter Robinson" || creators[0].ItemCount != 2 {
		t.Fatalf("expected one Porter Robinson with 2 items, got %+v", creators)
	}

	// Everything by that creator, through the catalog filter.
	recorder = call(t, engine, http.MethodGet, "/api/catalog?creator_id="+creatorID(t, engine), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("catalog filter status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("X-Total") != "2" {
		t.Fatalf("expected 2 items by creator, got total %q", recorder.Header().Get("X-Total"))
	}

	// An unknown creator is a 404, not an empty list.
	recorder = call(t, engine, http.MethodGet, "/api/creators/00000000-0000-0000-0000-0000000000ff", nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing creator status = %d, body %s", recorder.Code, recorder.Body.String())
	}
}

// creatorID picks the creator's id out of the list response.
func creatorID(t *testing.T, engine *gin.Engine) string {
	t.Helper()

	recorder := call(t, engine, http.MethodGet, "/api/creators", nil)
	var creators []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &creators); err != nil || len(creators) == 0 {
		t.Fatalf("decode creator id: %v (%s)", err, recorder.Body.String())
	}
	return creators[0].ID
}
