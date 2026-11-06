package securityctl

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/auth"
	"src.solsynth.dev/sosys/stargate/config"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/store"
)

// passwordTestDSN mirrors config.example.toml.
const passwordTestDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

// TestPasswordFactorRouteRegistered guards the gin tree: the static
// factors/password path must coexist with the factors/:id param routes.
func TestPasswordFactorRouteRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	Register(e.Group("/api"), Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	for _, route := range e.Routes() {
		if route.Method == http.MethodPost && route.Path == "/api/factors/password" {
			return
		}
	}
	t.Fatal("POST /api/factors/password is not registered")
}

// passwordEngine builds the handler under a request context carrying the
// seeded account, so the test exercises the real body validation and store
// writes without minting a token.
func passwordEngine(t *testing.T, pool *pgxpool.Pool) (*gin.Engine, string, *store.Store) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	st := store.New(pool)
	c := &controller{d: Deps{Store: st, Cfg: config.Default(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}

	accountID := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(context.Background(), `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, "pwchange_"+accountID[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM account_auth_factors WHERE account_id = $1`, accountID)
		pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = $1`, accountID)
	})
	user := &model.Account{Id: accountID, Name: "pwchange_" + accountID[:8], Nick: "PW"}

	e := gin.New()
	e.Use(func(ctx *gin.Context) {
		ctx.Request = ctx.Request.WithContext(middleware.WithCurrentUser(ctx.Request.Context(), user))
		ctx.Next()
	})
	e.POST("/api/factors/password", c.changePassword)
	return e, accountID, st
}

func seedPassword(t *testing.T, st *store.Store, ctx context.Context, accountID, password string) {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertAuthFactor(ctx, &model.AuthFactor{
		Type:        model.AuthFactorTypePassword,
		Trustworthy: 1,
		AccountId:   accountID,
		Secret:      hash,
		EnabledAt:   model.NewTime(time.Now().UTC()),
	}); err != nil {
		t.Fatalf("seed password factor: %v", err)
	}
}

func postChangePassword(t *testing.T, e *gin.Engine, current, next string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"current_password": current, "new_password": next})
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/factors/password", bytes.NewReader(body)))
	return w
}

func passwordPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), passwordTestDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	return pool
}

// TestChangePasswordWithoutSpell pins the self-service change: the current
// password is the proof, no emailed spell or elevation is involved.
func TestChangePasswordWithoutSpell(t *testing.T) {
	pool := passwordPool(t)
	ctx := context.Background()
	e, accountID, st := passwordEngine(t, pool)
	seedPassword(t, st, ctx, accountID, "current-password-1")

	if w := postChangePassword(t, e, "current-password-1", "next-password-2"); w.Code != http.StatusOK {
		t.Fatalf("change password status = %d, body %s", w.Code, w.Body.String())
	}
	factor, err := st.GetEnabledPasswordFactor(ctx, accountID)
	if err != nil {
		t.Fatalf("load factor: %v", err)
	}
	if ok, _ := auth.VerifyFactorPassword(factor, "next-password-2"); !ok {
		t.Fatal("new password does not verify")
	}
	if ok, _ := auth.VerifyFactorPassword(factor, "current-password-1"); ok {
		t.Fatal("old password still verifies")
	}
}

// TestChangePasswordRejectsWrongCurrent: a stolen session cannot rewrite the
// password without knowing the current one.
func TestChangePasswordRejectsWrongCurrent(t *testing.T) {
	pool := passwordPool(t)
	ctx := context.Background()
	e, accountID, st := passwordEngine(t, pool)
	seedPassword(t, st, ctx, accountID, "current-password-1")

	w := postChangePassword(t, e, "wrong-password", "next-password-2")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "AUTH_INVALID_PASSWORD") {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	factor, err := st.GetEnabledPasswordFactor(ctx, accountID)
	if err != nil {
		t.Fatalf("load factor: %v", err)
	}
	if ok, _ := auth.VerifyFactorPassword(factor, "current-password-1"); !ok {
		t.Fatal("failed change mutated the password")
	}
}

// TestChangePasswordRejectsShortPassword pins the same 4–128 rule the
// registration form and the reset spell enforce.
func TestChangePasswordRejectsShortPassword(t *testing.T) {
	pool := passwordPool(t)
	ctx := context.Background()
	e, accountID, st := passwordEngine(t, pool)
	seedPassword(t, st, ctx, accountID, "current-password-1")

	w := postChangePassword(t, e, "current-password-1", "abc")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "VALIDATION_ERROR") {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
}
