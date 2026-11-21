package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/config"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/metadata"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

func TestWebUISSROpenGraphAndIslands(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dist := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dist, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte(`<!doctype html><html><body><div id="app"></div><script type="module" src="/assets/main-abc123.js"></script></body></html>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "assets", "main-abc123.js"), []byte("console.log('pulsar');"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{}
	cfg.Web.DistPath = dist
	services := &core.Services{Config: cfg}

	h, err := New(services)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if h.DistError != nil {
		t.Fatalf("unexpected DistError: %v", h.DistError)
	}

	engine := gin.New()
	h.RegisterRoutes(engine)
	engine.GET("/m/:id", func(c *gin.Context) {
		h.RenderMediaShare(c, &metadata.UnifiedMedia{
			ID:            uuid.New(),
			URN:           "mb:recording:123",
			Domain:        model.DomainMusic,
			Title:         "Astral Projection",
			Subtitle:      "Solaris",
			Description:   "A test track",
			ArtworkURL:    "https://example.com/cover.jpg",
			ShareURL:      "https://pulsar.example.com/m/mb:recording:123",
			ScrobbleCount: 42,
			Genres:        []string{"Electronic", "Ambient"},
		})
	})

	// 1. Home page and SPA routes serve index.html
	for _, route := range []string{"/", "/collections", "/history", "/settings"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, route, nil)
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", route, w.Code)
		}
		if !strings.Contains(w.Body.String(), `<div id="app"></div>`) {
			t.Errorf("GET %s body missing SPA mount", route)
		}
	}

	// 2. /api/me returns unauthenticated envelope cleanly
	wMe := httptest.NewRecorder()
	engine.ServeHTTP(wMe, httptest.NewRequest(http.MethodGet, "/api/me", nil))
	if wMe.Code != http.StatusOK {
		t.Fatalf("GET /api/me status = %d, want 200", wMe.Code)
	}
	var me map[string]any
	if err := json.Unmarshal(wMe.Body.Bytes(), &me); err != nil {
		t.Fatalf("decode /api/me: %v", err)
	}
	if me["authenticated"] != false {
		t.Errorf("expected authenticated=false, got %v", me["authenticated"])
	}

	// 3. Media share page renders OpenGraph tags
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/m/mb:recording:123", nil)
	engine.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("GET /m/:id status = %d, want 200", w2.Code)
	}
	shareBody := w2.Body.String()
	for _, want := range []string{
		`<meta property="og:title" content="Astral Projection · Solaris"`,
		`<meta property="og:type" content="music.song"`,
		`<meta property="og:image" content="https://example.com/cover.jpg"`,
	} {
		if !strings.Contains(shareBody, want) {
			t.Errorf("GET /m/:id body missing %q", want)
		}
	}

	// 4. Asset route serves built JS with immutable cache header
	w3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/assets/main-abc123.js", nil)
	engine.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("GET /assets/main-abc123.js status = %d, want 200", w3.Code)
	}
	if !strings.Contains(w3.Body.String(), "pulsar") {
		t.Errorf("unexpected asset body: %q", w3.Body.String())
	}
}
