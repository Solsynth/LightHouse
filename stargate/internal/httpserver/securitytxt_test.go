package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/stargate/internal/config"
)

// TestSecurityTxt serves the RFC 9116 document with the configured contact and
// an always-future Expires (a stale security.txt is treated as invalid).
func TestSecurityTxt(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := config.Default()
	cfg.BaseUrl = "https://api.solian.app"
	cfg.SecurityTxt.Contact = "mailto:security@example.com"

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/.well-known/security.txt", nil)
	(&Server{cfg: cfg}).securityTxt(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content-type = %q, want text/plain", ct)
	}
	body := w.Body.String()
	for _, want := range []string{
		"Contact: mailto:security@example.com",
		"Expires: ",
		"Canonical: https://api.solian.app/.well-known/security.txt",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("security.txt missing %q:\n%s", want, body)
		}
	}
}

// TestSecurityTxtFallsBackToDefaultContact pins the built-in contact so a
// deployment that never configures [securityTxt] still publishes a usable
// disclosure address.
func TestSecurityTxtFallsBackToDefaultContact(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/.well-known/security.txt", nil)
	(&Server{cfg: config.Default()}).securityTxt(c)

	if !strings.Contains(w.Body.String(), "Contact: "+config.DefaultSecurityContact) {
		t.Fatalf("security.txt does not carry the default contact:\n%s", w.Body.String())
	}
}
