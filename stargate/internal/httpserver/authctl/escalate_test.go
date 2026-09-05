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

// seedEscalateChallenge inserts a challenge for the given account with the
// provided step/blacklist/prompt state.
func seedEscalateChallenge(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID string, stepTotal, stepRemain int, blacklist []string, promptRequested bool) *model.AuthChallenge {
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
	if promptRequested {
		ch.PromptRequestedAt = model.NewTime(now)
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
		accountID := seedRiskAccount(t, ctx, pool, model.AuthFactorTypePassword, model.AuthFactorTypeInAppCode)
		// StepRemain=1 (one factor already done), password blacklisted so the
		// remaining factor is in-app; a prompt was requested.
		ch := seedEscalateChallenge(t, ctx, pool, accountID, 2, 1, []string{uuid.NewString()}, true)

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
		if ch.PromptRequestedAt != nil {
			t.Fatal("PromptRequestedAt not cleared after escalation")
		}
		reloaded, err := st.GetAuthChallenge(ctx, uuid.MustParse(ch.Id))
		if err != nil {
			t.Fatalf("reload challenge: %v", err)
		}
		if reloaded.StepTotal != 2 || reloaded.StepRemain != 2 || reloaded.DeclinedAt == nil || reloaded.PromptRequestedAt != nil {
			t.Fatalf("persisted challenge state wrong: %+v", reloaded)
		}
	})

	t.Run("single-factor decline is terminal", func(t *testing.T) {
		accountID := seedRiskAccount(t, ctx, pool, model.AuthFactorTypePassword)
		ch := seedEscalateChallenge(t, ctx, pool, accountID, 1, 1, nil, true)

		if err := h.escalateChallenge(ctx, ch); err != nil {
			t.Fatalf("escalateChallenge: %v", err)
		}
		if ch.StepRemain != 1 {
			t.Fatalf("StepRemain = %d, want unchanged 1 (no higher level to escalate to)", ch.StepRemain)
		}
		if ch.DeclinedAt == nil {
			t.Fatal("DeclinedAt not set for terminal single-factor decline")
		}
		if ch.PromptRequestedAt != nil {
			t.Fatal("PromptRequestedAt not cleared for terminal decline")
		}
		reloaded, err := st.GetAuthChallenge(ctx, uuid.MustParse(ch.Id))
		if err != nil {
			t.Fatalf("reload challenge: %v", err)
		}
		if reloaded.StepRemain != 1 || reloaded.DeclinedAt == nil || reloaded.PromptRequestedAt != nil {
			t.Fatalf("persisted challenge state wrong: %+v", reloaded)
		}
	})
}
