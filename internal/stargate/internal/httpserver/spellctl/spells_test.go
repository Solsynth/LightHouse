package spellctl

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func newTestEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	api := e.Group("/api")
	Register(api, Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return e
}

// TestSpellRoutesRegistered pins the route tree: the static
// contact-verification/resend path coexists with the :word / :id param
// routes without a gin panic, and every C# MagicSpellController path exists.
func TestSpellRoutesRegistered(t *testing.T) {
	e := newTestEngine()
	var paths []string
	for _, route := range e.Routes() {
		paths = append(paths, route.Method+" "+route.Path)
	}
	for _, want := range []string{
		"POST /api/spells/contact-verification/resend",
		"GET /api/spells/:word",
		"POST /api/spells/:word/apply",
		"POST /api/spells/:word/resend",
	} {
		found := false
		for _, got := range paths {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("route %q not registered; got %v", want, paths)
		}
	}
}

// TestResendContactVerificationRequiresAuth: the resend endpoint is
// [Authorize]-protected and returns the canonical 401 without a session.
func TestResendContactVerificationRequiresAuth(t *testing.T) {
	e := newTestEngine()
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/spells/contact-verification/resend", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"code":"UNAUTHORIZED"`) {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
}
