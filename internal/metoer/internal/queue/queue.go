// Package queue provides the in-process async dispatcher for push
// notifications and emails. It replaces the C# pusher_queue JetStream
// transport: jobs are queued on a buffered channel and drained by a worker
// pool. Delivery is exactly-once-per-enqueue — there is no redelivery, no
// consumer state and no replay-on-restart; jobs still buffered at shutdown
// are lost (non-durable by design).
package queue

import (
	"context"
	"log/slog"
	"sync"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/metoer/internal/model"
)

// JobType mirrors the old pusher_queue QueueMessageType (declaration order
// preserved).
type JobType int

const (
	JobTypeEmail JobType = iota
	JobTypePushNotification
)

// EmailJob carries an outbound email (EnqueueEmail).
type EmailJob struct {
	ToName    string
	ToAddress string
	Subject   string
	Body      string
}

// PushJob carries a push notification delivery (EnqueuePushNotification).
// The notification is passed by reference: timestamps and app id survive,
// so DeliverPushNotification sees the real created_at (no wire round-trip).
type PushJob struct {
	Notification               *model.Notification
	ExcludedWebSocketDeviceIDs []string
	IsSavable                  bool
}

// Job is one unit of async work.
type Job struct {
	Type  JobType
	Email *EmailJob
	Push  *PushJob
}

// MessageHandler processes one job. Returning an error is logged; there is
// no retry or redelivery — delivery failures are terminal by design.
type MessageHandler func(ctx context.Context, job *Job) error

// Service is the in-process dispatcher: producers send Jobs on a buffered
// channel, Run starts the worker pool that drains it.
type Service struct {
	jobs    chan *Job
	workers int
	log     *slog.Logger

	mu      sync.Mutex
	handler MessageHandler
}

// New builds the dispatcher. workers is the pool size (0 → 1, mirroring the
// C# consumer-count fallback); the buffer is fixed at 1024 jobs.
func New(workers int, log *slog.Logger) *Service {
	if workers < 1 {
		workers = 1
	}
	return &Service{jobs: make(chan *Job, 1024), workers: workers, log: log}
}

// SetHandler registers the job dispatch (must be set before Run).
func (s *Service) SetHandler(handler MessageHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler = handler
}

// EnqueueEmail queues an outbound email (EnqueueEmail). Blocks while the
// buffer is full, honoring ctx cancellation.
func (s *Service) EnqueueEmail(ctx context.Context, toName, toAddress, subject, body string) error {
	return s.enqueue(ctx, &Job{Type: JobTypeEmail, Email: &EmailJob{ToName: toName, ToAddress: toAddress, Subject: subject, Body: body}})
}

// EnqueuePushNotification queues a push notification delivery
// (EnqueuePushNotification). The notification's AccountId is set to userID
// first (parity with the C# mutating behavior).
func (s *Service) EnqueuePushNotification(ctx context.Context, notification *model.Notification, userID uuid.UUID, excludedWebSocketDeviceIDs []string, isSavable bool) error {
	notification.AccountId = userID
	return s.enqueue(ctx, &Job{Type: JobTypePushNotification, Push: &PushJob{Notification: notification, ExcludedWebSocketDeviceIDs: excludedWebSocketDeviceIDs, IsSavable: isSavable}})
}

func (s *Service) enqueue(ctx context.Context, job *Job) error {
	select {
	case s.jobs <- job:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Run starts the worker pool and blocks until ctx is cancelled. Jobs still
// buffered at shutdown are lost (in-process, non-durable by design).
func (s *Service) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for range s.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job := <-s.jobs:
					s.dispatch(ctx, job)
				}
			}
		}()
	}
	wg.Wait()
	return nil
}

func (s *Service) dispatch(ctx context.Context, job *Job) {
	if job == nil {
		return
	}
	s.mu.Lock()
	handler := s.handler
	s.mu.Unlock()
	if handler == nil {
		s.log.Warn("no queue handler registered; dropping job", "type", job.Type)
		return
	}
	if err := handler(ctx, job); err != nil {
		s.log.Error("job processing failed", "type", job.Type, "error", err)
	}
}
