package realm

import (
	"context"
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

// ErrUnavailable marks a collaborator (payments, files) that is not
// configured, mirroring ports.ErrUnavailable.
var ErrUnavailable = errors.New("realm: collaborator unavailable")

// LabelCap is the number of labels a boost level allows.
func LabelCap(level int) int { return labelCap(level) }

// NormalizeCurrency validates an incoming boost currency.
func NormalizeCurrency(currency string) (string, error) { return normalizeCurrency(currency) }

// AmountForShares is the price of a boost in the given currency.
func AmountForShares(currency string, shares int) (Decimal, error) {
	amount, err := amountForShares(currency, shares)
	if err != nil {
		return Decimal{}, err
	}
	return NewDecimal(amount), nil
}

// BoostCutoff is the start of the active contribution window at now.
func BoostCutoff(now time.Time) time.Time { return boostCutoff(now) }

// Aliases for the permission rows, so the controller package can build them
// without exporting the entity field names twice.
type RolePermissionEntity = RealmRolePermissionEntity

type UserPermissionEntity = RealmUserPermissionEntity

// perSharePrice is the price of one share in the given currency.
func perSharePrice(currency string) decimal.Decimal {
	if currency == PointsCurrency {
		return pointsPerShare
	}
	return goldsPerShare
}

// ApplyPaidOrder is the in-process counterpart of the payment_orders consumer:
// it applies an order the local wallet just paid. It is idempotent (the
// contribution is deduplicated by order id), so a configured event bus that
// delivers the same order again is a no-op.
func (s *Service) ApplyPaidOrder(ctx context.Context, order PaymentOrder) error {
	if order.ProductIdentifier != "realms.boost" {
		return nil
	}
	contribution, shares, err := boostFromOrder(order.OrderID, order.Meta)
	if err != nil {
		s.Log.Warn("realm boost order has unusable metadata", "order", order.OrderID, "error", err)
		return nil
	}
	if err := s.RecordBoost(ctx, contribution); err != nil {
		if errors.Is(err, ErrNotFound) {
			s.Log.Warn("realm boost order references a missing realm", "realm", contribution.RealmID)
			return nil
		}
		return err
	}
	if shares > 0 {
		_, err = s.AddExperience(ctx, contribution.RealmID, contribution.AccountID,
			"realm.boost.purchase", contribution.OrderID.String(), BoostShareXP*shares, nil)
	}
	return err
}

// PaymentOrder is one paid order as the in-process subscriber sees it.
type PaymentOrder struct {
	OrderID           string
	ProductIdentifier string
	Meta              map[string]any
}
