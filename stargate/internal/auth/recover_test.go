package auth

// Regression test for the production failure "null value in column
// "audiences" of relation "auth_sessions" violates not-null constraint
// (SQLSTATE 23502) when using recovery code": RecoverAccountWithRecoveryCode
// inserted the fresh session without the audiences/scopes jsonb columns,
// which are NOT NULL in the schema. Recovery must create a normal login
// session: empty audiences, full scope.
//
// Mirrors the authorized_apps_test.go convention: skip when Postgres or the
// dev signing keys are unavailable.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/geo"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

const recoveryTestDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

func TestRecoverAccountWithRecoveryCodeCreatesFullLoginSession(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, recoveryTestDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file path")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	priv := filepath.Join(root, "Keys", "PrivateKey.pem")
	pub := filepath.Join(root, "Keys", "PublicKey.pem")
	if _, err := os.Stat(priv); err != nil {
		t.Skipf("dev signing keys missing: %v", err)
	}

	cfg := &config.Config{}
	cfg.Auth.Issuer = "solar-network"
	cfg.Auth.Audiences = []string{"solar-network"}
	cfg.Auth.PublicKeyPath = pub
	cfg.Auth.PrivateKeyPath = priv
	cfg.Auth.AccessTokenLifetime = "5m"
	cfg.Auth.RefreshTokenLifetime = "720h"
	jwtSvc, err := NewJWTService(cfg)
	if err != nil {
		t.Fatalf("jwt service: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.New(pool)
	tokenSvc := NewTokenAuthService(st, nil, jwtSvc, nil, nil, log)
	svc := NewAuthService(st, nil, cfg, geo.NewService(""), jwtSvc, tokenSvc, nil, nil, log)

	accountID := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts
		(id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`,
		accountID, "recovery_regression_"+uuid.NewString()[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = $1`, accountID) })

	code := "0123456789abcdef0123456789abcdef"
	if _, err := st.InsertAuthFactor(ctx, &model.AuthFactor{
		AccountId:   accountID,
		Type:        model.AuthFactorTypeRecoveryCode,
		Trustworthy: 0,
		Secret:      code,
		EnabledAt:   model.NewTime(now),
	}); err != nil {
		t.Fatalf("seed recovery factor: %v", err)
	}

	// A live session the recovery flow must revoke.
	oldSessionID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions
		(id, account_id, audiences, scopes, epoch, type, expired_at, created_at, updated_at)
		VALUES ($1, $2, '[]', '["*"]', 0, 0, $3, $4, $4)`,
		oldSessionID, accountID, now.Add(time.Hour), now); err != nil {
		t.Fatalf("seed old session: %v", err)
	}

	pair, err := svc.RecoverAccountWithRecoveryCode(ctx, accountID, code,
		uuid.NewString(), model.ClientPlatformWeb, nil, "203.0.113.7", "recovery-regression-agent")
	if err != nil {
		t.Fatalf("recover account: %v", err)
	}
	if pair == nil || pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatal("recovery returned an empty token pair")
	}

	// The created session is a fresh login session with non-null jsonb
	// arrays (the NOT NULL violation that crashed the endpoint).
	var sessionID uuid.UUID
	var typeVal int
	var audiencesRaw, scopesRaw []byte
	var clientID *uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id, type, audiences, scopes, client_id
		FROM auth_sessions WHERE account_id = $1 AND id <> $2`,
		accountID, oldSessionID).Scan(&sessionID, &typeVal, &audiencesRaw, &scopesRaw, &clientID); err != nil {
		t.Fatalf("load recovered session: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM auth_sessions WHERE id = $1`, sessionID) })
	if typeVal != int(model.SessionTypeLogin) {
		t.Fatalf("recovered session type = %d, want login", typeVal)
	}
	if clientID == nil {
		t.Fatal("recovered session not bound to a device")
	}
	var audiences, scopes []string
	if err := json.Unmarshal(audiencesRaw, &audiences); err != nil {
		t.Fatalf("audiences not valid json: %v", err)
	}
	if err := json.Unmarshal(scopesRaw, &scopes); err != nil {
		t.Fatalf("scopes not valid json: %v", err)
	}
	if len(audiences) != 0 {
		t.Fatalf("recovered session audiences = %v, want empty", audiences)
	}
	if !slices.Equal(scopes, []string{"*"}) {
		t.Fatalf("recovered session scopes = %v, want [*]", scopes)
	}

	// The old session was revoked by the recovery.
	var oldExpired *time.Time
	if err := pool.QueryRow(ctx, `SELECT expired_at FROM auth_sessions WHERE id = $1`, oldSessionID).Scan(&oldExpired); err != nil {
		t.Fatalf("load old session: %v", err)
	}
	if oldExpired == nil {
		t.Fatal("old session was not revoked by recovery")
	}

	// The access token behaves like a normal login: full-scope claim.
	valid, claims := jwtSvc.ValidateJwt(pair.AccessToken)
	if !valid {
		t.Fatal("recovery access token does not validate")
	}
	if scope, _ := claims["scope"].(string); scope != "*" {
		t.Fatalf("recovery access token scope claim = %q, want %q", scope, "*")
	}
}
