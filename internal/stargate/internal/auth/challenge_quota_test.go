package auth

// Pins the release half of the per-IP new-challenge quota: completing a
// challenge into a session frees its slot, so an IP that logs in successfully
// is never rate limited while an IP that only abandons challenges still is.
// Mirrors the recover_test.go convention: skip when Postgres, Redis or the dev
// signing keys are unavailable.

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/geo"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/risk"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

func TestChallengeQuotaReleasedOnSuccessfulLogin(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, recoveryTestDSN)
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
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	priv := filepath.Join(root, "Keys", "PrivateKey.pem")
	pub := filepath.Join(root, "Keys", "PublicKey.pem")
	if _, err := os.Stat(priv); err != nil {
		t.Skipf("dev signing keys missing: %v", err)
	}

	cfg := config.Default()
	cfg.Auth.PublicKeyPath = pub
	cfg.Auth.PrivateKeyPath = priv
	cfg.Security.MaxChallengesPerIp = 1
	cfg.Security.ChallengeWindow = "1h"
	jwtSvc, err := NewJWTService(cfg)
	if err != nil {
		t.Fatalf("jwt service: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.New(pool)
	svc := NewAuthService(st, rc, cfg, geo.NewService(""), jwtSvc, NewTokenAuthService(st, rc, jwtSvc, nil, nil, log), nil, nil, log)

	accountID := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts
		(id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`,
		accountID, "quota_regression_"+uuid.NewString()[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = $1`, accountID) })

	ip := "203.0.113.21"
	t.Cleanup(func() { _ = rc.Raw.Del(ctx, "auth:challenge:ip:"+ip).Err() })

	// The IP's single slot is taken by an unfinished challenge.
	first := uuid.NewString()
	if !risk.ChallengeAllowed(ctx, rc, cfg, ip, first) {
		t.Fatal("first challenge should be admitted")
	}
	if risk.ChallengeAllowed(ctx, rc, cfg, ip, uuid.NewString()) {
		t.Fatal("second challenge should be refused while the first is unfinished")
	}

	// Complete the first challenge into a session.
	challenge := &model.AuthChallenge{
		Id:         first,
		AccountId:  accountID,
		DeviceId:   "quota-test-device",
		DeviceName: new("Quota test device"),
		IpAddress:  &ip,
		UserAgent:  new("quota-test-agent"),
		StepTotal:  1,
		StepRemain: 0,
		ExpiredAt:  model.NewTime(now.Add(time.Hour)),
		CreatedAt:  model.NewTime(now),
		UpdatedAt:  model.NewTime(now),
	}
	if err := st.CreateAuthChallenge(ctx, challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	if _, err := svc.CreateSessionAndIssueTokens(ctx, challenge); err != nil {
		t.Fatalf("CreateSessionAndIssueTokens: %v", err)
	}

	// The successful login freed the slot: the IP may start over.
	if !risk.ChallengeAllowed(ctx, rc, cfg, ip, uuid.NewString()) {
		t.Fatal("a successful login must not count against the new-challenge quota")
	}
}
