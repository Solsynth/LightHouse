package adminctl

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/stargate/internal/permission"
)

// adminRouteTables returns every route table registered under the /api/admin
// group (the tables that Register consumes).
func adminRouteTables() map[string][]adminRoute {
	return map[string][]adminRoute{
		"/api/admin/accounts":              accountAdminRoutes(Deps{}),
		"/api/admin/permissions":           permissionAdminRoutes(Deps{}),
		"/api/admin/cache":                 cacheAdminRoutes(Deps{}),
		"/api/admin/stats/users/geography": geographyRoutes(Deps{}),
	}
}

// TestAdminRoutesNotDefaultGranted fails if any route mounted under
// /api/admin is gated on a permission key that the `default` permission group
// grants every account: such a route would be reachable by any logged-in
// account and is therefore not an admin route at all.
func TestAdminRoutesNotDefaultGranted(t *testing.T) {
	defaultKeys := make(map[string]struct{})
	for _, key := range permission.DefaultPermissionKeys() {
		defaultKeys[key] = struct{}{}
	}

	routes := 0
	for group, table := range adminRouteTables() {
		for _, route := range table {
			routes++
			if len(route.Keys) == 0 {
				t.Errorf("%s %s %s has no permission key", group, route.Method, route.Path)
				continue
			}
			for _, key := range route.Keys {
				if _, ok := defaultKeys[key]; ok {
					t.Errorf("%s %s %s is gated on %q, which the `default` group grants every account",
						group, route.Method, route.Path, key)
				}
			}
		}
	}
	if routes == 0 {
		t.Fatal("no admin routes found")
	}
}

// TestAccountAdminRoutesUseDedicatedKeys pins the credential-mutating account
// admin routes to the dedicated admin.accounts.* keys. They previously reused
// self-service keys (auth.factors.manage, auth.sessions.manage,
// account.devices.manage, account.contacts.manage) that the `default` group
// grants every account, letting any logged-in caller manage another account's
// password/2FA factors, sessions, devices and contacts.
func TestAccountAdminRoutesUseDedicatedKeys(t *testing.T) {
	selfServiceKeys := map[string]struct{}{
		permission.AuthFactorsManage:     {},
		permission.AuthSessionsManage:    {},
		permission.AccountDevicesManage:  {},
		permission.AccountContactsManage: {},
	}
	dedicatedKeys := map[string]struct{}{
		permission.AdminAccountsFactorsManage:  {},
		permission.AdminAccountsSessionsManage: {},
		permission.AdminAccountsDevicesManage:  {},
		permission.AdminAccountsContactsManage: {},
	}

	used := make(map[string]struct{})
	for _, route := range accountAdminRoutes(Deps{}) {
		for _, key := range route.Keys {
			if _, bad := selfServiceKeys[key]; bad {
				t.Errorf("%s %s is gated on self-service key %q (granted to every account)",
					route.Method, route.Path, key)
			}
			if _, ok := dedicatedKeys[key]; ok {
				used[key] = struct{}{}
			}
		}
	}
	for key := range dedicatedKeys {
		if _, ok := used[key]; !ok {
			t.Errorf("dedicated key %q is not required by any account admin route", key)
		}
	}
}

// TestAccountPunishmentReadsRequireAuth pins RequireAuth on the account
// punishment reads, which the report found serving any account's punishment
// records to anonymous callers.
func TestAccountPunishmentReadsRequireAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	api := engine.Group("/api")
	Register(api, Deps{Log: slog.Default()})

	for _, path := range []string{
		"/api/accounts/me/punishments",
		"/api/accounts/alice/punishments",
		"/api/accounts/alice/punishments/overview",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s anonymous: got %d, want 401", path, rec.Code)
		}
	}
}
