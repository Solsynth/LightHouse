package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"src.solsynth.dev/sosys/stargate/config"
	"src.solsynth.dev/sosys/stargate/geo"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/store"
)

func setupTemporaryTestAuthService(t *testing.T, pool *pgxpool.Pool) (*AuthService, *TokenAuthService, *config.Config) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	cfg := config.Default()
	cfg.Auth.Issuer = "solar-network"
	cfg.Auth.Audiences = []string{"solar-network"}
	cfg.Auth.TemporarySessionLifetime = "15m"
	cfg.Auth.RefreshTokenLifetime = "720h"
	cfg.Auth.AccessTokenLifetime = "5m"

	js := &JWTService{
		issuer:       "solar-network",
		validIssuers: []string{"solar-network"},
		audience:     "solar-network",
		private:      priv,
		public:       &priv.PublicKey,
	}

	st := store.New(pool)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tokenSvc := NewTokenAuthService(st, nil, js, nil, nil, logger)
	authSvc := NewAuthService(st, nil, cfg, geo.NewService(""), js, tokenSvc, nil, nil, logger)
	return authSvc, tokenSvc, cfg
}

func seedTestAccount(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	accountID := uuid.NewString()
	now := time.Now().UTC()
	name := "temp_sess_" + uuid.NewString()[:8]
	if _, err := pool.Exec(ctx, `INSERT INTO accounts
		(id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, name, now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO account_profiles
		(id, account_id, created_at, updated_at, experience, social_credits)
		VALUES ($1, $2, $3, $3, 0, 100)`, uuid.NewString(), accountID, now); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM account_profiles WHERE account_id = $1`, accountID)
		_, _ = pool.Exec(bg, `DELETE FROM auth_challenges WHERE account_id = $1`, accountID)
		_, _ = pool.Exec(bg, `DELETE FROM auth_sessions WHERE account_id = $1`, accountID)
		_, _ = pool.Exec(bg, `DELETE FROM authorized_devices WHERE account_id = $1`, accountID)
		_, _ = pool.Exec(bg, `DELETE FROM accounts WHERE id = $1`, accountID)
	})
	return accountID
}

// TestTemporarySession_Creation verifies that creating a session from a temporary
// challenge configures short TTL (TemporarySessionLifetime), IsTemporary=true,
// and AutoRenewable=false.
func TestTemporarySession_Creation(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), apiKeysDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	ctx = context.Background()

	authSvc, _, _ := setupTemporaryTestAuthService(t, pool)
	accountID := seedTestAccount(t, pool)

	// 1. Temporary login challenge
	now := time.Now().UTC()
	st := store.New(pool)
	tempChal := &model.AuthChallenge{
		Id:             uuid.NewString(),
		AccountId:      accountID,
		DeviceId:       "dev-temp-1",
		Audiences:      []string{},
		IsTemporary:    true,
		AutoRenewable:  false,
		StepTotal:      1,
		StepRemain:     0,
		FailedAttempts: 0,
		CreatedAt:      model.NewTime(now),
		UpdatedAt:      model.NewTime(now),
	}
	if err := st.CreateAuthChallenge(ctx, tempChal); err != nil {
		t.Fatalf("create temp challenge: %v", err)
	}

	pair, err := authSvc.CreateSessionAndIssueTokens(ctx, tempChal)
	if err != nil {
		t.Fatalf("CreateSessionAndIssueTokens(temp): %v", err)
	}
	if pair == nil || pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatalf("expected non-empty tokens, got %v", pair)
	}

	// Verify session state in database
	var isTemp, autoRenew bool
	var expiredAt time.Time
	if err := pool.QueryRow(ctx, `SELECT is_temporary, auto_renewable, expired_at FROM auth_sessions WHERE account_id = $1 ORDER BY created_at DESC LIMIT 1`, accountID).
		Scan(&isTemp, &autoRenew, &expiredAt); err != nil {
		t.Fatalf("read created session: %v", err)
	}

	if !isTemp {
		t.Fatalf("expected is_temporary=true, got false")
	}
	if autoRenew {
		t.Fatalf("expected auto_renewable=false, got true")
	}
	// ExpiredAt should be ~15m from now (within [13m, 17m])
	diff := time.Until(expiredAt)
	if diff < 13*time.Minute || diff > 17*time.Minute {
		t.Fatalf("expected session expiry around 15m, got diff %v", diff)
	}

	// 2. Standard persistent login challenge (IsTemporary: false, AutoRenewable: true)
	permChal := &model.AuthChallenge{
		Id:             uuid.NewString(),
		AccountId:      accountID,
		DeviceId:       "dev-perm-1",
		Audiences:      []string{},
		IsTemporary:    false,
		AutoRenewable:  true,
		StepTotal:      1,
		StepRemain:     0,
		FailedAttempts: 0,
		CreatedAt:      model.NewTime(now),
		UpdatedAt:      model.NewTime(now),
	}
	if err := st.CreateAuthChallenge(ctx, permChal); err != nil {
		t.Fatalf("create perm challenge: %v", err)
	}

	_, err = authSvc.CreateSessionAndIssueTokens(ctx, permChal)
	if err != nil {
		t.Fatalf("CreateSessionAndIssueTokens(perm): %v", err)
	}

	if err := pool.QueryRow(ctx, `SELECT is_temporary, auto_renewable, expired_at FROM auth_sessions WHERE id = (SELECT id FROM auth_sessions WHERE account_id = $1 ORDER BY created_at DESC LIMIT 1)`, accountID).
		Scan(&isTemp, &autoRenew, &expiredAt); err != nil {
		t.Fatalf("read perm session: %v", err)
	}
	if isTemp {
		t.Fatalf("expected is_temporary=false, got true")
	}
	if !autoRenew {
		t.Fatalf("expected auto_renewable=true, got false")
	}
	if time.Until(expiredAt) < 700*time.Hour {
		t.Fatalf("expected session expiry ~720h, got %v", time.Until(expiredAt))
	}
}

// TestTemporarySession_RefreshRejection verifies that temporary and non-auto-renewable
// sessions cannot be renewed using refresh tokens.
func TestTemporarySession_RefreshRejection(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), apiKeysDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	ctx = context.Background()

	authSvc, _, _ := setupTemporaryTestAuthService(t, pool)
	accountID := seedTestAccount(t, pool)
	now := time.Now().UTC()

	// 1. Temporary session refresh attempt
	st := store.New(pool)
	tempChal := &model.AuthChallenge{
		Id:             uuid.NewString(),
		AccountId:      accountID,
		DeviceId:       "dev-temp-2",
		Audiences:      []string{},
		IsTemporary:    true,
		AutoRenewable:  false,
		StepTotal:      1,
		StepRemain:     0,
		FailedAttempts: 0,
		CreatedAt:      model.NewTime(now),
		UpdatedAt:      model.NewTime(now),
	}
	if err := st.CreateAuthChallenge(ctx, tempChal); err != nil {
		t.Fatalf("create temp challenge: %v", err)
	}
	pair, err := authSvc.CreateSessionAndIssueTokens(ctx, tempChal)
	if err != nil {
		t.Fatalf("create temp session: %v", err)
	}

	_, _, err = authSvc.RefreshSessionAndIssueTokens(ctx, pair.RefreshToken)
	if err == nil {
		t.Fatalf("expected refresh of temporary session to fail, got success")
	}
	if err.Error() != "Session is not renewable." {
		t.Fatalf("expected 'Session is not renewable.', got %v", err)
	}

	// 2. Non-auto-renewable persistent session refresh attempt
	nonRenewChal := &model.AuthChallenge{
		Id:             uuid.NewString(),
		AccountId:      accountID,
		DeviceId:       "dev-nonrenew-1",
		Audiences:      []string{},
		IsTemporary:    false,
		AutoRenewable:  false,
		StepTotal:      1,
		StepRemain:     0,
		FailedAttempts: 0,
		CreatedAt:      model.NewTime(now),
		UpdatedAt:      model.NewTime(now),
	}
	if err := st.CreateAuthChallenge(ctx, nonRenewChal); err != nil {
		t.Fatalf("create non-renew challenge: %v", err)
	}
	pair2, err := authSvc.CreateSessionAndIssueTokens(ctx, nonRenewChal)
	if err != nil {
		t.Fatalf("create non-renew session: %v", err)
	}

	_, _, err = authSvc.RefreshSessionAndIssueTokens(ctx, pair2.RefreshToken)
	if err == nil {
		t.Fatalf("expected refresh of non-auto-renewable session to fail, got success")
	}
	if err.Error() != "Session is not renewable." {
		t.Fatalf("expected 'Session is not renewable.', got %v", err)
	}

	// 3. Auto-renewable session refresh attempt succeeds
	renewChal := &model.AuthChallenge{
		Id:             uuid.NewString(),
		AccountId:      accountID,
		DeviceId:       "dev-renew-1",
		Audiences:      []string{},
		IsTemporary:    false,
		AutoRenewable:  true,
		StepTotal:      1,
		StepRemain:     0,
		FailedAttempts: 0,
		CreatedAt:      model.NewTime(now),
		UpdatedAt:      model.NewTime(now),
	}
	if err := st.CreateAuthChallenge(ctx, renewChal); err != nil {
		t.Fatalf("create renew challenge: %v", err)
	}
	pair3, err := authSvc.CreateSessionAndIssueTokens(ctx, renewChal)
	if err != nil {
		t.Fatalf("create renew session: %v", err)
	}

	newPair, newSession, err := authSvc.RefreshSessionAndIssueTokens(ctx, pair3.RefreshToken)
	if err != nil {
		t.Fatalf("expected refresh of auto-renewable session to succeed, got: %v", err)
	}
	if newPair.RefreshToken == "" || newSession == nil {
		t.Fatalf("expected valid new tokens and session")
	}
}

// TestNonExpirableLoginSession_Rejected verifies that legacy login sessions with
// expired_at IS NULL are rejected both at AuthenticateToken and RefreshSessionAndIssueTokens.
func TestNonExpirableLoginSession_Rejected(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), apiKeysDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	ctx = context.Background()

	authSvc, tokenSvc, _ := setupTemporaryTestAuthService(t, pool)
	accountID := seedTestAccount(t, pool)
	now := time.Now().UTC()

	// Seed legacy login session with expired_at NULL
	sessionUUID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions
		(id, account_id, audiences, scopes, epoch, type, expired_at, created_at, updated_at)
		VALUES ($1, $2, '[]', '["*"]', 0, 0, NULL, $3, $3)`,
		sessionUUID, accountID, now); err != nil {
		t.Fatalf("seed null expired session: %v", err)
	}

	st := store.New(pool)
	sess, err := st.GetOwnedSession(ctx, accountID, sessionUUID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	pair, err := authSvc.CreateTokenPair(ctx, sess)
	if err != nil {
		t.Fatalf("create token pair: %v", err)
	}

	// 1. AuthenticateToken should reject it because login session without expiry is invalid
	ok, _, reason, _ := tokenSvc.AuthenticateToken(ctx, pair.AccessToken, "127.0.0.1")
	if ok {
		t.Fatalf("expected AuthenticateToken to reject login session with nil expired_at")
	}
	if reason != "Session has been expired." {
		t.Fatalf("expected 'Session has been expired.', got %v", reason)
	}

	// 2. RefreshSessionAndIssueTokens should reject it
	_, _, err = authSvc.RefreshSessionAndIssueTokens(ctx, pair.RefreshToken)
	if err == nil {
		t.Fatalf("expected RefreshSessionAndIssueTokens to reject login session with nil expired_at")
	}
	if err.Error() != "Session has been expired." {
		t.Fatalf("expected 'Session has been expired.', got %v", err)
	}
}

// TestCreateSessionFromParent_Temporary verifies child session creation inherits or
// applies temporary session limits and rejects non-expirable parent login sessions.
func TestCreateSessionFromParent_Temporary(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), apiKeysDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	ctx = context.Background()

	authSvc, _, _ := setupTemporaryTestAuthService(t, pool)
	accountID := seedTestAccount(t, pool)
	now := time.Now().UTC()

	// 1. Create a parent session with expiry
	parentUUID := uuid.New()
	parentExp := now.Add(720 * time.Hour)
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions
		(id, account_id, audiences, scopes, epoch, type, expired_at, created_at, updated_at)
		VALUES ($1, $2, '[]', '["*"]', 0, 0, $3, $4, $4)`,
		parentUUID, accountID, parentExp, now); err != nil {
		t.Fatalf("seed parent session: %v", err)
	}

	st := store.New(pool)
	parentSess, err := st.GetOwnedSession(ctx, accountID, parentUUID)
	if err != nil {
		t.Fatalf("get parent session: %v", err)
	}

	trueVal := true
	childSess, err := authSvc.CreateSessionFromParent(ctx, parentSess, "dev-1", nil, model.ClientPlatformWeb, nil, &trueVal, nil)
	if err != nil {
		t.Fatalf("CreateSessionFromParent(temp): %v", err)
	}

	var isTemp, autoRenew bool
	var expiredAt time.Time
	if err := pool.QueryRow(ctx, `SELECT is_temporary, auto_renewable, expired_at FROM auth_sessions WHERE id = $1`, childSess.Id).
		Scan(&isTemp, &autoRenew, &expiredAt); err != nil {
		t.Fatalf("read child session: %v", err)
	}
	if !isTemp {
		t.Fatalf("expected child is_temporary=true, got false")
	}
	if autoRenew {
		t.Fatalf("expected child auto_renewable=false, got true")
	}
	if diff := time.Until(expiredAt); diff < 13*time.Minute || diff > 17*time.Minute {
		t.Fatalf("expected child session expiry ~15m, got diff %v", diff)
	}

	// 2. Parent login session with nil expired_at must be rejected
	legacyParentUUID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions
		(id, account_id, audiences, scopes, epoch, type, expired_at, created_at, updated_at)
		VALUES ($1, $2, '[]', '["*"]', 0, 0, NULL, $3, $3)`,
		legacyParentUUID, accountID, now); err != nil {
		t.Fatalf("seed legacy parent session: %v", err)
	}
	legacyParent, err := st.GetOwnedSession(ctx, accountID, legacyParentUUID)
	if err != nil {
		t.Fatalf("get legacy parent: %v", err)
	}
	_, err = authSvc.CreateSessionFromParent(ctx, legacyParent, "dev-2", nil, model.ClientPlatformWeb, nil, nil, nil)
	if err == nil {
		t.Fatalf("expected CreateSessionFromParent with legacy parent to fail")
	}
	if err.Error() != "Parent session is expired." {
		t.Fatalf("expected 'Parent session is expired.', got %v", err)
	}
}
