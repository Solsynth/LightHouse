package authctl

// Regression tests for the Stargate disclosure fixes: the fail2ban/quota gates
// run before the live-challenge reuse branch, credential submission is
// throttled, the factor surface is bound to the challenge's caller, and the
// password-reset request writes a spell into the same store spellctl reads.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/geo"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/spell"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// throttleHarness is the smoke-DB/Redis harness for the disclosure-fix tests.
// The config opts out of captcha by default so ValidateCaptcha passes; tests
// that need it unconfigured flip AllowDisabled themselves.
type throttleHarness struct {
	h    *handler
	rc   *redis.Client
	cfg  *config.Config
	ring *fakeRing
	pool *pgxpool.Pool
	ctx  context.Context
}

func newThrottleHarness(t *testing.T, mutate func(*config.Config)) *throttleHarness {
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

	cfg := config.Default()
	cfg.Captcha.AllowDisabled = true
	if mutate != nil {
		mutate(cfg)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.New(pool)
	ring := &fakeRing{}
	return &throttleHarness{
		h: &handler{d: Deps{
			Store:  st,
			Redis:  rc,
			Cfg:    cfg,
			Geo:    &geo.Service{},
			Auth:   auth.NewAuthService(st, rc, cfg, geo.NewService(""), nil, nil, nil, nil, log),
			Spells: spell.NewService(st, rc, ring, cfg.SiteUrl, cfg, log),
			Log:    log,
		}},
		rc:   rc,
		cfg:  cfg,
		ring: ring,
		pool: pool,
		ctx:  ctx,
	}
}

// TestCreateChallengeRateLimitsBeforeReuse pins the reordered gates: an IP
// that is fail2banned must not answer 200 by handing back the live challenge it
// already holds (the reuse branch used to run before risk.IPAllowed).
func TestCreateChallengeRateLimitsBeforeReuse(t *testing.T) {
	hr := newThrottleHarness(t, nil)
	accountID := seedRiskAccount(t, hr.ctx, hr.pool, model.AuthFactorTypePassword)
	account, err := hr.h.d.Store.GetAccountByID(hr.ctx, uuid.MustParse(accountID))
	if err != nil {
		t.Fatalf("load account: %v", err)
	}

	const ip = "203.0.113.77"
	blockKey := "auth:fail2ban:block:" + ip
	t.Cleanup(func() {
		_ = hr.rc.Raw.Del(hr.ctx, blockKey, "auth:challenge:ip:"+ip).Err()
	})

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/auth/challenge", hr.h.createChallenge)

	post := func() *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{
			"account":     account.Name,
			"device_id":   "reuse-block-device",
			"device_name": "Reuse Block Test",
			"platform":    int(model.ClientPlatformIos),
		})
		req := httptest.NewRequest(http.MethodPost, "/auth/challenge", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", ip)
		req.Header.Set("User-Agent", "reuse-block-agent/1.0")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("first challenge status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	// The live challenge now exists for this IP+UA+device. Block the IP and
	// replay the exact request: reuse must be refused, not served.
	if err := hr.rc.Raw.Set(hr.ctx, blockKey, "1", time.Minute).Err(); err != nil {
		t.Fatalf("set fail2ban block: %v", err)
	}
	rec := post()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked reuse status = %d, want 429 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"code":"RATE_LIMITED"`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

// TestDoChallengeThrottlesCredentialGuessing pins that a challenge cannot be
// ground forever: the per-challenge attempt cap and the fail2ban IP block are
// both consulted before a submitted credential is verified.
func TestDoChallengeThrottlesCredentialGuessing(t *testing.T) {
	hr := newThrottleHarness(t, func(c *config.Config) { c.Security.MaxChallengeAttempts = 3 })

	newChallenge := func(t *testing.T, accountID, ip string, failedAttempts int) *model.AuthChallenge {
		t.Helper()
		now := time.Now().UTC()
		challenge := &model.AuthChallenge{
			Id:               uuid.NewString(),
			AccountId:        accountID,
			DeviceId:         "throttle-device",
			StepTotal:        1,
			StepRemain:       1,
			BlacklistFactors: []string{},
			Audiences:        []string{},
			Scopes:           []string{},
			IpAddress:        &ip,
			ExpiredAt:        model.NewTime(now.Add(10 * time.Minute)),
			CreatedAt:        model.NewTime(now),
			UpdatedAt:        model.NewTime(now),
			FailedAttempts:   failedAttempts,
		}
		if err := hr.h.d.Store.CreateAuthChallenge(hr.ctx, challenge); err != nil {
			t.Fatalf("create challenge: %v", err)
		}
		t.Cleanup(func() { _, _ = hr.pool.Exec(hr.ctx, `DELETE FROM auth_challenges WHERE id = $1`, challenge.Id) })
		return challenge
	}

	patch := func(t *testing.T, challengeID, factorID, ip string) *httptest.ResponseRecorder {
		t.Helper()
		router := gin.New()
		router.PATCH("/auth/challenge/:id", hr.h.doChallenge)
		body, _ := json.Marshal(map[string]any{"factor_id": factorID, "password": "not-the-password"})
		req := httptest.NewRequest(http.MethodPatch, "/auth/challenge/"+challengeID, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", ip)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	t.Run("per-challenge cap refuses further guesses", func(t *testing.T) {
		accountID, factorID := seedFactorAccount(t, hr.ctx, hr.pool, model.AuthFactorTypePassword, false)
		challenge := newChallenge(t, accountID, "", hr.cfg.Security.MaxChallengeAttempts)
		rec := patch(t, challenge.Id, factorID, "")
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429 (body %s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"code":"RATE_LIMITED"`) {
			t.Fatalf("unexpected body: %s", rec.Body.String())
		}
	})

	t.Run("fail2ban block refuses further guesses", func(t *testing.T) {
		accountID, factorID := seedFactorAccount(t, hr.ctx, hr.pool, model.AuthFactorTypePassword, false)
		const ip = "198.51.100.44"
		blockKey := "auth:fail2ban:block:" + ip
		t.Cleanup(func() { _ = hr.rc.Raw.Del(hr.ctx, blockKey).Err() })
		challenge := newChallenge(t, accountID, ip, 0)
		if err := hr.rc.Raw.Set(hr.ctx, blockKey, "1", time.Minute).Err(); err != nil {
			t.Fatalf("set fail2ban block: %v", err)
		}
		rec := patch(t, challenge.Id, factorID, ip)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429 (body %s)", rec.Code, rec.Body.String())
		}
	})
}

// TestGetChallengeFactorsBoundToCaller pins that the factor surface is only
// served to the caller that created the challenge (same derived client IP and
// user agent). A foreign or partially recorded challenge is a 404, exactly like
// a missing one — never a wildcard match.
func TestGetChallengeFactorsBoundToCaller(t *testing.T) {
	hr := newThrottleHarness(t, nil)
	accountID, _ := seedFactorAccount(t, hr.ctx, hr.pool, model.AuthFactorTypePassword, true)

	ip := "203.0.113.201"
	ua := "binding-agent/1.0"
	now := time.Now().UTC()
	challenge := &model.AuthChallenge{
		Id:               uuid.NewString(),
		AccountId:        accountID,
		DeviceId:         "binding-device",
		StepTotal:        1,
		StepRemain:       1,
		BlacklistFactors: []string{},
		Audiences:        []string{},
		Scopes:           []string{},
		IpAddress:        &ip,
		UserAgent:        new(ua),
		ExpiredAt:        model.NewTime(now.Add(10 * time.Minute)),
		CreatedAt:        model.NewTime(now),
		UpdatedAt:        model.NewTime(now),
	}
	if err := hr.h.d.Store.CreateAuthChallenge(hr.ctx, challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	t.Cleanup(func() { _, _ = hr.pool.Exec(hr.ctx, `DELETE FROM auth_challenges WHERE id = $1`, challenge.Id) })

	// A challenge with no recorded address/agent must never match anything.
	bareIP := ""
	bare := &model.AuthChallenge{
		Id:               uuid.NewString(),
		AccountId:        accountID,
		DeviceId:         "binding-device-bare",
		StepTotal:        1,
		StepRemain:       1,
		BlacklistFactors: []string{},
		Audiences:        []string{},
		Scopes:           []string{},
		IpAddress:        &bareIP,
		ExpiredAt:        model.NewTime(now.Add(10 * time.Minute)),
		CreatedAt:        model.NewTime(now),
		UpdatedAt:        model.NewTime(now),
	}
	if err := hr.h.d.Store.CreateAuthChallenge(hr.ctx, bare); err != nil {
		t.Fatalf("create bare challenge: %v", err)
	}
	t.Cleanup(func() { _, _ = hr.pool.Exec(hr.ctx, `DELETE FROM auth_challenges WHERE id = $1`, bare.Id) })

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/auth/challenge/:id/factors", hr.h.getChallengeFactors)

	get := func(t *testing.T, id, fromIP, agent string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/auth/challenge/"+id+"/factors", nil)
		req.Header.Set("X-Forwarded-For", fromIP)
		req.Header.Set("User-Agent", agent)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	t.Run("own challenge with matching IP and agent", func(t *testing.T) {
		rec := get(t, challenge.Id, ip, ua)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		var factors []map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &factors); err != nil {
			t.Fatalf("decode factors: %v", err)
		}
		if len(factors) == 0 {
			t.Fatalf("factor list is empty: %s", body)
		}
		// Only the picker's fields: no factor secret, no account contacts.
		if strings.Contains(body, "secret") || strings.Contains(body, "factor@example.com") {
			t.Fatalf("factor surface leaks sensitive fields: %s", body)
		}
	})

	t.Run("same challenge from another IP", func(t *testing.T) {
		rec := get(t, challenge.Id, "198.51.100.9", ua)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("same challenge from another user agent", func(t *testing.T) {
		rec := get(t, challenge.Id, ip, "other-agent/2.0")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("challenge without recorded IP or agent never matches", func(t *testing.T) {
		rec := get(t, bare.Id, "", "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("unknown challenge", func(t *testing.T) {
		rec := get(t, uuid.NewString(), ip, ua)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
		}
	})
}

// postPasswordReset drives POST /api/accounts/recovery/password.
func postPasswordReset(t *testing.T, hr *throttleHarness, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/accounts/recovery/password", hr.h.requestPasswordRecovery)
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/accounts/recovery/password", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// TestRequestPasswordResetDeliversSpellEndToEnd pins finding #22: the request
// endpoint must create the reset spell in the store spellctl reads (so
// GET /spells/{word} + apply work), email the /spells/<word> link, and answer
// with the C# contract.
func TestRequestPasswordResetDeliversSpellEndToEnd(t *testing.T) {
	hr := newThrottleHarness(t, nil)
	accountID, _ := seedFactorAccount(t, hr.ctx, hr.pool, model.AuthFactorTypePassword, true)
	account, err := hr.h.d.Store.GetAccountByID(hr.ctx, uuid.MustParse(accountID))
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	t.Cleanup(func() { _, _ = hr.pool.Exec(hr.ctx, `DELETE FROM magic_spells WHERE account_id = $1`, accountID) })

	rec := postPasswordReset(t, hr, map[string]any{"account": account.Name, "captcha_token": "anything"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	// The spell exists in the same store GET /spells/:word reads.
	created, err := hr.h.d.Store.FindLiveMagicSpell(hr.ctx, accountID, model.MagicSpellTypeAuthPasswordReset)
	if err != nil {
		t.Fatalf("reset spell was not created: %v", err)
	}
	if created.ExpiresAt == nil || !created.ExpiresAt.Time().After(time.Now().UTC().Add(23*time.Hour)) {
		t.Fatalf("reset spell expiry = %v, want ~24h", created.ExpiresAt)
	}
	byWord, err := hr.h.d.Store.GetMagicSpellByWord(hr.ctx, created.Spell)
	if err != nil {
		t.Fatalf("spell is not readable by word: %v", err)
	}
	if byWord.Id != created.Id {
		t.Fatalf("by-word lookup returned %s, want %s", byWord.Id, created.Id)
	}

	// The email carries the /spells/<word> link the client follows.
	if len(hr.ring.emails) != 1 {
		t.Fatalf("emails sent = %d, want 1", len(hr.ring.emails))
	}
	email := hr.ring.emails[0]
	if email.ToAddress != "factor@example.com" {
		t.Fatalf("email recipient = %q, want factor@example.com", email.ToAddress)
	}
	if !strings.Contains(email.Body, "/spells/"+created.Spell) {
		t.Fatalf("email body does not link the reset spell:\n%s", email.Body)
	}
}

// TestRequestPasswordResetRejectsUnknownAccountAndNoContact pins the C#
// response parity for the two 400 cases.
func TestRequestPasswordResetRejectsUnknownAccountAndNoContact(t *testing.T) {
	hr := newThrottleHarness(t, nil)

	rec := postPasswordReset(t, hr, map[string]any{"account": "no_such_account_" + uuid.NewString(), "captcha_token": "anything"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown account status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "PASSPORT_ACCOUNT_NOT_FOUND") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}

	accountID, _ := seedFactorAccount(t, hr.ctx, hr.pool, model.AuthFactorTypePassword, false)
	account, err := hr.h.d.Store.GetAccountByID(hr.ctx, uuid.MustParse(accountID))
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	rec = postPasswordReset(t, hr, map[string]any{"account": account.Name, "captcha_token": "anything"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no-contact status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "PASSPORT_ACCOUNT_NO_CONTACT_METHOD") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

// TestRequestPasswordResetFailsClosedWithoutCaptcha pins item 4 at the new
// endpoint: no explicit opt-out and no verifier means the request is rejected
// (400 VALIDATION_ERROR), never served unchecked.
func TestRequestPasswordResetFailsClosedWithoutCaptcha(t *testing.T) {
	hr := newThrottleHarness(t, func(c *config.Config) { c.Captcha.AllowDisabled = false })
	accountID, _ := seedFactorAccount(t, hr.ctx, hr.pool, model.AuthFactorTypePassword, true)
	account, err := hr.h.d.Store.GetAccountByID(hr.ctx, uuid.MustParse(accountID))
	if err != nil {
		t.Fatalf("load account: %v", err)
	}

	rec := postPasswordReset(t, hr, map[string]any{"account": account.Name, "captcha_token": "anything"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "VALIDATION_ERROR") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
	if _, err := hr.h.d.Store.FindLiveMagicSpell(hr.ctx, accountID, model.MagicSpellTypeAuthPasswordReset); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a reset spell was created for a rejected request: %v", err)
	}
}

// TestRequestPasswordResetRollsBackFailedDelivery pins the delivery contract:
// a lost email must not look like success, so a spell this request created is
// deleted and the caller gets a 5xx. An already-live spell is left alone.
func TestRequestPasswordResetRollsBackFailedDelivery(t *testing.T) {
	t.Run("new spell is rolled back", func(t *testing.T) {
		hr := newThrottleHarness(t, nil)
		hr.ring.failEmail = errors.New("ring down")
		accountID, _ := seedFactorAccount(t, hr.ctx, hr.pool, model.AuthFactorTypePassword, true)
		account, err := hr.h.d.Store.GetAccountByID(hr.ctx, uuid.MustParse(accountID))
		if err != nil {
			t.Fatalf("load account: %v", err)
		}

		rec := postPasswordReset(t, hr, map[string]any{"account": account.Name, "captcha_token": "anything"})
		if rec.Code < 500 {
			t.Fatalf("status = %d, want 5xx on delivery failure (body %s)", rec.Code, rec.Body.String())
		}
		if _, err := hr.h.d.Store.FindLiveMagicSpell(hr.ctx, accountID, model.MagicSpellTypeAuthPasswordReset); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("unsent reset spell was kept: %v", err)
		}
	})

	t.Run("existing live spell survives a failed resend", func(t *testing.T) {
		hr := newThrottleHarness(t, nil)
		accountID, _ := seedFactorAccount(t, hr.ctx, hr.pool, model.AuthFactorTypePassword, true)
		account, err := hr.h.d.Store.GetAccountByID(hr.ctx, uuid.MustParse(accountID))
		if err != nil {
			t.Fatalf("load account: %v", err)
		}
		t.Cleanup(func() { _, _ = hr.pool.Exec(hr.ctx, `DELETE FROM magic_spells WHERE account_id = $1`, accountID) })

		expiresAt := time.Now().UTC().Add(24 * time.Hour)
		prior, err := hr.h.d.Spells.CreateMagicSpell(hr.ctx, accountID, model.MagicSpellTypeAuthPasswordReset,
			map[string]any{}, spell.CreateOptions{ExpiresAt: &expiresAt})
		if err != nil {
			t.Fatalf("seed live spell: %v", err)
		}
		hr.ring.failEmail = errors.New("ring down")

		rec := postPasswordReset(t, hr, map[string]any{"account": account.Name, "captcha_token": "anything"})
		if rec.Code < 500 {
			t.Fatalf("status = %d, want 5xx on delivery failure (body %s)", rec.Code, rec.Body.String())
		}
		if _, err := hr.h.d.Store.GetMagicSpellByWord(hr.ctx, prior.Spell); err != nil {
			t.Fatalf("pre-existing reset spell was deleted: %v", err)
		}
	})
}
