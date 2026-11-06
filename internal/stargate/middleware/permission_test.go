package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/model"
)

type fakePermissionChecker struct {
	allow  bool
	called int
}

func (f *fakePermissionChecker) HasPermission(context.Context, uuid.UUID, string) (bool, error) {
	f.called++
	return f.allow, nil
}

// TestAskPermissionGate pins the [AskPermission] contract: a superuser is
// allowed without consulting the permission service, so admin routes gated on
// it are not denied to operators who hold is_superuser but no matching
// permission node — but the token scope still narrows the session, and a
// regular account must hold the node.
func TestAskPermissionGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const key = "admin.accounts.factors.manage"
	for _, tc := range []struct {
		name       string
		superuser  bool
		allow      bool
		session    *model.AuthSession
		wantStatus int
		wantLookup bool
	}{
		{name: "superuser without the node", superuser: true, allow: false, wantStatus: http.StatusOK, wantLookup: false},
		{name: "superuser with a wildcard oauth scope", superuser: true, session: &model.AuthSession{Type: model.SessionTypeOAuth, Scopes: []string{"*"}}, wantStatus: http.StatusOK, wantLookup: false},
		{name: "superuser with a narrow oauth scope", superuser: true, session: &model.AuthSession{Type: model.SessionTypeOAuth, Scopes: []string{"chat.read"}}, wantStatus: http.StatusForbidden, wantLookup: false},
		{name: "regular account without the node", allow: false, wantStatus: http.StatusForbidden, wantLookup: true},
		{name: "regular account with the node", allow: true, wantStatus: http.StatusOK, wantLookup: true},
		{name: "regular account with the exact oauth scope", allow: true, session: &model.AuthSession{Type: model.SessionTypeOAuth, Scopes: []string{key}}, wantStatus: http.StatusOK, wantLookup: true},
		{name: "regular account with a narrow oauth scope", allow: true, session: &model.AuthSession{Type: model.SessionTypeOAuth, Scopes: []string{"chat.read"}}, wantStatus: http.StatusForbidden, wantLookup: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checker := &fakePermissionChecker{allow: tc.allow}
			engine := gin.New()
			engine.GET("/admin", func(c *gin.Context) {
				account := &model.Account{Id: uuid.NewString(), IsSuperuser: tc.superuser}
				ctx := WithCurrentUser(c.Request.Context(), account)
				if tc.session != nil {
					ctx = WithCurrentSession(ctx, tc.session)
				}
				c.Request = c.Request.WithContext(ctx)
				c.Next()
			}, AskPermission(checker, key), func(c *gin.Context) {
				c.Status(http.StatusOK)
			})

			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := checker.called > 0; got != tc.wantLookup {
				t.Fatalf("permission lookup = %v, want %v", got, tc.wantLookup)
			}
		})
	}
}
