package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/stargate/internal/config"
)

func TestAssetLinksUsesSupportedAppLinkPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/.well-known/assetlinks.json", nil)
	(&Server{}).assetLinks(c)

	var document []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &document); err != nil {
		t.Fatalf("assetlinks response is not valid JSON: %v", err)
	}
	if len(document) != 1 {
		t.Fatalf("assetlinks statement count = %d, want 1", len(document))
	}

	relations, ok := document[0]["relation_extensions"].(map[string]any)
	if !ok {
		t.Fatal("assetlinks response is missing relation_extensions")
	}
	common, ok := relations["delegate_permission/common.handle_all_urls"].(map[string]any)
	if !ok {
		t.Fatal("assetlinks response is missing common URL relation")
	}
	components, ok := common["dynamic_app_link_components"].([]any)
	if !ok {
		t.Fatal("assetlinks response is missing dynamic_app_link_components")
	}

	paths := make(map[string]bool, len(components))
	for _, raw := range components {
		component, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("dynamic component has unexpected type: %T", raw)
		}
		path, ok := component["/"].(string)
		if !ok {
			t.Fatalf("dynamic component has no path: %#v", component)
		}
		paths[path] = true
	}

	for _, path := range []string{"/", "/posts/*", "/account/me/stellar-program"} {
		if !paths[path] {
			t.Errorf("supported path %q is missing", path)
		}
	}
	for _, path := range []string{"/pricing", "/nonexistent"} {
		if paths[path] {
			t.Errorf("unsupported path %q is associated with the app", path)
		}
	}
}

// TestSPAMountOrder pins how New wires [web] distPath: an existing dist file
// is served before the auth middleware, while API and client-side routes still
// pass through it. gin rebuilds the NoRoute chain on Use, so mounting the SPA
// before engine.Use(authMiddleware) must not drop auth for the fallback.
func TestSPAMountOrder(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<!doctype html>spa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dist, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "assets", "app.js"), []byte("js"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Web.DistPath = dist

	var authPaths []string
	auth := func(c *gin.Context) {
		authPaths = append(authPaths, c.Request.URL.Path)
		c.Next()
	}
	srv := New(cfg, auth)
	if srv.WebError != nil {
		t.Fatalf("WebError = %v", srv.WebError)
	}

	do := func(method, target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		srv.Engine.ServeHTTP(w, httptest.NewRequest(method, target, nil))
		return w
	}

	if w := do(http.MethodGet, "/assets/app.js"); w.Code != http.StatusOK || w.Body.String() != "js" {
		t.Fatalf("asset = %d %q", w.Code, w.Body.String())
	}
	if len(authPaths) != 0 {
		t.Fatalf("auth middleware ran for a static asset: %v", authPaths)
	}

	if w := do(http.MethodGet, "/account/me/settings"); w.Code != http.StatusOK || w.Body.String() != "<!doctype html>spa" {
		t.Fatalf("client route = %d %q", w.Code, w.Body.String())
	}
	if len(authPaths) != 1 || authPaths[0] != "/account/me/settings" {
		t.Fatalf("client route did not pass through auth: %v", authPaths)
	}

	if w := do(http.MethodGet, "/api/does-not-exist"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown API route = %d, want 404", w.Code)
	}
	if len(authPaths) != 2 || authPaths[1] != "/api/does-not-exist" {
		t.Fatalf("API route did not pass through auth: %v", authPaths)
	}
}
