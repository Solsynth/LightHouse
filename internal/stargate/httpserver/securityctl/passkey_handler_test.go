package securityctl

import (
	"bytes"
	"context"
	"encoding/base64"
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

	"src.solsynth.dev/sosys/stargate/config"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/redis"
	"src.solsynth.dev/sosys/stargate/store"
)

// TestPasskeyRegistrationHandler drives the real start/complete handlers with
// a seeded account, PostgreSQL and Redis, and a synthetic authenticator. It
// covers the failure the web console reported: a ceremony from the Vite dev
// origin (localhost:5173) must register, while an origin outside the
// configured set must still be refused (with the cause in `detail`).
func TestPasskeyRegistrationHandler(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, passwordTestDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	rc, err := redis.Connect(ctx, "localhost:6379", "", 0)
	if err != nil {
		t.Skipf("redis unavailable: %v", err)
	}
	t.Cleanup(func() { _ = rc.Raw.Close() })

	cfg := config.Default()
	st := store.New(pool)
	accountID := seedPasskeyAccount(t, ctx, pool, st)

	gin.SetMode(gin.TestMode)
	c := &controller{d: Deps{
		Store: st,
		Redis: rc,
		Cfg:   cfg,
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}
	user := &model.Account{Id: accountID, Name: "passkey_" + accountID[:8], Nick: "Passkey"}
	engine := gin.New()
	engine.Use(func(ginCtx *gin.Context) {
		ginCtx.Request = ginCtx.Request.WithContext(
			middleware.WithCurrentUser(ginCtx.Request.Context(), user))
		ginCtx.Next()
	})
	engine.POST("/api/factors/passkey/start", c.startPasskeyRegistration)
	engine.POST("/api/factors/passkey/complete", c.completePasskeyRegistration)

	register := func(t *testing.T, deviceID, origin string) *httptest.ResponseRecorder {
		t.Helper()
		start := postJSON(t, engine, "/api/factors/passkey/start",
			map[string]any{"device_id": deviceID, "device_name": "web"})
		if start.Code != http.StatusOK {
			t.Fatalf("start: got %d, want 200 (body %s)", start.Code, start.Body.String())
		}
		var options passkeyRegistrationStartResponse
		if err := json.Unmarshal(start.Body.Bytes(), &options); err != nil {
			t.Fatalf("decode start: %v", err)
		}
		attestation, _ := syntheticAttestation(t, options.RpId, options.Challenge, origin)
		b64 := func(v []byte) string { return base64.RawURLEncoding.EncodeToString(v) }
		return postJSON(t, engine, "/api/factors/passkey/complete", map[string]any{
			"device_id":          deviceID,
			"client_data_json":   b64(attestation.clientData),
			"attestation_object": b64(attestation.attestationObject),
			"label":              "web",
		})
	}

	t.Run("registers from the Vite dev origin", func(t *testing.T) {
		rec := register(t, "web-dev", "http://localhost:5173")
		if rec.Code != http.StatusOK {
			t.Fatalf("complete: got %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		passkeys, err := st.ListPasskeys(ctx, accountID)
		if err != nil {
			t.Fatalf("list passkeys: %v", err)
		}
		if len(passkeys) != 1 || passkeys[0].Label != "web" {
			t.Fatalf("stored passkeys = %+v", passkeys)
		}
	})

	t.Run("refuses an unlisted origin", func(t *testing.T) {
		rec := register(t, "web-evil", "https://evil.example")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("complete: got %d, want 400 (body %s)", rec.Code, rec.Body.String())
		}
		var apiErr struct {
			Code   string  `json:"code"`
			Detail *string `json:"detail"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &apiErr); err != nil {
			t.Fatalf("decode error: %v", err)
		}
		if apiErr.Code != "PADLOCK_PASSKEY_REGISTRATION_FAILED" {
			t.Fatalf("code = %q", apiErr.Code)
		}
		if apiErr.Detail == nil || !strings.Contains(*apiErr.Detail, "origin") {
			t.Fatalf("detail = %v, want the origin validation cause", apiErr.Detail)
		}
	})
}

func postJSON(t *testing.T, engine *gin.Engine, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
	request.Host = "localhost:8080"
	request.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(recorder, request)
	return recorder
}

// seedPasskeyAccount inserts an account with the recovery and passkey factors
// registration requires, and removes them after the test.
func seedPasskeyAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, st *store.Store) string {
	t.Helper()
	accountID := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, "passkey_"+accountID[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM account_passkeys WHERE account_id = $1`, accountID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM account_auth_factors WHERE account_id = $1`, accountID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = $1`, accountID)
	})
	for _, factor := range []model.AuthFactor{
		{Type: model.AuthFactorTypeRecoveryCode, Trustworthy: 0},
		{Type: model.AuthFactorTypePasskey, Trustworthy: 4},
	} {
		factor.AccountId = accountID
		factor.EnabledAt = model.NewTime(now)
		if _, err := st.InsertAuthFactor(ctx, &factor); err != nil {
			t.Fatalf("seed factor %d: %v", factor.Type, err)
		}
	}
	return accountID
}
