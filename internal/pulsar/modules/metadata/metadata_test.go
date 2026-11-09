package metadata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/config"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	pulsarmeta "src.solsynth.dev/sosys/lighthouse/internal/pulsar/metadata"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/store"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/app"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

type dummyProvider struct{}

func (d *dummyProvider) Name() string   { return "testprov" }
func (d *dummyProvider) Domain() string { return model.DomainMusic }
func (d *dummyProvider) FetchByProviderKey(_ context.Context, key string) (*pulsarmeta.UnifiedMedia, error) {
	return &pulsarmeta.UnifiedMedia{
		URN:         "pulsar:music:testprov:" + key,
		Domain:      model.DomainMusic,
		Slug:        "testprov-" + key,
		Title:       "Test Song",
		Subtitle:    "Test Artist",
		Description: "A great song description",
		ArtworkURL:  "https://example.com/art.jpg",
	}, nil
}
func (d *dummyProvider) Search(_ context.Context, _ string, _ int) ([]pulsarmeta.UnifiedMedia, error) {
	return nil, nil
}

func setupTestMetadataRouter(t *testing.T, testAccID uuid.UUID) (*gin.Engine, *gorm.DB) {
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite test db: %v", err)
	}

	_ = db.AutoMigrate(
		&model.CatalogItem{},
		&model.ActiveSession{},
		&model.Scrobble{},
	)

	cfg := &config.Config{}
	cfg.SiteUrl = "https://solian.live"
	cfg.Security.MaxTake = 50

	st := store.New(db)
	services := &core.Services{
		Env:    &app.Env{DB: db},
		Config: cfg,
		Store:  st,
	}

	authMiddleware := func(c *gin.Context) {
		c.Set(authn.ContextKeyUser, &gen.DyAccount{Id: testAccID.String()})
		c.Next()
	}

	srv := httpx.New(httpx.Options{
		Auth: authMiddleware,
	})

	host := &module.Host[core.Services]{
		Core:    services,
		Engine:  srv.Engine,
		API:     srv.API(),
		Enabled: map[string]bool{"app.solian.pulsar.metadata": true},
	}

	factory := Factory()
	inst := factory.New().(*instance)
	if err := inst.Configure(context.Background(), host); err != nil {
		t.Fatalf("failed to configure metadata module: %v", err)
	}

	// Register dummy provider
	inst.engine.RegisterProvider(&dummyProvider{})

	return srv.Engine, db
}

func TestMetadataEndpoints(t *testing.T) {
	testAcc := uuid.New()
	router, db := setupTestMetadataRouter(t, testAcc)

	// 1. Resolve metadata via API JSON
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/metadata/testprov:42", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var media pulsarmeta.UnifiedMedia
	if err := json.Unmarshal(w.Body.Bytes(), &media); err != nil {
		t.Fatalf("failed to parse JSON response: %v", err)
	}
	if media.Title != "Test Song" || media.Slug != "testprov-42" {
		t.Fatalf("unexpected media: %+v", media)
	}

	// 2. Access public share card with Accept: text/html
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/share/m/testprov-42", nil)
	req.Header.Set("Accept", "text/html")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from HTML share, got %d", w.Code)
	}
	bodyStr := w.Body.String()
	if !contains(bodyStr, "og:title") || !contains(bodyStr, "Test Song") {
		t.Fatalf("expected OpenGraph tags in HTML share response, got: %s", bodyStr)
	}

	// 3. Link scrobble to catalog item
	scrobble := &model.Scrobble{
		ID:         uuid.New(),
		AccountID:  testAcc,
		Domain:     model.DomainMusic,
		ClientName: "TestClient",
		Title:      "Test Song",
	}
	_ = db.Create(scrobble)

	linkReq := `{"urn": "testprov:42"}`
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodPut, "/api/scrobbles/"+scrobble.ID.String()+"/link", bytesNewReader(linkReq))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from scrobble link, got %d: %s", w.Code, w.Body.String())
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || (len(s) > 0 && len(substr) > 0 && stringContains(s, substr)))
}

func stringContains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func bytesNewReader(s string) *bytesReader {
	return &bytesReader{s: s, i: 0}
}

type bytesReader struct {
	s string
	i int
}

func (r *bytesReader) Read(b []byte) (n int, err error) {
	if r.i >= len(r.s) {
		return 0, ioEOF
	}
	n = copy(b, r.s[r.i:])
	r.i += n
	return n, nil
}

type errEOF string

func (e errEOF) Error() string { return string(e) }

const ioEOF = errEOF("EOF")
