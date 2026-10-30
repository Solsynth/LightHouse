package queue

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/metoer/internal/model"
)

// newTestService starts a one-worker dispatcher with the given handler.
func newTestService(t *testing.T, handler MessageHandler) *Service {
	t.Helper()
	svc := New(1, slog.Default())
	svc.SetHandler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	go svc.Run(ctx)
	t.Cleanup(cancel)
	return svc
}

// TestEnqueueDeliversPushJob pins the in-process contract: an enqueued push
// job reaches the handler with the notification by reference.
func TestEnqueueDeliversPushJob(t *testing.T) {
	delivered := make(chan *PushJob, 1)
	svc := newTestService(t, func(ctx context.Context, job *Job) error {
		delivered <- job.Push
		return nil
	})

	n := &model.Notification{ModelBase: model.ModelBase{Id: uuid.New()}, AccountId: uuid.New()}
	if err := svc.EnqueuePushNotification(context.Background(), n, n.AccountId, []string{"d1"}, true); err != nil {
		t.Fatal(err)
	}
	select {
	case job := <-delivered:
		if job.Notification != n {
			t.Fatal("notification must be delivered by reference")
		}
		if !job.IsSavable || len(job.ExcludedWebSocketDeviceIDs) != 1 || job.ExcludedWebSocketDeviceIDs[0] != "d1" {
			t.Fatalf("push job fields must round-trip: %+v", job)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("push job was not dispatched")
	}
}

// TestEnqueueDeliversEmailJob pins the email job fields.
func TestEnqueueDeliversEmailJob(t *testing.T) {
	delivered := make(chan *EmailJob, 1)
	svc := newTestService(t, func(ctx context.Context, job *Job) error {
		delivered <- job.Email
		return nil
	})

	if err := svc.EnqueueEmail(context.Background(), "A", "a@b.c", "S", "B"); err != nil {
		t.Fatal(err)
	}
	select {
	case job := <-delivered:
		if job.ToName != "A" || job.ToAddress != "a@b.c" || job.Subject != "S" || job.Body != "B" {
			t.Fatalf("email fields mismatch: %+v", job)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("email job was not dispatched")
	}
}

// TestEnqueueCanceledContext pins that a canceled caller context fails the
// enqueue instead of blocking forever on a full buffer.
func TestEnqueueCanceledContext(t *testing.T) {
	svc := New(1, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := svc.EnqueueEmail(ctx, "A", "a@b.c", "S", "B"); err == nil {
		t.Fatal("enqueue with canceled ctx must return an error")
	}
}

// TestDispatchErrorsAreLogged pins that a failing handler neither crashes
// the worker nor stops the queue from draining.
func TestDispatchErrorsAreLogged(t *testing.T) {
	var mu sync.Mutex
	count := 0
	svc := newTestService(t, func(ctx context.Context, job *Job) error {
		mu.Lock()
		count++
		mu.Unlock()
		return fmt.Errorf("boom")
	})

	for i := 0; i < 3; i++ {
		if err := svc.EnqueueEmail(context.Background(), "A", "a@b.c", "S", "B"); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		c := count
		mu.Unlock()
		if c >= 3 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of 3 jobs dispatched", c)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
