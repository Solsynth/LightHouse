package adminctl

import (
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestRegisterRoutes validates that Register mounts every admin route
// without panicking (gin panics on conflicting route trees) and that the
// tree resolves the exact C# paths.
func TestRegisterRoutes(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	api := r.Group("/api")
	Register(api, Deps{Log: slog.Default()})

	gets := []string{
		"/api/admin/accounts",
		"/api/admin/accounts/alice",
		"/api/admin/accounts/alice/devices",
		"/api/admin/accounts/alice/sessions",
		"/api/admin/accounts/alice/sessions/11111111-1111-1111-1111-111111111111/children",
		"/api/admin/accounts/alice/contacts",
		"/api/admin/accounts/alice/factors",
		"/api/admin/accounts/alice/spells",
		"/api/admin/accounts/punishments/created",
		"/api/admin/accounts/emails/export",
		"/api/admin/permissions/groups",
		"/api/admin/permissions/groups/11111111-1111-1111-1111-111111111111",
		"/api/admin/permissions/actors/alice",
		"/api/admin/cache/stats",
		"/api/admin/cache/groups/foo",
		"/api/admin/stats/users/geography",
		"/api/actions",
		"/api/accounts/me/punishments",
		"/api/accounts/alice/punishments",
		"/api/accounts/alice/punishments/overview",
	}
	for _, path := range gets {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		// Routes are registered without the auth middleware here, so they
		// reach the handlers; with nil Deps most abort with 500/404, but
		// 404 means the route did not resolve.
		if w.Code == 404 {
			t.Errorf("route not found: %s", path)
		}
	}

	methods := []struct{ method, path string }{
		{"DELETE", "/api/admin/accounts/alice/sessions/11111111-1111-1111-1111-111111111111"},
		{"POST", "/api/admin/accounts/alice/sessions/revoke"},
		{"POST", "/api/admin/accounts/alice/spells"},
		{"POST", "/api/admin/accounts/alice/spells/11111111-1111-1111-1111-111111111111/resend"},
		{"DELETE", "/api/admin/accounts/alice/spells/11111111-1111-1111-1111-111111111111"},
		{"POST", "/api/admin/accounts/alice/suspend"},
		{"PATCH", "/api/admin/accounts/alice/devices/abc/label"},
		{"PUT", "/api/admin/permissions/groups/11111111-1111-1111-1111-111111111111/permissions/chat.create"},
		{"POST", "/api/admin/accounts/alice/factors/password/reset"},
		{"POST", "/api/admin/accounts/notifications"},
		{"POST", "/api/admin/accounts/emails"},
		{"POST", "/api/admin/cache/keys/clear"},
		{"POST", "/api/admin/cache/groups/clear"},
		{"POST", "/api/admin/cache/clear"},
	}
	for _, tc := range methods {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code == 404 {
			t.Errorf("route not found: %s %s", tc.method, tc.path)
		}
	}
}
