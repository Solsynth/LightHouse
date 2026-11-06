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

	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/permission"
	"src.solsynth.dev/sosys/stargate/store"
)

const scopeTestDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

// TestRequirePermScopeIsAFilterNotAGrant pins the admin gate: a "*" token
// scope removes the scope narrowing but never replaces the permission lookup,
// so an account without the node is denied regardless of its scope. A narrow
// scope on an OAuth session still narrows a granted permission.
func TestRequirePermScopeIsAFilterNotAGrant(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), scopeTestDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}

	accountID := uuid.NewString()
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM permission_nodes WHERE actor = $1`, accountID)
	})
	grant := func() {
		if _, err := pool.Exec(ctx, `INSERT INTO permission_nodes (id, actor, type, "key", value, created_at, updated_at)
			VALUES ($1, $2, 0, $3, 'true'::jsonb, now(), now())`,
			uuid.NewString(), accountID, permission.AccountsManage); err != nil {
			t.Fatalf("grant permission: %v", err)
		}
	}

	st := store.New(pool)
	d := Deps{Store: st, Perm: permission.New(st.DB), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	user := &model.Account{Id: accountID, Name: "scope_" + accountID[:8]}
	superuser := &model.Account{Id: accountID, Name: user.Name, IsSuperuser: true}

	run := func(account *model.Account, session *model.AuthSession) int {
		gin.SetMode(gin.TestMode)
		e := gin.New()
		e.Use(func(c *gin.Context) {
			reqCtx := middleware.WithCurrentUser(c.Request.Context(), account)
			reqCtx = middleware.WithCurrentSession(reqCtx, session)
			c.Request = c.Request.WithContext(reqCtx)
			c.Next()
		})
		e.GET("/admin", requirePerm(d, permission.AccountsManage), func(c *gin.Context) {
			c.Status(http.StatusOK)
		})
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin", nil))
		return w.Code
	}

	login := &model.AuthSession{Type: model.SessionTypeLogin}
	oauthWildcard := &model.AuthSession{Type: model.SessionTypeOAuth, Scopes: []string{"*"}}
	oauthNarrow := &model.AuthSession{Type: model.SessionTypeOAuth, Scopes: []string{"chat.read"}}
	oauthExact := &model.AuthSession{Type: model.SessionTypeOAuth, Scopes: []string{permission.AccountsManage}}

	if got := run(user, login); got != http.StatusForbidden {
		t.Fatalf("login without the node = %d, want 403", got)
	}
	if got := run(user, oauthWildcard); got != http.StatusForbidden {
		t.Fatalf("oauth wildcard without the node = %d, want 403 (scope is not a grant)", got)
	}
	if got := run(user, oauthNarrow); got != http.StatusForbidden {
		t.Fatalf("oauth narrow without the node = %d, want 403", got)
	}

	grant()

	if got := run(user, login); got != http.StatusOK {
		t.Fatalf("login with the node = %d, want 200", got)
	}
	if got := run(user, oauthWildcard); got != http.StatusOK {
		t.Fatalf("oauth wildcard with the node = %d, want 200", got)
	}
	if got := run(user, oauthExact); got != http.StatusOK {
		t.Fatalf("oauth exact scope with the node = %d, want 200", got)
	}
	if got := run(user, oauthNarrow); got != http.StatusForbidden {
		t.Fatalf("oauth narrow scope with the node = %d, want 403", got)
	}

	// A superuser is not above its token's scope.
	if got := run(superuser, oauthNarrow); got != http.StatusForbidden {
		t.Fatalf("superuser oauth narrow = %d, want 403", got)
	}
	if got := run(superuser, oauthWildcard); got != http.StatusOK {
		t.Fatalf("superuser oauth wildcard = %d, want 200", got)
	}
}
