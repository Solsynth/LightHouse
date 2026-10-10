package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

func sudoTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// sudoTestDSN mirrors config.example.toml (same convention as the other auth
// smoke tests).
const sudoTestDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

// TestSudoElevationCache pins the Redis flag contract: GrantSudo sets the
// flag, IsSudoElevated reads it, and ClearSudo drops it.
func TestSudoElevationCache(t *testing.T) {
	ctx := context.Background()
	rc, err := redis.Connect(ctx, "localhost:6379", "", 0)
	if err != nil {
		t.Skipf("redis unavailable: %v", err)
	}
	t.Cleanup(func() { _ = rc.Raw.Close() })

	svc := NewAuthService(nil, rc, config.Default(), nil, nil, nil, nil, nil, sudoTestLogger())
	sessionID := uuid.NewString()
	t.Cleanup(func() { _ = rc.Cache.Remove(ctx, "accounts:"+sessionID+":sudo") })

	elevated, err := svc.IsSudoElevated(ctx, sessionID)
	if err != nil {
		t.Fatalf("IsSudoElevated (initial): %v", err)
	}
	if elevated {
		t.Fatal("session elevated before any grant")
	}

	until, err := svc.GrantSudo(ctx, sessionID)
	if err != nil {
		t.Fatalf("GrantSudo: %v", err)
	}
	if !until.After(time.Now()) {
		t.Fatalf("sudo_until %v is not in the future", until)
	}
	elevated, err = svc.IsSudoElevated(ctx, sessionID)
	if err != nil || !elevated {
		t.Fatalf("IsSudoElevated after grant = %v, %v; want true", elevated, err)
	}

	svc.ClearSudo(ctx, sessionID)
	elevated, err = svc.IsSudoElevated(ctx, sessionID)
	if err != nil || elevated {
		t.Fatalf("IsSudoElevated after clear = %v, %v; want false", elevated, err)
	}
}

// TestSudoFailsClosedWithoutRedis pins the fail-closed contract: an
// unavailable elevation store is an error (503 upstream), never an implicit
// elevation, and ClearSudo degrades to a no-op.
func TestSudoFailsClosedWithoutRedis(t *testing.T) {
	svc := NewAuthService(nil, nil, config.Default(), nil, nil, nil, nil, nil, sudoTestLogger())
	ctx := context.Background()

	if _, err := svc.IsSudoElevated(ctx, "sess"); !errors.Is(err, ErrSudoUnavailable) {
		t.Fatalf("IsSudoElevated err = %v, want ErrSudoUnavailable", err)
	}
	if _, err := svc.GrantSudo(ctx, "sess"); !errors.Is(err, ErrSudoUnavailable) {
		t.Fatalf("GrantSudo err = %v, want ErrSudoUnavailable", err)
	}
	svc.ClearSudo(ctx, "sess") // must not panic
}

// seedSudoHintAccount inserts an account (plus an optional verified email
// contact) and one enabled factor per entry, returning the account id.
func seedSudoHintAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, verifiedEmail bool, factors ...model.AuthFactorType) string {
	t.Helper()
	accountID := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, "hint_"+uuid.NewString()[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if verifiedEmail {
		if _, err := pool.Exec(ctx, `INSERT INTO account_contacts (id, account_id, type, content, is_primary, is_public, verified_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, true, false, $5, $5, $5)`,
			uuid.NewString(), accountID, int(model.ContactTypeEmail), "hint@example.com", now); err != nil {
			t.Fatalf("seed contact: %v", err)
		}
	}
	for _, ft := range factors {
		trust := 1
		switch ft {
		case model.AuthFactorTypeTimedCode:
			trust = 3
		case model.AuthFactorTypePasskey:
			trust = 4
		}
		if _, err := pool.Exec(ctx, `INSERT INTO account_auth_factors (id, account_id, type, secret, config, trustworthy, enabled_at, created_at, updated_at)
			VALUES ($1, $2, $3, '', '{}', $4, $5, $5, $5)`,
			uuid.NewString(), accountID, int(ft), trust, now); err != nil {
			t.Fatalf("seed factor: %v", err)
		}
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, accountID) })
	return accountID
}

func TestSudoFactorHint(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, sudoTestDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	svc := NewAuthService(store.New(pool), nil, config.Default(), nil, nil, nil, nil, nil, sudoTestLogger())

	t.Run("password and timed_code", func(t *testing.T) {
		accountID := seedSudoHintAccount(t, ctx, pool, false, model.AuthFactorTypePassword, model.AuthFactorTypeTimedCode)
		hint, err := svc.SudoFactorHint(ctx, accountID)
		if err != nil {
			t.Fatalf("SudoFactorHint: %v", err)
		}
		if hint != "password,timed_code" {
			t.Fatalf("hint = %q, want password,timed_code", hint)
		}
	})

	t.Run("passkey admits the emailed fallback", func(t *testing.T) {
		accountID := seedSudoHintAccount(t, ctx, pool, true, model.AuthFactorTypePasskey)
		hint, err := svc.SudoFactorHint(ctx, accountID)
		if err != nil {
			t.Fatalf("SudoFactorHint: %v", err)
		}
		if hint != "passkey,email_code" {
			t.Fatalf("hint = %q, want passkey,email_code", hint)
		}
	})

	t.Run("password without an email contact has no fallback", func(t *testing.T) {
		accountID := seedSudoHintAccount(t, ctx, pool, false, model.AuthFactorTypePassword)
		hint, err := svc.SudoFactorHint(ctx, accountID)
		if err != nil {
			t.Fatalf("SudoFactorHint: %v", err)
		}
		if hint != "password" {
			t.Fatalf("hint = %q, want password", hint)
		}
	})
}
