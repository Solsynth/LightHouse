package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestStaticBypassesAuthChain pins the mount order: a dist file is served
// before the auth middleware runs, so assets never trigger a session lookup.
func TestStaticBypassesAuthChain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newTestHandler(t)

	authCalled := false
	engine := gin.New()
	engine.Use(h.ServeStatic)
	engine.Use(func(c *gin.Context) { authCalled = true })
	engine.NoRoute(h.Serve)

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/assets/app-abc123.js", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("asset status = %d, want 200", w.Code)
	}
	if authCalled {
		t.Fatal("auth middleware ran for a static asset")
	}
	if w.Body.String() != "console.log('app')" {
		t.Fatalf("asset body = %q", w.Body.String())
	}
}

// TestAPIRequestsReachAuthChain keeps the fast path from swallowing API calls.
func TestAPIRequestsReachAuthChain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newTestHandler(t)

	authCalled := false
	engine := gin.New()
	engine.Use(h.ServeStatic)
	engine.Use(func(c *gin.Context) { authCalled = true; c.Next() })
	engine.NoRoute(h.Serve)

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/accounts/me", nil))
	if !authCalled {
		t.Fatal("auth middleware skipped for an API request")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}
