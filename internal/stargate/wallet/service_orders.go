package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// TransactionInput describes one ledger entry.
type TransactionInput struct {
	PayerWalletID       *uuid.UUID
	PayeeWalletID       *uuid.UUID
	Currency            string
	Amount              decimal.Decimal
	Remarks             *string
	Type                int
	Silent              bool
	Force               bool
	Freeze              bool
	RequireConfirmation bool
	HoldInEscrow        bool
}

// CreateTransaction applies the C# ledger rules: the payer is debited
// immediately, pending/frozen amounts are additionally tracked as held, and
// the payee is credited only for confirmed entries that are not escrowed.
func (s *Service) CreateTransaction(ctx context.Context, in TransactionInput) (*WalletTransactionEntity, error) {
	if in.PayerWalletID == nil && in.PayeeWalletID == nil {
		return nil, errors.New("at least one wallet must be specified")
	}
	if in.Amount.LessThanOrEqual(decimal.Zero) {
		return nil, errors.New("cannot create transaction with negative or zero amount")
	}
	amount := truncate3(in.Amount)
	now := time.Now().UTC()

	var status int
	var expiresAt, frozenAt *time.Time
	switch {
	case !in.Freeze && !in.RequireConfirmation:
		status = TransactionConfirmed
	case in.Freeze && !in.RequireConfirmation:
		status = TransactionFrozen
		frozenAt = &now
		expiry := now.Add(24 * time.Hour)
		expiresAt = &expiry
	case !in.Freeze && in.RequireConfirmation:
		status = TransactionPending
		expiry := now.Add(24 * time.Hour)
		expiresAt = &expiry
	default:
		status = TransactionFrozen
		frozenAt = &now
		expiry := now.Add(24 * time.Hour)
		expiresAt = &expiry
	}

	tx := WalletTransactionEntity{
		ID:                  newID(),
		Currency:            in.Currency,
		Amount:              amount,
		Remarks:             in.Remarks,
		Type:                in.Type,
		Status:              status,
		IsFrozen:            in.Freeze,
		RequireConfirmation: in.RequireConfirmation,
		FrozenAt:            frozenAt,
		ExpiresAt:           expiresAt,
		PayerWalletID:       in.PayerWalletID,
		PayeeWalletID:       in.PayeeWalletID,
	}
	if status == TransactionConfirmed {
		tx.ConfirmedAt = &now
	}

	err := s.DB.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if in.PayerWalletID != nil {
			pocket, created, err := s.GetOrCreatePocket(ctx, db, *in.PayerWalletID, in.Currency, nil)
			if err != nil {
				return err
			}
			held := decimal.Zero
			if status == TransactionPending || status == TransactionFrozen {
				held = amount
			}
			if in.Force {
				if err := addToPocket(db, pocket.ID, amount.Neg(), held); err != nil {
					return err
				}
			} else {
				// The affordability test is part of the write: a separate
				// read lets two concurrent transactions both observe the
				// pre-debit balance and overdraw the pocket.
				updates := map[string]any{"amount": gorm.Expr("amount - ?", amount)}
				if !held.IsZero() {
					updates["held_amount"] = gorm.Expr("held_amount + ?", held)
				}
				debit := db.Model(&WalletPocketEntity{}).
					Where("id = ? AND amount >= ?", pocket.ID, amount).
					Updates(updates)
				if debit.Error != nil {
					return debit.Error
				}
				if created || debit.RowsAffected != 1 {
					return ErrInsufficientFunds
				}
			}
		}

		if in.PayeeWalletID != nil && status == TransactionConfirmed && !in.HoldInEscrow {
			pocket, created, err := s.GetOrCreatePocket(ctx, db, *in.PayeeWalletID, in.Currency, &amount)
			if err != nil {
				return err
			}
			if !created {
				if err := addToPocket(db, pocket.ID, amount, decimal.Zero); err != nil {
					return err
				}
			}
		}

		if err := db.Create(&tx).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if !in.Silent {
		s.notifyTransaction(ctx, &tx, packetTransactionCreated)
	}
	if in.PayerWalletID != nil {
		s.notifyPocket(ctx, *in.PayerWalletID, in.Currency)
	}
	if in.PayeeWalletID != nil && tx.Status == TransactionConfirmed {
		s.notifyPocket(ctx, *in.PayeeWalletID, in.Currency)
	}
	return &tx, nil
}

// CreateTransactionWithAccounts resolves account wallets and delegates.
func (s *Service) CreateTransactionWithAccounts(ctx context.Context, payerAccountID, payeeAccountID *uuid.UUID, currency string, amount decimal.Decimal, remarks *string, txType int) (*WalletTransactionEntity, error) {
	var payerWalletID, payeeWalletID *uuid.UUID
	if payerAccountID != nil {
		wallet, _, err := s.GetAccountWallet(ctx, *payerAccountID)
		if err != nil {
			return nil, errors.New("payer account was specified, but wallet was not found")
		}
		payerWalletID = &wallet.ID
	}
	if payeeAccountID != nil {
		wallet, _, err := s.GetAccountWallet(ctx, *payeeAccountID)
		if err != nil {
			return nil, errors.New("payee account was specified, but wallet was not found")
		}
		payeeWalletID = &wallet.ID
	}
	return s.CreateTransaction(ctx, TransactionInput{
		PayerWalletID: payerWalletID,
		PayeeWalletID: payeeWalletID,
		Currency:      currency,
		Amount:        amount,
		Remarks:       remarks,
		Type:          txType,
	})
}

// TransferBetweenWallets moves funds between two known wallets.
func (s *Service) TransferBetweenWallets(ctx context.Context, payerWalletID, payeeWalletID uuid.UUID, currency string, amount decimal.Decimal, remarks *string, freeze, requireConfirmation bool) (*WalletTransactionEntity, error) {
	if _, _, err := s.GetWallet(ctx, payerWalletID); err != nil {
		return nil, fmt.Errorf("payer wallet not found: %w", err)
	}
	if _, _, err := s.GetWallet(ctx, payeeWalletID); err != nil {
		return nil, fmt.Errorf("payee wallet not found: %w", err)
	}
	return s.CreateTransaction(ctx, TransactionInput{
		PayerWalletID:       &payerWalletID,
		PayeeWalletID:       &payeeWalletID,
		Currency:            currency,
		Amount:              truncate3(amount),
		Remarks:             remarks,
		Type:                TransactionTransfer,
		Freeze:              freeze,
		RequireConfirmation: requireConfirmation,
	})
}

// ConfirmTransaction releases the held amount and credits the payee.
func (s *Service) ConfirmTransaction(ctx context.Context, transactionID, payeeAccountID uuid.UUID) (*WalletTransactionEntity, error) {
	var tx WalletTransactionEntity
	err := s.DB.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&tx, "id = ?", transactionID).Error; err != nil {
			return errors.New("transaction not found")
		}
		if tx.Status != TransactionPending && tx.Status != TransactionFrozen {
			return fmt.Errorf("transaction is in status %d, cannot confirm", tx.Status)
		}
		if tx.ExpiresAt == nil || tx.ExpiresAt.Before(time.Now().UTC()) {
			return errors.New("transaction has expired")
		}
		if tx.PayeeWalletID != nil {
			var payee WalletEntity
			if err := db.First(&payee, "id = ?", *tx.PayeeWalletID).Error; err != nil {
				return err
			}
			if payee.AccountID == nil || *payee.AccountID != payeeAccountID {
				return errors.New("you are not the payee of this transaction")
			}
		}
		now := time.Now().UTC()
		if tx.PayerWalletID != nil {
			if err := releaseHeld(db, *tx.PayerWalletID, tx.Currency, tx.Amount); err != nil {
				return err
			}
		}
		if tx.PayeeWalletID != nil {
			pocket, created, err := s.GetOrCreatePocket(ctx, db, *tx.PayeeWalletID, tx.Currency, &tx.Amount)
			if err != nil {
				return err
			}
			if !created {
				if err := addToPocket(db, pocket.ID, tx.Amount, decimal.Zero); err != nil {
					return err
				}
			}
		}
		tx.Status = TransactionConfirmed
		tx.ConfirmedAt = &now
		return db.Model(&WalletTransactionEntity{}).Where("id = ?", tx.ID).
			Updates(map[string]any{"status": TransactionConfirmed, "confirmed_at": now}).Error
	})
	if err != nil {
		return nil, err
	}
	s.notifyTransaction(ctx, &tx, packetTransactionConfirmed)
	if tx.PayeeWalletID != nil {
		s.notifyPocket(ctx, *tx.PayeeWalletID, tx.Currency)
	}
	return &tx, nil
}

// RejectTransaction refunds a pending/frozen transaction to the payer.
func (s *Service) RejectTransaction(ctx context.Context, transactionID, payeeAccountID uuid.UUID) (*WalletTransactionEntity, error) {
	var tx WalletTransactionEntity
	err := s.DB.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&tx, "id = ?", transactionID).Error; err != nil {
			return errors.New("transaction not found")
		}
		if tx.Status != TransactionPending && tx.Status != TransactionFrozen {
			return fmt.Errorf("transaction is in status %d, cannot reject", tx.Status)
		}
		if tx.PayeeWalletID != nil {
			var payee WalletEntity
			if err := db.First(&payee, "id = ?", *tx.PayeeWalletID).Error; err != nil {
				return err
			}
			if payee.AccountID == nil || *payee.AccountID != payeeAccountID {
				return errors.New("you are not the payee of this transaction")
			}
		}
		if tx.PayerWalletID != nil {
			if err := refundHeld(db, *tx.PayerWalletID, tx.Currency, tx.Amount); err != nil {
				return err
			}
		}
		tx.Status = TransactionRefunded
		return db.Model(&WalletTransactionEntity{}).Where("id = ?", tx.ID).
			Update("status", TransactionRefunded).Error
	})
	if err != nil {
		return nil, err
	}
	s.notifyTransaction(ctx, &tx, packetTransactionRefunded)
	if tx.PayerWalletID != nil {
		s.notifyPocket(ctx, *tx.PayerWalletID, tx.Currency)
	}
	return &tx, nil
}

// releaseHeld drops the held amount without touching the balance (funds were
// already debited when the entry was created).
func releaseHeld(db *gorm.DB, walletID uuid.UUID, currency string, amount decimal.Decimal) error {
	return db.Model(&WalletPocketEntity{}).
		Where("wallet_id = ? AND currency = ?", walletID, currency).
		UpdateColumn("held_amount", gorm.Expr("held_amount - ?", amount)).Error
}

// refundHeld releases the held amount and returns it to the payer balance.
func refundHeld(db *gorm.DB, walletID uuid.UUID, currency string, amount decimal.Decimal) error {
	return db.Model(&WalletPocketEntity{}).
		Where("wallet_id = ? AND currency = ?", walletID, currency).
		Updates(map[string]any{
			"held_amount": gorm.Expr("held_amount - ?", amount),
			"amount":      gorm.Expr("amount + ?", amount),
		}).Error
}

// ProcessExpiredTransactions auto-releases frozen entries and refunds entries
// that awaited confirmation.
func (s *Service) ProcessExpiredTransactions(ctx context.Context) error {
	var expired []WalletTransactionEntity
	now := time.Now().UTC()
	if err := s.DB.WithContext(ctx).
		Where("status IN ? AND expires_at IS NOT NULL AND expires_at < ?", []int{TransactionPending, TransactionFrozen}, now).
		Find(&expired).Error; err != nil {
		return err
	}
	for i := range expired {
		tx := expired[i]
		err := s.DB.WithContext(ctx).Transaction(func(db *gorm.DB) error {
			if tx.IsFrozen && !tx.RequireConfirmation {
				if tx.PayerWalletID != nil {
					if err := releaseHeld(db, *tx.PayerWalletID, tx.Currency, tx.Amount); err != nil {
						return err
					}
				}
				if tx.PayeeWalletID != nil {
					pocket, created, err := s.GetOrCreatePocket(ctx, db, *tx.PayeeWalletID, tx.Currency, &tx.Amount)
					if err != nil {
						return err
					}
					if !created {
						if err := addToPocket(db, pocket.ID, tx.Amount, decimal.Zero); err != nil {
							return err
						}
					}
				}
				tx.Status = TransactionConfirmed
				tx.ConfirmedAt = &now
				if err := db.Model(&WalletTransactionEntity{}).Where("id = ?", tx.ID).
					Updates(map[string]any{"status": TransactionConfirmed, "confirmed_at": now}).Error; err != nil {
					return err
				}
				return nil
			}
			if tx.PayerWalletID != nil {
				if err := refundHeld(db, *tx.PayerWalletID, tx.Currency, tx.Amount); err != nil {
					return err
				}
			}
			tx.Status = TransactionRefunded
			return db.Model(&WalletTransactionEntity{}).Where("id = ?", tx.ID).
				Update("status", TransactionRefunded).Error
		})
		if err != nil {
			s.Log.Error("expiring transaction failed", "transaction", tx.ID, "error", err)
			continue
		}
		packet := packetTransactionConfirmed
		if tx.Status == TransactionRefunded {
			packet = packetTransactionExpired
		}
		s.notifyTransaction(ctx, &tx, packet)
	}
	return nil
}

// ── orders ─────────────────────────────────────────────────────────────────

// OrderInput describes a new payment order.
type OrderInput struct {
	PayeeWalletID     *uuid.UUID
	PayerWalletID     *uuid.UUID
	Currency          string
	Amount            decimal.Decimal
	Expiration        *time.Duration
	AppIdentifier     *string
	ProductIdentifier *string
	Remarks           *string
	Meta              map[string]any
	Reuseable         bool
	Items             []OrderItemInput
	InboundOrderID    *uuid.UUID
}

// OrderItemInput is one order line.
type OrderItemInput struct {
	ProductIdentifier string
	Quantity          int
	UnitPrice         decimal.Decimal
	Currency          string
}

// CreateOrder creates (or reuses) an unpaid order.
func (s *Service) CreateOrder(ctx context.Context, in OrderInput) (*WalletOrderEntity, error) {
	now := time.Now().UTC()
	expiry := 24 * time.Hour
	if in.Expiration != nil {
		expiry = *in.Expiration
	}

	if len(in.Items) == 0 && in.Reuseable && in.AppIdentifier != nil {
		var existing WalletOrderEntity
		query := s.DB.WithContext(ctx).Where(
			"status = ? AND payee_wallet_id IS NOT DISTINCT FROM ? AND payer_wallet_id IS NOT DISTINCT FROM ? AND currency = ? AND amount = ? AND app_identifier = ? AND product_identifier IS NOT DISTINCT FROM ? AND expired_at > ?",
			OrderUnpaid, in.PayeeWalletID, in.PayerWalletID, in.Currency, truncate3(in.Amount), *in.AppIdentifier, in.ProductIdentifier, now,
		)
		if err := query.First(&existing).Error; err == nil {
			if in.Meta == nil || existing.Meta == nil {
				return &existing, nil
			}
			var existingMeta map[string]any
			if err := json.Unmarshal(existing.Meta, &existingMeta); err != nil {
				return &existing, nil
			}
			if len(existingMeta) == len(in.Meta) && mapsEqual(existingMeta, in.Meta) {
				return &existing, nil
			}
		}
	}

	var meta datatypes.JSON
	if in.Meta != nil {
		meta = mustJSON(in.Meta)
	}
	order := WalletOrderEntity{
		ID:                newID(),
		Status:            OrderUnpaid,
		Currency:          in.Currency,
		Remarks:           in.Remarks,
		AppIdentifier:     in.AppIdentifier,
		ProductIdentifier: in.ProductIdentifier,
		Meta:              meta,
		Amount:            truncate3(in.Amount),
		ExpiredAt:         now.Add(expiry),
		PayerWalletID:     in.PayerWalletID,
		PayeeWalletID:     in.PayeeWalletID,
		InboundOrderID:    in.InboundOrderID,
	}
	err := s.DB.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if err := db.Create(&order).Error; err != nil {
			return err
		}
		for _, item := range in.Items {
			row := WalletOrderItemEntity{
				ID:                newID(),
				OrderID:           order.ID,
				ProductIdentifier: item.ProductIdentifier,
				Quantity:          item.Quantity,
				UnitPrice:         truncate3(item.UnitPrice),
				Currency:          item.Currency,
			}
			if err := db.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &order, nil
}

// CreateInboundOrder stores one provider order, returning the existing row on
// a (provider, external_id) collision.
func (s *Service) CreateInboundOrder(ctx context.Context, in InboundOrderInput) (*InboundOrderEntity, error) {
	provider := strings.ToLower(strings.TrimSpace(in.Provider))
	externalID := strings.TrimSpace(in.ExternalID)
	if provider == "" {
		return nil, errors.New("provider order provider is missing")
	}
	if externalID == "" {
		return nil, errors.New("provider order identifier is missing")
	}
	var existing InboundOrderEntity
	err := s.DB.WithContext(ctx).Where("provider = ? AND external_id = ?", provider, externalID).First(&existing).Error
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	now := time.Now().UTC()
	begun := now
	if in.BegunAt != nil {
		begun = *in.BegunAt
	}
	order := InboundOrderEntity{
		ID:                  newID(),
		Provider:            provider,
		ExternalID:          externalID,
		CorrelationID:       strPtr(in.CorrelationID),
		ProviderReferenceID: strPtr(in.SubscriptionID),
		ProductIdentifier:   strPtr(in.SubscriptionID),
		AccountIdentifier:   strPtr(in.AccountID),
		BegunAt:             begun,
		Duration:            intervalFromDuration(in.Duration),
		IsTesting:           in.IsTesting,
	}
	if err := s.DB.WithContext(ctx).Create(&order).Error; err != nil {
		var conflict InboundOrderEntity
		if findErr := s.DB.WithContext(ctx).Where("provider = ? AND external_id = ?", provider, externalID).
			First(&conflict).Error; findErr == nil {
			return &conflict, nil
		}
		return nil, err
	}
	return &order, nil
}

// InboundOrderInput is one provider order.
type InboundOrderInput struct {
	Provider       string
	ExternalID     string
	CorrelationID  string
	SubscriptionID string
	AccountID      string
	BegunAt        *time.Time
	Duration       time.Duration
	IsTesting      bool
}

// PayOrder charges an order from the given payer wallet.
func (s *Service) PayOrder(ctx context.Context, orderID uuid.UUID, payerWallet *WalletEntity) (*WalletOrderEntity, error) {
	var order WalletOrderEntity
	if err := s.DB.WithContext(ctx).First(&order, "id = ?", orderID).Error; err != nil {
		return nil, errors.New("order not found")
	}
	if order.PayerWalletID != nil && *order.PayerWalletID != payerWallet.ID {
		return nil, errors.New("order is reserved for a different wallet")
	}
	if order.Status == OrderPaid {
		// A claimed order whose transaction has not landed yet is a payment in
		// flight: running the hooks here would grant twice.
		if order.TransactionID == nil {
			return nil, errors.New("order is already being paid")
		}
		if err := s.runOrderHooks(ctx, &order); err != nil {
			return nil, err
		}
		// The C# wallet re-published PaymentOrderEvent for an already-paid
		// order, so a retried checkout still reaches the fleet's consumers.
		s.publishOrderPaid(ctx, &order, payerWallet)
		return &order, nil
	}
	if order.Status != OrderUnpaid {
		return nil, fmt.Errorf("order is in invalid status: %d", order.Status)
	}
	if order.ExpiredAt.Before(time.Now().UTC()) {
		_ = s.DB.WithContext(ctx).Model(&WalletOrderEntity{}).Where("id = ?", order.ID).
			Update("status", OrderExpired).Error
		return nil, errors.New("order has expired")
	}

	isAppOrder := order.AppIdentifier != nil && *order.AppIdentifier != ""
	var merchant *MerchantEntity
	if isAppOrder && order.PayeeWalletID != nil {
		appID := strings.TrimPrefix(*order.AppIdentifier, "developer.app:")
		if id, err := uuid.Parse(appID); err == nil {
			merchant = s.resolveMerchantForApp(ctx, id, *order.PayeeWalletID)
		}
	}
	escrow := isAppOrder && merchant != nil

	if order.PayerWalletID == nil {
		order.PayerWalletID = &payerWallet.ID
	}
	var payeeWalletID *uuid.UUID
	if !escrow {
		payeeWalletID = order.PayeeWalletID
	}
	remarks := order.Remarks
	if remarks == nil {
		text := fmt.Sprintf("Payment for Order #%s", order.ID)
		remarks = &text
	}
	// Elect a single payer before the debit. The status flip is the claim:
	// two concurrent checkouts would otherwise both pass the Unpaid check
	// and charge the wallet twice for one order.
	claim := s.DB.WithContext(ctx).Model(&WalletOrderEntity{}).
		Where("id = ? AND status = ?", order.ID, OrderUnpaid).
		Updates(map[string]any{"status": OrderPaid, "payer_wallet_id": payerWallet.ID})
	if claim.Error != nil {
		return nil, claim.Error
	}
	if claim.RowsAffected == 0 {
		return nil, errors.New("order is already being paid")
	}
	transaction, err := s.CreateTransaction(ctx, TransactionInput{
		PayerWalletID: &payerWallet.ID,
		PayeeWalletID: payeeWalletID,
		Currency:      order.Currency,
		Amount:        order.Amount,
		Remarks:       remarks,
		Type:          TransactionOrder,
		Silent:        true,
		HoldInEscrow:  escrow,
	})
	if err != nil {
		// Release the claim: the debit never happened.
		_ = s.DB.WithContext(ctx).Model(&WalletOrderEntity{}).
			Where("id = ? AND transaction_id IS NULL", order.ID).
			Update("status", OrderUnpaid).Error
		return nil, err
	}

	order.TransactionID = &transaction.ID
	order.Status = OrderPaid
	if err := s.DB.WithContext(ctx).Model(&WalletOrderEntity{}).Where("id = ?", order.ID).
		Update("transaction_id", transaction.ID).Error; err != nil {
		return nil, err
	}
	if escrow && merchant != nil {
		if _, err := s.CreateSettlement(ctx, merchant.ID, &transaction.ID, *order.PayeeWalletID, order.Currency, order.Amount, &order.ID); err != nil {
			s.Log.Error("creating settlement failed", "order", order.ID, "error", err)
		}
	}
	if err := s.runOrderHooks(ctx, &order); err != nil {
		s.Log.Error("order paid hook failed", "order", order.ID, "error", err)
	}
	s.notifyAccount(ctx, payerWallet.AccountID, "wallet.order.paid", map[string]any{
		"id":       order.ID.String(),
		"amount":   order.Amount,
		"currency": order.Currency,
		"status":   order.Status,
	})
	s.publishOrderPaid(ctx, &order, payerWallet)
	return &order, nil
}

// runOrderHooks invokes the subscription/product handlers.
func (s *Service) runOrderHooks(ctx context.Context, order *WalletOrderEntity) error {
	for _, hook := range s.OrderHooks {
		if err := hook(ctx, order); err != nil {
			return err
		}
	}
	return nil
}

// CancelOrder cancels an unpaid order.
func (s *Service) CancelOrder(ctx context.Context, orderID uuid.UUID) (*WalletOrderEntity, error) {
	var order WalletOrderEntity
	if err := s.DB.WithContext(ctx).First(&order, "id = ?", orderID).Error; err != nil {
		return nil, errors.New("order not found")
	}
	if order.Status != OrderUnpaid {
		return nil, fmt.Errorf("cannot cancel order in status: %d", order.Status)
	}
	order.Status = OrderCancelled
	if err := s.DB.WithContext(ctx).Model(&WalletOrderEntity{}).Where("id = ?", order.ID).
		Update("status", OrderCancelled).Error; err != nil {
		return nil, err
	}
	return &order, nil
}

// RefundOrder reverses a paid order and marks it finished.
func (s *Service) RefundOrder(ctx context.Context, orderID uuid.UUID) (*WalletOrderEntity, *WalletTransactionEntity, error) {
	var order WalletOrderEntity
	if err := s.DB.WithContext(ctx).First(&order, "id = ?", orderID).Error; err != nil {
		return nil, nil, errors.New("order not found")
	}
	if order.Status != OrderPaid {
		return nil, nil, fmt.Errorf("cannot refund order in status: %d", order.Status)
	}
	if order.TransactionID == nil {
		return nil, nil, errors.New("order has no associated transaction")
	}
	var original WalletTransactionEntity
	if err := s.DB.WithContext(ctx).First(&original, "id = ?", *order.TransactionID).Error; err != nil {
		return nil, nil, errors.New("order has no associated transaction")
	}
	remarks := fmt.Sprintf("Refund for order %s", order.ID)
	refund, err := s.CreateTransaction(ctx, TransactionInput{
		PayerWalletID: order.PayeeWalletID,
		PayeeWalletID: original.PayerWalletID,
		Currency:      order.Currency,
		Amount:        order.Amount,
		Remarks:       &remarks,
	})
	if err != nil {
		return nil, nil, err
	}
	order.Status = OrderFinished
	if err := s.DB.WithContext(ctx).Model(&WalletOrderEntity{}).Where("id = ?", order.ID).
		Update("status", OrderFinished).Error; err != nil {
		return nil, nil, err
	}
	return &order, refund, nil
}

// ExpireOrders marks overdue unpaid orders expired.
func (s *Service) ExpireOrders(ctx context.Context) error {
	return s.DB.WithContext(ctx).Model(&WalletOrderEntity{}).
		Where("status = ? AND expired_at < ?", OrderUnpaid, time.Now().UTC()).
		Update("status", OrderExpired).Error
}

func mapsEqual(a, b map[string]any) bool {
	for key, value := range a {
		other, ok := b[key]
		if !ok {
			return false
		}
		if fmt.Sprint(value) != fmt.Sprint(other) {
			return false
		}
	}
	return true
}
