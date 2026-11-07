package authctl

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/stargate/config"
)

// TestWebAuthnDiscoveryPublishesVerificationOrigins pins the related-origin
// document to the same set the assertion verifier accepts. Browsers fetch
// /.well-known/webauthn for cross-origin ceremonies; publishing a different
// (or stale default) list makes the browser refuse the ceremony before any
// request reaches Stargate.
func TestWebAuthnDiscoveryPublishesVerificationOrigins(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Default()
	cfg.SiteUrl = "https://web.solian.app"
	cfg.BaseUrl = "https://api.solian.app"

	engine := gin.New()
	RegisterWellKnown(engine, cfg)

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/.well-known/webauthn", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var body struct {
		Origins []string `json:"origins"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	want := cfg.WebAuthnOrigins()
	if len(body.Origins) != len(want) {
		t.Fatalf("origins = %v, want %v", body.Origins, want)
	}
	for i := range want {
		if body.Origins[i] != want[i] {
			t.Fatalf("origins = %v, want %v", body.Origins, want)
		}
	}
}
