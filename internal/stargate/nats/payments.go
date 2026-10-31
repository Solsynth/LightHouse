package nats

import (
	"context"
	"time"

	"github.com/google/uuid"

	eb "src.solsynth.dev/sosys/lighthouse/shared/pkg/eventbus"
)

// Fleet streams whose subjects are not the shared account_events stream.
const (
	paymentEventsStream   = "payment_events"
	paymentOrdersSubject  = "payment_orders"
	realmActivitiesStream = "realm_activity_events"
	realmActivityPrefix   = "realm.activity."
)

// paymentOrderEvent mirrors DysonNetwork.Shared.Queue.PaymentOrderEventBase.
// The wallet publishes it after an order is paid; Passport's consumers
// (realm boosts, name-change cards, affiliation invites) read the metadata it
// carries. The C# EventBase serializes the envelope fields, so they stay.
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

// PublishOrderPaid emits payment_orders on the payment_events stream after the
// order reaches the paid state. A replayed call re-emits (the C# wallet did
// the same for an already-paid order); consumers deduplicate by order id.
func (c *Client) PublishOrderPaid(ctx context.Context, order PaidOrder) error {
	if c == nil || c.Conn == nil {
		return nil
	}
	return c.PublishJetStream(ctx, paymentOrdersSubject, paymentEventsStream, paymentOrderEvent{
		Event: eb.Event{
			EventID:    uuid.NewString(),
			Timestamp:  time.Now().UTC(),
			StreamName: paymentEventsStream,
			EventType:  paymentOrdersSubject,
		},
		OrderID:           order.OrderID,
		WalletID:          order.WalletID,
		AccountID:         order.AccountID,
		AppIdentifier:     order.AppIdentifier,
		ProductIdentifier: order.ProductIdentifier,
		Meta:              order.Meta,
		Status:            order.Status,
	})
}

// PaidOrder is the subset of a paid order the payment event carries.
type PaidOrder struct {
	OrderID           string
	WalletID          string
	AccountID         string
	AppIdentifier     *string
	ProductIdentifier *string
	Meta              map[string]any
	Status            int
}

// ConsumeRealmActivity runs a durable consumer over realm.activity.> on the
// realm_activity_events stream. Sphere and Messager publish realm XP signals
// there (chat messages, published posts).
func (c *Client) ConsumeRealmActivity(ctx context.Context, consumerName string, handler func(payload []byte) error) error {
	if c == nil || c.Conn == nil {
		return nil
	}
	return c.Consume(ctx, realmActivitiesStream, realmActivityPrefix+">", consumerName, handler)
}

// ConsumePaymentOrders runs a durable consumer over payment_orders on the
// payment_events stream.
func (c *Client) ConsumePaymentOrders(ctx context.Context, consumerName string, handler func(payload []byte) error) error {
	if c == nil || c.Conn == nil {
		return nil
	}
	return c.Consume(ctx, paymentEventsStream, paymentOrdersSubject, consumerName, handler)
}

// ConsumeActionLogs runs a durable consumer over the action_log_events stream.
// Subjects are action_logs.<action>; the progression module listens to every
// tracked action.
func (c *Client) ConsumeActionLogs(ctx context.Context, consumerName string, handler func(payload []byte) error) error {
	if c == nil || c.Conn == nil {
		return nil
	}
	return c.Consume(ctx, actionLogEventsStream, actionLogsSubjectPrefix+">", consumerName, handler)
}
