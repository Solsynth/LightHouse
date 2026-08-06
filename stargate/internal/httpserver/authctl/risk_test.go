package authctl

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// smokeDSN mirrors config.example.toml (same convention as e2eectl and
// spell's smoke tests).
const smokeDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

// seedRiskAccount inserts an account plus the given enabled factor types and
// returns the account id.
func seedRiskAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, factorTypes ...model.AuthFactorType) string {
	t.Helper()
	accountID := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, "risk_"+uuid.NewString()[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	for _, ft := range factorTypes {
		if _, err := pool.Exec(ctx, `INSERT INTO account_auth_factors (id, account_id, type, secret, config, trustworthy, enabled_at, created_at, updated_at)
			VALUES ($1, $2, $3, '', '{}', 1, $4, $4, $4)`,
			uuid.NewString(), accountID, int(ft), now); err != nil {
			t.Fatalf("seed factor type %d: %v", ft, err)
		}
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, accountID) })
	return accountID
}

// TestDetectChallengeRiskSkipsUncompletableFactors pins the step-count
// contract: Passkey (7) and NfcToken (6) factors can never satisfy a step of
// the username-challenge flow (the client picker offers passkeys only via the
// separate discoverable flow, and NFC verification is not ported), so they
// must not inflate StepTotal — a password+passkey account on a fresh device
// requires exactly one step instead of stranding the login at an empty
// factor picker.
func TestDetectChallengeRiskSkipsUncompletableFactors(t *testing.T) {
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

	h := &handler{d: Deps{Store: store.New(pool)}}

	// Fresh IP/UA/device signals, so the risk score is high enough that every
	// counted factor is demanded.
	freshIP := "203.0.113." + uuid.NewString()[:2]
	freshUA := "RiskTestUA-" + uuid.NewString()[:8]

	t.Run("password plus passkey requires only the password step", func(t *testing.T) {
		accountID := seedRiskAccount(t, ctx, pool, model.AuthFactorTypePassword, model.AuthFactorTypePasskey)
		steps, err := h.detectChallengeRisk(ctx, accountID, freshIP, freshUA)
		if err != nil {
			t.Fatalf("detectChallengeRisk: %v", err)
		}
		if steps != 1 {
			t.Fatalf("password+passkey steps = %d, want 1 (passkey cannot satisfy a challenge step)", steps)
		}
	})

	t.Run("password plus nfc token requires only the password step", func(t *testing.T) {
		accountID := seedRiskAccount(t, ctx, pool, model.AuthFactorTypePassword, model.AuthFactorTypeNfcToken)
		steps, err := h.detectChallengeRisk(ctx, accountID, freshIP, freshUA)
		if err != nil {
			t.Fatalf("detectChallengeRisk: %v", err)
		}
		if steps != 1 {
			t.Fatalf("password+nfc steps = %d, want 1 (NFC verification is not ported)", steps)
		}
	})

	t.Run("password plus in-app code still demands both steps", func(t *testing.T) {
		accountID := seedRiskAccount(t, ctx, pool, model.AuthFactorTypePassword, model.AuthFactorTypeInAppCode)
		steps, err := h.detectChallengeRisk(ctx, accountID, freshIP, freshUA)
		if err != nil {
			t.Fatalf("detectChallengeRisk: %v", err)
		}
		if steps != 2 {
			t.Fatalf("password+in-app-code steps = %d, want 2 (both are completable via the picker)", steps)
		}
	})
}
