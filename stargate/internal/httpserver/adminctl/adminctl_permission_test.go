package adminctl

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/permission"
	"src.solsynth.dev/sosys/stargate/internal/store"
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
// punishment reads that expose the detailed records: the caller's own list and
// any account's full list. The public summary banner/sheet
// (/{name}/punishments/overview) is deliberately absent — see
// TestAnonymousPunishmentOverviewIsPublic below.
func TestAccountPunishmentReadsRequireAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	api := engine.Group("/api")
	Register(api, Deps{Log: slog.Default()})

	for _, path := range []string{
		"/api/accounts/me/punishments",
		"/api/accounts/alice/punishments",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s anonymous: got %d, want 401", path, rec.Code)
		}
	}
}

// punishmentOverviewDSN mirrors config.example.toml and the other DB-backed
// smoke tests.
const punishmentOverviewDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

// TestAnonymousPunishmentOverviewIsPublic pins the restored public summary: an
// anonymous GET /api/accounts/{name}/punishments/overview is served (200)
// instead of rejected, because clients render the banner/sheet for signed-out
// visitors. The detailed records stay 401 (TestAccountPunishmentReadsRequireAuth).
func TestAnonymousPunishmentOverviewIsPublic(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), punishmentOverviewDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}

	accountID := uuid.New()
	name := "punish_overview_" + uuid.NewString()[:8]
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts
		(id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, name, now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	defer func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM punishments WHERE account_id = $1`, accountID)
		_, _ = pool.Exec(bg, `DELETE FROM accounts WHERE id = $1`, accountID)
	}()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	api := engine.Group("/api")
	Register(api, Deps{
		Store: store.New(pool),
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/accounts/"+name+"/punishments/overview", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("anonymous overview: got %d, want 200: %s", rec.Code, rec.Body.String())
	}
}
