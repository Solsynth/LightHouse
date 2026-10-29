package authctl

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// seedEscalateChallenge inserts a challenge for the given account with the
// provided step/blacklist state.
func seedEscalateChallenge(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID string, stepTotal, stepRemain int, blacklist []string) *model.AuthChallenge {
	t.Helper()
	now := time.Now().UTC()
	ch := &model.AuthChallenge{
		Id:               uuid.NewString(),
		AccountId:        accountID,
		DeviceId:         "escalate-test-device",
		StepTotal:        stepTotal,
		StepRemain:       stepRemain,
		BlacklistFactors: blacklist,
		Audiences:        []string{},
		Scopes:           []string{},
		ExpiredAt:        model.NewTime(now.Add(10 * time.Minute)),
		CreatedAt:        model.NewTime(now),
		UpdatedAt:        model.NewTime(now),
	}
	if blacklist == nil {
		ch.BlacklistFactors = []string{}
	}
	if err := store.New(pool).CreateAuthChallenge(ctx, ch); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	return ch
}

// TestEscalateChallenge pins the decline-escalation contract: declining an
// in-app approval prompt escalates the challenge to require every enabled
// (completable) factor — step_remain/total reset to the full count, the
// blacklist clears, and DeclinedAt is set (marking "declined + escalated").
// For a single-factor account there is no higher level, so the decline is
// terminal: StepRemain is unchanged and DeclinedAt is set.
func TestEscalateChallenge(t *testing.T) {
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
	h := &handler{d: Deps{Store: st}}

	t.Run("multi-factor decline escalates to full step count", func(t *testing.T) {
		accountID := seedRiskAccount(t, ctx, pool, model.AuthFactorTypePassword, model.AuthFactorTypeEmailCode)
		// StepRemain=1 (one factor already done), password blacklisted so the
		// remaining factor is the email code.
		ch := seedEscalateChallenge(t, ctx, pool, accountID, 2, 1, []string{uuid.NewString()})

		if err := h.escalateChallenge(ctx, ch); err != nil {
			t.Fatalf("escalateChallenge: %v", err)
		}
		if ch.StepTotal != 2 {
			t.Fatalf("StepTotal = %d, want 2", ch.StepTotal)
		}
		if ch.StepRemain != 2 {
			t.Fatalf("StepRemain = %d, want 2", ch.StepRemain)
		}
		if len(ch.BlacklistFactors) != 0 {
			t.Fatalf("BlacklistFactors = %v, want empty", ch.BlacklistFactors)
		}
		if ch.DeclinedAt == nil {
			t.Fatal("DeclinedAt not set after escalation")
		}
		reloaded, err := st.GetAuthChallenge(ctx, uuid.MustParse(ch.Id))
		if err != nil {
			t.Fatalf("reload challenge: %v", err)
		}
		if reloaded.StepTotal != 2 || reloaded.StepRemain != 2 || reloaded.DeclinedAt == nil {
			t.Fatalf("persisted challenge state wrong: %+v", reloaded)
		}
	})

	t.Run("single-factor decline is terminal", func(t *testing.T) {
		accountID := seedRiskAccount(t, ctx, pool, model.AuthFactorTypePassword)
		ch := seedEscalateChallenge(t, ctx, pool, accountID, 1, 1, nil)

		if err := h.escalateChallenge(ctx, ch); err != nil {
			t.Fatalf("escalateChallenge: %v", err)
		}
		if ch.StepRemain != 1 {
			t.Fatalf("StepRemain = %d, want unchanged 1 (no higher level to escalate to)", ch.StepRemain)
		}
		if ch.DeclinedAt == nil {
			t.Fatal("DeclinedAt not set for terminal single-factor decline")
		}
		reloaded, err := st.GetAuthChallenge(ctx, uuid.MustParse(ch.Id))
		if err != nil {
			t.Fatalf("reload challenge: %v", err)
		}
		if reloaded.StepRemain != 1 || reloaded.DeclinedAt == nil {
			t.Fatalf("persisted challenge state wrong: %+v", reloaded)
		}
	})
}

// TestMaybeEscalateFailure verifies that failed attempts above the
// configured threshold bump the challenge step count, while preserving
// already-completed factors. Lockoff mode skips escalation.
func TestMaybeEscalateFailure(t *testing.T) {
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
	cfg := &config.Config{}
	cfg.Security.ChallengeFailEscalateAfter = 2
	h := &handler{d: Deps{Store: st, Cfg: cfg}}

	t.Run("escalates past threshold preserving completed factors", func(t *testing.T) {
		accountID := seedRiskAccount(t, ctx, pool, model.AuthFactorTypePassword, model.AuthFactorTypeEmailCode)
		blacklisted := []string{uuid.NewString()}
		ch := seedEscalateChallenge(t, ctx, pool, accountID, 1, 1, blacklisted)
		ch.FailedAttempts = 3 // above threshold (2)

		if err := h.maybeEscalateFailure(ctx, ch); err != nil {
			t.Fatalf("maybeEscalateFailure: %v", err)
		}
		if ch.StepTotal != 2 {
			t.Fatalf("StepTotal = %d, want 2", ch.StepTotal)
		}
		if ch.StepRemain != 1 { // 2 total - 1 blacklisted = 1
			t.Fatalf("StepRemain = %d, want 1 (2 total - 1 blacklisted)", ch.StepRemain)
		}
		if len(ch.BlacklistFactors) != 1 {
			t.Fatalf("BlacklistFactors = %v, want unchanged", ch.BlacklistFactors)
		}
	})

	t.Run("does not escalate below threshold", func(t *testing.T) {
		accountID := seedRiskAccount(t, ctx, pool, model.AuthFactorTypePassword, model.AuthFactorTypeEmailCode)
		ch := seedEscalateChallenge(t, ctx, pool, accountID, 1, 1, nil)
		ch.FailedAttempts = 2 // at threshold, not above

		if err := h.maybeEscalateFailure(ctx, ch); err != nil {
			t.Fatalf("maybeEscalateFailure: %v", err)
		}
		if ch.StepTotal != 1 {
			t.Fatalf("StepTotal = %d, want 1 (at threshold, not above)", ch.StepTotal)
		}
	})

	t.Run("lockoff skips escalation", func(t *testing.T) {
		accountID := seedRiskAccount(t, ctx, pool, model.AuthFactorTypePassword, model.AuthFactorTypeEmailCode)
		ch := seedEscalateChallenge(t, ctx, pool, accountID, 1, 1, nil)
		ch.FailedAttempts = 5

		if err := st.SetSecurityMode(ctx, accountID, model.SecurityModeLockoff); err != nil {
			t.Fatalf("set security mode: %v", err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(ctx, `UPDATE accounts SET security_mode = 0 WHERE id = $1`, accountID) })

		if err := h.maybeEscalateFailure(ctx, ch); err != nil {
			t.Fatalf("maybeEscalateFailure: %v", err)
		}
		if ch.StepTotal != 1 {
			t.Fatalf("StepTotal = %d, want 1 (lockoff skips escalation)", ch.StepTotal)
		}
	})
}
