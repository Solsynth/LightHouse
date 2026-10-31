// Package wallet is the ported DysonNetwork.Wallet service. It owns wallets,
// pockets, the transaction ledger, orders, subscriptions, gifts, coupons,
// merchants and the payment-provider inbound orders, and serves them over the
// same HTTP and gRPC surfaces the C# service served.
//
// Entities keep the legacy table and column names, so a C# wallet database
// maps row-for-row: stargate-migrate copies it with --wallet-dsn, and the
// module can also be pointed straight at it.
package wallet

import (
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// EntityBase is embedded by every persisted wallet entity and mirrors the C#
// ModelBase (timestamps plus soft delete).
type EntityBase struct {
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

// WalletEntity is `wallets`.
type WalletEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AccountID *uuid.UUID `gorm:"column:account_id;index"`
	RealmID   *uuid.UUID `gorm:"column:realm_id;index"`
	Name      string     `gorm:"column:name;not null"`
	IsPrimary bool       `gorm:"column:is_primary;not null"`
	PublicID  *string    `gorm:"column:public_id"`
}

func (WalletEntity) TableName() string { return "wallets" }

// WalletPocketEntity is `wallet_pockets`.
type WalletPocketEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	Currency   string          `gorm:"column:currency;not null"`
	Amount     decimal.Decimal `gorm:"column:amount;type:numeric;not null"`
	HeldAmount decimal.Decimal `gorm:"column:held_amount;type:numeric;not null"`
	WalletID   uuid.UUID       `gorm:"column:wallet_id;index"`
}

func (WalletPocketEntity) TableName() string { return "wallet_pockets" }

// WalletOrderEntity is `payment_orders`.
type WalletOrderEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	Status            int             `gorm:"column:status;not null"`
	Currency          string          `gorm:"column:currency;not null"`
	Remarks           *string         `gorm:"column:remarks"`
	AppIdentifier     *string         `gorm:"column:app_identifier"`
	ProductIdentifier *string         `gorm:"column:product_identifier"`
	Meta              datatypes.JSON  `gorm:"column:meta;type:jsonb"`
	Amount            decimal.Decimal `gorm:"column:amount;type:numeric;not null"`
	ExpiredAt         time.Time       `gorm:"column:expired_at;not null"`
	PayerWalletID     *uuid.UUID      `gorm:"column:payer_wallet_id;index"`
	PayeeWalletID     *uuid.UUID      `gorm:"column:payee_wallet_id;index"`
	TransactionID     *uuid.UUID      `gorm:"column:transaction_id;index"`
	InboundOrderID    *uuid.UUID      `gorm:"column:inbound_order_id;index"`
}

func (WalletOrderEntity) TableName() string { return "payment_orders" }

// WalletOrderItemEntity is `wallet_order_items`.
type WalletOrderItemEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	OrderID           uuid.UUID       `gorm:"column:order_id;index"`
	ProductIdentifier string          `gorm:"column:product_identifier;not null"`
	Quantity          int             `gorm:"column:quantity;not null"`
	UnitPrice         decimal.Decimal `gorm:"column:unit_price;type:numeric;not null"`
	Currency          string          `gorm:"column:currency;not null"`
}

func (WalletOrderItemEntity) TableName() string { return "wallet_order_items" }

// InboundOrderEntity is `inbound_orders`.
type InboundOrderEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	Provider            string          `gorm:"column:provider;not null;uniqueIndex:ux_inbound_orders_provider_external_id"`
	ExternalID          string          `gorm:"column:external_id;not null;uniqueIndex:ux_inbound_orders_provider_external_id"`
	CorrelationID       *string         `gorm:"column:correlation_id"`
	ProviderReferenceID *string         `gorm:"column:provider_reference_id"`
	ProductIdentifier   *string         `gorm:"column:product_identifier"`
	AccountIdentifier   *string         `gorm:"column:account_identifier"`
	BegunAt             time.Time       `gorm:"column:begun_at;not null"`
	Duration            pgtype.Interval `gorm:"column:duration;type:interval;not null"`
	IsTesting           bool            `gorm:"column:is_testing;not null"`
	NotificationSentAt  *time.Time      `gorm:"column:notification_sent_at"`
}

func (InboundOrderEntity) TableName() string { return "inbound_orders" }

// WalletTransactionEntity is `payment_transactions`.
type WalletTransactionEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	Currency            string          `gorm:"column:currency;not null"`
	Amount              decimal.Decimal `gorm:"column:amount;type:numeric;not null"`
	Remarks             *string         `gorm:"column:remarks"`
	Type                int             `gorm:"column:type;not null"`
	Status              int             `gorm:"column:status;not null"`
	IsFrozen            bool            `gorm:"column:is_frozen;not null"`
	RequireConfirmation bool            `gorm:"column:require_confirmation;not null"`
	FrozenAt            *time.Time      `gorm:"column:frozen_at"`
	ExpiresAt           *time.Time      `gorm:"column:expires_at"`
	ConfirmedAt         *time.Time      `gorm:"column:confirmed_at"`
	PayerWalletID       *uuid.UUID      `gorm:"column:payer_wallet_id;index"`
	PayeeWalletID       *uuid.UUID      `gorm:"column:payee_wallet_id;index"`
}

func (WalletTransactionEntity) TableName() string { return "payment_transactions" }

// WalletTransferRequestEntity is `wallet_transfer_requests`.
type WalletTransferRequestEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	Status              int             `gorm:"column:status;not null"`
	Currency            string          `gorm:"column:currency;not null"`
	Amount              decimal.Decimal `gorm:"column:amount;type:numeric;not null"`
	Remark              *string         `gorm:"column:remark"`
	Freeze              bool            `gorm:"column:freeze;not null"`
	RequireConfirmation bool            `gorm:"column:require_confirmation;not null"`
	ExpiresAt           time.Time       `gorm:"column:expires_at;not null"`
	FulfilledAt         *time.Time      `gorm:"column:fulfilled_at"`
	CreatorAccountID    uuid.UUID       `gorm:"column:creator_account_id;index"`
	PayeeWalletID       uuid.UUID       `gorm:"column:payee_wallet_id;index"`
	TransactionID       *uuid.UUID      `gorm:"column:transaction_id"`
}

func (WalletTransferRequestEntity) TableName() string { return "wallet_transfer_requests" }

// WalletFundEntity is `wallet_funds`.
type WalletFundEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	Currency           string          `gorm:"column:currency;not null"`
	TotalAmount        decimal.Decimal `gorm:"column:total_amount;type:numeric;not null"`
	RemainingAmount    decimal.Decimal `gorm:"column:remaining_amount;type:numeric;not null"`
	AmountOfSplits     int             `gorm:"column:amount_of_splits;not null"`
	SplitType          int             `gorm:"column:split_type;not null"`
	Status             int             `gorm:"column:status;not null"`
	Message            *string         `gorm:"column:message"`
	IsOpen             bool            `gorm:"column:is_open;not null"`
	IsRaising          bool            `gorm:"column:is_raising;not null"`
	TargetAmount       decimal.Decimal `gorm:"column:target_amount;type:numeric;not null"`
	ContributionType   int             `gorm:"column:contribution_type;not null"`
	ContributionAmount decimal.Decimal `gorm:"column:contribution_amount;type:numeric;not null"`
	DeadlineAt         *time.Time      `gorm:"column:deadline_at"`
	CreatorAccountID   uuid.UUID       `gorm:"column:creator_account_id;index"`
	ExpiredAt          time.Time       `gorm:"column:expired_at;not null"`
}

func (WalletFundEntity) TableName() string { return "wallet_funds" }

// WalletFundRecipientEntity is `wallet_fund_recipients`.
type WalletFundRecipientEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	FundID             uuid.UUID       `gorm:"column:fund_id;index"`
	RecipientAccountID uuid.UUID       `gorm:"column:recipient_account_id;index"`
	Amount             decimal.Decimal `gorm:"column:amount;type:numeric;not null"`
	IsReceived         bool            `gorm:"column:is_received;not null"`
	ReceivedAt         *time.Time      `gorm:"column:received_at"`
}

func (WalletFundRecipientEntity) TableName() string { return "wallet_fund_recipients" }

// WalletSubscriptionEntity is `wallet_subscriptions`.
type WalletSubscriptionEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	BegunAt         time.Time       `gorm:"column:begun_at;not null"`
	EndedAt         *time.Time      `gorm:"column:ended_at"`
	Identifier      string          `gorm:"column:identifier;not null;index"`
	GroupIdentifier *string         `gorm:"column:group_identifier"`
	DisplayName     *string         `gorm:"column:display_name"`
	PerkLevel       int             `gorm:"column:perk_level;not null"`
	IsTesting       bool            `gorm:"column:is_testing;not null"`
	IsActive        bool            `gorm:"column:is_active;not null"`
	IsFreeTrial     bool            `gorm:"column:is_free_trial;not null"`
	Status          int             `gorm:"column:status;not null;index"`
	PaymentMethod   string          `gorm:"column:payment_method;not null"`
	PaymentDetails  datatypes.JSON  `gorm:"column:payment_details;type:jsonb;not null"`
	InboundOrderID  *uuid.UUID      `gorm:"column:inbound_order_id;index"`
	BasePrice       decimal.Decimal `gorm:"column:base_price;type:numeric;not null"`
	CouponID        *uuid.UUID      `gorm:"column:coupon_id;index"`
	RenewalAt       *time.Time      `gorm:"column:renewal_at"`
	AccountID       uuid.UUID       `gorm:"column:account_id;index"`
}

func (WalletSubscriptionEntity) TableName() string { return "wallet_subscriptions" }

// WalletSubscriptionDefinitionEntity is `wallet_subscription_definitions`.
type WalletSubscriptionDefinitionEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	Identifier           string           `gorm:"column:identifier;not null"`
	GroupIdentifier      *string          `gorm:"column:group_identifier"`
	DisplayName          string           `gorm:"column:display_name;not null"`
	Currency             string           `gorm:"column:currency;not null"`
	BasePrice            decimal.Decimal  `gorm:"column:base_price;type:numeric;not null"`
	PerkLevel            int              `gorm:"column:perk_level;not null"`
	MinimumAccountLevel  *int             `gorm:"column:minimum_account_level"`
	ExperienceMultiplier *decimal.Decimal `gorm:"column:experience_multiplier;type:numeric"`
	GoldenPointReward    *int             `gorm:"column:golden_point_reward"`
	DisplayConfig        datatypes.JSON   `gorm:"column:display_config;type:jsonb"`
	PaymentPolicy        datatypes.JSON   `gorm:"column:payment_policy;type:jsonb;not null"`
	GiftPolicy           datatypes.JSON   `gorm:"column:gift_policy;type:jsonb"`
	ProviderMappings     datatypes.JSON   `gorm:"column:provider_mappings;type:jsonb;not null"`
	AppIdentifier        *string          `gorm:"column:app_identifier"`
}

func (WalletSubscriptionDefinitionEntity) TableName() string {
	return "wallet_subscription_definitions"
}

// WalletGiftEntity is `wallet_gifts`.
type WalletGiftEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	GifterID               uuid.UUID       `gorm:"column:gifter_id;index"`
	RecipientID            *uuid.UUID      `gorm:"column:recipient_id;index"`
	GiftCode               string          `gorm:"column:gift_code;not null;index"`
	Message                *string         `gorm:"column:message"`
	SubscriptionIdentifier string          `gorm:"column:subscription_identifier;not null"`
	BasePrice              decimal.Decimal `gorm:"column:base_price;type:numeric;not null"`
	FinalPrice             decimal.Decimal `gorm:"column:final_price;type:numeric;not null"`
	Status                 int             `gorm:"column:status;not null"`
	RedeemedAt             *time.Time      `gorm:"column:redeemed_at"`
	RedeemerID             *uuid.UUID      `gorm:"column:redeemer_id"`
	SubscriptionID         *uuid.UUID      `gorm:"column:subscription_id;uniqueIndex"`
	ExpiresAt              time.Time       `gorm:"column:expires_at;not null"`
	IsOpenGift             bool            `gorm:"column:is_open_gift;not null"`
	PaymentMethod          string          `gorm:"column:payment_method;not null"`
	PaymentDetails         datatypes.JSON  `gorm:"column:payment_details;type:jsonb;not null"`
	CouponID               *uuid.UUID      `gorm:"column:coupon_id;index"`
}

func (WalletGiftEntity) TableName() string { return "wallet_gifts" }

// WalletCouponEntity is `wallet_coupons`.
type WalletCouponEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	Identifier     *string          `gorm:"column:identifier;index"`
	Code           *string          `gorm:"column:code;index"`
	AffectedAt     *time.Time       `gorm:"column:affected_at"`
	ExpiredAt      *time.Time       `gorm:"column:expired_at"`
	DiscountAmount *decimal.Decimal `gorm:"column:discount_amount;type:numeric"`
	DiscountRate   *float64         `gorm:"column:discount_rate"`
	MaxUsage       *int             `gorm:"column:max_usage"`
}

func (WalletCouponEntity) TableName() string { return "wallet_coupons" }

// MerchantEntity is `merchants`.
type MerchantEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	PublisherID     uuid.UUID  `gorm:"column:publisher_id;index"`
	PaymentWalletID *uuid.UUID `gorm:"column:payment_wallet_id;index"`
	Name            *string    `gorm:"column:name"`
}

func (MerchantEntity) TableName() string { return "merchants" }

// MerchantSettlementEntity is `merchant_settlements`.
type MerchantSettlementEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	MerchantID              uuid.UUID       `gorm:"column:merchant_id;index"`
	OrderID                 *uuid.UUID      `gorm:"column:order_id;index"`
	AwardID                 *uuid.UUID      `gorm:"column:award_id"`
	PaymentTransactionID    *uuid.UUID      `gorm:"column:payment_transaction_id;index"`
	PaymentWalletID         uuid.UUID       `gorm:"column:payment_wallet_id;index"`
	Currency                string          `gorm:"column:currency;not null"`
	Amount                  decimal.Decimal `gorm:"column:amount;type:numeric;not null"`
	Status                  int             `gorm:"column:status;not null;index"`
	SettledBy               *int            `gorm:"column:settled_by"`
	SettledAt               *time.Time      `gorm:"column:settled_at"`
	SettlementTransactionID *uuid.UUID      `gorm:"column:settlement_transaction_id;index"`
}

func (MerchantSettlementEntity) TableName() string { return "merchant_settlements" }

// schemaHooks are the indexes AutoMigrate cannot express. The C# schema made
// the subscription-definition identifier unique per live row (identifier,
// deleted_at), which needs a partial index.
var schemaHooks = []func(*gorm.DB) error{
	func(db *gorm.DB) error {
		return db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS ux_wallet_subscription_definitions_identifier
			ON wallet_subscription_definitions (identifier)
			WHERE deleted_at IS NULL`).Error
	},
	func(db *gorm.DB) error {
		return db.Exec(`CREATE INDEX IF NOT EXISTS ix_wallet_subscriptions_account_identifier
			ON wallet_subscriptions (account_id, identifier)`).Error
	},
	func(db *gorm.DB) error {
		return db.Exec(`CREATE INDEX IF NOT EXISTS ix_wallet_subscriptions_account_active
			ON wallet_subscriptions (account_id, is_active)`).Error
	},
}

// entities is the AutoMigrate input for the module.
func entities() []any {
	return []any{
		&WalletEntity{},
		&WalletPocketEntity{},
		&WalletOrderEntity{},
		&WalletOrderItemEntity{},
		&InboundOrderEntity{},
		&WalletTransactionEntity{},
		&WalletTransferRequestEntity{},
		&WalletFundEntity{},
		&WalletFundRecipientEntity{},
		&WalletSubscriptionEntity{},
		&WalletSubscriptionDefinitionEntity{},
		&WalletGiftEntity{},
		&WalletCouponEntity{},
		&MerchantEntity{},
		&MerchantSettlementEntity{},
	}
}
