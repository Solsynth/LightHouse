package progression

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	eb "src.solsynth.dev/sosys/go/pkg/eventbus"
)

// ActionLogSource is the NATS surface the consumer needs: the fleet's
// action_log_events stream, whose subjects are action_logs.<action>.
type ActionLogSource interface {
	ConsumeActionLogs(ctx context.Context, consumerName string, handler func(payload []byte) error) error
}

// actionLogEvent mirrors the C# ActionLogTriggeredEvent wire shape.
type actionLogEvent struct {
	eb.Event
	ActionLogID string         `json:"action_log_id"`
	AccountID   string         `json:"account_id"`
	Action      string         `json:"action"`
	Meta        map[string]any `json:"meta"`
	SessionID   *string        `json:"session_id,omitempty"`
	OccurredAt  time.Time      `json:"occurred_at"`
}

// StartConsumer runs the action-log consumer and returns a stop function.
func StartConsumer(ctx context.Context, service *Service, source ActionLogSource, log *slog.Logger) func() {
	if service == nil || source == nil {
		return func() {}
	}
	if log == nil {
		log = slog.Default()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := source.ConsumeActionLogs(runCtx, "stargate_progression_action_logs", func(payload []byte) error {
			return handleActionLog(runCtx, service, payload, log)
		}); err != nil {
			log.Warn("progression action-log consumer stopped", "error", err)
		}
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}

// handleActionLog applies one event. A malformed payload is acked (it can
// never succeed on redelivery); a storage error is retried.
func handleActionLog(ctx context.Context, service *Service, payload []byte, log *slog.Logger) error {
	var event actionLogEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		log.Warn("malformed action log event", "error", err)
		return nil
	}
	accountID, err := uuid.Parse(event.AccountID)
	if err != nil {
		log.Warn("action log event has invalid account id", "account_id", event.AccountID)
		return nil
	}
	eventID := event.EventID
	if eventID == "" {
		eventID = event.ActionLogID
	}
	parsed, err := uuid.Parse(eventID)
	if err != nil {
		log.Warn("action log event has invalid event id", "event_id", eventID)
		return nil
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = event.Timestamp
	}
	if err := service.ApplyActionLog(ctx, ActionLogEvent{
		EventID: parsed, AccountID: accountID, Action: event.Action,
		Meta: event.Meta, OccurredAt: event.OccurredAt,
	}); err != nil {
		log.Warn("progression action log apply failed", "action", event.Action, "error", err)
		return err
	}
	return nil
}
