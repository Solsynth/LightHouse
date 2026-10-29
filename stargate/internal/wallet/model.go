package wallet

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// Currency identifiers (WalletCurrency).
const (
	CurrencyPoints = "points"
	CurrencyGolds  = "golds"
)

// OrderStatus mirrors the C# enum (serialized as an integer).
const (
	OrderUnpaid = iota
	OrderPaid
	OrderCancelled
	OrderFinished
	OrderExpired
)

// TransactionType.
const (
	TransactionSystem = iota
	TransactionTransfer
	TransactionOrder
)

// TransactionStatus.
const (
	TransactionPending = iota
	TransactionFrozen
	TransactionConfirmed
	TransactionRefunded
	TransactionCancelled
)

// WalletTransferRequestStatus.
const (
	TransferActive = iota
	TransferFulfilled
	TransferExpired
	TransferCancelled
)

// FundSplitType / FundStatus / ContributionType.
const (
	FundSplitEven = iota
	FundSplitRandom
)

const (
	FundCreated = iota
	FundPartiallyReceived
	FundFullyReceived
	FundExpired
	FundRefunded
)

const (
	ContributionFree = iota
	ContributionFixed
)

// SubscriptionStatus / GiftStatus.
const (
	SubscriptionUnpaid = iota
	SubscriptionActive
	SubscriptionExpired
	SubscriptionCancelled
)

const (
	GiftCreated = iota
	GiftSent
	GiftRedeemed
	GiftExpired
	GiftCancelled
)

// MerchantSettlementStatus / MerchantSettlementTrigger.
const (
	SettlementPending = iota
	SettlementSettled
	SettlementCancelled
)

const (
	SettlementAutomatic = iota
	SettlementManual
)

// SubscriptionPaymentMethod constants.
const (
	PaymentInAppWallet = "solian.wallet"
	PaymentAfdian      = "afdian"
	PaymentPaddle      = "paddle"
	PaymentAppleStore  = "apple_store"
	PaymentGift        = "gift"
)

// PaymentDetails is the C# SnPaymentDetails jsonb shape.
type PaymentDetails struct {
	Currency string  `json:"currency"`
	OrderID  *string `json:"order_id"`
}

func paymentDetailsJSON(details PaymentDetails) datatypes.JSON {
	raw, _ := json.Marshal(details)
	return datatypes.JSON(raw)
}

func parsePaymentDetails(raw datatypes.JSON) PaymentDetails {
	var details PaymentDetails
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &details)
	}
	return details
}

// Wallet is the wire shape of SnWallet.
type Wallet struct {
	ID        string      `json:"id"`
	Pockets   []Pocket    `json:"pockets"`
	AccountID *string     `json:"account_id"`
	RealmID   *string     `json:"realm_id"`
	Name      string      `json:"name"`
	IsPrimary bool        `json:"is_primary"`
	PublicID  *string     `json:"public_id"`
	Account   any         `json:"account"`
	Realm     any         `json:"realm"`
	IsDefault bool        `json:"is_default"`
	CreatedAt *model.Time `json:"created_at"`
	UpdatedAt *model.Time `json:"updated_at"`
	DeletedAt *model.Time `json:"deleted_at"`
}

// Pocket is the wire shape of SnWalletPocket.
type Pocket struct {
	ID              string          `json:"id"`
	Currency        string          `json:"currency"`
	Amount          decimal.Decimal `json:"amount"`
	HeldAmount      decimal.Decimal `json:"held_amount"`
	WalletID        string          `json:"wallet_id"`
	AvailableAmount decimal.Decimal `json:"available_amount"`
	CreatedAt       *model.Time     `json:"created_at"`
	UpdatedAt       *model.Time     `json:"updated_at"`
	DeletedAt       *model.Time     `json:"deleted_at"`
}

// Transaction is the wire shape of SnWalletTransaction.
type Transaction struct {
	ID                  string          `json:"id"`
	Currency            string          `json:"currency"`
	Amount              decimal.Decimal `json:"amount"`
	Remarks             *string         `json:"remarks"`
	Type                int             `json:"type"`
	Status              int             `json:"status"`
	IsFrozen            bool            `json:"is_frozen"`
	RequireConfirmation bool            `json:"require_confirmation"`
	FrozenAt            *model.Time     `json:"frozen_at"`
	ExpiresAt           *model.Time     `json:"expires_at"`
	ConfirmedAt         *model.Time     `json:"confirmed_at"`
	PayerWalletID       *string         `json:"payer_wallet_id"`
	PayerWallet         *Wallet         `json:"payer_wallet"`
	PayeeWalletID       *string         `json:"payee_wallet_id"`
	PayeeWallet         *Wallet         `json:"payee_wallet"`
	CreatedAt           *model.Time     `json:"created_at"`
	UpdatedAt           *model.Time     `json:"updated_at"`
	DeletedAt           *model.Time     `json:"deleted_at"`
}

// Order is the wire shape of SnWalletOrder.
type Order struct {
	ID                string          `json:"id"`
	Status            int             `json:"status"`
	Currency          string          `json:"currency"`
	Remarks           *string         `json:"remarks"`
	AppIdentifier     *string         `json:"app_identifier"`
	ProductIdentifier *string         `json:"product_identifier"`
	Meta              any             `json:"meta"`
	Amount            decimal.Decimal `json:"amount"`
	ExpiredAt         *model.Time     `json:"expired_at"`
	PayerWalletID     *string         `json:"payer_wallet_id"`
	PayeeWalletID     *string         `json:"payee_wallet_id"`
	PayeeWallet       *Wallet         `json:"payee_wallet"`
	TransactionID     *string         `json:"transaction_id"`
	Transaction       *Transaction    `json:"transaction"`
	InboundOrderID    *string         `json:"inbound_order_id"`
	Items             []OrderItem     `json:"items"`
	CreatedAt         *model.Time     `json:"created_at"`
	UpdatedAt         *model.Time     `json:"updated_at"`
	DeletedAt         *model.Time     `json:"deleted_at"`
}

// OrderItem is the wire shape of SnWalletOrderItem.
type OrderItem struct {
	ID                string          `json:"id"`
	OrderID           string          `json:"order_id"`
	ProductIdentifier string          `json:"product_identifier"`
	Quantity          int             `json:"quantity"`
	UnitPrice         decimal.Decimal `json:"unit_price"`
	Currency          string          `json:"currency"`
	CreatedAt         *model.Time     `json:"created_at"`
	UpdatedAt         *model.Time     `json:"updated_at"`
	DeletedAt         *model.Time     `json:"deleted_at"`
}

// InboundOrder is the wire shape of SnWalletInboundOrder.
type InboundOrder struct {
	ID                  string      `json:"id"`
	Provider            string      `json:"provider"`
	ExternalID          string      `json:"external_id"`
	CorrelationID       *string     `json:"correlation_id"`
	ProviderReferenceID *string     `json:"provider_reference_id"`
	ProductIdentifier   *string     `json:"product_identifier"`
	AccountIdentifier   *string     `json:"account_identifier"`
	BegunAt             *model.Time `json:"begun_at"`
	Duration            string      `json:"duration"`
	IsTesting           bool        `json:"is_testing"`
	NotificationSentAt  *model.Time `json:"notification_sent_at"`
	CreatedAt           *model.Time `json:"created_at"`
	UpdatedAt           *model.Time `json:"updated_at"`
	DeletedAt           *model.Time `json:"deleted_at"`
}

// TransferRequest is the wire shape of SnWalletTransferRequest.
type TransferRequest struct {
	ID                  string          `json:"id"`
	Status              int             `json:"status"`
	Currency            string          `json:"currency"`
	Amount              decimal.Decimal `json:"amount"`
	Remark              *string         `json:"remark"`
	Freeze              bool            `json:"freeze"`
	RequireConfirmation bool            `json:"require_confirmation"`
	ExpiresAt           *model.Time     `json:"expires_at"`
	FulfilledAt         *model.Time     `json:"fulfilled_at"`
	CreatorAccountID    string          `json:"creator_account_id"`
	PayeeWalletID       string          `json:"payee_wallet_id"`
	PayeeWallet         *Wallet         `json:"payee_wallet"`
	TransactionID       *string         `json:"transaction_id"`
	Transaction         *Transaction    `json:"transaction"`
	IsExpired           bool            `json:"is_expired"`
	CreatedAt           *model.Time     `json:"created_at"`
	UpdatedAt           *model.Time     `json:"updated_at"`
	DeletedAt           *model.Time     `json:"deleted_at"`
}

// Fund is the wire shape of SnWalletFund.
type Fund struct {
	ID                 string          `json:"id"`
	Currency           string          `json:"currency"`
	TotalAmount        decimal.Decimal `json:"total_amount"`
	RemainingAmount    decimal.Decimal `json:"remaining_amount"`
	AmountOfSplits     int             `json:"amount_of_splits"`
	SplitType          int             `json:"split_type"`
	Status             int             `json:"status"`
	Message            *string         `json:"message"`
	IsOpen             bool            `json:"is_open"`
	IsRaising          bool            `json:"is_raising"`
	TargetAmount       decimal.Decimal `json:"target_amount"`
	ContributionType   int             `json:"contribution_type"`
	ContributionAmount decimal.Decimal `json:"contribution_amount"`
	DeadlineAt         *model.Time     `json:"deadline_at"`
	CreatorAccountID   string          `json:"creator_account_id"`
	CreatorAccount     any             `json:"creator_account"`
	Recipients         []FundRecipient `json:"recipients"`
	ExpiredAt          *model.Time     `json:"expired_at"`
	RaisedAmount       decimal.Decimal `json:"raised_amount"`
	CreatedAt          *model.Time     `json:"created_at"`
	UpdatedAt          *model.Time     `json:"updated_at"`
	DeletedAt          *model.Time     `json:"deleted_at"`
}

// FundRecipient is the wire shape of SnWalletFundRecipient.
type FundRecipient struct {
	ID                 string          `json:"id"`
	FundID             string          `json:"fund_id"`
	RecipientAccountID string          `json:"recipient_account_id"`
	RecipientAccount   any             `json:"recipient_account"`
	Amount             decimal.Decimal `json:"amount"`
	IsReceived         bool            `json:"is_received"`
	ReceivedAt         *model.Time     `json:"received_at"`
	CreatedAt          *model.Time     `json:"created_at"`
	UpdatedAt          *model.Time     `json:"updated_at"`
	DeletedAt          *model.Time     `json:"deleted_at"`
}

// Subscription is the wire shape of SnWalletSubscription.
type Subscription struct {
	ID                  string          `json:"id"`
	BegunAt             *model.Time     `json:"begun_at"`
	EndedAt             *model.Time     `json:"ended_at"`
	Identifier          string          `json:"identifier"`
	GroupIdentifier     *string         `json:"group_identifier"`
	DisplayName         *string         `json:"display_name"`
	PerkLevel           int             `json:"perk_level"`
	IsTesting           bool            `json:"is_testing"`
	IsActive            bool            `json:"is_active"`
	IsFreeTrial         bool            `json:"is_free_trial"`
	Status              int             `json:"status"`
	PaymentMethod       string          `json:"payment_method"`
	PaymentDetails      PaymentDetails  `json:"payment_details"`
	InboundOrderID      *string         `json:"inbound_order_id"`
	BasePrice           decimal.Decimal `json:"base_price"`
	CouponID            *string         `json:"coupon_id"`
	Coupon              *Coupon         `json:"coupon"`
	RenewalAt           *model.Time     `json:"renewal_at"`
	AccountID           string          `json:"account_id"`
	Account             any             `json:"account"`
	Gift                *Gift           `json:"gift"`
	IsAvailable         bool            `json:"is_available"`
	IsPendingActivation bool            `json:"is_pending_activation"`
	FinalPrice          decimal.Decimal `json:"final_price"`
	CreatedAt           *model.Time     `json:"created_at"`
	UpdatedAt           *model.Time     `json:"updated_at"`
	DeletedAt           *model.Time     `json:"deleted_at"`
}

// Coupon is the wire shape of SnWalletCoupon.
type Coupon struct {
	ID             string           `json:"id"`
	Identifier     *string          `json:"identifier"`
	Code           *string          `json:"code"`
	AffectedAt     *model.Time      `json:"affected_at"`
	ExpiredAt      *model.Time      `json:"expired_at"`
	DiscountAmount *decimal.Decimal `json:"discount_amount"`
	DiscountRate   *float64         `json:"discount_rate"`
	MaxUsage       *int             `json:"max_usage"`
	CreatedAt      *model.Time      `json:"created_at"`
	UpdatedAt      *model.Time      `json:"updated_at"`
	DeletedAt      *model.Time      `json:"deleted_at"`
}

// Gift is the wire shape of SnWalletGift.
type Gift struct {
	ID                     string          `json:"id"`
	GifterID               string          `json:"gifter_id"`
	Gifter                 any             `json:"gifter"`
	RecipientID            *string         `json:"recipient_id"`
	Recipient              any             `json:"recipient"`
	GiftCode               string          `json:"gift_code"`
	Message                *string         `json:"message"`
	SubscriptionIdentifier string          `json:"subscription_identifier"`
	BasePrice              decimal.Decimal `json:"base_price"`
	FinalPrice             decimal.Decimal `json:"final_price"`
	Status                 int             `json:"status"`
	RedeemedAt             *model.Time     `json:"redeemed_at"`
	RedeemerID             *string         `json:"redeemer_id"`
	Redeemer               any             `json:"redeemer"`
	SubscriptionID         *string         `json:"subscription_id"`
	ExpiresAt              *model.Time     `json:"expires_at"`
	IsOpenGift             bool            `json:"is_open_gift"`
	PaymentMethod          string          `json:"payment_method"`
	PaymentDetails         PaymentDetails  `json:"payment_details"`
	CouponID               *string         `json:"coupon_id"`
	Coupon                 *Coupon         `json:"coupon"`
	IsRedeemable           bool            `json:"is_redeemable"`
	IsExpired              bool            `json:"is_expired"`
	CreatedAt              *model.Time     `json:"created_at"`
	UpdatedAt              *model.Time     `json:"updated_at"`
	DeletedAt              *model.Time     `json:"deleted_at"`
}

// Merchant is the wire shape of SnMerchant.
type Merchant struct {
	ID              string       `json:"id"`
	PublisherID     string       `json:"publisher_id"`
	PaymentWalletID *string      `json:"payment_wallet_id"`
	Name            *string      `json:"name"`
	Settlements     []Settlement `json:"settlements"`
	CreatedAt       *model.Time  `json:"created_at"`
	UpdatedAt       *model.Time  `json:"updated_at"`
	DeletedAt       *model.Time  `json:"deleted_at"`
}

// Settlement is the wire shape of SnMerchantSettlement.
type Settlement struct {
	ID                      string          `json:"id"`
	MerchantID              string          `json:"merchant_id"`
	OrderID                 *string         `json:"order_id"`
	AwardID                 *string         `json:"award_id"`
	Award                   any             `json:"award"`
	PaymentTransactionID    *string         `json:"payment_transaction_id"`
	PaymentTransaction      *Transaction    `json:"payment_transaction"`
	PaymentWalletID         string          `json:"payment_wallet_id"`
	Currency                string          `json:"currency"`
	Amount                  decimal.Decimal `json:"amount"`
	Status                  int             `json:"status"`
	SettledBy               *int            `json:"settled_by"`
	SettledAt               *model.Time     `json:"settled_at"`
	SettlementTransactionID *string         `json:"settlement_transaction_id"`
	SettlementTransaction   *Transaction    `json:"settlement_transaction"`
	CreatedAt               *model.Time     `json:"created_at"`
	UpdatedAt               *model.Time     `json:"updated_at"`
	DeletedAt               *model.Time     `json:"deleted_at"`
}

// ── conversions ────────────────────────────────────────────────────────────

func timeValue(t time.Time) *model.Time {
	if t.IsZero() {
		return nil
	}
	return model.NewTime(t)
}

func timePointer(t *time.Time) *model.Time {
	if t == nil {
		return nil
	}
	return model.NewTime(*t)
}

func uuidString(id uuid.UUID) string { return id.String() }

func uuidPointer(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

func walletModel(e *WalletEntity, pockets []WalletPocketEntity) Wallet {
	out := Wallet{
		ID:        uuidString(e.ID),
		Pockets:   make([]Pocket, 0, len(pockets)),
		AccountID: uuidPointer(e.AccountID),
		RealmID:   uuidPointer(e.RealmID),
		Name:      e.Name,
		IsPrimary: e.IsPrimary,
		PublicID:  e.PublicID,
		IsDefault: e.IsPrimary,
		CreatedAt: timeValue(e.CreatedAt),
		UpdatedAt: timeValue(e.UpdatedAt),
		DeletedAt: timePointer(deletedTime(e.DeletedAt)),
	}
	for _, pocket := range pockets {
		out.Pockets = append(out.Pockets, pocketModel(&pocket))
	}
	return out
}

func pocketModel(e *WalletPocketEntity) Pocket {
	return Pocket{
		ID:              uuidString(e.ID),
		Currency:        e.Currency,
		Amount:          e.Amount,
		HeldAmount:      e.HeldAmount,
		WalletID:        uuidString(e.WalletID),
		AvailableAmount: e.Amount.Sub(e.HeldAmount),
		CreatedAt:       timeValue(e.CreatedAt),
		UpdatedAt:       timeValue(e.UpdatedAt),
		DeletedAt:       timePointer(deletedTime(e.DeletedAt)),
	}
}

func transactionModel(e *WalletTransactionEntity) Transaction {
	return Transaction{
		ID:                  uuidString(e.ID),
		Currency:            e.Currency,
		Amount:              e.Amount,
		Remarks:             e.Remarks,
		Type:                e.Type,
		Status:              e.Status,
		IsFrozen:            e.IsFrozen,
		RequireConfirmation: e.RequireConfirmation,
		FrozenAt:            timePointer(e.FrozenAt),
		ExpiresAt:           timePointer(e.ExpiresAt),
		ConfirmedAt:         timePointer(e.ConfirmedAt),
		PayerWalletID:       uuidPointer(e.PayerWalletID),
		PayeeWalletID:       uuidPointer(e.PayeeWalletID),
		CreatedAt:           timeValue(e.CreatedAt),
		UpdatedAt:           timeValue(e.UpdatedAt),
		DeletedAt:           timePointer(deletedTime(e.DeletedAt)),
	}
}

func orderModel(e *WalletOrderEntity, items []WalletOrderItemEntity) Order {
	out := Order{
		ID:                uuidString(e.ID),
		Status:            e.Status,
		Currency:          e.Currency,
		Remarks:           e.Remarks,
		AppIdentifier:     e.AppIdentifier,
		ProductIdentifier: e.ProductIdentifier,
		Meta:              jsonValue(e.Meta),
		Amount:            e.Amount,
		ExpiredAt:         timeValue(e.ExpiredAt),
		PayerWalletID:     uuidPointer(e.PayerWalletID),
		PayeeWalletID:     uuidPointer(e.PayeeWalletID),
		TransactionID:     uuidPointer(e.TransactionID),
		InboundOrderID:    uuidPointer(e.InboundOrderID),
		Items:             make([]OrderItem, 0, len(items)),
		CreatedAt:         timeValue(e.CreatedAt),
		UpdatedAt:         timeValue(e.UpdatedAt),
		DeletedAt:         timePointer(deletedTime(e.DeletedAt)),
	}
	for _, item := range items {
		out.Items = append(out.Items, orderItemModel(&item))
	}
	return out
}

func orderItemModel(e *WalletOrderItemEntity) OrderItem {
	return OrderItem{
		ID:                uuidString(e.ID),
		OrderID:           uuidString(e.OrderID),
		ProductIdentifier: e.ProductIdentifier,
		Quantity:          e.Quantity,
		UnitPrice:         e.UnitPrice,
		Currency:          e.Currency,
		CreatedAt:         timeValue(e.CreatedAt),
		UpdatedAt:         timeValue(e.UpdatedAt),
		DeletedAt:         timePointer(deletedTime(e.DeletedAt)),
	}
}

func inboundOrderModel(e *InboundOrderEntity) InboundOrder {
	return InboundOrder{
		ID:                  uuidString(e.ID),
		Provider:            e.Provider,
		ExternalID:          e.ExternalID,
		CorrelationID:       e.CorrelationID,
		ProviderReferenceID: e.ProviderReferenceID,
		ProductIdentifier:   e.ProductIdentifier,
		AccountIdentifier:   e.AccountIdentifier,
		BegunAt:             timeValue(e.BegunAt),
		Duration:            formatInterval(e.Duration),
		IsTesting:           e.IsTesting,
		NotificationSentAt:  timePointer(e.NotificationSentAt),
		CreatedAt:           timeValue(e.CreatedAt),
		UpdatedAt:           timeValue(e.UpdatedAt),
		DeletedAt:           timePointer(deletedTime(e.DeletedAt)),
	}
}

func transferRequestModel(e *WalletTransferRequestEntity) TransferRequest {
	expired := e.Status == TransferExpired || e.ExpiresAt.Before(time.Now())
	return TransferRequest{
		ID:                  uuidString(e.ID),
		Status:              e.Status,
		Currency:            e.Currency,
		Amount:              e.Amount,
		Remark:              e.Remark,
		Freeze:              e.Freeze,
		RequireConfirmation: e.RequireConfirmation,
		ExpiresAt:           timeValue(e.ExpiresAt),
		FulfilledAt:         timePointer(e.FulfilledAt),
		CreatorAccountID:    uuidString(e.CreatorAccountID),
		PayeeWalletID:       uuidString(e.PayeeWalletID),
		TransactionID:       uuidPointer(e.TransactionID),
		IsExpired:           expired,
		CreatedAt:           timeValue(e.CreatedAt),
		UpdatedAt:           timeValue(e.UpdatedAt),
		DeletedAt:           timePointer(deletedTime(e.DeletedAt)),
	}
}

func fundModel(e *WalletFundEntity, recipients []WalletFundRecipientEntity) Fund {
	out := Fund{
		ID:                 uuidString(e.ID),
		Currency:           e.Currency,
		TotalAmount:        e.TotalAmount,
		RemainingAmount:    e.RemainingAmount,
		AmountOfSplits:     e.AmountOfSplits,
		SplitType:          e.SplitType,
		Status:             e.Status,
		Message:            e.Message,
		IsOpen:             e.IsOpen,
		IsRaising:          e.IsRaising,
		TargetAmount:       e.TargetAmount,
		ContributionType:   e.ContributionType,
		ContributionAmount: e.ContributionAmount,
		DeadlineAt:         timePointer(e.DeadlineAt),
		CreatorAccountID:   uuidString(e.CreatorAccountID),
		Recipients:         make([]FundRecipient, 0, len(recipients)),
		ExpiredAt:          timeValue(e.ExpiredAt),
		CreatedAt:          timeValue(e.CreatedAt),
		UpdatedAt:          timeValue(e.UpdatedAt),
		DeletedAt:          timePointer(deletedTime(e.DeletedAt)),
	}
	raised := decimal.Zero
	for _, recipient := range recipients {
		out.Recipients = append(out.Recipients, fundRecipientModel(&recipient))
		if recipient.IsReceived {
			raised = raised.Add(recipient.Amount)
		}
	}
	if e.IsRaising {
		out.RaisedAmount = raised
	}
	return out
}

func fundRecipientModel(e *WalletFundRecipientEntity) FundRecipient {
	return FundRecipient{
		ID:                 uuidString(e.ID),
		FundID:             uuidString(e.FundID),
		RecipientAccountID: uuidString(e.RecipientAccountID),
		Amount:             e.Amount,
		IsReceived:         e.IsReceived,
		ReceivedAt:         timePointer(e.ReceivedAt),
		CreatedAt:          timeValue(e.CreatedAt),
		UpdatedAt:          timeValue(e.UpdatedAt),
		DeletedAt:          timePointer(deletedTime(e.DeletedAt)),
	}
}

func subscriptionModel(e *WalletSubscriptionEntity) Subscription {
	now := time.Now()
	return Subscription{
		ID:                  uuidString(e.ID),
		BegunAt:             timeValue(e.BegunAt),
		EndedAt:             timePointer(e.EndedAt),
		Identifier:          e.Identifier,
		GroupIdentifier:     e.GroupIdentifier,
		DisplayName:         e.DisplayName,
		PerkLevel:           e.PerkLevel,
		IsTesting:           e.IsTesting,
		IsActive:            e.IsActive,
		IsFreeTrial:         e.IsFreeTrial,
		Status:              e.Status,
		PaymentMethod:       e.PaymentMethod,
		PaymentDetails:      parsePaymentDetails(e.PaymentDetails),
		InboundOrderID:      uuidPointer(e.InboundOrderID),
		BasePrice:           e.BasePrice,
		CouponID:            uuidPointer(e.CouponID),
		RenewalAt:           timePointer(e.RenewalAt),
		AccountID:           uuidString(e.AccountID),
		IsAvailable:         subscriptionAvailable(e, now),
		IsPendingActivation: subscriptionPending(e, now),
		FinalPrice:          e.BasePrice,
		CreatedAt:           timeValue(e.CreatedAt),
		UpdatedAt:           timeValue(e.UpdatedAt),
		DeletedAt:           timePointer(deletedTime(e.DeletedAt)),
	}
}

func couponModel(e *WalletCouponEntity) Coupon {
	return Coupon{
		ID:             uuidString(e.ID),
		Identifier:     e.Identifier,
		Code:           e.Code,
		AffectedAt:     timePointer(e.AffectedAt),
		ExpiredAt:      timePointer(e.ExpiredAt),
		DiscountAmount: e.DiscountAmount,
		DiscountRate:   e.DiscountRate,
		MaxUsage:       e.MaxUsage,
		CreatedAt:      timeValue(e.CreatedAt),
		UpdatedAt:      timeValue(e.UpdatedAt),
		DeletedAt:      timePointer(deletedTime(e.DeletedAt)),
	}
}

func giftModel(e *WalletGiftEntity) Gift {
	now := time.Now()
	return Gift{
		ID:                     uuidString(e.ID),
		GifterID:               uuidString(e.GifterID),
		RecipientID:            uuidPointer(e.RecipientID),
		GiftCode:               e.GiftCode,
		Message:                e.Message,
		SubscriptionIdentifier: e.SubscriptionIdentifier,
		BasePrice:              e.BasePrice,
		FinalPrice:             e.FinalPrice,
		Status:                 e.Status,
		RedeemedAt:             timePointer(e.RedeemedAt),
		RedeemerID:             uuidPointer(e.RedeemerID),
		SubscriptionID:         uuidPointer(e.SubscriptionID),
		ExpiresAt:              timeValue(e.ExpiresAt),
		IsOpenGift:             e.IsOpenGift,
		PaymentMethod:          e.PaymentMethod,
		PaymentDetails:         parsePaymentDetails(e.PaymentDetails),
		CouponID:               uuidPointer(e.CouponID),
		IsRedeemable:           e.Status == GiftSent && !now.After(e.ExpiresAt),
		IsExpired:              e.Status != GiftRedeemed && e.Status != GiftCancelled && now.After(e.ExpiresAt),
		CreatedAt:              timeValue(e.CreatedAt),
		UpdatedAt:              timeValue(e.UpdatedAt),
		DeletedAt:              timePointer(deletedTime(e.DeletedAt)),
	}
}

func merchantModel(e *MerchantEntity, settlements []MerchantSettlementEntity) Merchant {
	out := Merchant{
		ID:              uuidString(e.ID),
		PublisherID:     uuidString(e.PublisherID),
		PaymentWalletID: uuidPointer(e.PaymentWalletID),
		Name:            e.Name,
		Settlements:     make([]Settlement, 0, len(settlements)),
		CreatedAt:       timeValue(e.CreatedAt),
		UpdatedAt:       timeValue(e.UpdatedAt),
		DeletedAt:       timePointer(deletedTime(e.DeletedAt)),
	}
	for _, settlement := range settlements {
		out.Settlements = append(out.Settlements, settlementModel(&settlement))
	}
	return out
}

func settlementModel(e *MerchantSettlementEntity) Settlement {
	return Settlement{
		ID:                      uuidString(e.ID),
		MerchantID:              uuidString(e.MerchantID),
		OrderID:                 uuidPointer(e.OrderID),
		AwardID:                 uuidPointer(e.AwardID),
		PaymentTransactionID:    uuidPointer(e.PaymentTransactionID),
		PaymentWalletID:         uuidString(e.PaymentWalletID),
		Currency:                e.Currency,
		Amount:                  e.Amount,
		Status:                  e.Status,
		SettledBy:               e.SettledBy,
		SettledAt:               timePointer(e.SettledAt),
		SettlementTransactionID: uuidPointer(e.SettlementTransactionID),
		CreatedAt:               timeValue(e.CreatedAt),
		UpdatedAt:               timeValue(e.UpdatedAt),
		DeletedAt:               timePointer(deletedTime(e.DeletedAt)),
	}
}

func deletedTime(deleted gorm.DeletedAt) *time.Time {
	if !deleted.Valid {
		return nil
	}
	t := deleted.Time
	return &t
}

func jsonValue(raw datatypes.JSON) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return value
}

// formatInterval renders a Postgres interval as an ISO-8601 duration, matching
// the C# NodaTime Duration serializer.
func formatInterval(interval pgtype.Interval) string {
	if !interval.Valid {
		return "PT0S"
	}
	micros := interval.Microseconds
	seconds := micros / 1_000_000
	frac := micros % 1_000_000
	days := interval.Days
	months := interval.Months
	out := "P"
	if months != 0 {
		out += fmt.Sprintf("%dM", months)
	}
	if days != 0 {
		out += fmt.Sprintf("%dD", days)
	}
	if seconds != 0 || frac != 0 || (months == 0 && days == 0) {
		out += "T"
		if frac == 0 {
			out += fmt.Sprintf("%dS", seconds)
		} else {
			out += fmt.Sprintf("%d.%06dS", seconds, frac)
		}
	}
	return out
}
