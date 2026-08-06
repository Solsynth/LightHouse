// Package queue ports DysonNetwork.Ring.Services.QueueService +
// QueueBackgroundService: the JetStream `pusher_queue` stream producer and
// its durable, load-balanced consumer. Wire formats are pinned by tests in
// queue_test.go (the empirically verified C# shapes).
package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"

	eb "src.solsynth.dev/sosys/go/pkg/eventbus"

	"src.solsynth.dev/sosys/metoer/internal/model"
)

// Names mirror QueueBackgroundService constants verbatim.
const (
	QueueName   = "pusher_queue"
	StreamName  = "pusher_queue"
	QueueGroup  = "pusher_workers"
	ConsumerName = "pusher_workers"
)

// MessageHandler processes one queue envelope. Returning an error nacks the
// message (redelivery); returning nil acks it.
type MessageHandler func(ctx context.Context, msg *model.QueueMessage) error

// Service is the queue producer/consumer (thread-safe; singleton).
type Service struct {
	bus           *eb.Bus
	consumerCount int
	log           *slog.Logger
	handler       MessageHandler

	ensureOnce sync.Once
	ensureErr  error
}

// New builds the queue service over the event bus. consumerCount is the
// configured ConsumerCount (0 → resolved by the caller, mirroring the C#
// `?? Environment.ProcessorCount` fallback).
func New(bus *eb.Bus, consumerCount int, log *slog.Logger) *Service {
	return &Service{bus: bus, consumerCount: consumerCount, log: log}
}

func (s *Service) ensureStream(ctx context.Context) error {
	if s.bus == nil || s.bus.Conn == nil {
		return nil
	}
	s.ensureOnce.Do(func() {
		s.ensureErr = s.bus.EnsureStream(ctx, StreamName, []string{QueueName})
	})
	return s.ensureErr
}

// EnqueueEmail queues an Email queue message (EnqueueEmail).
func (s *Service) EnqueueEmail(ctx context.Context, toName, toAddress, subject, body string) error {
	data, err := json.Marshal(model.EmailMessage{ToName: toName, ToAddress: toAddress, Subject: subject, Body: body})
	if err != nil {
		return err
	}
	return s.publish(ctx, &model.QueueMessage{
		Type: model.QueueMessageTypeEmail,
		Data: string(data),
	})
}

// EnqueuePushNotification queues a PushNotification message
// (EnqueuePushNotification). The notification's AccountId is set to userID
// first (the C# mutates the passed object).
func (s *Service) EnqueuePushNotification(ctx context.Context, notification *model.Notification, userID uuid.UUID, excludedWebSocketDeviceIDs []string, isSavable bool) error {
	notification.AccountId = userID
	data, err := json.Marshal(model.NewQueueNotification(notification))
	if err != nil {
		return err
	}
	target := userID.String()
	return s.publish(ctx, &model.QueueMessage{
		Type:                      model.QueueMessageTypePushNotification,
		TargetId:                  &target,
		Data:                      string(data),
		ExcludedWebSocketDeviceIds: excludedWebSocketDeviceIDs,
		IsSavable:                 isSavable,
	})
}

func (s *Service) publish(ctx context.Context, msg *model.QueueMessage) error {
	if s.bus == nil || s.bus.Conn == nil {
		return nil
	}
	if err := s.ensureStream(ctx); err != nil {
		return fmt.Errorf("ensure pusher_queue stream: %w", err)
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := s.bus.JS.Publish(ctx, QueueName, raw); err != nil {
		return fmt.Errorf("publish to %s: %w", QueueName, err)
	}
	return nil
}

// SetHandler registers the message dispatch (wired from main to break the
// queue ↔ push/email import cycle).
func (s *Service) SetHandler(handler MessageHandler) { s.handler = handler }

// Run starts consumerCount consumer goroutines over the shared durable
// consumer (deliver group pusher_workers → the server load-balances, exactly
// like the C# per-task ConsumeAsync loops) and blocks until ctx is cancelled.
// Messages published while down survive and are redelivered on boot
// (DeliverPolicy All on first creation; ack-tracked afterwards).
func (s *Service) Run(ctx context.Context) error {
	if s.bus == nil || s.bus.Conn == nil || s.bus.JS == nil {
		s.log.Warn("queue consumer disabled (nats unavailable)")
		return nil
	}
	if s.consumerCount < 1 {
		s.consumerCount = 1
	}
	if err := s.ensureStream(ctx); err != nil {
		return fmt.Errorf("queue stream: %w", err)
	}

	consumer, err := s.bus.JS.CreateOrUpdateConsumer(ctx, StreamName, jetstream.ConsumerConfig{
		Name:          ConsumerName,
		FilterSubject: QueueName,
		DeliverGroup:  QueueGroup,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
		MaxDeliver:    5,
	})
	if err != nil {
		return fmt.Errorf("create pusher_workers consumer: %w", err)
	}

	s.log.Info("starting queue consumers", "count", s.consumerCount)
	var wg sync.WaitGroup
	for range s.consumerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cc, err := consumer.Consume(func(msg jetstream.Msg) {
				if err := s.process(ctx, msg); err != nil {
					s.log.Error("queue message processing failed", "subject", msg.Subject(), "error", err)
					_ = msg.Nak()
					return
				}
				_ = msg.Ack()
			})
			if err != nil {
				s.log.Error("queue consumer failed to start", "error", err)
				return
			}
			<-ctx.Done()
			cc.Stop()
		}()
	}
	wg.Wait()
	return nil
}

func (s *Service) process(ctx context.Context, msg jetstream.Msg) error {
	var envelope model.QueueMessage
	if err := json.Unmarshal(msg.Data(), &envelope); err != nil {
		// Invalid message format → C# logs a warning and acks.
		s.log.Warn("invalid queue message format", "error", err)
		return nil
	}
	if s.handler == nil {
		s.log.Warn("no queue message handler registered; dropping message", "type", envelope.Type)
		return nil
	}
	return s.handler(ctx, &envelope)
}
