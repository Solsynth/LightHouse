package realm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	eb "src.solsynth.dev/sosys/lighthouse/shared/pkg/eventbus"
)

// Background work ported from Passport's Quartz job and event listeners.

const (
	// tenureInterval mirrors RealmTenureLevelingJob's cron ("0 0 1 * * ?",
	// daily at 01:00); a daily ticker from start is the equivalent here.
	tenureInterval = 24 * time.Hour
	// tenureStartDelay lets the process finish booting before the first sweep.
	tenureStartDelay = 2 * time.Minute
)

// Events is the NATS surface the realm module consumes on.
type Events interface {
	ConsumeRealmActivity(ctx context.Context, consumerName string, handler func(payload []byte) error) error
	ConsumePaymentOrders(ctx context.Context, consumerName string, handler func(payload []byte) error) error
}

// realmActivityEvent mirrors DysonNetwork.Shared.Queue.RealmActivityEvent.
type realmActivityEvent struct {
	eb.Event
	RealmID      string `json:"realm_id"`
	AccountID    string `json:"account_id"`
	ActivityType string `json:"activity_type"`
	ReferenceID  string `json:"reference_id"`
	Delta        int    `json:"delta"`
}

// paymentOrderEvent mirrors DysonNetwork.Shared.Queue.PaymentOrderEvent.
type paymentOrderEvent struct {
	eb.Event
	OrderID           string         `json:"order_id"`
	WalletID          string         `json:"wallet_id"`
	AccountID         string         `json:"account_id"`
	AppIdentifier     *string        `json:"app_identifier,omitempty"`
	ProductIdentifier *string        `json:"product_identifier,omitempty"`
	Meta              map[string]any `json:"meta"`
	Status            int            `json:"status"`
}

// StartJobs launches the realm background work under ctx and returns a stop
// function that cancels every goroutine and waits for it.
func StartJobs(ctx context.Context, service *Service, events Events, log *slog.Logger) func() {
	if service == nil {
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
		runTenure(runCtx, service, log)
	}()

	if events != nil {
		wg.Add(2)
		go func() {
			defer wg.Done()
			consumeRealmActivity(runCtx, service, events, log)
		}()
		go func() {
			defer wg.Done()
			consumePayments(runCtx, service, events, log)
		}()
	}

	return func() {
		cancel()
		wg.Wait()
	}
}

// runTenure grants the daily realm tenure XP (RealmTenureLevelingJob). The
// (reason_type, reason) key is the day's date, so a restart inside the same
// day never double-grants.
func runTenure(ctx context.Context, service *Service, log *slog.Logger) {
	timer := time.NewTimer(tenureStartDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		grantTenure(ctx, service, log, time.Now().UTC())
		timer.Reset(tenureInterval)
	}
}

func grantTenure(ctx context.Context, service *Service, log *slog.Logger, now time.Time) {
	members, err := service.Store.TenureMembers(ctx)
	if err != nil {
		log.Error("realm tenure listing failed", "error", err)
		return
	}
	day := now.Format("2006-01-02")
	granted := 0
	for _, member := range members {
		if member.JoinedAt == nil || !member.JoinedAt.Before(now) {
			continue
		}
		record, err := service.AddExperience(ctx, member.RealmID, member.AccountID,
			ReasonTenure, day, TenureDailyXP, nil)
		if err != nil {
			log.Warn("realm tenure grant failed",
				"realm", member.RealmID, "account", member.AccountID, "error", err)
			continue
		}
		if record != nil {
			granted++
		}
	}
	log.Info("realm tenure sweep complete", "members", len(members), "granted", granted, "day", day)
}

// consumeRealmActivity applies realm.activity.<type> events (Sphere posts,
// Messager chat messages). The C# listener granted `realm.activity.<type>` XP
// with a one-minute cooldown for chat messages and no cooldown otherwise.
func consumeRealmActivity(ctx context.Context, service *Service, events Events, log *slog.Logger) {
	err := events.ConsumeRealmActivity(ctx, "stargate_realm_activity_xp", func(payload []byte) error {
		var event realmActivityEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			log.Warn("malformed realm.activity event", "error", err)
			return nil
		}
		realmID, err := uuid.Parse(event.RealmID)
		if err != nil {
			log.Warn("realm.activity event has invalid realm id", "realm_id", event.RealmID)
			return nil
		}
		accountID, err := uuid.Parse(event.AccountID)
		if err != nil {
			log.Warn("realm.activity event has invalid account id", "account_id", event.AccountID)
			return nil
		}
		var cooldown *time.Duration
		if event.ActivityType == "chat_message" {
			cooldown = new(time.Minute)
		}
		record, err := service.AddExperience(ctx, realmID, accountID,
			ReasonActivity+event.ActivityType, event.ReferenceID, event.Delta, cooldown)
		if err != nil {
			return err
		}
		if record != nil {
			log.Debug("granted realm activity xp", "type", event.ActivityType,
				"realm", realmID, "account", accountID, "delta", record.Delta)
		}
		return nil
	})
	if err != nil {
		log.Warn("realm activity consumer stopped", "error", err)
	}
}

// consumePayments applies realms.boost orders (PaymentOrderEventBase with
// product identifier "realms.boost"). The C# listener recorded the
// contribution, refreshed the boost points, and granted shares × 100 XP.
func consumePayments(ctx context.Context, service *Service, events Events, log *slog.Logger) {
	err := events.ConsumePaymentOrders(ctx, "stargate_realm_boost_orders", func(payload []byte) error {
		var event paymentOrderEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			log.Warn("malformed payment_orders event", "error", err)
			return nil
		}
		if event.ProductIdentifier == nil || *event.ProductIdentifier != "realms.boost" {
			return nil
		}
		contribution, shares, err := boostFromOrder(event.OrderID, event.Meta)
		if err != nil {
			log.Warn("payment_orders realm boost has unusable metadata", "order", event.OrderID, "error", err)
			return nil
		}
		if err := service.RecordBoost(ctx, contribution); err != nil {
			if errors.Is(err, ErrNotFound) {
				log.Warn("payment_orders realm boost references a missing realm",
					"order", event.OrderID, "realm", contribution.RealmID)
				return nil
			}
			return err
		}
		if shares > 0 {
			if _, err := service.AddExperience(ctx, contribution.RealmID, contribution.AccountID,
				"realm.boost.purchase", contribution.OrderID.String(), BoostShareXP*shares, nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		log.Warn("payment order consumer stopped", "error", err)
	}
}

// boostFromOrder reads the metadata the boost controller wrote. The C# side
// also accepted a replayed event whose meta lived on the order, so the
// amount/currency fall back to the scanned values when meta omits them.
func boostFromOrder(rawOrderID string, meta map[string]any) (BoostContribution, int, error) {
	orderID, err := uuid.Parse(rawOrderID)
	if err != nil {
		return BoostContribution{}, 0, fmt.Errorf("order_id: %w", err)
	}
	realmID, err := uuid.Parse(metaString(meta, "realm_id"))
	if err != nil {
		return BoostContribution{}, 0, fmt.Errorf("realm_id: %w", err)
	}
	accountID, err := uuid.Parse(metaString(meta, "account_id"))
	if err != nil {
		return BoostContribution{}, 0, fmt.Errorf("account_id: %w", err)
	}
	currency, err := normalizeCurrency(metaString(meta, "currency"))
	if err != nil {
		return BoostContribution{}, 0, err
	}
	amount, err := decimal.NewFromString(metaString(meta, "amount"))
	if err != nil {
		return BoostContribution{}, 0, fmt.Errorf("amount: %w", err)
	}
	shares := intFromAny(meta["shares"])
	if shares <= 0 {
		// A publisher that dropped the share count still knows the price; the
		// currency ratio recovers it.
		recovered := amount.Div(perSharePrice(currency))
		shares = int(recovered.IntPart())
	}
	return BoostContribution{
		RealmID: realmID, AccountID: accountID, Currency: currency,
		Amount: NewDecimal(amount), OrderID: orderID,
	}, shares, nil
}

func metaString(meta map[string]any, key string) string {
	value, ok := meta[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return decimal.NewFromFloat(typed).String()
	default:
		return fmt.Sprint(typed)
	}
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0
		}
		return int(parsed)
	case string:
		parsed, err := decimal.NewFromString(typed)
		if err != nil {
			return 0
		}
		return int(parsed.IntPart())
	default:
		return 0
	}
}
