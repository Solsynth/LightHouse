package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const botKeysDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

func TestListApiKeysByAccountUsesSessionExpiry(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), botKeysDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}

	accountID := uuid.New()
	sessionID := uuid.New()
	keyID := uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	expiredAt := now.Add(24 * time.Hour)
	name := "bot_key_list_" + uuid.NewString()[:8]
	defer pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = $1`, accountID)

	if _, err := pool.Exec(ctx, `INSERT INTO accounts
		(id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, name, now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions
		(id, account_id, audiences, scopes, epoch, type, expired_at, created_at, updated_at)
		VALUES ($1, $2, '[]', '[]', 0, 2, $3, $4, $4)`, sessionID, accountID, expiredAt, now); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO api_keys
		(id, account_id, label, session_id, created_at, updated_at)
		VALUES ($1, $2, 'regression', $3, $4, $4)`, keyID, accountID, sessionID, now); err != nil {
		t.Fatalf("seed api key: %v", err)
	}

	keys, err := New(pool).ListApiKeysByAccount(ctx, accountID.String())
	if err != nil {
		t.Fatalf("list api keys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("got %d keys, want 1", len(keys))
	}
	if keys[0].ExpiredAt == nil || !time.Time(*keys[0].ExpiredAt).Equal(expiredAt) {
		t.Fatalf("key expiry = %v, want %v", keys[0].ExpiredAt, expiredAt)
	}
}
