package wallet

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/nats"
	"src.solsynth.dev/sosys/stargate/ports"
)

// MaxWalletsPerAccount mirrors WalletService.MaxWalletsPerAccount.
const MaxWalletsPerAccount = 3

// Domain errors. The HTTP layer maps them to the C# ApiError codes.
var (
	ErrNotFound            = errors.New("not found")
	ErrInsufficientFunds   = errors.New("insufficient funds")
	ErrInvalidState        = errors.New("invalid state")
	ErrForbidden           = errors.New("forbidden")
	ErrWalletQuotaExceeded = errors.New("account wallet quota exceeded")
)

// OrderPaidHook runs after an order is paid. The subscription and wallet
// product services register hooks here, replacing the C# event-bus listener.
type OrderPaidHook func(ctx context.Context, order *WalletOrderEntity) error

// Service is the wallet domain graph shared by the HTTP and gRPC surfaces.
type Service struct {
	DB  *gorm.DB
	Log *slog.Logger

	// NATS and Push are optional; a missing one only drops notifications.
	NATS *nats.Client
	Push ports.Push

	// OrderHooks run after an order is paid, in registration order.
	OrderHooks []OrderPaidHook

	// AppResolver maps Develop apps to publishers for app orders; optional.
	AppResolver AppPublisherResolver

	// Logs records account action logs. Optional: when nil, emits are skipped.
	// It matches actionlog.Service.Create.
	Logs ActionLogSink
}

// NewService builds the domain graph.
func NewService(db *gorm.DB, log *slog.Logger, bus *nats.Client, push ports.Push) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{DB: db, Log: log, NATS: bus, Push: push}
}

// ── notifications ──────────────────────────────────────────────────────────

// WebSocket packet types, mirroring WebSocketPacketType.
const (
	packetTransactionCreated   = "wallet.transaction.created"
	packetTransactionConfirmed = "wallet.transaction.confirmed"
	packetTransactionRefunded  = "wallet.transaction.refunded"
	packetTransactionExpired   = "wallet.transaction.expired"
	packetPocketUpdated        = "wallet.pocket.updated"
	packetFundContributed      = "wallet.fund.contributed"
	packetFundCompleted        = "wallet.fund.completed"
)

// notifyAccount pushes one wallet packet to an account. Errors are logged and
// never fail the money path.
func (s *Service) notifyAccount(ctx context.Context, accountID *uuid.UUID, packet string, payload any) {
	if accountID == nil || s.NATS == nil {
		return
	}
	if err := s.NATS.PublishWS(ctx, accountID.String(), packet, payload); err != nil {
		s.Log.Warn("wallet websocket packet failed", "packet", packet, "account", accountID, "error", err)
	}
}

// publishOrderPaid emits the fleet's PaymentOrderEvent so consumers outside
// this binary (realm boosts, name-change cards, affiliation invites) apply
// the order. The C# wallet published it from PaymentService.PayOrderAsync;
// here it is the single place a paid order leaves the module.
func (s *Service) publishOrderPaid(ctx context.Context, order *WalletOrderEntity, payerWallet *WalletEntity) {
	if s.NATS == nil || order == nil || payerWallet == nil || payerWallet.AccountID == nil {
		return
	}
	event := nats.PaidOrder{
		OrderID:           order.ID.String(),
		WalletID:          payerWallet.ID.String(),
		AccountID:         payerWallet.AccountID.String(),
		AppIdentifier:     order.AppIdentifier,
		ProductIdentifier: order.ProductIdentifier,
		Meta:              orderMeta(order),
		Status:            order.Status,
	}
	if err := s.NATS.PublishOrderPaid(ctx, event); err != nil {
		s.Log.Warn("wallet payment event publish failed", "order", order.ID, "error", err)
	}
}

func (s *Service) notifyTransaction(ctx context.Context, tx *WalletTransactionEntity, packet string) {
	payload := map[string]any{
		"id":                   tx.ID.String(),
		"type":                 tx.Type,
		"amount":               tx.Amount,
		"currency":             tx.Currency,
		"status":               tx.Status,
		"remarks":              tx.Remarks,
		"payer_wallet_id":      uuidPtrString(tx.PayerWalletID),
		"payee_wallet_id":      uuidPtrString(tx.PayeeWalletID),
		"is_frozen":            tx.IsFrozen,
		"require_confirmation": tx.RequireConfirmation,
		"frozen_at":            tx.FrozenAt,
		"expires_at":           tx.ExpiresAt,
		"confirmed_at":         tx.ConfirmedAt,
	}
	s.notifyWalletOwner(ctx, tx.PayerWalletID, packet, payload)
	s.notifyWalletOwner(ctx, tx.PayeeWalletID, packet, payload)
}

func (s *Service) notifyWalletOwner(ctx context.Context, walletID *uuid.UUID, packet string, payload any) {
	if walletID == nil {
		return
	}
	s.notifyAccount(ctx, s.accountIDForWallet(ctx, *walletID), packet, payload)
}

// accountIDForWallet resolves the account that owns a wallet. A realm or
// ownerless wallet has no account, so notifications are skipped for it.
func (s *Service) accountIDForWallet(ctx context.Context, walletID uuid.UUID) *uuid.UUID {
	var row struct {
		AccountID uuid.NullUUID `gorm:"column:account_id"`
	}
	if err := s.DB.WithContext(ctx).Model(&WalletEntity{}).Where("id = ?", walletID).
		Select("account_id").Take(&row).Error; err != nil {
		return nil
	}
	if !row.AccountID.Valid {
		return nil
	}
	id := row.AccountID.UUID
	return &id
}

func (s *Service) notifyPocket(ctx context.Context, walletID uuid.UUID, currency string) {
	var pocket WalletPocketEntity
	if err := s.DB.WithContext(ctx).Where("wallet_id = ? AND currency = ?", walletID, currency).
		First(&pocket).Error; err != nil {
		return
	}
	s.notifyAccount(ctx, s.accountIDForWallet(ctx, walletID), packetPocketUpdated, map[string]any{
		"wallet_id":        walletID.String(),
		"currency":         pocket.Currency,
		"amount":           pocket.Amount,
		"held_amount":      pocket.HeldAmount,
		"available_amount": pocket.Amount.Sub(pocket.HeldAmount),
	})
}

// ── helpers ────────────────────────────────────────────────────────────────

func truncate3(amount decimal.Decimal) decimal.Decimal { return amount.Truncate(3) }

func uuidPtrString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

func strPtr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

func newID() uuid.UUID { return uuid.New() }

// ── wallets ────────────────────────────────────────────────────────────────

func (s *Service) walletWithPockets(ctx context.Context, query *gorm.DB) (*WalletEntity, []WalletPocketEntity, error) {
	var wallet WalletEntity
	if err := query.WithContext(ctx).First(&wallet).Error; err != nil {
		return nil, nil, err
	}
	var pockets []WalletPocketEntity
	if err := s.DB.WithContext(ctx).Where("wallet_id = ?", wallet.ID).
		Order("created_at").Find(&pockets).Error; err != nil {
		return nil, nil, err
	}
	return &wallet, pockets, nil
}

// GetWallet returns a wallet with its pockets.
func (s *Service) GetWallet(ctx context.Context, id uuid.UUID) (*WalletEntity, []WalletPocketEntity, error) {
	return s.walletWithPockets(ctx, s.DB.Model(&WalletEntity{}).Where("id = ?", id))
}

// GetWalletByPublicID resolves a wallet by its public id.
func (s *Service) GetWalletByPublicID(ctx context.Context, publicID string) (*WalletEntity, []WalletPocketEntity, error) {
	return s.walletWithPockets(ctx, s.DB.Model(&WalletEntity{}).Where("public_id = ?", publicID))
}

// GetAccountWallets lists an account's wallets, primary first.
func (s *Service) GetAccountWallets(ctx context.Context, accountID uuid.UUID) ([]WalletEntity, error) {
	var wallets []WalletEntity
	err := s.DB.WithContext(ctx).Where("account_id = ?", accountID).
		Order("is_primary DESC, created_at ASC").Find(&wallets).Error
	return wallets, err
}

// GetRealmWallets lists a realm's wallets, primary first.
func (s *Service) GetRealmWallets(ctx context.Context, realmID uuid.UUID) ([]WalletEntity, error) {
	var wallets []WalletEntity
	err := s.DB.WithContext(ctx).Where("realm_id = ?", realmID).
		Order("is_primary DESC, created_at ASC").Find(&wallets).Error
	return wallets, err
}

// GetAccountWallet returns the account's primary wallet.
func (s *Service) GetAccountWallet(ctx context.Context, accountID uuid.UUID) (*WalletEntity, []WalletPocketEntity, error) {
	return s.walletWithPockets(ctx, s.DB.Model(&WalletEntity{}).Where("account_id = ?", accountID).
		Order("is_primary DESC, created_at ASC"))
}

// CheckWalletExists reports whether the account owns a wallet.
func (s *Service) CheckWalletExists(ctx context.Context, accountID uuid.UUID) (bool, error) {
	var count int64
	err := s.DB.WithContext(ctx).Model(&WalletEntity{}).Where("account_id = ?", accountID).Count(&count).Error
	return count > 0, err
}

// CreateWallet creates an account or realm wallet. The first wallet of an
// owner becomes primary.
func (s *Service) CreateWallet(ctx context.Context, accountID, realmID *uuid.UUID, name *string) (*WalletEntity, error) {
	if accountID == nil && realmID == nil {
		return nil, errors.New("wallet must belong to an account or a realm")
	}
	if accountID != nil && realmID != nil {
		return nil, errors.New("wallet cannot belong to both an account and a realm")
	}
	wallet := WalletEntity{ID: newID(), AccountID: accountID, RealmID: realmID}
	switch {
	case name != nil && strings.TrimSpace(*name) != "":
		wallet.Name = strings.TrimSpace(*name)
	case realmID != nil:
		wallet.Name = "Organization Wallet"
	default:
		wallet.Name = "Wallet"
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if accountID != nil {
			var count int64
			if err := tx.Model(&WalletEntity{}).Where("account_id = ?", *accountID).Count(&count).Error; err != nil {
				return err
			}
			if count >= MaxWalletsPerAccount {
				return ErrWalletQuotaExceeded
			}
			var primary int64
			if err := tx.Model(&WalletEntity{}).Where("account_id = ? AND is_primary", *accountID).Count(&primary).Error; err != nil {
				return err
			}
			wallet.IsPrimary = primary == 0
		} else {
			var primary int64
			if err := tx.Model(&WalletEntity{}).Where("realm_id = ? AND is_primary", *realmID).Count(&primary).Error; err != nil {
				return err
			}
			wallet.IsPrimary = primary == 0
		}
		return tx.Create(&wallet).Error
	})
	if err != nil {
		return nil, err
	}
	return &wallet, nil
}

// SetPrimaryWallet promotes one wallet and demotes the owner's others.
func (s *Service) SetPrimaryWallet(ctx context.Context, walletID uuid.UUID) (*WalletEntity, error) {
	var wallet WalletEntity
	if err := s.DB.WithContext(ctx).First(&wallet, "id = ?", walletID).Error; err != nil {
		return nil, err
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if wallet.AccountID != nil {
			if err := tx.Model(&WalletEntity{}).
				Where("account_id = ? AND id <> ?", *wallet.AccountID, wallet.ID).
				Update("is_primary", false).Error; err != nil {
				return err
			}
		} else if wallet.RealmID != nil {
			if err := tx.Model(&WalletEntity{}).
				Where("realm_id = ? AND id <> ?", *wallet.RealmID, wallet.ID).
				Update("is_primary", false).Error; err != nil {
				return err
			}
		}
		wallet.IsPrimary = true
		return tx.Model(&WalletEntity{}).Where("id = ?", wallet.ID).Update("is_primary", true).Error
	})
	if err != nil {
		return nil, err
	}
	return &wallet, nil
}

const publicIDChars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// EnablePublicID assigns a DNW-XXXX-XXXX-XXXX public id when missing.
func (s *Service) EnablePublicID(ctx context.Context, walletID uuid.UUID) (*WalletEntity, error) {
	var wallet WalletEntity
	if err := s.DB.WithContext(ctx).First(&wallet, "id = ?", walletID).Error; err != nil {
		return nil, err
	}
	if wallet.PublicID == nil {
		for {
			id := generatePublicID()
			var count int64
			if err := s.DB.WithContext(ctx).Model(&WalletEntity{}).Where("public_id = ?", id).Count(&count).Error; err != nil {
				return nil, err
			}
			if count > 0 {
				continue
			}
			wallet.PublicID = &id
			if err := s.DB.WithContext(ctx).Model(&WalletEntity{}).Where("id = ?", wallet.ID).
				Update("public_id", id).Error; err != nil {
				return nil, err
			}
			break
		}
	}
	return &wallet, nil
}

// DisablePublicID clears a wallet's public id.
func (s *Service) DisablePublicID(ctx context.Context, walletID uuid.UUID) (*WalletEntity, error) {
	var wallet WalletEntity
	if err := s.DB.WithContext(ctx).First(&wallet, "id = ?", walletID).Error; err != nil {
		return nil, err
	}
	if err := s.DB.WithContext(ctx).Model(&WalletEntity{}).Where("id = ?", wallet.ID).
		Update("public_id", nil).Error; err != nil {
		return nil, err
	}
	wallet.PublicID = nil
	return &wallet, nil
}

func generatePublicID() string {
	var b strings.Builder
	b.WriteString("DNW-")
	for i := range 12 {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(publicIDChars[rand.IntN(len(publicIDChars))])
	}
	return b.String()
}

// ── pockets ────────────────────────────────────────────────────────────────

// GetOrCreatePocket returns the wallet's pocket for a currency, creating it
// (unsaved when created=false) with the given initial amount.
func (s *Service) GetOrCreatePocket(ctx context.Context, tx *gorm.DB, walletID uuid.UUID, currency string, initial *decimal.Decimal) (*WalletPocketEntity, bool, error) {
	if tx == nil {
		// Take the wallet lock in a transaction of our own so the find/create
		// pair below is serialized even for callers outside a transaction.
		var pocket *WalletPocketEntity
		var created bool
		err := s.DB.WithContext(ctx).Transaction(func(db *gorm.DB) error {
			var err error
			pocket, created, err = s.GetOrCreatePocket(ctx, db, walletID, currency, initial)
			return err
		})
		return pocket, created, err
	}
	db := tx
	// Two concurrent first credits to one wallet would otherwise both miss
	// the pocket and create one row each, hiding one balance from every later
	// `First`. The wallet row lock makes the second caller wait and then see
	// the pocket the first one created.
	if err := db.WithContext(ctx).Exec(`SELECT id FROM wallets WHERE id = ? FOR UPDATE`, walletID).Error; err != nil {
		return nil, false, err
	}
	var pocket WalletPocketEntity
	err := db.WithContext(ctx).Where("wallet_id = ? AND currency = ?", walletID, currency).First(&pocket).Error
	if err == nil {
		return &pocket, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	pocket = WalletPocketEntity{
		ID:         newID(),
		WalletID:   walletID,
		Currency:   currency,
		Amount:     decimal.Zero,
		HeldAmount: decimal.Zero,
	}
	if initial != nil {
		pocket.Amount = *initial
	}
	if err := db.WithContext(ctx).Create(&pocket).Error; err != nil {
		return nil, false, err
	}
	return &pocket, true, nil
}

// addToPocket atomically adjusts a pocket balance (and optionally the held
// amount) by the given deltas.
func addToPocket(tx *gorm.DB, pocketID uuid.UUID, amountDelta, heldDelta decimal.Decimal) error {
	updates := map[string]any{}
	if !amountDelta.IsZero() {
		updates["amount"] = gorm.Expr("amount + ?", amountDelta)
	}
	if !heldDelta.IsZero() {
		updates["held_amount"] = gorm.Expr("held_amount + ?", heldDelta)
	}
	if len(updates) == 0 {
		return nil
	}
	return tx.Model(&WalletPocketEntity{}).Where("id = ?", pocketID).Updates(updates).Error
}

// ── transfers ──────────────────────────────────────────────────────────────

// CreateTransferRequest records a claimable transfer to a public wallet.
func (s *Service) CreateTransferRequest(ctx context.Context, creatorAccountID, payeeWalletID uuid.UUID, currency string, amount decimal.Decimal, remark *string, expiration *time.Duration, freeze, requireConfirmation bool) (*WalletTransferRequestEntity, error) {
	if amount.LessThanOrEqual(decimal.Zero) {
		return nil, errors.New("amount must be greater than zero")
	}
	var wallet WalletEntity
	if err := s.DB.WithContext(ctx).First(&wallet, "id = ?", payeeWalletID).Error; err != nil {
		return nil, errors.New("payee wallet not found")
	}
	if wallet.PublicID == nil || strings.TrimSpace(*wallet.PublicID) == "" {
		return nil, errors.New("payee wallet must have a public ID enabled")
	}
	expiry := 24 * time.Hour
	if expiration != nil {
		expiry = *expiration
	}
	request := WalletTransferRequestEntity{
		ID:                  newID(),
		Status:              TransferActive,
		Currency:            currency,
		Amount:              truncate3(amount),
		Remark:              strPtr(deref(remark)),
		Freeze:              freeze,
		RequireConfirmation: requireConfirmation,
		ExpiresAt:           time.Now().UTC().Add(expiry),
		CreatorAccountID:    creatorAccountID,
		PayeeWalletID:       payeeWalletID,
	}
	if err := s.DB.WithContext(ctx).Create(&request).Error; err != nil {
		return nil, err
	}
	return &request, nil
}

// GetTransferRequest loads a request and marks it expired when overdue.
func (s *Service) GetTransferRequest(ctx context.Context, id uuid.UUID) (*WalletTransferRequestEntity, error) {
	var request WalletTransferRequestEntity
	if err := s.DB.WithContext(ctx).First(&request, "id = ?", id).Error; err != nil {
		return nil, err
	}
	if request.Status == TransferActive && request.ExpiresAt.Before(time.Now().UTC()) {
		request.Status = TransferExpired
		if err := s.DB.WithContext(ctx).Model(&WalletTransferRequestEntity{}).
			Where("id = ?", request.ID).Update("status", TransferExpired).Error; err != nil {
			return nil, err
		}
	}
	return &request, nil
}

// FulfillTransferRequest marks an active request fulfilled by a transaction.
func (s *Service) FulfillTransferRequest(ctx context.Context, id, transactionID uuid.UUID) (*WalletTransferRequestEntity, error) {
	var request WalletTransferRequestEntity
	if err := s.DB.WithContext(ctx).First(&request, "id = ?", id).Error; err != nil {
		return nil, err
	}
	if request.Status != TransferActive {
		return nil, errors.New("transfer request is no longer active")
	}
	if request.ExpiresAt.Before(time.Now().UTC()) {
		return nil, errors.New("transfer request has expired")
	}
	now := time.Now().UTC()
	request.Status = TransferFulfilled
	request.TransactionID = &transactionID
	request.FulfilledAt = &now
	if err := s.DB.WithContext(ctx).Model(&WalletTransferRequestEntity{}).Where("id = ?", request.ID).
		Updates(map[string]any{"status": TransferFulfilled, "transaction_id": transactionID, "fulfilled_at": now}).Error; err != nil {
		return nil, err
	}
	return &request, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// intervalFromDuration converts a Go duration into a Postgres interval value.
func intervalFromDuration(d time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: d.Microseconds(), Valid: true}
}

func durationFromInterval(i pgtype.Interval) time.Duration {
	if !i.Valid {
		return 0
	}
	return time.Duration(i.Microseconds)*time.Microsecond + time.Duration(i.Days)*24*time.Hour + time.Duration(i.Months)*30*24*time.Hour
}
