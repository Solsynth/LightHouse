package authctl

// Pins the password-first login policy:
//
//   - only the password can be submitted, whatever else the account has;
//   - the risk model (including lockdown) still sets the step total, and the
//     steps after the password wait for an in-app approval rather than an
//     emailed code;
//   - repeated failures never add factors;
//   - the post-password review prompt survives the exchange that expires the
//     challenge, and denying it signs out the session the login minted.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/auth"
	"src.solsynth.dev/sosys/stargate/config"
	"src.solsynth.dev/sosys/stargate/geo"
	"src.solsynth.dev/sosys/stargate/grpcclient"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/store"
)

// TestRevokeSessionMintedByChallengeSignsOutSession covers the effect the
// decline handler relies on without the Redis-backed HTTP harness: the session
// bound to a satisfied challenge is expired and its epoch rotated, and the
// trail records the denial.
func TestRevokeSessionMintedByChallengeSignsOutSession(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), smokeDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}

	st := store.New(pool)
	h := &handler{d: Deps{
		Store: st,
		Auth:  auth.NewAuthService(st, nil, &config.Config{}, &geo.Service{}, nil, nil, nil, nil, nil),
	}}
	accountID := seedRiskAccount(t, ctx, pool, model.AuthFactorTypePassword)
	now := time.Now().UTC()
	ch := &model.AuthChallenge{
		Id:               uuid.NewString(),
		AccountId:        accountID,
		DeviceId:         "revoke-test-device",
		StepTotal:        1,
		StepRemain:       0,
		BlacklistFactors: []string{},
		Audiences:        []string{},
		Scopes:           []string{},
		Origin:           model.ChallengeOriginPassword,
		ExpiredAt:        model.NewTime(now),
		ReviewUntil:      model.NewTime(now.Add(5 * time.Minute)),
		CreatedAt:        model.NewTime(now),
		UpdatedAt:        model.NewTime(now),
	}
	if err := st.CreateAuthChallenge(ctx, ch); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	clientID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_clients
		(id, account_id, device_id, device_name, platform, created_at, updated_at)
		VALUES ($1, $2, $3, $3, 0, $4, $4)`,
		clientID, accountID, "revoke-test-device", now); err != nil {
		t.Fatalf("seed client: %v", err)
	}
	sessionID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions
		(id, account_id, audiences, scopes, epoch, type, expired_at, created_at, updated_at, challenge_id, client_id)
		VALUES ($1, $2, '[]', '[]', 0, 0, $3, $4, $4, $5, $6)`,
		sessionID, accountID, now.Add(time.Hour), now, ch.Id, clientID); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM auth_sessions WHERE id = $1`, sessionID)
		_, _ = pool.Exec(ctx, `DELETE FROM auth_clients WHERE id = $1`, clientID)
		_, _ = pool.Exec(ctx, `DELETE FROM auth_challenges WHERE id = $1`, ch.Id)
	})

	bound, err := st.FindSessionIDByChallenge(ctx, ch.Id)
	if err != nil || bound != sessionID {
		t.Fatalf("FindSessionIDByChallenge = %q, %v; want %q", bound, err, sessionID)
	}
	pending, err := st.ListPendingChallenges(ctx, accountID)
	if err != nil {
		t.Fatalf("list pending challenges: %v", err)
	}
	listed := false
	for _, item := range pending {
		if item.Id == ch.Id {
			listed = true
		}
	}
	if !listed {
		t.Fatalf("satisfied challenge %s is missing from the review list: %+v", ch.Id, pending)
	}
	h.revokeSessionMintedByChallenge(ctx, ch, "reviewer-session", "203.0.113.5", "reviewer-agent")

	var epoch int
	var expiredAt time.Time
	if err := pool.QueryRow(ctx, `SELECT epoch, expired_at FROM auth_sessions WHERE id = $1`, sessionID).
		Scan(&epoch, &expiredAt); err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if epoch != 1 {
		t.Fatalf("session epoch = %d, want 1 (revoked)", epoch)
	}
	if expiredAt.After(time.Now().UTC().Add(time.Minute)) {
		t.Fatalf("session expired_at = %s, want it signed out now", expiredAt)
	}

	reloaded, err := st.GetAuthChallenge(ctx, uuid.MustParse(ch.Id))
	if err != nil {
		t.Fatalf("reload challenge: %v", err)
	}
	found := false
	for _, event := range reloaded.Trail {
		if event.Type == model.ChallengeEventSessionRevoked && event.SessionId == sessionID {
			found = true
		}
	}
	if !found {
		t.Fatalf("challenge trail has no session_revoked event: %+v", reloaded.Trail)
	}
}

func TestChallengeFactorOptionsPasswordFirst(t *testing.T) {
	now := model.NewTime(time.Now().UTC())
	factors := []model.AuthFactor{
		{Id: "pw", Type: model.AuthFactorTypePassword, Trustworthy: 1, EnabledAt: now},
		{Id: "email", Type: model.AuthFactorTypeEmailCode, Trustworthy: 2, EnabledAt: now},
		{Id: "totp", Type: model.AuthFactorTypeTimedCode, Trustworthy: 2, EnabledAt: now},
		{Id: "disabled", Type: model.AuthFactorTypeNfcToken, Trustworthy: 1},
	}

	got := challengeFactorOptions(factors, true, 1)
	if len(got) != 1 || got[0].Id != "pw" {
		t.Fatalf("password-first options = %+v, want only the password", got)
	}

	passwordless := challengeFactorOptions(factors, false, 1)
	ids := map[string]bool{}
	for _, f := range passwordless {
		ids[f.Id] = true
	}
	if !ids["pw"] || !ids["email"] || !ids["totp"] {
		t.Fatalf("passwordless options = %+v, want every enabled factor", passwordless)
	}
	if ids["disabled"] {
		t.Fatal("passwordless options offered a disabled factor")
	}

	// Sudo raises the floor: the picker must not advertise the password it
	// would reject on submit.
	sudo := challengeFactorOptions(factors, false, auth.SudoMinTrustworthy)
	sudoIDs := map[string]bool{}
	for _, f := range sudo {
		sudoIDs[f.Id] = true
	}
	if sudoIDs["pw"] || !sudoIDs["email"] || !sudoIDs["totp"] {
		t.Fatalf("sudo options = %+v, want only Trustworthy >= %d factors", sudo, auth.SudoMinTrustworthy)
	}
}

// TestMaybeEscalateFailureKeepsPasswordFirstSingleStep pins that repeated
// password failures cannot reintroduce a factor the policy removed. The
// account has an email code factor, so an escalation would have been possible
// under the old rules.
func TestMaybeEscalateFailureKeepsPasswordFirstSingleStep(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), smokeDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}

	accountID := seedRiskAccount(t, ctx, pool, model.AuthFactorTypePassword, model.AuthFactorTypeEmailCode)
	cfg := &config.Config{}
	cfg.Security.ChallengeFailEscalateAfter = 0
	h := &handler{d: Deps{Store: store.New(pool), Cfg: cfg}}

	now := time.Now().UTC()
	ch := &model.AuthChallenge{
		Id:               uuid.NewString(),
		AccountId:        accountID,
		DeviceId:         "password-first-escalate",
		StepTotal:        1,
		StepRemain:       1,
		BlacklistFactors: []string{},
		Audiences:        []string{},
		Scopes:           []string{},
		Origin:           model.ChallengeOriginPassword,
		FailedAttempts:   5,
		ExpiredAt:        model.NewTime(now.Add(time.Hour)),
		CreatedAt:        model.NewTime(now),
		UpdatedAt:        model.NewTime(now),
	}
	if err := h.d.Store.CreateAuthChallenge(ctx, ch); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM auth_challenges WHERE id = $1`, ch.Id) })

	if err := h.maybeEscalateFailure(ctx, ch); err != nil {
		t.Fatalf("maybeEscalateFailure: %v", err)
	}
	if ch.StepTotal != 1 || ch.StepRemain != 1 {
		t.Fatalf("steps = %d/%d, want 1/1 (password-first must not escalate)", ch.StepRemain, ch.StepTotal)
	}
}

// TestDeclineSatisfiedChallengeRevokesSession pins the review decision on a
// completed password login: the challenge stays reviewable after the exchange
// expired it, and a denial signs out the session the login minted.
func TestDeclineSatisfiedChallengeRevokesSession(t *testing.T) {
	dr := newDeclineHarness(t, 2, model.AuthFactorTypePassword)
	ctx := dr.ctx

	now := time.Now().UTC()
	challenge := &model.AuthChallenge{
		Id:               uuid.NewString(),
		AccountId:        dr.accountID,
		DeviceId:         "review-test-device",
		DeviceName:       new("Review test device"),
		UserAgent:        new("review-test-agent"),
		IpAddress:        new(declineRequesterIP),
		StepTotal:        1,
		StepRemain:       0,
		BlacklistFactors: []string{},
		Audiences:        []string{},
		Scopes:           []string{},
		Origin:           model.ChallengeOriginPassword,
		ExpiredAt:        model.NewTime(now),
		ReviewUntil:      model.NewTime(now.Add(5 * time.Minute)),
		CreatedAt:        model.NewTime(now),
		UpdatedAt:        model.NewTime(now),
	}
	if err := dr.h.d.Store.CreateAuthChallenge(ctx, challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	clientID := uuid.NewString()
	if _, err := dr.pool.Exec(ctx, `INSERT INTO auth_clients
		(id, account_id, device_id, device_name, platform, created_at, updated_at)
		VALUES ($1, $2, $3, $3, 0, $4, $4)`,
		clientID, dr.accountID, "review-test-device", now); err != nil {
		t.Fatalf("seed client: %v", err)
	}
	sessionID := uuid.NewString()
	if _, err := dr.pool.Exec(ctx, `INSERT INTO auth_sessions
		(id, account_id, audiences, scopes, epoch, type, expired_at, created_at, updated_at, challenge_id, client_id)
		VALUES ($1, $2, '[]', '[]', 0, 0, $3, $4, $4, $5, $6)`,
		sessionID, dr.accountID, now.Add(time.Hour), now, challenge.Id, clientID); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	t.Cleanup(func() {
		_, _ = dr.pool.Exec(ctx, `DELETE FROM auth_sessions WHERE id = $1`, sessionID)
		_, _ = dr.pool.Exec(ctx, `DELETE FROM auth_clients WHERE id = $1`, clientID)
		_, _ = dr.pool.Exec(ctx, `DELETE FROM auth_challenges WHERE id = $1`, challenge.Id)
	})

	rec := dr.decline(t, "/auth/challenge/"+challenge.Id+"/decline")
	if rec.Code != http.StatusOK {
		t.Fatalf("decline status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var epoch int
	var expiredAt time.Time
	if err := dr.pool.QueryRow(ctx, `SELECT epoch, expired_at FROM auth_sessions WHERE id = $1`, sessionID).
		Scan(&epoch, &expiredAt); err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if epoch != 1 {
		t.Fatalf("session epoch = %d, want 1 (revoked)", epoch)
	}
	if expiredAt.After(time.Now().UTC().Add(time.Minute)) {
		t.Fatalf("session expired_at = %s, want it signed out now", expiredAt)
	}

	reloaded, err := dr.h.d.Store.GetAuthChallenge(ctx, uuid.MustParse(challenge.Id))
	if err != nil {
		t.Fatalf("reload challenge: %v", err)
	}
	if reloaded.DeclinedAt == nil {
		t.Fatal("challenge declined_at is null, want the review decision recorded")
	}

	// The review decision is not a new pending row.
	pending, err := dr.h.d.Store.ListPendingChallenges(ctx, dr.accountID)
	if err != nil {
		t.Fatalf("list pending challenges: %v", err)
	}
	for _, item := range pending {
		if item.Id == challenge.Id {
			t.Fatalf("declined challenge %s still listed as pending", challenge.Id)
		}
	}
}

// TestCreateChallengePasswordFirstDefersPrompt pins the HTTP contract: a
// password account keeps the risk model's step count (here password + email),
// the picker offers only the password, no login-attempt push is sent before the
// password verifies, and the remaining step is settled by the in-app prompt
// instead of an emailed code.
func TestCreateChallengePasswordFirstDefersPrompt(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), smokeDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}

	st := store.New(pool)
	ring := &fakeRing{}
	h := &handler{d: Deps{
		Store:   st,
		Geo:     &geo.Service{},
		Cfg:     &config.Config{},
		Clients: &grpcclient.Clients{Ring: ring},
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}
	accountID := seedRiskAccount(t, ctx, pool,
		model.AuthFactorTypePassword, model.AuthFactorTypeEmailCode)
	account, err := st.GetAccountByID(ctx, uuid.MustParse(accountID))
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	const secret = "correct horse battery staple"
	hash, err := auth.HashPassword(secret)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE account_auth_factors SET secret = $1
		WHERE account_id = $2 AND type = $3`, hash, accountID, int(model.AuthFactorTypePassword)); err != nil {
		t.Fatalf("seed password secret: %v", err)
	}
	passwordFactor, err := st.GetAuthFactorByType(ctx, accountID, model.AuthFactorTypePassword)
	if err != nil {
		t.Fatalf("load password factor: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/auth/challenge", h.createChallenge)
	router.GET("/auth/challenge/:id/factors", h.getChallengeFactors)
	router.PATCH("/auth/challenge/:id", h.doChallenge)

	body, _ := json.Marshal(map[string]any{
		"account":     account.Name,
		"device_id":   "password-first-device",
		"device_name": "Password First Test",
		"platform":    int(model.ClientPlatformWeb),
	})
	req := httptest.NewRequest(http.MethodPost, "/auth/challenge", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create challenge status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var created model.AuthChallenge
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM auth_challenges WHERE id = $1`, created.Id) })
	// A fresh device plus no prior session scores high enough to demand both
	// completable factors; the policy must not flatten that to one step.
	if created.StepTotal != 2 || created.StepRemain != 2 {
		t.Fatalf("steps = %d/%d, want 2/2 (password + reviewable second step)", created.StepRemain, created.StepTotal)
	}
	if len(ring.pushes) != 0 {
		t.Fatalf("prompt pushed before the password verified: %+v", ring.pushes)
	}
	reloaded, err := st.GetAuthChallenge(ctx, uuid.MustParse(created.Id))
	if err != nil {
		t.Fatalf("reload challenge: %v", err)
	}
	if reloaded.ReviewUntil != nil {
		t.Fatal("ReviewUntil set before the password verified, want the prompt deferred")
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/auth/challenge/"+created.Id+"/factors", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("factors status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var factors []model.AuthFactor
	if err := json.Unmarshal(rec.Body.Bytes(), &factors); err != nil {
		t.Fatalf("decode factors: %v", err)
	}
	if len(factors) != 1 || factors[0].Type != model.AuthFactorTypePassword {
		t.Fatalf("factors = %+v, want only the password", factors)
	}

	// The password settles one step. The second one waits for the in-app
	// approval, so the challenge is still incomplete and the login cannot be
	// exchanged yet.
	submit, _ := json.Marshal(map[string]any{
		"factor_id": passwordFactor.Id,
		"password":  secret,
	})
	req = httptest.NewRequest(http.MethodPatch, "/auth/challenge/"+created.Id, bytes.NewReader(submit))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit password status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var afterPassword model.AuthChallenge
	if err := json.Unmarshal(rec.Body.Bytes(), &afterPassword); err != nil {
		t.Fatalf("decode challenge after password: %v", err)
	}
	if afterPassword.StepRemain != 1 || afterPassword.ApprovedAt != nil {
		t.Fatalf("after password remain = %d approved = %v, want 1 and not approved",
			afterPassword.StepRemain, afterPassword.ApprovedAt)
	}
	if len(ring.pushes) != 1 || ring.pushes[0].Topic != "auth.login_attempt" {
		t.Fatalf("post-password prompt not pushed as auth.login_attempt: %+v", ring.pushes)
	}

	// Nothing is left to submit: only a trusted device's approval can settle
	// the remaining step.
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/auth/challenge/"+created.Id+"/factors", nil))
	var remaining []model.AuthFactor
	if err := json.Unmarshal(rec.Body.Bytes(), &remaining); err != nil {
		t.Fatalf("decode remaining factors: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("remaining factors = %+v, want none after the password", remaining)
	}

	reloaded, err = st.GetAuthChallenge(ctx, uuid.MustParse(created.Id))
	if err != nil {
		t.Fatalf("reload challenge: %v", err)
	}
	if reloaded.ReviewUntil == nil {
		t.Fatal("ReviewUntil is nil, want a post-login review window")
	}
	pending, err := st.ListPendingChallenges(ctx, accountID)
	if err != nil {
		t.Fatalf("list pending challenges: %v", err)
	}
	listed := false
	for _, item := range pending {
		if item.Id == created.Id {
			listed = true
		}
	}
	if !listed {
		t.Fatalf("password-verified challenge %s is missing from the review list: %+v", created.Id, pending)
	}
}

// TestCreateChallengePasswordFirstRespectsLockdown pins that password-first
// does not weaken lockdown: every completable factor still raises the step
// total, and none of those extra steps is an emailed code.
func TestCreateChallengePasswordFirstRespectsLockdown(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), smokeDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}

	st := store.New(pool)
	ring := &fakeRing{}
	h := &handler{d: Deps{
		Store:   st,
		Geo:     &geo.Service{},
		Cfg:     &config.Config{},
		Clients: &grpcclient.Clients{Ring: ring},
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}
	accountID := seedRiskAccount(t, ctx, pool,
		model.AuthFactorTypePassword, model.AuthFactorTypeEmailCode)
	account, err := st.GetAccountByID(ctx, uuid.MustParse(accountID))
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	if err := st.SetSecurityMode(ctx, accountID, model.SecurityModeLockdown); err != nil {
		t.Fatalf("set lockdown: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `UPDATE accounts SET security_mode = 0 WHERE id = $1`, accountID)
	})

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/auth/challenge", h.createChallenge)

	body, _ := json.Marshal(map[string]any{
		"account":     account.Name,
		"device_id":   "lockdown-password-device",
		"device_name": "Lockdown Test",
		"platform":    int(model.ClientPlatformWeb),
	})
	req := httptest.NewRequest(http.MethodPost, "/auth/challenge", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create challenge status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var created model.AuthChallenge
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM auth_challenges WHERE id = $1`, created.Id) })
	if created.StepTotal != 2 || created.StepRemain != 2 {
		t.Fatalf("lockdown steps = %d/%d, want 2/2 (password + second factor)",
			created.StepRemain, created.StepTotal)
	}
	if len(ring.pushes) != 0 {
		t.Fatalf("lockdown prompt pushed before the password verified: %+v", ring.pushes)
	}
}
