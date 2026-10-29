package authctl

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/go/pkg/handoff"
	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/geo"
	"src.solsynth.dev/sosys/stargate/internal/grpcclient"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// The hand-off endpoints are the one place a session leaves this deployment for
// an app on another domain. These tests drive the real handlers over HTTP with a
// real session, so the properties that matter are exercised end to end: the code
// is single-use, the target host decides whether a code is issued at all, and a
// redeemed grant mints a pair for the session the person actually signed in with.

const handoffClientID = "app.solian.express"
const handoffClientSecret = "handoff-test-secret"

type handoffHarness struct {
	router    *gin.Engine
	rc        *redis.Client
	pool      *pgxpool.Pool
	cfg       *config.Config
	accountID string
	sessionID string
	token     string
	tokenSvc  *auth.TokenAuthService
	ctx       context.Context
}

func newHandoffHarness(t *testing.T) *handoffHarness {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, smokeDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	rc, err := redis.Connect(ctx, "localhost:6379", "", 0)
	if err != nil {
		t.Skipf("redis unavailable: %v", err)
	}
	t.Cleanup(func() { _ = rc.Raw.Close() })

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file path")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file))))
	privPath := filepath.Join(root, "Keys", "PrivateKey.pem")
	pubPath := filepath.Join(root, "Keys", "PublicKey.pem")
	if _, err := os.Stat(privPath); err != nil {
		t.Skipf("dev signing keys missing: %v", err)
	}

	cfg := config.Default()
	cfg.Auth.PublicKeyPath = pubPath
	cfg.Auth.PrivateKeyPath = privPath
	cfg.Captcha.AllowDisabled = true
	// A hand-off client on another registrable domain, which is the whole
	// reason the code flow exists.
	cfg.Auth.HandoffClients = []config.HandoffClient{{
		Id:            handoffClientID,
		Secret:        handoffClientSecret,
		RedirectHosts: []string{"dx.solsynth.dev"},
	}}
	jwtSvc, err := auth.NewJWTService(cfg)
	if err != nil {
		t.Fatalf("jwt service: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.New(pool)
	tokenSvc := auth.NewTokenAuthService(st, rc, jwtSvc, nil, nil, log)
	authSvc := auth.NewAuthService(st, rc, cfg, geo.NewService(""), jwtSvc, tokenSvc, nil, nil, log)
	h := &handler{d: Deps{
		Store:   st,
		Redis:   rc,
		Cfg:     cfg,
		Token:   tokenSvc,
		Auth:    authSvc,
		Geo:     &geo.Service{},
		Clients: &grpcclient.Clients{},
		Log:     log,
	}}

	accountID, sessionID, token := seedHandoffSession(t, ctx, pool, jwtSvc)

	router := gin.New()
	authMW := middleware.Auth(middleware.AuthDeps{
		Token: tokenSvc, Renewer: authSvc, CookieDomain: cfg.Auth.CookieDomain, Log: log,
	})
	api := router.Group("/api/auth")
	api.Use(authMW)
	registerHandoff(api, h)

	t.Cleanup(func() {
		keys, err := rc.Raw.Keys(ctx, "dyson:auth:handoff:*").Result()
		if err == nil && len(keys) > 0 {
			_ = rc.Raw.Del(ctx, keys...).Err()
		}
	})

	return &handoffHarness{
		router: router, rc: rc, pool: pool, cfg: cfg, tokenSvc: tokenSvc,
		accountID: accountID, sessionID: sessionID, token: token, ctx: ctx,
	}
}

// seedHandoffSession creates an account with a live login session and returns an
// access token for it.
func seedHandoffSession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, jwtSvc *auth.JWTService) (string, string, string) {
	t.Helper()
	accountID := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, "handoff_"+uuid.NewString()[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	clientID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_clients
		(id, account_id, device_id, device_name, platform, created_at, updated_at)
		VALUES ($1, $2, $3, $3, $4, $5, $5)`,
		clientID, accountID, "handoff-test-device", int(model.ClientPlatformWeb), now); err != nil {
		t.Fatalf("seed client: %v", err)
	}
	sessionID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions
		(id, account_id, audiences, scopes, epoch, type, expired_at, created_at, updated_at, client_id, last_granted_at)
		VALUES ($1, $2, '[]', '["*"]', 0, 0, $3, $4, $4, $5, $4)`,
		sessionID, accountID, now.Add(time.Hour), now, clientID); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	st := store.New(pool)
	account, err := st.GetAccountByID(ctx, uuid.MustParse(accountID))
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	session := &model.AuthSession{
		Id: sessionID, AccountId: accountID, Type: model.SessionTypeLogin,
		Scopes: []string{"*"}, Audiences: []string{}, Epoch: 0,
		ExpiredAt: model.NewTime(now.Add(time.Hour)), LastGrantedAt: model.NewTime(now),
	}
	token, err := jwtSvc.CreateUserToken(session, account, now.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("create access token: %v", err)
	}
	return accountID, sessionID, token
}

// post sends a JSON body with the harness session attached and decodes the reply.
func (h *handoffHarness) post(t *testing.T, path string, body any) (int, []byte) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.token)
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// TestHandoffIssueResolvesClientFromTargetHost proves the console never has to
// know an app's client id: naming the host the browser should land on is enough,
// and a host no client registered for gets no code.
func TestHandoffIssueResolvesClientFromTargetHost(t *testing.T) {
	h := newHandoffHarness(t)

	status, body := h.post(t, "/api/auth"+handoff.IssuePathSuffix, map[string]any{
		"redirect_uri": "https://dx.solsynth.dev/releases",
	})
	if status != http.StatusOK {
		t.Fatalf("issue status = %d body=%s", status, body)
	}
	var issued handoff.IssueWireResponse
	if err := json.Unmarshal(body, &issued); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	if issued.Code == "" || issued.RedirectURI != "https://dx.solsynth.dev/releases" {
		t.Fatalf("issue response = %+v", issued)
	}

	// A host no client is registered for must not be attributed to one.
	status, body = h.post(t, "/api/auth"+handoff.IssuePathSuffix, map[string]any{
		"redirect_uri": "https://evil.example/",
	})
	if status != http.StatusBadRequest {
		t.Fatalf("unregistered host status = %d body=%s, want 400", status, body)
	}
	var failure handoff.ErrorWireResponse
	if err := json.Unmarshal(body, &failure); err != nil {
		t.Fatalf("decode failure: %v", err)
	}
	if failure.Error != handoff.CodeInvalidTarget && failure.Error != handoff.CodeInvalidClient {
		t.Fatalf("unregistered host error = %q", failure.Error)
	}
}

// TestHandoffRoundTripMintsForTheSignedInSession is the whole point: a code
// issued for the caller's session redeems into a pair whose session is that same
// one, and the code is not redeemable twice.
func TestHandoffRoundTripMintsForTheSignedInSession(t *testing.T) {
	h := newHandoffHarness(t)

	_, body := h.post(t, "/api/auth"+handoff.IssuePathSuffix, map[string]any{
		"redirect_uri": "https://dx.solsynth.dev/releases",
	})
	var issued handoff.IssueWireResponse
	if err := json.Unmarshal(body, &issued); err != nil {
		t.Fatalf("decode issue: %v", err)
	}

	// The relying app's server redeems, unauthenticated: the code is the
	// credential there, not a session.
	redeem := func(code, secret string) (int, []byte) {
		payload, _ := json.Marshal(map[string]any{
			"client_id": handoffClientID, "code": code, "client_secret": secret,
			"redirect_uri": "https://dx.solsynth.dev/releases",
		})
		req := httptest.NewRequest(http.MethodPost, "/api/auth"+handoff.RedeemPathSuffix, bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.router.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}

	status, body := redeem(issued.Code, handoffClientSecret)
	if status != http.StatusOK {
		t.Fatalf("redeem status = %d body=%s", status, body)
	}
	var tokens handoff.RedeemWireResponse
	if err := json.Unmarshal(body, &tokens); err != nil {
		t.Fatalf("decode redeem: %v", err)
	}
	if tokens.Token == "" || tokens.RefreshToken == "" {
		t.Fatalf("redeem returned no pair: %+v", tokens)
	}

	// The access token authenticates as the account that signed in, and as the
	// very session the code was issued for: a hand-off is not a new identity.
	valid, handedOff, _, _ := h.tokenSvc.AuthenticateToken(context.Background(), tokens.Token, "198.18.0.9")
	if !valid || handedOff == nil {
		t.Fatal("handed-off token did not authenticate")
	}
	if handedOff.Id != h.sessionID || handedOff.AccountId != h.accountID {
		t.Fatalf("handed-off session = %s/%s, want %s/%s", handedOff.Id, handedOff.AccountId, h.sessionID, h.accountID)
	}

	// Replay of the same code must fail.
	status, body = redeem(issued.Code, handoffClientSecret)
	if status != http.StatusBadRequest {
		t.Fatalf("replayed redeem status = %d body=%s, want 400", status, body)
	}
	var failure handoff.ErrorWireResponse
	if err := json.Unmarshal(body, &failure); err != nil {
		t.Fatalf("decode failure: %v", err)
	}
	if failure.Error != handoff.CodeInvalidGrant {
		t.Fatalf("replayed redeem error = %q, want invalid_grant", failure.Error)
	}

	// A wrong secret must not consume the code: the rightful app can still use
	// a fresh one, and the wrong one is refused.
	_, body = h.post(t, "/api/auth"+handoff.IssuePathSuffix, map[string]any{
		"redirect_uri": "https://dx.solsynth.dev/releases",
	})
	var second handoff.IssueWireResponse
	if err := json.Unmarshal(body, &second); err != nil {
		t.Fatalf("decode second issue: %v", err)
	}
	if status, _ := redeem(second.Code, "wrong-secret"); status != http.StatusUnauthorized {
		t.Fatalf("wrong secret status = %d, want 401", status)
	}
	if status, _ := redeem(second.Code, handoffClientSecret); status != http.StatusOK {
		t.Fatalf("rightful redeem after a wrong attempt = %d, want 200", status)
	}
}

// TestHandoffRenewExtendsTheHandedOffSession: the app's server trades its refresh
// token for a fresh pair, so a short access-token lifetime does not force the
// person to sign in again.
func TestHandoffRenewExtendsTheHandedOffSession(t *testing.T) {
	h := newHandoffHarness(t)

	_, body := h.post(t, "/api/auth"+handoff.IssuePathSuffix, map[string]any{
		"redirect_uri": "https://dx.solsynth.dev/releases",
	})
	var issued handoff.IssueWireResponse
	if err := json.Unmarshal(body, &issued); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	status, body := h.post(t, "/api/auth"+handoff.RedeemPathSuffix, map[string]any{
		"client_id": handoffClientID, "code": issued.Code, "client_secret": handoffClientSecret,
	})
	if status != http.StatusOK {
		t.Fatalf("redeem status = %d body=%s", status, body)
	}
	var tokens handoff.RedeemWireResponse
	if err := json.Unmarshal(body, &tokens); err != nil {
		t.Fatalf("decode redeem: %v", err)
	}

	status, body = h.post(t, "/api/auth"+handoff.RenewPathSuffix, map[string]any{
		"client_id": handoffClientID, "client_secret": handoffClientSecret, "refresh_token": tokens.RefreshToken,
	})
	if status != http.StatusOK {
		t.Fatalf("renew status = %d body=%s", status, body)
	}
	var renewed handoff.RedeemWireResponse
	if err := json.Unmarshal(body, &renewed); err != nil {
		t.Fatalf("decode renew: %v", err)
	}
	if renewed.Token == "" || renewed.RefreshToken == "" {
		t.Fatalf("renew returned no pair: %+v", renewed)
	}

	// A secret is what stops a leaked refresh token from being traded in.
	if status, _ := h.post(t, "/api/auth"+handoff.RenewPathSuffix, map[string]any{
		"client_id": handoffClientID, "client_secret": "wrong", "refresh_token": renewed.RefreshToken,
	}); status != http.StatusUnauthorized {
		t.Fatalf("renew with a wrong secret = %d, want 401", status)
	}
}
