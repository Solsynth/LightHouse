package auth

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/store"
)

const apiKeysDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

func TestCreateApiKeyInitializesSessionJSONArrays(t *testing.T) {
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

	accountID := uuid.New()
	name := "api_key_session_" + uuid.NewString()[:8]
	defer pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = $1`, accountID)

	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts
		(id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, name, now); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	service := &AuthService{store: store.New(pool)}
	key, err := service.CreateApiKey(ctx, accountID.String(), "regression", nil, nil)
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}

	var audiences, scopes []string
	if err := pool.QueryRow(ctx, `SELECT audiences, scopes FROM auth_sessions WHERE id = $1`, key.SessionId).Scan(&audiences, &scopes); err != nil {
		t.Fatalf("load created API key session: %v", err)
	}
	if audiences == nil || scopes == nil {
		t.Fatalf("created API key session has nil JSON arrays: audiences=%v scopes=%v", audiences, scopes)
	}
	if len(audiences) != 0 || len(scopes) != 0 {
		t.Fatalf("created API key session arrays = audiences=%v scopes=%v, want empty", audiences, scopes)
	}
}
