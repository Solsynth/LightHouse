package store

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

const passkeyInsertRegressionDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

func TestInsertPasskeyGeneratesID(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, passkeyInsertRegressionDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(pool.Close)

	var accountID string
	if err := pool.QueryRow(ctx, `SELECT id FROM accounts ORDER BY created_at LIMIT 1`).Scan(&accountID); err != nil {
		t.Skipf("no local account to attach the passkey: %v", err)
	}

	credentialID := "regression-" + uuid.NewString()
	passkey, err := New(pool).InsertPasskey(ctx, &model.Passkey{
		AccountId:    accountID,
		Label:        "regression passkey",
		CredentialId: credentialID,
		Credential:   "{}",
	})
	if err != nil {
		t.Fatalf("insert passkey: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM account_passkeys WHERE id = $1`, passkey.Id)
	})

	if passkey.Id == "" {
		t.Fatal("inserted passkey has no id")
	}
	if _, err := uuid.Parse(passkey.Id); err != nil {
		t.Fatalf("inserted passkey id %q is not a UUID: %v", passkey.Id, err)
	}
}
