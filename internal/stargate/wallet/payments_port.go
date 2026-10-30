package wallet

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"src.solsynth.dev/sosys/stargate/ports"
)

// PaymentsProvider publishes ports.Payments over the local wallet service, so
// a feature module in the same binary (the realm boost flow) opens its orders
// through the ledger instead of calling itself over gRPC.
type PaymentsProvider struct {
	Service *Service
}

// Available reports whether the provider is wired.
func (p *PaymentsProvider) Available() bool { return p != nil && p.Service != nil }

// CreateOrder opens an unpaid order for the caller's product. The account's
// primary wallet is not required: the C# realm boost controller only created
// the order and let Checkout pick the payer.
func (p *PaymentsProvider) CreateOrder(ctx context.Context, req ports.OrderRequest) (*ports.CreatedOrder, error) {
	if !p.Available() {
		return nil, ports.ErrUnavailable
	}
	if req.Amount.IsNegative() {
		return nil, errors.New("order amount must not be negative")
	}
	in := OrderInput{
		Currency:          req.Currency,
		Amount:            req.Amount,
		ProductIdentifier: req.ProductIdentifier,
		AppIdentifier:     req.AppIdentifier,
		Remarks:           req.Remarks,
		Meta:              req.Meta,
		Reuseable:         req.Reuseable,
	}
	// The C# RemotePaymentService defaulted the app identifier to "internal"
	// when the caller (Passport's own controllers) did not set one.
	if in.AppIdentifier == nil || strings.TrimSpace(*in.AppIdentifier) == "" {
		in.AppIdentifier = new("internal")
	}
	if accountID := strings.TrimSpace(req.AccountID); accountID != "" {
		if id, err := uuid.Parse(accountID); err == nil {
			wallets, err := p.Service.GetAccountWallets(ctx, id)
			if err != nil {
				return nil, err
			}
			if len(wallets) > 0 {
				in.PayerWalletID = &wallets[0].ID
			}
		}
	}
	order, err := p.Service.CreateOrder(ctx, in)
	if err != nil {
		return nil, err
	}
	return &ports.CreatedOrder{ID: order.ID.String(), Amount: order.Amount}, nil
}

var _ ports.Payments = (*PaymentsProvider)(nil)

// OrderEventsProvider exposes the local order hooks as a port, so a feature
// module in the same binary applies its product without an event bus.
// Subscribers run after the wallet's own hooks, on the same goroutine as the
// payment.
type OrderEventsProvider struct {
	Service *Service
}

// Available reports whether the provider is wired.
func (p *OrderEventsProvider) Available() bool { return p != nil && p.Service != nil }

// OnOrderPaid registers a subscriber. The wallet deduplicates nothing here:
// the subscriber must be idempotent, since the fleet's NATS consumer may also
// deliver the same order when a bus is configured.
func (p *OrderEventsProvider) OnOrderPaid(handler func(ctx context.Context, event ports.OrderEvent) error) {
	if !p.Available() || handler == nil {
		return
	}
	p.Service.OrderHooks = append(p.Service.OrderHooks, func(ctx context.Context, order *WalletOrderEntity) error {
		product := ""
		if order.ProductIdentifier != nil {
			product = *order.ProductIdentifier
		}
		return handler(ctx, ports.OrderEvent{
			OrderID: order.ID.String(), ProductIdentifier: product, Meta: orderMeta(order),
		})
	})
}

var _ ports.OrderEvents = (*OrderEventsProvider)(nil)

// Credit implements ports.Creditor: it mints currency into the account's
// wallet with a system transaction, the way the C# payment service paid
// progression rewards (a null payer means the system is the source).
//
// A missing wallet is not an error: the C# service logged and skipped the
// reward, treating the grant as delivered. The same happens here, so a
// progression reward never blocks on wallet provisioning.
func (p *PaymentsProvider) Credit(ctx context.Context, accountID, currency, remarks string, amount int64) error {
	if !p.Available() {
		return ports.ErrUnavailable
	}
	if amount <= 0 {
		return nil
	}
	parsed, err := uuid.Parse(accountID)
	if err != nil {
		return err
	}
	sum := decimal.NewFromInt(amount)
	if _, err := p.Service.CreateTransactionWithAccounts(ctx, nil, &parsed, currency, sum, &remarks, TransactionSystem); err != nil {
		if isMissingWallet(err) {
			p.Service.Log.Warn("skipping progression points reward: account has no wallet",
				"account", accountID, "currency", currency, "error", err)
			return nil
		}
		return err
	}
	return nil
}

// isMissingWallet matches the C# IsMissingWalletError heuristic.
func isMissingWallet(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "wallet was not found") ||
		strings.Contains(strings.ToLower(err.Error()), "wallet not found")
}

var _ ports.Creditor = (*PaymentsProvider)(nil)
