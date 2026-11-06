package authctl

// Concurrency pins for the auth-challenge state machine: approval, decline and
// step consumption are single-shot, and failed attempts add up under parallel
// submissions. The handler used to read the row, mutate it and write the whole
// row back, so two parallel requests both "won" or both overwrote the count.

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/model"
)

func seedAtomicChallenge(t *testing.T, dr *declineHarness) string {
	t.Helper()
	now := time.Now().UTC()
	challenge := &model.AuthChallenge{
		Id:         uuid.NewString(),
		AccountId:  dr.accountID,
		DeviceId:   "atomic-test-device",
		UserAgent:  new("atomic-test-agent"),
		IpAddress:  new(declineRequesterIP),
		StepTotal:  1,
		StepRemain: 1,
		ExpiredAt:  model.NewTime(now.Add(time.Hour)),
		CreatedAt:  model.NewTime(now),
		UpdatedAt:  model.NewTime(now),
	}
	if err := dr.h.d.Store.CreateAuthChallenge(dr.ctx, challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	return challenge.Id
}

func TestApproveAuthChallengeIsSingleShot(t *testing.T) {
	dr := newDeclineHarness(t, 0, model.AuthFactorTypePassword)
	id := uuid.MustParse(seedAtomicChallenge(t, dr))
	sessionID := uuid.New()
	accountID := uuid.MustParse(dr.accountID)

	const workers = 8
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := dr.h.d.Store.ApproveAuthChallenge(dr.ctx, id, &accountID, &sessionID, time.Now().UTC())
			if err != nil {
				t.Errorf("approve: %v", err)
				return
			}
			if ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := wins.Load(); got != 1 {
		t.Fatalf("concurrent approvals: %d winners, want exactly 1", got)
	}
}

func TestConsumeAuthChallengeStepIsSingleShot(t *testing.T) {
	dr := newDeclineHarness(t, 0, model.AuthFactorTypePassword)
	id := uuid.MustParse(seedAtomicChallenge(t, dr))

	const workers = 8
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := dr.h.d.Store.ConsumeAuthChallengeStep(dr.ctx, id, "factor-1", time.Now().UTC(), nil)
			if err != nil {
				t.Errorf("consume step: %v", err)
				return
			}
			if ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := wins.Load(); got != 1 {
		t.Fatalf("concurrent step consumption: %d winners, want exactly 1", got)
	}
	challenge, err := dr.h.d.Store.GetAuthChallenge(dr.ctx, id)
	if err != nil {
		t.Fatalf("load challenge: %v", err)
	}
	if challenge.StepRemain != 0 {
		t.Fatalf("step_remain = %d after one successful consume, want 0", challenge.StepRemain)
	}
}
func TestIncrementAuthChallengeFailuresAddsUp(t *testing.T) {
	dr := newDeclineHarness(t, 0, model.AuthFactorTypePassword)
	id := uuid.MustParse(seedAtomicChallenge(t, dr))

	const workers = 10
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := dr.h.d.Store.IncrementAuthChallengeFailures(dr.ctx, id, time.Now().UTC()); err != nil {
				t.Errorf("increment: %v", err)
			}
		}()
	}
	wg.Wait()
	challenge, err := dr.h.d.Store.GetAuthChallenge(dr.ctx, id)
	if err != nil {
		t.Fatalf("load challenge: %v", err)
	}
	if challenge.FailedAttempts != workers {
		t.Fatalf("failed_attempts = %d, want %d", challenge.FailedAttempts, workers)
	}
}
