package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	dist := t.TempDir()
	files := map[string]string{
		"index.html":           "<!doctype html><div id=\"app\"></div>",
		"favicon.ico":          "icon",
		"assets/app-abc123.js": "console.log('app')",
	}
	for name, content := range files {
		path := filepath.Join(dist, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h, err := New(dist)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func serve(t *testing.T, h *Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(h.ServeStatic)
	engine.NoRoute(h.Serve)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}

func TestServesIndexAtRoot(t *testing.T) {
	w := serve(t, newTestHandler(t), http.MethodGet, "/")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
	if w.Body.String() != "<!doctype html><div id=\"app\"></div>" {
		t.Errorf("body = %q", w.Body.String())
	}
}

func TestHistoryFallbackForClientRoutes(t *testing.T) {
	h := newTestHandler(t)
	for _, target := range []string{"/account/me/settings", "/posts/hello-world", "/auth/login"} {
		w := serve(t, h, http.MethodGet, target)
		if w.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", target, w.Code)
		}
		if w.Body.String() != "<!doctype html><div id=\"app\"></div>" {
			t.Errorf("%s: body is not index.html", target)
		}
	}
}

func TestServesStaticAssets(t *testing.T) {
	h := newTestHandler(t)

	w := serve(t, h, http.MethodGet, "/assets/app-abc123.js")
	if w.Code != http.StatusOK {
		t.Fatalf("asset status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("asset Cache-Control = %q", got)
	}
	if w.Body.String() != "console.log('app')" {
		t.Errorf("asset body = %q", w.Body.String())
	}

	w = serve(t, h, http.MethodGet, "/favicon.ico")
	if w.Code != http.StatusOK {
		t.Fatalf("favicon status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("favicon Cache-Control = %q, want no-cache", got)
	}
}

func TestMissingAssetIs404(t *testing.T) {
	w := serve(t, newTestHandler(t), http.MethodGet, "/assets/missing.js")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("404 body is not JSON: %v", err)
	}
	if body["code"] != "NOT_FOUND" {
		t.Errorf("code = %v, want NOT_FOUND", body["code"])
	}
}

func TestReservedPathsNeverFallBackToSPA(t *testing.T) {
	h := newTestHandler(t)
	for _, target := range []string{
		"/api/unknown",
		"/api",
		"/.well-known/unknown",
		"/swagger/unknown",
		"/health",
		"/alive",
	} {
		w := serve(t, h, http.MethodGet, target)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", target, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Errorf("%s: Content-Type = %q, want JSON", target, ct)
		}
	}
}

func TestPrefixMatchIsSegmentAware(t *testing.T) {
	w := serve(t, newTestHandler(t), http.MethodGet, "/apiary")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (client route)", w.Code)
	}
}

func TestNonGETIs404(t *testing.T) {
	w := serve(t, newTestHandler(t), http.MethodPost, "/account/me/settings")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestPathTraversalIsRejected(t *testing.T) {
	h := newTestHandler(t)
	for _, target := range []string{"/../go.mod", "/..%2f..%2fetc%2fpasswd", "/%2e%2e/%2e%2e/etc/passwd"} {
		w := serve(t, h, http.MethodGet, target)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", target, w.Code)
		}
		if w.Body.String() == "<!doctype html><div id=\"app\"></div>" {
			t.Errorf("%s: traversal fell back to index", target)
		}
	}
}

func TestDisabledHandlerAnswersJSON404(t *testing.T) {
	h, err := New("")
	if err != nil {
		t.Fatalf("New(\"\"): %v", err)
	}
	w := serve(t, h, http.MethodGet, "/")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestNewRejectsMissingDirectory(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("New on a missing directory returned no error")
	}
}
