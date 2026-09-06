package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

const testDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

// TestIsTrustedSession verifies the trust predicate: native platform + recent
// activity → trusted; Web platform → never trusted; stale activity → not
// trusted; nil client → not trusted.
func TestIsTrustedSession(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), testDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}

	st := New(pool)
	gap := 720 * time.Hour

	// Seed account.
	accountID := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, "trusted_test", now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, accountID) })

	// Seed a native client.
	nativeClientID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_clients (id, device_id, device_name, account_id, platform, created_at, updated_at)
		VALUES ($1, 'native-dev', 'Test Native', $2, $3, $4, $4)`,
		nativeClientID, accountID, int(model.ClientPlatformIos), now); err != nil {
		t.Fatalf("seed native client: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM auth_clients WHERE id = $1`, nativeClientID) })

	// Seed a Web client.
	webClientID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_clients (id, device_id, device_name, account_id, platform, created_at, updated_at)
		VALUES ($1, 'web-dev', 'Test Web', $2, $3, $4, $4)`,
		webClientID, accountID, int(model.ClientPlatformWeb), now); err != nil {
		t.Fatalf("seed web client: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM auth_clients WHERE id = $1`, webClientID) })

	t.Run("native with recent activity is trusted", func(t *testing.T) {
		s := &model.AuthSession{
			ClientId:      &nativeClientID,
			LastGrantedAt: model.NewTime(now),
		}
		trusted, err := st.IsTrustedSession(ctx, s, gap)
		if err != nil {
			t.Fatalf("IsTrustedSession: %v", err)
		}
		if !trusted {
			t.Fatal("native session with recent activity should be trusted")
		}
	})

	t.Run("web session is never trusted", func(t *testing.T) {
		s := &model.AuthSession{
			ClientId:      &webClientID,
			LastGrantedAt: model.NewTime(now),
		}
		trusted, err := st.IsTrustedSession(ctx, s, gap)
		if err != nil {
			t.Fatalf("IsTrustedSession: %v", err)
		}
		if trusted {
			t.Fatal("web session should never be trusted")
		}
	})

	t.Run("native with stale activity is not trusted", func(t *testing.T) {
		stale := now.Add(-gap - time.Hour)
		s := &model.AuthSession{
			ClientId:      &nativeClientID,
			LastGrantedAt: model.NewTime(stale),
		}
		trusted, err := st.IsTrustedSession(ctx, s, gap)
		if err != nil {
			t.Fatalf("IsTrustedSession: %v", err)
		}
		if trusted {
			t.Fatal("native session with stale activity should not be trusted")
		}
	})

	t.Run("nil client is not trusted", func(t *testing.T) {
		s := &model.AuthSession{
			LastGrantedAt: model.NewTime(now),
		}
		trusted, err := st.IsTrustedSession(ctx, s, gap)
		if err != nil {
			t.Fatalf("IsTrustedSession: %v", err)
		}
		if trusted {
			t.Fatal("nil client should not be trusted")
		}
	})
}
