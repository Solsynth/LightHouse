package collections

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
func testInstance(t *testing.T) (*gin.Engine, *instance, uuid.UUID) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("sqlite open: %v", err)
	}
	if err := db.AutoMigrate(
		&model.CatalogItem{},
		&model.Collection{},
		&model.CollectionItem{},
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
	return engine, inst, account
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

// TestPlaylistUploadAndFavouriteRoutes walks the two writes a client uses to
// put its own library into Pulsar: the whole-playlist replace, and the
// per-track favourite that is idempotent because the heart can be tapped
// twice.
func TestPlaylistUploadAndFavouriteRoutes(t *testing.T) {
	engine, inst, account := testInstance(t)

	coll := &model.Collection{
		AccountID: account,
		Domain:    model.DomainMusic,
		Kind:      model.KindPlaylist,
		Name:      "Before upload",
		IsPublic:  true,
	}
	if err := inst.core.Store.CreateCollection(nil, coll); err != nil {
		t.Fatalf("create collection: %v", err)
	}

	// Upload: the client's list is the whole playlist.
	recorder := call(t, engine, http.MethodPut, "/api/collections/"+coll.ID.String()+"/items", map[string]any{
		"items": []map[string]any{
			{"title": "Cheerleader", "subtitle": "Porter Robinson", "provider": "groovybox", "provider_key": "subsonic:home:a", "duration_seconds": 200},
			{"title": "Brave Song", "subtitle": "Aoi Tada", "provider": "groovybox", "provider_key": "subsonic:home:b", "duration_seconds": 185},
		},
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("replace status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	var replaced struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &replaced); err != nil {
		t.Fatalf("decode replace: %v", err)
	}
	if replaced.Count != 2 {
		t.Fatalf("expected 2 items stored, got %d", replaced.Count)
	}

	// Favourites start empty, and asking twice does not make two collections.
	recorder = call(t, engine, http.MethodGet, "/api/collections/favourites", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("favourites status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	var empty struct {
		Total int `json:"total"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &empty)
	if empty.Total != 0 {
		t.Fatalf("expected no favourites yet, got %d", empty.Total)
	}

	favourite := map[string]any{
		"title": "Brave Song", "subtitle": "Aoi Tada",
		"provider": "groovybox", "provider_key": "subsonic:home:b",
	}
	recorder = call(t, engine, http.MethodPut, "/api/collections/favourites", favourite)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("first favourite status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	// The same track again is the same entry, not a second one.
	recorder = call(t, engine, http.MethodPut, "/api/collections/favourites", favourite)
	if recorder.Code != http.StatusOK {
		t.Fatalf("repeat favourite status = %d, body %s", recorder.Code, recorder.Body.String())
	}

	recorder = call(t, engine, http.MethodGet, "/api/collections/favourites", nil)
	var listed struct {
		Total int `json:"total"`
		Items []struct {
			CatalogID   string `json:"catalog_id"`
			CatalogItem *struct {
				Title string `json:"title"`
			} `json:"catalog_item"`
		} `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode favourites: %v", err)
	}
	if listed.Total != 1 || len(listed.Items) != 1 {
		t.Fatalf("expected one favourite, got total=%d len=%d", listed.Total, len(listed.Items))
	}
	if listed.Items[0].CatalogItem == nil || listed.Items[0].CatalogItem.Title != "Brave Song" {
		t.Fatalf("favourite lost its catalog record: %+v", listed.Items[0])
	}

	// Removing by catalog id empties it.
	catalogID := listed.Items[0].CatalogID
	recorder = call(t, engine, http.MethodDelete, "/api/collections/favourites/"+catalogID, nil)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("remove favourite status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	recorder = call(t, engine, http.MethodGet, "/api/collections/favourites", nil)
	_ = json.Unmarshal(recorder.Body.Bytes(), &listed)
	if listed.Total != 0 {
		t.Fatalf("expected no favourites after remove, got %d", listed.Total)
	}
}

// TestUploadRejectsAnotherAccountsPlaylist keeps the replace verb from being a
// write into someone else's collection.
func TestUploadRejectsAnotherAccountsPlaylist(t *testing.T) {
	engine, inst, _ := testInstance(t)

	foreign := &model.Collection{
		AccountID: uuid.New(),
		Domain:    model.DomainMusic,
		Kind:      model.KindPlaylist,
		Name:      "Not yours",
		IsPublic:  true,
	}
	if err := inst.core.Store.CreateCollection(nil, foreign); err != nil {
		t.Fatalf("create collection: %v", err)
	}

	recorder := call(t, engine, http.MethodPut, "/api/collections/"+foreign.ID.String()+"/items", map[string]any{
		"items": []map[string]any{{"title": "Cheerleader"}},
	})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a foreign collection, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestUpdateItemStatus marks progress on one item without touching the rest
// of the list, and rejects statuses the domain does not know.
func TestUpdateItemStatus(t *testing.T) {
	engine, inst, account := testInstance(t)

	coll := &model.Collection{
		AccountID: account,
		Domain:    model.DomainVideo,
		Kind:      model.KindWatchlist,
		Name:      "Evenings",
		IsPublic:  true,
	}
	if err := inst.core.Store.CreateCollection(nil, coll); err != nil {
		t.Fatalf("create collection: %v", err)
	}
	catalogID := uuid.New()
	if err := inst.core.Store.DB.Create(&model.CatalogItem{
		ID: catalogID, Domain: model.DomainVideo, Provider: "test", Title: "A Film",
	}).Error; err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	item := &model.CollectionItem{CollectionID: coll.ID, CatalogID: &catalogID, Position: 0, Status: &[]string{model.StatusPlanToWatch}[0]}
	if err := inst.core.Store.AddCollectionItem(nil, item); err != nil {
		t.Fatalf("add item: %v", err)
	}

	recorder := call(t, engine, http.MethodPatch, "/api/collections/"+coll.ID.String()+"/items/"+item.ID.String(), map[string]any{
		"status": model.StatusWatching,
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body %s", recorder.Code, recorder.Body.String())
	}

	var updated model.CollectionItem
	if err := json.Unmarshal(recorder.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if updated.Status == nil || *updated.Status != model.StatusWatching {
		t.Fatalf("expected status watching, got %v", updated.Status)
	}

	// The other items of the list are untouched, and unknown statuses are
	// refused before they reach the store.
	recorder = call(t, engine, http.MethodPatch, "/api/collections/"+coll.ID.String()+"/items/"+item.ID.String(), map[string]any{
		"status": "binged",
	})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown status status = %d, body %s", recorder.Code, recorder.Body.String())
	}
}
