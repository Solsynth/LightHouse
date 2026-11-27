package lyrics

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

// harness mounts the module behind a fixed account, which is the one thing
// authn.RequireAuth reads. switching the account is how a test plays a second
// listener.
type harness struct {
	engine   *gin.Engine
	db       *gorm.DB
	account  uuid.UUID
	catalog  uuid.UUID
	services *core.Services
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("sqlite open: %v", err)
	}
	if err := db.AutoMigrate(append([]any{&model.CatalogItem{}}, model.LyricsContributionModels()...)...); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	cfg := &config.Config{}
	cfg.Security.MaxTake = 100
	h := &harness{
		db:       db,
		account:  uuid.New(),
		services: &core.Services{Env: &platformapp.Env{DB: db}, Store: store.New(db), Config: cfg},
	}
	item := &model.CatalogItem{
		ID:          uuid.New(),
		Domain:      model.DomainMusic,
		Provider:    "groovybox",
		ProviderKey: "lib:1",
		Title:       "Test Track",
		Subtitle:    new("Wrong Artist"),
	}
	if err := db.Create(item).Error; err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	h.catalog = item.ID

	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set(authn.ContextKeyUser, &gen.DyAccount{Id: h.account.String()})
		c.Next()
	})
	host := &module.Host[core.Services]{Core: h.services, Log: nil}
	host.API = engine.Group("/api")
	host.Engine = engine
	inst := &instance{}
	if err := inst.Configure(nil, host); err != nil {
		t.Fatalf("configure: %v", err)
	}
	h.engine = engine
	return h
}

func (h *harness) call(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
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
	h.engine.ServeHTTP(recorder, request)
	return recorder
}

func decode[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return value
}

const sampleLyrics = `[{"at":1000,"t":"first line"}]`

// TestLyricContributionRanksByAdoption covers the path a listener takes: two
// accounts contribute words for one recording, a third adopts one of them, and
// the adopted one is what the next reader is offered first.
func TestLyricContributionRanksByAdoption(t *testing.T) {
	h := newHarness(t)

	first := h.call(t, http.MethodPut, "/api/lyrics", map[string]any{
		"catalog_id": h.catalog,
		"language":   "en",
		"lines":      sampleLyrics,
		"synced":     true,
		"origin":     "lrclib",
		"remote_id":  42,
	})
	if first.Code != http.StatusCreated {
		t.Fatalf("first contribution = %d: %s", first.Code, first.Body.String())
	}
	contributionA := decode[model.SharedLyric](t, first)

	h.account = uuid.New()
	second := h.call(t, http.MethodPut, "/api/lyrics", map[string]any{
		"catalog_id": h.catalog,
		"language":   "en",
		"lines":      sampleLyrics,
		"synced":     true,
		"origin":     "manual",
	})
	if second.Code != http.StatusCreated {
		t.Fatalf("second contribution = %d: %s", second.Code, second.Body.String())
	}
	contributionB := decode[model.SharedLyric](t, second)

	// A third listener reads the list and keeps A's words.
	h.account = uuid.New()
	list := h.call(t, http.MethodGet, "/api/lyrics/suggestions?catalog_id="+h.catalog.String()+"&language=en", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", list.Code, list.Body.String())
	}
	var suggestions []lyricContribution
	if err := json.Unmarshal(list.Body.Bytes(), &suggestions); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(suggestions) != 2 {
		t.Fatalf("suggestions = %d, want 2", len(suggestions))
	}
	if suggestions[0].ID != contributionB.ID && suggestions[0].ID != contributionA.ID {
		t.Fatalf("unexpected first suggestion %+v", suggestions[0])
	}
	if suggestions[0].Mine {
		t.Fatalf("a listing must not claim a stranger's row is the caller's")
	}

	adopt := h.call(t, http.MethodPost, "/api/lyrics/"+contributionA.ID.String()+"/adopt", nil)
	if adopt.Code != http.StatusOK {
		t.Fatalf("adopt = %d: %s", adopt.Code, adopt.Body.String())
	}
	adopted := decode[model.SharedLyric](t, adopt)
	if adopted.AdoptionCount != 1 || adopted.Score != 1 {
		t.Fatalf("adopted counters = %+v", adopted)
	}

	// Adopting twice is one listener, not two.
	again := h.call(t, http.MethodPost, "/api/lyrics/"+contributionA.ID.String()+"/adopt", nil)
	if again.Code != http.StatusOK {
		t.Fatalf("repeat adopt = %d", again.Code)
	}
	if repeat := decode[model.SharedLyric](t, again); repeat.AdoptionCount != 1 {
		t.Fatalf("repeat adoption counted twice: %+v", repeat)
	}

	list = h.call(t, http.MethodGet, "/api/lyrics/suggestions?catalog_id="+h.catalog.String(), nil)
	suggestions = nil
	if err := json.Unmarshal(list.Body.Bytes(), &suggestions); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(suggestions) == 0 || suggestions[0].ID != contributionA.ID {
		t.Fatalf("adopted lyrics did not rank first: %+v", suggestions)
	}
}

// TestLyricVoteRejectsSelfPlay: the author cannot vote on its own row, and a
// thumb-down of somebody else's moves the standing down.
func TestLyricVoteRejectsOwn(t *testing.T) {
	h := newHarness(t)
	created := h.call(t, http.MethodPut, "/api/lyrics", map[string]any{
		"catalog_id": h.catalog,
		"lines":      sampleLyrics,
	})
	contribution := decode[model.SharedLyric](t, created)

	self := h.call(t, http.MethodPost, "/api/lyrics/"+contribution.ID.String()+"/vote", map[string]any{"value": 1})
	if self.Code != http.StatusForbidden {
		t.Fatalf("self vote = %d, want 403: %s", self.Code, self.Body.String())
	}

	h.account = uuid.New()
	down := h.call(t, http.MethodPost, "/api/lyrics/"+contribution.ID.String()+"/vote", map[string]any{"value": -1})
	if down.Code != http.StatusOK {
		t.Fatalf("downvote = %d: %s", down.Code, down.Body.String())
	}
	voted := decode[model.SharedLyric](t, down)
	if voted.Downvotes != 1 || voted.Score != -1 {
		t.Fatalf("downvote counters = %+v", voted)
	}
}

// TestMetadataSuggestionAgreement: the same correction from two accounts is a
// leader at two supports; a single account's proposal is not.
func TestMetadataSuggestionAgreement(t *testing.T) {
	h := newHarness(t)

	proposal := map[string]any{
		"catalog_id": h.catalog,
		"field":      "subtitle",
		"value":      "Right Artist",
	}
	if code := h.call(t, http.MethodPut, "/api/metadata/suggestions", proposal).Code; code != http.StatusCreated {
		t.Fatalf("first proposal = %d", code)
	}

	leaders := decode[[]store.MetadataFieldLeader](
		t, h.call(t, http.MethodGet, "/api/metadata/suggestions/leaders?catalog_id="+h.catalog.String(), nil))
	if len(leaders) != 0 {
		t.Fatalf("one proposal is not a consensus: %+v", leaders)
	}

	h.account = uuid.New()
	if code := h.call(t, http.MethodPut, "/api/metadata/suggestions", proposal).Code; code != http.StatusCreated {
		t.Fatalf("second proposal = %d", code)
	}

	leaders = decode[[]store.MetadataFieldLeader](
		t, h.call(t, http.MethodGet, "/api/metadata/suggestions/leaders?catalog_id="+h.catalog.String(), nil))
	if len(leaders) != 1 {
		t.Fatalf("leaders = %+v", leaders)
	}
	if leaders[0].Field != "subtitle" || leaders[0].Value != "Right Artist" || leaders[0].Support != 2 {
		t.Fatalf("leader = %+v", leaders[0])
	}

	// Proposing the value the row already has is not a suggestion.
	h.account = uuid.New()
	unchanged := h.call(t, http.MethodPut, "/api/metadata/suggestions", map[string]any{
		"catalog_id": h.catalog,
		"field":      "title",
		"value":      "Test Track",
	})
	if unchanged.Code != http.StatusBadRequest {
		t.Fatalf("no-op proposal = %d, want 400", unchanged.Code)
	}
}

// TestMetadataFieldRejected: an unknown field is a client bug, not a new
// column the server should store.
func TestMetadataFieldRejected(t *testing.T) {
	h := newHarness(t)
	response := h.call(t, http.MethodPut, "/api/metadata/suggestions", map[string]any{
		"catalog_id": h.catalog,
		"field":      "duration_seconds",
		"value":      "1",
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d, want 400: %s", response.Code, response.Body.String())
	}
}

// TestUnknownCatalogItem: a contribution filed under an id nobody can resolve
// is refused, so the table cannot fill with orphan opinions.
func TestUnknownCatalogItem(t *testing.T) {
	h := newHarness(t)
	response := h.call(t, http.MethodPut, "/api/lyrics", map[string]any{
		"catalog_id": uuid.New(),
		"lines":      sampleLyrics,
	})
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown catalog = %d, want 404", response.Code)
	}
}

// TestCatalogResolvesTrackKey: a client holding only a track key gets the
// catalog id its suggestions hang off, and a key nobody wrote is a 404 rather
// than an invented row.
func TestCatalogResolvesTrackKey(t *testing.T) {
	h := newHarness(t)

	found := h.call(t, http.MethodGet, "/api/lyrics/catalog?provider_key=lib:1", nil)
	if found.Code != http.StatusOK {
		t.Fatalf("resolve = %d: %s", found.Code, found.Body.String())
	}
	item := decode[model.CatalogItem](t, found)
	if item.ID != h.catalog {
		t.Fatalf("resolved id = %s, want %s", item.ID, h.catalog)
	}

	missing := h.call(t, http.MethodGet, "/api/lyrics/catalog?provider_key=lib:2", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown key = %d, want 404", missing.Code)
	}
}
