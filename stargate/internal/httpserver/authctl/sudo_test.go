package authctl

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	"github.com/pquerna/otp/totp"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/geo"
	"src.solsynth.dev/sosys/stargate/internal/grpcclient"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/spell"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

const sudoTestPassword = "hunter2"

// sudoFactorSeed describes one enabled factor to seed for an elevation test.
type sudoFactorSeed struct {
	ftype       model.AuthFactorType
	trustworthy int
	secret      string
}

type sudoSeed struct {
	verifiedEmail bool
	factors       []sudoFactorSeed
}

func sudoPasswordSeed(t *testing.T, ftype model.AuthFactorType, plaintext string, trustworthy int) sudoFactorSeed {
	t.Helper()
	hash, err := auth.HashPassword(plaintext)
	if err != nil {
		t.Fatalf("hash secret: %v", err)
	}
	return sudoFactorSeed{ftype: ftype, trustworthy: trustworthy, secret: hash}
}

// sudoHarness is a handler wired with real middleware, DB, Redis, signing
// keys, one account with seeded factors and a login session for it.
type sudoHarness struct {
	h         *handler
	router    *gin.Engine
	tokenSvc  *auth.TokenAuthService
	rc        *redis.Client
	pool      *pgxpool.Pool
	cfg       *config.Config
	authSvc   *auth.AuthService
	ring      *fakeRing
	token     string
	accountID string
	sessionID string
	ip        string
	ctx       context.Context
}

func newSudoHarness(t *testing.T, seed sudoSeed) *sudoHarness {
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
	jwtSvc, err := auth.NewJWTService(cfg)
	if err != nil {
		t.Fatalf("jwt service: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.New(pool)
	tokenSvc := auth.NewTokenAuthService(st, rc, jwtSvc, nil, nil, log)
	ring := &fakeRing{}
	spells := spell.NewService(st, rc, ring, cfg.SiteUrl, cfg, log)
	authSvc := auth.NewAuthService(st, rc, cfg, geo.NewService(""), jwtSvc, tokenSvc, nil, nil, log)
	h := &handler{d: Deps{
		Store:   st,
		Redis:   rc,
		Cfg:     cfg,
		Token:   tokenSvc,
		Auth:    authSvc,
		Geo:     &geo.Service{},
		Clients: &grpcclient.Clients{Ring: ring},
		Spells:  spells,
		Log:     log,
	}}
	middleware.SetSudoChecker(authSvc)
	t.Cleanup(func() { middleware.SetSudoChecker(nil) })

	accountID := seedSudoAccount(t, ctx, pool, seed.verifiedEmail)
	for _, f := range seed.factors {
		insertSudoFactor(t, ctx, pool, accountID, f.ftype, f.trustworthy, f.secret)
	}

	clientID := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_clients
		(id, account_id, device_id, device_name, platform, created_at, updated_at)
		VALUES ($1, $2, $3, $3, $4, $5, $5)`,
		clientID, accountID, "sudo-test-device", int(model.ClientPlatformIos), now); err != nil {
		t.Fatalf("seed client: %v", err)
	}
	sessionID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions
		(id, account_id, audiences, scopes, epoch, type, expired_at, created_at, updated_at, client_id, last_granted_at)
		VALUES ($1, $2, '[]', '[]', 0, 0, $3, $4, $4, $5, $4)`,
		sessionID, accountID, now.Add(time.Hour), now, clientID); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	account, err := st.GetAccountByID(ctx, uuid.MustParse(accountID))
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	session := &model.AuthSession{
		Id:            sessionID,
		AccountId:     accountID,
		Type:          model.SessionTypeLogin,
		Scopes:        []string{},
		Audiences:     []string{},
		Epoch:         0,
		ExpiredAt:     model.NewTime(now.Add(time.Hour)),
		LastGrantedAt: model.NewTime(now),
	}
	token, err := jwtSvc.CreateUserToken(session, account, now.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("create access token: %v", err)
	}
	t.Cleanup(func() { _ = rc.Cache.Remove(ctx, "accounts:"+sessionID+":sudo") })

	// A unique source IP per harness isolates the per-IP challenge quota and
	// fail2ban keys from other tests sharing this Redis instance.
	ipBytes := uuid.New()
	sudoIP := fmt.Sprintf("198.18.%d.%d", ipBytes[0], ipBytes[1])
	t.Cleanup(func() {
		_ = rc.Raw.Del(ctx,
			"auth:challenge:ip:"+sudoIP,
			"auth:fail2ban:ip:"+sudoIP,
			"auth:fail2ban:block:"+sudoIP,
			"auth:fail2ban:decline:"+sudoIP,
			"auth:fail2ban:decline-block:"+sudoIP).Err()
	})

	router := gin.New()
	authMW := middleware.Auth(middleware.AuthDeps{Token: tokenSvc, CookieDomain: cfg.Auth.CookieDomain, Log: log})
	router.POST("/auth/sudo", authMW, middleware.RequireAuth(), middleware.RequireInteractive(), h.createSudoChallenge)
	router.GET("/auth/challenge/:id/factors", authMW, h.getChallengeFactors)
	router.POST("/auth/challenge/:id/factors/:factorId", authMW, h.requestFactorCode)
	router.PATCH("/auth/challenge/:id", authMW, h.doChallenge)
	router.POST("/auth/challenge/:id/passkey/complete", authMW, h.completePasskeyChallenge)
	router.POST("/auth/token", h.exchangeToken)
	router.POST("/auth/logout", authMW, middleware.RequireAuth(), middleware.RequireInteractive(), h.logout)
	router.GET("/gated", authMW, middleware.RequireAuth(), middleware.RequireSudo(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	return &sudoHarness{
		h: h, router: router, tokenSvc: tokenSvc, rc: rc, pool: pool, cfg: cfg,
		authSvc: authSvc, ring: ring, token: token, accountID: accountID,
		sessionID: sessionID, ip: sudoIP, ctx: ctx,
	}
}

func seedSudoAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, verifiedEmail bool) string {
	t.Helper()
	accountID := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, "sudo_"+uuid.NewString()[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if verifiedEmail {
		if _, err := pool.Exec(ctx, `INSERT INTO account_contacts (id, account_id, type, content, is_primary, is_public, verified_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, true, false, $5, $5, $5)`,
			uuid.NewString(), accountID, int(model.ContactTypeEmail), "sudo@example.com", now); err != nil {
			t.Fatalf("seed contact: %v", err)
		}
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, accountID) })
	return accountID
}

func insertSudoFactor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID string, ftype model.AuthFactorType, trustworthy int, secret string) string {
	t.Helper()
	id := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO account_auth_factors (id, account_id, type, secret, config, trustworthy, enabled_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, '{}', $5, $6, $6, $6)`,
		id, accountID, int(ftype), secret, trustworthy, now); err != nil {
		t.Fatalf("seed factor: %v", err)
	}
	return id
}

func (sh *sudoHarness) factorID(t *testing.T, ftype model.AuthFactorType) string {
	t.Helper()
	var id string
	if err := sh.pool.QueryRow(sh.ctx,
		`SELECT id FROM account_auth_factors WHERE account_id = $1 AND type = $2 AND enabled_at IS NOT NULL LIMIT 1`,
		sh.accountID, int(ftype)).Scan(&id); err != nil {
		t.Fatalf("load factor %d: %v", ftype, err)
	}
	return id
}

func (sh *sudoHarness) request(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+sh.token)
	req.Header.Set("X-Forwarded-For", sh.ip)
	rec := httptest.NewRecorder()
	sh.router.ServeHTTP(rec, req)
	return rec
}

func (sh *sudoHarness) createSudo(t *testing.T) *model.AuthChallenge {
	t.Helper()
	rec := sh.request(t, http.MethodPost, "/auth/sudo", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("create sudo challenge: got %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var ch model.AuthChallenge
	if err := json.Unmarshal(rec.Body.Bytes(), &ch); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}
	return &ch
}

func (sh *sudoHarness) submitFactor(t *testing.T, challengeID, factorID, code string) *httptest.ResponseRecorder {
	t.Helper()
	return sh.request(t, http.MethodPatch, "/auth/challenge/"+challengeID, map[string]any{
		"factor_id": factorID,
		"password":  code,
	})
}

func (sh *sudoHarness) isElevated(t *testing.T) bool {
	t.Helper()
	elevated, err := sh.authSvc.IsSudoElevated(sh.ctx, sh.sessionID)
	if err != nil {
		t.Fatalf("IsSudoElevated: %v", err)
	}
	return elevated
}

func responseErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	code, _ := body["code"].(string)
	return code
}

func decodeChallenge(t *testing.T, rec *httptest.ResponseRecorder) *model.AuthChallenge {
	t.Helper()
	var ch model.AuthChallenge
	if err := json.Unmarshal(rec.Body.Bytes(), &ch); err != nil {
		t.Fatalf("decode challenge %q: %v", rec.Body.String(), err)
	}
	return &ch
}

// TestSudoPasswordOnlyCannotElevate pins (a): a password-only account's elevation
// demand is 2, password alone drops it by 1, and no elevation is granted. The emailed
// fallback is advertised because the account has a verified email.
func TestSudoPasswordOnlyCannotElevate(t *testing.T) {
	sh := newSudoHarness(t, sudoSeed{
		verifiedEmail: true,
		factors:       []sudoFactorSeed{sudoPasswordSeed(t, model.AuthFactorTypePassword, sudoTestPassword, 1)},
	})
	ch := sh.createSudo(t)
	if ch.Purpose != model.AuthChallengePurposeSudo {
		t.Fatalf("purpose = %q, want sudo", ch.Purpose)
	}
	if ch.StepTotal != 2 || ch.StepRemain != 2 {
		t.Fatalf("step %d/%d, want 2/2", ch.StepRemain, ch.StepTotal)
	}

	// The synthetic fallback factor (id == challenge id) is advertised.
	rec := sh.request(t, http.MethodGet, "/auth/challenge/"+ch.Id+"/factors", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("factors: %d %s", rec.Code, rec.Body.String())
	}
	var factors []model.AuthFactor
	if err := json.Unmarshal(rec.Body.Bytes(), &factors); err != nil {
		t.Fatalf("decode factors: %v", err)
	}
	synthetic := false
	for _, f := range factors {
		if f.Id == ch.Id {
			synthetic = true
		}
	}
	if !synthetic {
		t.Fatalf("emailed fallback factor not advertised: %+v", factors)
	}

	rec = sh.submitFactor(t, ch.Id, sh.factorID(t, model.AuthFactorTypePassword), sudoTestPassword)
	if rec.Code != http.StatusOK {
		t.Fatalf("password step: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeChallenge(t, rec); got.StepRemain != 1 {
		t.Fatalf("step_remain = %d, want 1", got.StepRemain)
	}
	if sh.isElevated(t) {
		t.Fatal("password-only account was elevated by password alone")
	}
}

// TestSudoPasswordAndTotpElevates pins (b): a password+TOTP account satisfies the
// demand in two steps and is granted elevation; no login session is minted.
func TestSudoPasswordAndTotpElevates(t *testing.T) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "SolarNetwork", AccountName: "sudo-test"})
	if err != nil {
		t.Fatalf("generate totp: %v", err)
	}
	totpSecret := key.Secret()
	sh := newSudoHarness(t, sudoSeed{factors: []sudoFactorSeed{
		sudoPasswordSeed(t, model.AuthFactorTypePassword, sudoTestPassword, 1),
		{ftype: model.AuthFactorTypeTimedCode, trustworthy: 3, secret: totpSecret},
	}})
	ch := sh.createSudo(t)
	if ch.StepTotal != 2 {
		t.Fatalf("step_total = %d, want 2", ch.StepTotal)
	}
	if rec := sh.submitFactor(t, ch.Id, sh.factorID(t, model.AuthFactorTypePassword), sudoTestPassword); rec.Code != http.StatusOK {
		t.Fatalf("password step: %d %s", rec.Code, rec.Body.String())
	}
	code, err := totp.GenerateCode(totpSecret, time.Now())
	if err != nil {
		t.Fatalf("generate totp code: %v", err)
	}
	rec := sh.submitFactor(t, ch.Id, sh.factorID(t, model.AuthFactorTypeTimedCode), code)
	if rec.Code != http.StatusOK {
		t.Fatalf("totp step: %d %s", rec.Code, rec.Body.String())
	}
	got := decodeChallenge(t, rec)
	if got.StepRemain != 0 {
		t.Fatalf("step_remain = %d, want 0", got.StepRemain)
	}
	if got.SudoUntil == nil {
		t.Fatal("sudo_until not returned after elevation")
	}
	if !sh.isElevated(t) {
		t.Fatal("password+TOTP account was not elevated")
	}
}

// TestSudoRejectsPinAndRecoveryCode pins (d): PIN and RecoveryCode factors are
// untrustworthy and cannot satisfy an elevation step.
func TestSudoRejectsPinAndRecoveryCode(t *testing.T) {
	key, _ := totp.Generate(totp.GenerateOpts{Issuer: "SolarNetwork", AccountName: "sudo-test"})
	sh := newSudoHarness(t, sudoSeed{factors: []sudoFactorSeed{
		sudoPasswordSeed(t, model.AuthFactorTypePassword, sudoTestPassword, 1),
		{ftype: model.AuthFactorTypeTimedCode, trustworthy: 3, secret: key.Secret()},
		sudoPasswordSeed(t, model.AuthFactorTypePinCode, "9999", 0),
		{ftype: model.AuthFactorTypeRecoveryCode, trustworthy: 0, secret: "RECOVERY-CODE"},
	}})
	ch := sh.createSudo(t)

	for _, tc := range []struct {
		name  string
		ftype model.AuthFactorType
		code  string
	}{
		{"pin", model.AuthFactorTypePinCode, "9999"},
		{"recovery", model.AuthFactorTypeRecoveryCode, "RECOVERY-CODE"},
	} {
		rec := sh.submitFactor(t, ch.Id, sh.factorID(t, tc.ftype), tc.code)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: got %d, want 400 (body %s)", tc.name, rec.Code, rec.Body.String())
		}
		if code := responseErrorCode(t, rec); code != "AUTH_FACTOR_NOT_TRUSTWORTHY" {
			t.Fatalf("%s: code = %q, want AUTH_FACTOR_NOT_TRUSTWORTHY", tc.name, code)
		}
	}
	if sh.isElevated(t) {
		t.Fatal("PIN/RecoveryCode elevated the session")
	}
}

// TestSudoNoFactorFailsClosed pins (e): an account that cannot reach the
// elevation demand (no factor, or password without a verified email) cannot even
// start an elevation.
func TestSudoNoFactorFailsClosed(t *testing.T) {
	t.Run("no factors and no email", func(t *testing.T) {
		sh := newSudoHarness(t, sudoSeed{})
		rec := sh.request(t, http.MethodPost, "/auth/sudo", nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("got %d, want 403 (body %s)", rec.Code, rec.Body.String())
		}
		if code := responseErrorCode(t, rec); code != "AUTH_NO_AUTH_FACTORS" {
			t.Fatalf("code = %q, want AUTH_NO_AUTH_FACTORS", code)
		}
	})

	t.Run("password only without an email contact", func(t *testing.T) {
		sh := newSudoHarness(t, sudoSeed{factors: []sudoFactorSeed{
			sudoPasswordSeed(t, model.AuthFactorTypePassword, sudoTestPassword, 1),
		}})
		rec := sh.request(t, http.MethodPost, "/auth/sudo", nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("got %d, want 403 (body %s)", rec.Code, rec.Body.String())
		}
		if code := responseErrorCode(t, rec); code != "AUTH_NO_AUTH_FACTORS" {
			t.Fatalf("code = %q, want AUTH_NO_AUTH_FACTORS", code)
		}
	})
}

// TestSudoEmailFallbackEndToEnd pins (f): the synthetic emailed fallback sends
// a code through the spell service, stores it in Redis, decrements by 2 and
// grants elevation; a resend while a live code exists is refused.
func TestSudoEmailFallbackEndToEnd(t *testing.T) {
	sh := newSudoHarness(t, sudoSeed{
		verifiedEmail: true,
		factors:       []sudoFactorSeed{sudoPasswordSeed(t, model.AuthFactorTypePassword, sudoTestPassword, 1)},
	})
	ch := sh.createSudo(t)

	// Requesting the fallback code emails it through the fake Ring.
	rec := sh.request(t, http.MethodPost, "/auth/challenge/"+ch.Id+"/factors/"+ch.Id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("request fallback code: %d %s", rec.Code, rec.Body.String())
	}
	if len(sh.ring.emails) != 1 {
		t.Fatalf("emails sent = %d, want 1", len(sh.ring.emails))
	}
	var code string
	found, err := sh.rc.Cache.Get(sh.ctx, "authsudo:"+ch.Id+":code", &code)
	if err != nil || !found || len(code) != 6 {
		t.Fatalf("fallback code not stored (found=%v err=%v code=%q)", found, err, code)
	}

	// A live code refuses a resend.
	rec = sh.request(t, http.MethodPost, "/auth/challenge/"+ch.Id+"/factors/"+ch.Id, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("resend: got %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if ec := responseErrorCode(t, rec); ec != "AUTH_FACTOR_SEND_FAILED" {
		t.Fatalf("resend code = %q, want AUTH_FACTOR_SEND_FAILED", ec)
	}

	// Verifying the code completes the elevation in one step (weight 2).
	rec = sh.submitFactor(t, ch.Id, ch.Id, code)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify fallback: %d %s", rec.Code, rec.Body.String())
	}
	got := decodeChallenge(t, rec)
	if got.StepRemain != 0 || got.SudoUntil == nil {
		t.Fatalf("fallback did not complete the elevation: %+v", got)
	}
	if !sh.isElevated(t) {
		t.Fatal("email fallback did not elevate the session")
	}
	if found, _ := sh.rc.Cache.Get(sh.ctx, "authsudo:"+ch.Id+":code", &code); found {
		t.Fatal("fallback code was not consumed")
	}
}

// TestSudoGatedRouteRequiresElevation pins (g): a gated route answers 403
// AUTH_SUDO_REQUIRED (with the factor hint) until the session is elevated.
func TestSudoGatedRouteRequiresElevation(t *testing.T) {
	key, _ := totp.Generate(totp.GenerateOpts{Issuer: "SolarNetwork", AccountName: "sudo-test"})
	sh := newSudoHarness(t, sudoSeed{factors: []sudoFactorSeed{
		sudoPasswordSeed(t, model.AuthFactorTypePassword, sudoTestPassword, 1),
		{ftype: model.AuthFactorTypeTimedCode, trustworthy: 3, secret: key.Secret()},
	}})

	rec := sh.request(t, http.MethodGet, "/gated", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("gated (cold): got %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["code"] != "AUTH_SUDO_REQUIRED" {
		t.Fatalf("code = %v, want AUTH_SUDO_REQUIRED", body["code"])
	}
	if body["detail"] != "password,timed_code" {
		t.Fatalf("detail = %v, want password,timed_code", body["detail"])
	}

	if _, err := sh.authSvc.GrantSudo(sh.ctx, sh.sessionID); err != nil {
		t.Fatalf("GrantSudo: %v", err)
	}
	rec = sh.request(t, http.MethodGet, "/gated", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("gated (elevated): got %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestSudoChallengeRejectedByTokenExchange pins (h): an elevation challenge
// can never be exchanged into a login session.
func TestSudoChallengeRejectedByTokenExchange(t *testing.T) {
	sh := newSudoHarness(t, sudoSeed{factors: []sudoFactorSeed{
		sudoPasswordSeed(t, model.AuthFactorTypePassword, sudoTestPassword, 1),
		{ftype: model.AuthFactorTypeTimedCode, trustworthy: 3, secret: "JBSWY3DPEHPK3PXP"},
	}})
	ch := sh.createSudo(t)
	rec := sh.request(t, http.MethodPost, "/auth/token", map[string]any{
		"grant_type": "authorization_code",
		"code":       ch.Id,
	})
	if rec.Code == http.StatusOK {
		t.Fatalf("token exchange accepted an elevation challenge: %s", rec.Body.String())
	}
	if code := responseErrorCode(t, rec); code != "AUTH_INVALID_CODE" {
		t.Fatalf("code = %q, want AUTH_INVALID_CODE", code)
	}
}

// TestSudoElevationClearedByRevoke pins (i): revoking the session drops its
// elevation grant.
func TestSudoElevationClearedByRevoke(t *testing.T) {
	sh := newSudoHarness(t, sudoSeed{factors: []sudoFactorSeed{
		sudoPasswordSeed(t, model.AuthFactorTypePassword, sudoTestPassword, 1),
		{ftype: model.AuthFactorTypeTimedCode, trustworthy: 3, secret: "JBSWY3DPEHPK3PXP"},
	}})
	if _, err := sh.authSvc.GrantSudo(sh.ctx, sh.sessionID); err != nil {
		t.Fatalf("GrantSudo: %v", err)
	}
	if !sh.isElevated(t) {
		t.Fatal("grant did not elevate")
	}
	if _, err := sh.authSvc.RevokeSession(sh.ctx, uuid.MustParse(sh.sessionID)); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if sh.isElevated(t) {
		t.Fatal("revoke did not clear the elevation grant")
	}
}

// TestSudoRedisUnavailableFailsClosed pins (j): with no elevation store the
// gated route answers 503 and elevation challenge creation is refused.
func TestSudoRedisUnavailableFailsClosed(t *testing.T) {
	sh := newSudoHarness(t, sudoSeed{factors: []sudoFactorSeed{
		sudoPasswordSeed(t, model.AuthFactorTypePassword, sudoTestPassword, 1),
	}})

	// A checker whose Redis is unavailable fails the gated route closed.
	middleware.SetSudoChecker(auth.NewAuthService(sh.h.d.Store, nil, sh.cfg, nil, nil, nil, nil, nil, sh.h.d.Log))
	rec := sh.request(t, http.MethodGet, "/gated", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("gated without redis: got %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}
	if code := responseErrorCode(t, rec); code != "SERVICE_UNAVAILABLE" {
		t.Fatalf("code = %q, want SERVICE_UNAVAILABLE", code)
	}

	// Elevation challenge creation needs the cache too.
	noCache := &handler{d: Deps{Store: sh.h.d.Store, Cfg: sh.cfg, Auth: sh.authSvc, Geo: &geo.Service{}, Log: sh.h.d.Log}}
	router := gin.New()
	router.POST("/auth/sudo", noCache.createSudoChallenge)
	req := httptest.NewRequest(http.MethodPost, "/auth/sudo", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("create sudo without redis: got %d, want 503 (body %s)", w.Code, w.Body.String())
	}
}

// TestSudoPasskeyOnlyElevatesInOneCeremony pins (c): a passkey-only account's
// elevation demand is 2 and one passkey assertion (weight 4) completes it.
func TestSudoPasskeyOnlyElevatesInOneCeremony(t *testing.T) {
	sh := newSudoHarness(t, sudoSeed{factors: []sudoFactorSeed{
		{ftype: model.AuthFactorTypePasskey, trustworthy: 4},
	}})
	ch := sh.createSudo(t)
	if ch.StepTotal != 2 || ch.StepRemain != 2 {
		t.Fatalf("step %d/%d, want 2/2", ch.StepRemain, ch.StepTotal)
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	x := make([]byte, 32)
	y := make([]byte, 32)
	priv.PublicKey.X.FillBytes(x)
	priv.PublicKey.Y.FillBytes(y)
	credentialID := base64.StdEncoding.EncodeToString([]byte("sudo-passkey-credential"))
	credentialJSON, err := json.Marshal(model.PasskeyCredential{
		CredentialId: credentialID,
		PublicKeyX:   x,
		PublicKeyY:   y,
	})
	if err != nil {
		t.Fatalf("marshal credential: %v", err)
	}
	now := time.Now().UTC()
	if _, err := sh.pool.Exec(sh.ctx, `INSERT INTO account_passkeys
		(id, account_id, created_at, updated_at, credential, credential_id, label)
		VALUES ($1, $2, $3, $3, $4, $5, $6)`,
		uuid.NewString(), sh.accountID, now, string(credentialJSON), credentialID, "sudo-test"); err != nil {
		t.Fatalf("seed passkey: %v", err)
	}

	assertionChallenge, err := sh.h.generatePasskeyAssertionChallenge(sh.ctx, ch.Id)
	if err != nil {
		t.Fatalf("assertion challenge: %v", err)
	}
	authData := make([]byte, 37)
	authData[32] = 0x01 // UserPresent
	clientDataJSON := fmt.Sprintf(`{"type":"webauthn.get","challenge":"%s","origin":"http://localhost"}`, assertionChallenge)
	cd, ad, sig := signAssertion(t, priv, authData, clientDataJSON, false)

	rec := sh.request(t, http.MethodPost, "/auth/challenge/"+ch.Id+"/passkey/complete", map[string]any{
		"credential_id":      credentialID,
		"client_data_json":   cd,
		"authenticator_data": ad,
		"signature":          sig,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("passkey complete: got %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	got := decodeChallenge(t, rec)
	if got.StepRemain != 0 {
		t.Fatalf("step_remain = %d, want 0", got.StepRemain)
	}
	if !sh.isElevated(t) {
		t.Fatal("passkey ceremony did not elevate the session")
	}
}
