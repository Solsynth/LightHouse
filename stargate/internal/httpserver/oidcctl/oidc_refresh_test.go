package oidcctl

// End-to-end regression test for the OIDC refresh_token grant.
//
// Pins the production failure "clients lost their auth state on app restart
// + refresh": after a refresh rotated the session (epoch 0 -> 1), the new
// access token was rejected by AuthenticateToken because the epoch/ver JWT
// claims are string-encoded (the C# mints them as strings) but the Go port
// read them as float64, silently falling back to 0. The OIDC refresh flow
// itself also needs GetSessionWithAccount to execute (the unqualified-column
// ambiguity that broke the session JOIN).
//
// Mirrors the admin_oidc_test.go convention: skip when Postgres/Redis are
// unavailable.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/geo"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

const refreshTestDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file path")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file))))
}

// stubAppProvider resolves every OIDC app UUID to one slug, standing in for
// the Develop gRPC client.
type stubAppProvider struct{ slug string }

func (p stubAppProvider) GetCustomAppSlug(_ context.Context, _ string) (string, error) {
	return p.slug, nil
}

func newRefreshTestService(t *testing.T) (*service, *store.Store, *auth.JWTService) {
	t.Helper()
	ctx := context.Background()
	root := repoRoot(t)
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
	cfg.OidcProvider.IssuerUri = "https://solian.app"
	cfg.OidcProvider.PublicKeyPath = pub
	cfg.OidcProvider.PrivateKeyPath = priv
	cfg.OidcProvider.AccessTokenLifetime = "5m"
	cfg.OidcProvider.RefreshTokenLifetime = "720h"
	cfg.OidcProvider.AuthorizationCodeLifetime = "30m"

	jwtSvc, err := auth.NewJWTService(cfg)
	if err != nil {
		t.Fatalf("jwt service: %v", err)
	}
	pool, err := pgxpool.New(ctx, refreshTestDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	st := store.New(pool)
	rc, err := redis.Connect(ctx, "localhost:6379", "", 0)
	if err != nil {
		t.Skipf("redis unavailable: %v", err)
	}
	t.Cleanup(func() { _ = rc.Raw.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tokenSvc := auth.NewTokenAuthService(st, rc, jwtSvc, nil, stubAppProvider{slug: "maidkit"}, log)
	authSvc := auth.NewAuthService(st, rc, cfg, geo.NewService(""), jwtSvc, tokenSvc, nil, nil, log)
	svc, err := newService(Deps{
		Store: st,
		Redis: rc,
		Cfg:   cfg,
		JWT:   jwtSvc,
		Token: tokenSvc,
		Auth:  authSvc,
		Log:   log,
	})
	if err != nil {
		t.Fatalf("oidc service: %v", err)
	}
	return svc, st, jwtSvc
}

func TestAuthorizationCodeFlowCreatesSessionWithJSONArrays(t *testing.T) {
	ctx := context.Background()
	svc, st, _ := newRefreshTestService(t)

	var accountID string
	if err := st.DB.QueryRow(ctx, `SELECT id FROM accounts ORDER BY created_at LIMIT 1`).Scan(&accountID); err != nil {
		t.Skipf("no local account to attach the session: %v", err)
	}

	clientID := uuid.NewString()
	session, _, scopes, err := svc.handleAuthorizationCodeFlow(ctx, &authorizationCodeInfo{
		ClientId:  clientID,
		AccountId: &accountID,
		Scopes:    []string{"openid", "profile"},
	}, clientID, "", "")
	if err != nil {
		t.Fatalf("handle authorization code flow: %v", err)
	}
	if len(scopes) != 2 {
		t.Fatalf("authorization code scopes = %v, want [openid profile]", scopes)
	}
	sessionID := uuid.MustParse(session.Id)
	t.Cleanup(func() {
		_, _ = st.DB.Exec(ctx, `DELETE FROM auth_sessions WHERE id = $1`, sessionID)
	})

	var audiences, storedScopes []string
	if err := st.DB.QueryRow(ctx, `SELECT audiences, scopes FROM auth_sessions WHERE id = $1`, sessionID).Scan(&audiences, &storedScopes); err != nil {
		t.Fatalf("load created OIDC session: %v", err)
	}
	if audiences == nil || storedScopes == nil {
		t.Fatalf("created OIDC session has nil JSON arrays: audiences=%v scopes=%v", audiences, storedScopes)
	}
}

func TestOidcRefreshTokenFlow(t *testing.T) {
	ctx := context.Background()
	svc, st, jwtSvc := newRefreshTestService(t)

	// Any local account satisfies the accounts JOIN in GetSessionWithAccount.
	var accountID string
	if err := st.DB.QueryRow(ctx, `SELECT id FROM accounts ORDER BY created_at LIMIT 1`).Scan(&accountID); err != nil {
		t.Skipf("no local account to attach the session: %v", err)
	}

	// Seed an OAuth session exactly like CreateSessionForOidc writes it:
	// type=OAuth(1), epoch 0, expired_at NULL (refresh extends it), scopes
	// set, app_id = the OIDC client id.
	sessionID := uuid.New()
	clientID := uuid.New()
	clientIDStr := clientID.String()
	now := time.Now().UTC()
	scopes := []string{"openid", "profile", "email", "*"}
	scopesJSON, err := json.Marshal(scopes)
	if err != nil {
		t.Fatalf("marshal scopes: %v", err)
	}
	if _, err := st.DB.Exec(ctx, `INSERT INTO auth_sessions
		(id, type, created_at, last_granted_at, account_id, app_id, audiences, scopes, epoch, updated_at)
		VALUES ($1, $2, $3, $3, $4, $5, '[]', $6::jsonb, 0, $3)`,
		sessionID, int(model.SessionTypeOAuth), now, accountID, clientID, string(scopesJSON),
	); err != nil {
		t.Fatalf("seed oauth session: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.DB.Exec(ctx, `DELETE FROM auth_sessions WHERE id = $1`, sessionID)
	})

	version, err := svc.token.GetAccountVersion(ctx, accountID)
	if err != nil {
		t.Fatalf("account version: %v", err)
	}

	mintRefresh := func(session *model.AuthSession) string {
		t.Helper()
		token, err := jwtSvc.CreateRefreshToken(session, version, now.Add(30*24*time.Hour))
		if err != nil {
			t.Fatalf("mint refresh token: %v", err)
		}
		return token
	}

	// First refresh: epoch 0 -> 1, session expiry extended.
	rtk := mintRefresh(&model.AuthSession{
		Id:        sessionID.String(),
		AccountId: accountID,
		AppId:     &clientIDStr,
		Type:      model.SessionTypeOAuth,
		Epoch:     0,
		Scopes:    scopes,
	})
	refreshed, _, refreshedScopes, err := svc.handleRefreshTokenFlow(ctx, clientIDStr, rtk)
	if err != nil {
		t.Fatalf("first refresh rejected: %v", err)
	}
	if refreshed.Id != sessionID.String() || refreshed.Epoch != 1 {
		t.Fatalf("first refresh: session %s epoch %d, want %s epoch 1", refreshed.Id, refreshed.Epoch, sessionID.String())
	}
	if len(refreshedScopes) == 0 {
		t.Fatal("first refresh: scopes lost")
	}

	// The DB must reflect the rotation so the next refresh's epoch check
	// (tokenEpoch vs session.Epoch from the store) passes.
	reloaded, err := st.GetSessionWithAccount(ctx, sessionID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if reloaded.Epoch != 1 {
		t.Fatalf("DB epoch after refresh = %d, want 1", reloaded.Epoch)
	}
	if reloaded.ExpiredAt == nil || !reloaded.ExpiredAt.Time().After(now.Add(20*24*time.Hour)) {
		t.Fatalf("session expiry not extended: %v", reloaded.ExpiredAt)
	}

	// Second refresh with the rotated token must also succeed (chained
	// rotation; this is the "clients lost auth state" scenario).
	rtk2 := mintRefresh(refreshed)
	refreshed2, _, _, err := svc.handleRefreshTokenFlow(ctx, clientIDStr, rtk2)
	if err != nil {
		t.Fatalf("second refresh rejected: %v", err)
	}
	if refreshed2.Epoch != 2 {
		t.Fatalf("second refresh epoch = %d, want 2", refreshed2.Epoch)
	}
	if refreshed2.Account == nil || refreshed2.Account.Id == "" {
		t.Fatal("refreshed session missing account (JOIN failed?)")
	}

	// The access token minted after refresh must authenticate. Its epoch
	// claim is a string ("2"); the float64-only read silently fell back to 0
	// and rejected it as "Token has been invalidated."
	atk, err := jwtSvc.CreateOidcUserToken(
		refreshed2, refreshed2.Account, version,
		now.Add(5*time.Minute), svc.issuer, "maidkit", refreshed2.Scopes,
		map[string]any{"azp": "maidkit"},
	)
	if err != nil {
		t.Fatalf("mint access token: %v", err)
	}
	ok, _, msg, tokenUse := svc.token.AuthenticateToken(ctx, atk, "127.0.0.1")
	if !ok {
		t.Fatalf("access token after refresh rejected: %s (use=%s)", msg, tokenUse)
	}
}
