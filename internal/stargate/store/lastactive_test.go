package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const lastActiveDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

// TestTouchLastActive verifies the last-active flush (the LastActiveFlushHandler
// role moved to Stargate): profile last_seen_at is presented, the session's
// last_granted_at advances and expiring sessions get the 7-day keep-alive.
func TestTouchLastActive(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), lastActiveDSN)
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

	s := New(pool)

	accountID := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, "last_active_"+uuid.NewString()[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	defer pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, accountID)

	// Profile row: created by the account RPCs; seed it directly for the test.
	if _, err := pool.Exec(ctx, `INSERT INTO account_profiles (id, account_id, created_at, updated_at, experience, social_credits)
		VALUES ($1, $2, $3, $3, 0, 100)`, uuid.NewString(), accountID, now); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	sessionID := uuid.NewString()
	expiredAt := now.Add(24 * time.Hour)
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions
		(id, account_id, audiences, epoch, scopes, type, expired_at, created_at, updated_at)
		VALUES ($1, $2, '[]', 0, '[]', 0, $3, $4, $4)`, sessionID, accountID, expiredAt, now); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	seenAt := now.Add(30 * time.Minute)
	if err := s.TouchLastActive(ctx, accountID, sessionID, seenAt); err != nil {
		t.Fatalf("TouchLastActive: %v", err)
	}

	var profileSeen *time.Time
	if err := pool.QueryRow(ctx, `SELECT last_seen_at FROM account_profiles WHERE account_id = $1`, accountID).Scan(&profileSeen); err != nil {
		t.Fatalf("read profile last_seen: %v", err)
	}
	if profileSeen == nil || !profileSeen.UTC().Equal(seenAt.UTC()) {
		t.Fatalf("profile last_seen_at = %v, want %v", profileSeen, seenAt)
	}

	var granted, keptAlive time.Time
	if err := pool.QueryRow(ctx, `SELECT last_granted_at, expired_at FROM auth_sessions WHERE id = $1`, sessionID).Scan(&granted, &keptAlive); err != nil {
		t.Fatalf("read session: %v", err)
	}
	if !granted.UTC().Equal(seenAt.UTC()) {
		t.Fatalf("session last_granted_at = %v, want %v", granted, seenAt)
	}
	wantKeepAlive := seenAt.Add(7 * 24 * time.Hour)
	if !keptAlive.UTC().Equal(wantKeepAlive.UTC()) {
		t.Fatalf("session expired_at = %v, want keep-alive %v", keptAlive, wantKeepAlive)
	}

	// A session without an expiry must not be touched by the keep-alive.
	sessionNoExpiry := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions
		(id, account_id, audiences, epoch, scopes, type, created_at, updated_at)
		VALUES ($1, $2, '[]', 0, '[]', 0, $3, $3)`, sessionNoExpiry, accountID, now); err != nil {
		t.Fatalf("seed no-expiry session: %v", err)
	}
	if err := s.TouchLastActive(ctx, accountID, sessionNoExpiry, seenAt); err != nil {
		t.Fatalf("TouchLastActive (no expiry): %v", err)
	}
	var noExpiryExpired *time.Time
	if err := pool.QueryRow(ctx, `SELECT expired_at FROM auth_sessions WHERE id = $1`, sessionNoExpiry).Scan(&noExpiryExpired); err != nil {
		t.Fatalf("read no-expiry session: %v", err)
	}
	if noExpiryExpired != nil {
		t.Fatalf("no-expiry session gained expired_at %v", noExpiryExpired)
	}
}
