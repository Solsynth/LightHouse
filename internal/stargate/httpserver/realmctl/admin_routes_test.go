package realmctl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/permission"
)

// selfServiceChecker grants exactly the keys the `verified` group gives every
// activated account and nothing else.
type selfServiceChecker struct{}

func (selfServiceChecker) HasPermission(_ context.Context, _ uuid.UUID, key string) (bool, error) {
	switch key {
	case permission.RealmsUpdate, permission.RealmsDelete, permission.RealmsMembersManage, permission.RealmsCreate:
		return true, nil
	}
	return false, nil
}

// TestAdminRoutesRequirePlatformPermission pins the console routes to the
// platform permission `realms.moderate`. They used to be gated on the
// self-service keys (realms.update/delete/members.manage), which the `verified`
// group grants every activated account, so any user could transfer a realm's
// ownership, grant themselves a role, or delete the realm.
func TestAdminRoutesRequirePlatformPermission(t *testing.T) {
	defaultKeys := make(map[string]struct{})
	for _, key := range permission.DefaultPermissionKeys() {
		defaultKeys[key] = struct{}{}
	}
	selfServiceKeys := map[string]struct{}{
		permission.RealmsUpdate:        {},
		permission.RealmsDelete:        {},
		permission.RealmsMembersManage: {},
	}

	routes := adminRoutes(Deps{})
	if len(routes) == 0 {
		t.Fatal("no admin realm routes found")
	}
	for _, route := range routes {
		if route.Key != permission.RealmsModerate {
			t.Errorf("%s %s is gated on %q, want %q",
				route.Method, route.Path, route.Key, permission.RealmsModerate)
		}
		if _, ok := defaultKeys[route.Key]; ok {
			t.Errorf("%s %s is gated on %q, which every account holds",
				route.Method, route.Path, route.Key)
		}
		if _, ok := selfServiceKeys[route.Key]; ok {
			t.Errorf("%s %s is gated on the self-service key %q",
				route.Method, route.Path, route.Key)
		}
	}
}

// TestAdminMutationsDenySelfServiceUsers drives the real middleware stack: an
// activated account holding the self-service realm keys is rejected on every
// console mutation before any handler runs.
func TestAdminMutationsDenySelfServiceUsers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		ctx := middleware.WithCurrentUser(c.Request.Context(), &model.Account{Id: uuid.NewString()})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	RegisterAdmin(router.Group("/api"), Deps{Perm: selfServiceChecker{}})

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPatch, "/api/admin/realms/some-realm"},
		{http.MethodDelete, "/api/admin/realms/some-realm"},
		{http.MethodPatch, "/api/admin/realms/some-realm/members/" + uuid.NewString() + "/role"},
		{http.MethodDelete, "/api/admin/realms/some-realm/members/" + uuid.NewString()},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403 for a self-service user",
				tc.method, tc.path, rec.Code)
		}
	}
}
