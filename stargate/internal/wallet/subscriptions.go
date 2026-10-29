package wallet

// Subscription domain: creation, activation, cancellation, renewal, gifts and
// the provider-order application path. Ported from DysonNetwork.Wallet's
// Payment/SubscriptionService.cs, Payment/SubscriptionRenewalJob.cs and
// Payment/GiftCleanupJob.cs.
//
// The C# service subscribed to PaymentOrderEvent; Stargate instead exposes
// HandleOrderPaid as an OrderPaidHook. The wallet-product branch belongs to
// the product service, so this hook reports ErrNotHandled for orders it does
// not own and the module wiring orders the subscription and product hooks.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// ErrNotHandled reports that an order-paid hook did not apply the order. The
// module wiring uses it to decide which hook owns a paid order.
var ErrNotHandled = errors.New("order not handled by this hook")

// ProviderOrder is one paid order from an external payment provider. It is
// the ported ISubscriptionOrder: each provider handler resolves its payload
// into this shape before the subscription/product services see it.
type ProviderOrder struct {
	// ID is the provider order/transaction identifier (out_trade_no,
	// transaction id, transactionId).
	ID string
	// Provider is the payment method identifier: afdian, paddle, apple_store.
	Provider string
	// SubscriptionID is the SKU the provider charged (Afdian SKU/plan id,
	// Paddle price id, Apple product id).
	SubscriptionID string
	// AccountID is the provider's account token/user id. Some providers embed
	// the Dyson account id directly.
	AccountID string
	// BegunAt is when the purchase began.
	BegunAt time.Time
	// Duration is the billed cycle (zero = catalog default).
	Duration time.Duration
	// IsTesting marks sandbox purchases.
	IsTesting bool
	// CorrelationID links renewals of the same provider subscription.
	CorrelationID string
	// CustomOrderID is Afdian's custom_order_id (the Dyson account id when the
	// checkout set it).
	CustomOrderID string
	// Quantity is the purchased quantity (Apple only, 1 elsewhere).
	Quantity int
}

// ProviderOrderProcessor takes over paid provider orders that are not platform
// subscriptions (wallet product packs, app-owned products). The product
// service implements it; a nil processor disables the branch.
type ProviderOrderProcessor interface {
	// HandlesProviderOrder reports whether this processor owns the order.
	HandlesProviderOrder(ctx context.Context, order ProviderOrder) (bool, error)
	// ApplyProviderOrder applies the purchase and returns the result to echo
	// back to the client.
	ApplyProviderOrder(ctx context.Context, order ProviderOrder, inbound *InboundOrderEntity, extraMeta map[string]any) (any, error)
}

// Subscriptions is the subscription service graph. One instance exists per
// Service: HTTP, gRPC, jobs and the order hook all resolve it through
// NewSubscriptions, so the configured settings and provider handlers are
// shared.
type Subscriptions struct {
	svc *Service

	// Settings is the wallet module settings map. It mirrors the C#
	// Payment:* configuration sections; see subscription_catalog.go.
	Settings map[string]any

	// Products applies provider orders that are not platform subscriptions.
	Products ProviderOrderProcessor

	// Provider handlers. Configure builds them from Settings; a nil handler
	// means the provider is not configured.
	Afdian *AfdianProvider
	Paddle *PaddleProvider
	Apple  *AppleProvider

	log *slog.Logger
}

// subscriptionsInstances memoizes one Subscriptions per Service.
var subscriptionsInstances sync.Map // map[*Service]*Subscriptions

// NewSubscriptions returns the subscription service bound to svc. Repeated
// calls return the same instance so the catalog settings and provider
// handlers installed by Configure are visible to every surface.
func NewSubscriptions(svc *Service) *Subscriptions {
	if svc == nil {
		return &Subscriptions{log: slog.Default()}
	}
	if existing, ok := subscriptionsInstances.Load(svc); ok {
		return existing.(*Subscriptions)
	}
	created := &Subscriptions{svc: svc, log: svc.Log}
	actual, _ := subscriptionsInstances.LoadOrStore(svc, created)
	return actual.(*Subscriptions)
}

// Configure installs the module settings and rebuilds the provider handlers.
// It is idempotent; HTTP registration and the module wiring both call it.
func (s *Subscriptions) Configure(settings map[string]any, log *slog.Logger) {
	if log == nil && s.svc != nil {
		log = s.svc.Log
	}
	if log == nil {
		log = slog.Default()
	}
	s.log = log
	s.Settings = settings
	s.Afdian = newAfdianProvider(settings, log)
	s.Paddle = newPaddleProvider(settings, log)
	s.Apple = newAppleProvider(settings, log)
}

func (s *Subscriptions) logger() *slog.Logger {
	if s.log != nil {
		return s.log
	}
	return slog.Default()
}

func (s *Subscriptions) db(ctx context.Context) *gorm.DB {
	return s.svc.DB.WithContext(ctx)
}

// ── account helpers ────────────────────────────────────────────────────────

// accountRow and accountConnectionRow read the accounts module's tables. The
// C# service resolved accounts over gRPC; Stargate owns both tables in the
// same database, so the lookup is local.
type accountRow struct {
	ID uuid.UUID `gorm:"column:id"`
}

func (accountRow) TableName() string { return "accounts" }

type accountConnectionRow struct {
	AccountID uuid.UUID `gorm:"column:account_id"`
}

func (accountConnectionRow) TableName() string { return "account_connections" }

func (s *Subscriptions) accountExists(ctx context.Context, accountID uuid.UUID) (bool, error) {
	var row accountRow
	err := s.db(ctx).Where("id = ?", accountID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// resolveConnectionAccount maps a provider connection identifier to its
// account, mirroring accounts.GetAccountByConnection.
func (s *Subscriptions) resolveConnectionAccount(ctx context.Context, provider, identifier string) (*uuid.UUID, error) {
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(identifier) == "" {
		return nil, nil
	}
	var row accountConnectionRow
	err := s.db(ctx).
		Where("LOWER(provider) = LOWER(?) AND provided_identifier = ?", provider, identifier).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	accountID := row.AccountID
	return &accountID, nil
}

// ── coupon / price helpers ─────────────────────────────────────────────────

// findCoupon mirrors the C# lookup: a coupon matches by id or by identifier
// (not by its human-readable code).
func (s *Subscriptions) findCoupon(ctx context.Context, value string) (*WalletCouponEntity, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	var coupon WalletCouponEntity
	query := s.db(ctx).Where("identifier = ?", value)
	if id, err := uuid.Parse(value); err == nil {
		query = s.db(ctx).Where("id = ? OR (identifier IS NOT NULL AND identifier = ?)", id, value)
	}
	err := query.First(&coupon).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &coupon, nil
}

func (s *Subscriptions) couponByID(ctx context.Context, id *uuid.UUID) (*WalletCouponEntity, error) {
	if id == nil {
		return nil, nil
	}
	var coupon WalletCouponEntity
	err := s.db(ctx).Where("id = ?", *id).First(&coupon).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &coupon, nil
}

// calculateFinalPrice mirrors SnWalletSubscription.CalculateFinalPriceAt.
func calculateFinalPrice(basePrice decimal.Decimal, isFreeTrial bool, coupon *WalletCouponEntity, at time.Time) decimal.Decimal {
	if isFreeTrial {
		return decimal.Zero
	}
	if coupon == nil {
		return basePrice
	}
	if (coupon.AffectedAt != nil && at.Before(*coupon.AffectedAt)) ||
		(coupon.ExpiredAt != nil && at.After(*coupon.ExpiredAt)) {
		return basePrice
	}
	if coupon.DiscountAmount != nil {
		return basePrice.Sub(*coupon.DiscountAmount)
	}
	if coupon.DiscountRate != nil {
		rate := decimal.NewFromFloat(1 - *coupon.DiscountRate)
		return basePrice.Mul(rate)
	}
	return basePrice
}

// subscriptionFinalPrice computes the coupon-adjusted price of an entity.
func (s *Subscriptions) subscriptionFinalPrice(ctx context.Context, subscription *WalletSubscriptionEntity) (decimal.Decimal, error) {
	coupon, err := s.couponByID(ctx, subscription.CouponID)
	if err != nil {
		return decimal.Zero, err
	}
	return calculateFinalPrice(subscription.BasePrice, subscription.IsFreeTrial, coupon, time.Now().UTC()), nil
}

// ── subscriptions ──────────────────────────────────────────────────────────

// CreateSubscription mirrors CreateSubscriptionAsync. It records an unpaid
// subscription placeholder that a paid order activates.
func (s *Subscriptions) CreateSubscription(ctx context.Context, account *model.Account, identifier, paymentMethod string, details PaymentDetails, cycleDuration *time.Duration, coupon string, isFreeTrial, isAutoRenewal, noop bool) (*WalletSubscriptionEntity, error) {
	definition, err := s.GetDefinition(ctx, identifier)
	if err != nil {
		return nil, err
	}
	if definition == nil {
		return nil, fmt.Errorf("Subscription %s was not found.", identifier)
	}
	policy := parsePaymentPolicy(definition.PaymentPolicy)
	if !policy.IsPaymentMethodAllowed(paymentMethod) {
		return nil, fmt.Errorf("Payment method %s is not allowed for subscription %s.", paymentMethod, identifier)
	}
	if account == nil {
		return nil, errors.New("account is required")
	}
	accountID, err := uuid.Parse(account.Id)
	if err != nil {
		return nil, errors.New("account id is invalid")
	}
	if strings.EqualFold(paymentMethod, PaymentInAppWallet) && definition.MinimumAccountLevel != nil {
		level := 0
		if account.Profile != nil {
			level = account.Profile.Level
		}
		if level < *definition.MinimumAccountLevel {
			return nil, fmt.Errorf("Account level must be at least %d to purchase %s.", *definition.MinimumAccountLevel, identifier)
		}
	}

	cycle := s.DefaultSubscriptionDuration()
	if cycleDuration != nil {
		cycle = *cycleDuration
	}

	existing, err := s.GetSubscription(ctx, accountID, identifier)
	if err != nil {
		return nil, err
	}
	if existing != nil && noop {
		return existing, nil
	}

	var prevFreeTrial *WalletSubscriptionEntity
	if isFreeTrial {
		var previous WalletSubscriptionEntity
		err := s.db(ctx).Where("account_id = ? AND identifier = ? AND is_free_trial = ?", accountID, identifier, true).
			First(&previous).Error
		switch {
		case err == nil:
			prevFreeTrial = &previous
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return nil, err
		}
	}

	var couponData *WalletCouponEntity
	if strings.TrimSpace(coupon) != "" {
		couponData, err = s.findCoupon(ctx, coupon)
		if err != nil {
			return nil, err
		}
		if couponData == nil {
			return nil, fmt.Errorf("Coupon %s was not found.", coupon)
		}
	}
	if isFreeTrial && prevFreeTrial != nil {
		return nil, errors.New("Free trial already exists.")
	}

	now := time.Now().UTC()
	endedAt := now.Add(cycle)
	displayName := definition.DisplayName
	subscription := &WalletSubscriptionEntity{
		ID:              newID(),
		BegunAt:         now,
		EndedAt:         &endedAt,
		Identifier:      identifier,
		GroupIdentifier: definition.GroupIdentifier,
		DisplayName:     &displayName,
		PerkLevel:       definition.PerkLevel,
		IsActive:        true,
		IsFreeTrial:     isFreeTrial,
		Status:          SubscriptionUnpaid,
		PaymentMethod:   paymentMethod,
		PaymentDetails:  paymentDetailsJSON(details),
		BasePrice:       definition.BasePrice,
		AccountID:       accountID,
	}
	if couponData != nil {
		subscription.CouponID = &couponData.ID
	}
	if !isFreeTrial && isAutoRenewal && policy.AllowInternalWalletRenewal {
		renewal := endedAt
		subscription.RenewalAt = &renewal
	}
	if err := s.db(ctx).Create(subscription).Error; err != nil {
		return nil, err
	}
	return subscription, nil
}

// CancelSubscription mirrors CancelSubscriptionAsync.
func (s *Subscriptions) CancelSubscription(ctx context.Context, accountID uuid.UUID, identifier string) (*WalletSubscriptionEntity, error) {
	subscription, err := s.GetSubscription(ctx, accountID, identifier)
	if err != nil {
		return nil, err
	}
	if subscription == nil {
		return nil, fmt.Errorf("Subscription with identifier %s was not found.", identifier)
	}
	if subscription.Status != SubscriptionActive {
		return nil, errors.New("Subscription is already cancelled.")
	}
	if subscription.RenewalAt == nil {
		return nil, errors.New("Subscription is no need to be cancelled.")
	}
	if subscription.PaymentMethod != PaymentInAppWallet {
		return nil, errors.New("Only in-app wallet subscription can be cancelled. For other payment methods, please head to the payment provider.")
	}
	now := time.Now().UTC()
	endedAt := now
	if now.Before(subscription.BegunAt) {
		endedAt = subscription.BegunAt
	}
	subscription.IsActive = false
	subscription.Status = SubscriptionCancelled
	subscription.RenewalAt = nil
	subscription.EndedAt = &endedAt
	if err := s.db(ctx).Save(subscription).Error; err != nil {
		return nil, err
	}
	return subscription, nil
}

// CreateSubscriptionOrder mirrors CreateSubscriptionOrder: it bills the
// current cycle from the account wallet.
func (s *Subscriptions) CreateSubscriptionOrder(ctx context.Context, accountID uuid.UUID, identifier string) (*WalletOrderEntity, error) {
	var subscription WalletSubscriptionEntity
	err := s.db(ctx).
		Where("account_id = ? AND identifier = ? AND status IN ?", accountID, identifier,
			[]int{SubscriptionUnpaid, SubscriptionActive, SubscriptionExpired}).
		Order("begun_at DESC").
		First(&subscription).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.New("No matching subscription found.")
	}
	if err != nil {
		return nil, err
	}
	definition, err := s.GetDefinition(ctx, subscription.Identifier)
	if err != nil {
		return nil, err
	}
	if definition == nil {
		return nil, errors.New("No matching subscription found.")
	}
	price, err := s.subscriptionFinalPrice(ctx, &subscription)
	if err != nil {
		return nil, err
	}
	return s.createUserOrder(ctx, accountID, definition.Currency, price, "internal", &identifier, map[string]any{
		"subscription_id":         subscription.ID.String(),
		"subscription_identifier": subscription.Identifier,
	})
}

// CreateGiftOrder mirrors CreateGiftOrder.
func (s *Subscriptions) CreateGiftOrder(ctx context.Context, accountID, giftID uuid.UUID) (*WalletOrderEntity, error) {
	var gift WalletGiftEntity
	err := s.db(ctx).Where("id = ? AND gifter_id = ? AND status = ?", giftID, accountID, GiftCreated).First(&gift).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.New("No matching gift found.")
	}
	if err != nil {
		return nil, err
	}
	definition, err := s.GetDefinition(ctx, gift.SubscriptionIdentifier)
	if err != nil {
		return nil, err
	}
	if definition == nil {
		return nil, errors.New("No matching subscription found.")
	}
	return s.createUserOrder(ctx, accountID, definition.Currency, gift.FinalPrice, "gift", &gift.SubscriptionIdentifier, map[string]any{
		"gift_id": gift.ID.String(),
	})
}

// createUserOrder mirrors CreateUserOrderAsync: the account wallet pays.
func (s *Subscriptions) createUserOrder(ctx context.Context, accountID uuid.UUID, currency string, amount decimal.Decimal, appIdentifier string, productIdentifier *string, meta map[string]any) (*WalletOrderEntity, error) {
	wallet, _, err := s.svc.GetAccountWallet(ctx, accountID)
	if err != nil || wallet == nil {
		return nil, errors.New("Account wallet was not found.")
	}
	app := appIdentifier
	return s.svc.CreateOrder(ctx, OrderInput{
		PayerWalletID:     &wallet.ID,
		Currency:          currency,
		Amount:            amount,
		AppIdentifier:     &app,
		ProductIdentifier: productIdentifier,
		Meta:              meta,
		Reuseable:         true,
	})
}

// GetSubscription mirrors GetSubscriptionAsync: the available subscription
// for one identifier. It returns (nil, nil) when none is available.
func (s *Subscriptions) GetSubscription(ctx context.Context, accountID uuid.UUID, identifier string) (*WalletSubscriptionEntity, error) {
	var candidates []WalletSubscriptionEntity
	err := s.db(ctx).
		Where("account_id = ? AND identifier = ?", accountID, identifier).
		Order("begun_at DESC").
		Find(&candidates).Error
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for i := range candidates {
		if subscriptionAvailable(&candidates[i], now) {
			return &candidates[i], nil
		}
	}
	return nil, nil
}

// GetSubscriptionByGroup lists the account's subscriptions in a group.
func (s *Subscriptions) GetSubscriptionByGroup(ctx context.Context, accountID uuid.UUID, identifiers []string) ([]WalletSubscriptionEntity, error) {
	if len(identifiers) == 0 {
		return []WalletSubscriptionEntity{}, nil
	}
	var subscriptions []WalletSubscriptionEntity
	err := s.db(ctx).
		Where("account_id = ? AND identifier IN ?", accountID, identifiers).
		Order("begun_at ASC").
		Find(&subscriptions).Error
	return subscriptions, err
}

// GetPerkSubscription mirrors GetPerkSubscriptionAsync: the effective perk
// subscription of an account.
func (s *Subscriptions) GetPerkSubscription(ctx context.Context, accountID uuid.UUID) (*WalletSubscriptionEntity, error) {
	now := time.Now().UTC()
	var candidates []WalletSubscriptionEntity
	err := s.db(ctx).
		Where("account_id = ? AND status = ? AND (ended_at IS NULL OR ended_at > ?)", accountID, SubscriptionActive, now).
		Order("begun_at DESC").
		Find(&candidates).Error
	if err != nil {
		return nil, err
	}
	return s.selectEffectivePerkSubscription(ctx, candidates, now)
}

// GetPerkSubscriptions mirrors GetPerkSubscriptionsAsync.
func (s *Subscriptions) GetPerkSubscriptions(ctx context.Context, accountIDs []uuid.UUID) (map[uuid.UUID]*WalletSubscriptionEntity, error) {
	result := make(map[uuid.UUID]*WalletSubscriptionEntity, len(accountIDs))
	if len(accountIDs) == 0 {
		return result, nil
	}
	now := time.Now().UTC()
	var subscriptions []WalletSubscriptionEntity
	err := s.db(ctx).
		Where("account_id IN ? AND status = ? AND (ended_at IS NULL OR ended_at > ?)", accountIDs, SubscriptionActive, now).
		Order("begun_at DESC").
		Find(&subscriptions).Error
	if err != nil {
		return nil, err
	}
	grouped := map[uuid.UUID][]WalletSubscriptionEntity{}
	for _, subscription := range subscriptions {
		grouped[subscription.AccountID] = append(grouped[subscription.AccountID], subscription)
	}
	for accountID, candidates := range grouped {
		selected, err := s.selectEffectivePerkSubscription(ctx, candidates, now)
		if err != nil {
			return nil, err
		}
		if selected != nil {
			result[accountID] = selected
		}
	}
	return result, nil
}

// selectEffectivePerkSubscription mirrors SelectEffectivePerkSubscriptionAsync:
// missing metadata is backfilled from the definition, then the highest perk
// level wins.
func (s *Subscriptions) selectEffectivePerkSubscription(ctx context.Context, candidates []WalletSubscriptionEntity, now time.Time) (*WalletSubscriptionEntity, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	changed := false
	for i := range candidates {
		subscription := &candidates[i]
		if !subscriptionAvailable(subscription, now) {
			continue
		}
		if subscription.PerkLevel > 0 && subscription.DisplayName != nil && strings.TrimSpace(*subscription.DisplayName) != "" {
			continue
		}
		definition, err := s.GetDefinition(ctx, subscription.Identifier)
		if err != nil {
			return nil, err
		}
		if definition == nil {
			continue
		}
		if subscription.GroupIdentifier == nil {
			subscription.GroupIdentifier = definition.GroupIdentifier
		}
		if subscription.DisplayName == nil {
			name := definition.DisplayName
			subscription.DisplayName = &name
		}
		if subscription.IsTesting {
			if subscription.PerkLevel != 0 {
				subscription.PerkLevel = 0
				changed = true
			}
		} else if subscription.PerkLevel <= 0 {
			subscription.PerkLevel = definition.PerkLevel
			changed = true
		}
	}
	if changed {
		for i := range candidates {
			if err := s.db(ctx).Model(&WalletSubscriptionEntity{}).Where("id = ?", candidates[i].ID).
				Updates(map[string]any{
					"group_identifier": candidates[i].GroupIdentifier,
					"display_name":     candidates[i].DisplayName,
					"perk_level":       candidates[i].PerkLevel,
				}).Error; err != nil {
				return nil, err
			}
		}
	}
	var best *WalletSubscriptionEntity
	for i := range candidates {
		subscription := &candidates[i]
		if !subscriptionAvailable(subscription, now) || subscription.PerkLevel <= 0 {
			continue
		}
		if best == nil ||
			subscription.PerkLevel > best.PerkLevel ||
			(subscription.PerkLevel == best.PerkLevel && subscription.BegunAt.After(best.BegunAt)) {
			best = subscription
		}
	}
	return best, nil
}

// ── order hooks ────────────────────────────────────────────────────────────

// orderMeta decodes an order's jsonb metadata.
func orderMeta(order *WalletOrderEntity) map[string]any {
	meta := map[string]any{}
	if order == nil || len(order.Meta) == 0 {
		return meta
	}
	if err := json.Unmarshal(order.Meta, &meta); err != nil {
		return map[string]any{}
	}
	return meta
}

func metaString(meta map[string]any, key string) (string, bool) {
	value, ok := meta[key]
	if !ok {
		return "", false
	}
	switch typed := value.(type) {
	case string:
		return typed, true
	case json.Number:
		return typed.String(), true
	case float64:
		return decimal.NewFromFloat(typed).String(), true
	case bool:
		if typed {
			return "true", true
		}
		return "false", true
	default:
		return fmt.Sprint(typed), true
	}
}

func metaTable(meta map[string]any, key string) (map[string]any, bool) {
	value, ok := meta[key]
	if !ok {
		return nil, false
	}
	table, ok := value.(map[string]any)
	return table, ok
}

// HandleOrderPaid is the OrderPaidHook replacing the C# PaymentOrderEvent
// listener. It applies gift orders, subscription orders and app-subscription
// orders; everything else (wallet products included) returns ErrNotHandled so
// the module wiring can hand the order to the product hook.
func (s *Subscriptions) HandleOrderPaid(ctx context.Context, order *WalletOrderEntity) error {
	if order == nil {
		return ErrNotHandled
	}
	meta := orderMeta(order)
	if _, ok := meta["gift_id"]; ok {
		_, err := s.HandleGiftOrder(ctx, order)
		return err
	}
	if _, ok := meta["subscription_id"]; ok {
		_, err := s.HandleSubscriptionOrder(ctx, order)
		return err
	}
	if appSubscription, ok := metaTable(meta, "app_subscription"); ok {
		return s.applyAppSubscriptionOrder(ctx, order, appSubscription)
	}
	return ErrNotHandled
}

// applyAppSubscriptionOrder mirrors the listener's app_subscription branch: a
// developer app sold a recurring product and the wallet records the
// subscription directly.
func (s *Subscriptions) applyAppSubscriptionOrder(ctx context.Context, order *WalletOrderEntity, meta map[string]any) error {
	identifier, _ := metaString(meta, "identifier")
	currency, _ := metaString(meta, "currency")
	basePriceRaw, hasBasePrice := metaString(meta, "base_price")
	if strings.TrimSpace(identifier) == "" || strings.TrimSpace(currency) == "" || !hasBasePrice {
		s.logger().Warn("app subscription order is missing required meta", "order", order.ID)
		return nil
	}
	basePrice, err := decimal.NewFromString(basePriceRaw)
	if err != nil {
		return fmt.Errorf("app subscription base price is invalid: %w", err)
	}
	cycleDays := 30
	if raw, ok := metaString(meta, "cycle_duration_days"); ok {
		if parsed, err := decimal.NewFromString(raw); err == nil && parsed.GreaterThan(decimal.Zero) {
			cycleDays = int(parsed.IntPart())
		}
	}
	if order.PayerWalletID == nil {
		return errors.New("app subscription order has no payer wallet")
	}
	var payer WalletEntity
	if err := s.db(ctx).Where("id = ?", *order.PayerWalletID).First(&payer).Error; err != nil {
		return fmt.Errorf("app subscription order payer wallet was not found: %w", err)
	}
	if payer.AccountID == nil {
		return errors.New("app subscription order payer wallet is not account-owned")
	}

	now := time.Now().UTC()
	endedAt := now.AddDate(0, 0, cycleDays)
	subscription := &WalletSubscriptionEntity{
		ID:             newID(),
		BegunAt:        now,
		EndedAt:        &endedAt,
		Identifier:     identifier,
		PerkLevel:      0,
		IsActive:       true,
		Status:         SubscriptionActive,
		PaymentMethod:  PaymentInAppWallet,
		PaymentDetails: paymentDetailsJSON(PaymentDetails{Currency: currency, OrderID: strPtr(order.ID.String())}),
		BasePrice:      basePrice,
		RenewalAt:      &endedAt,
		AccountID:      *payer.AccountID,
	}
	if group, ok := metaString(meta, "group_identifier"); ok && strings.TrimSpace(group) != "" {
		subscription.GroupIdentifier = &group
	}
	if displayName, ok := metaString(meta, "display_name"); ok && strings.TrimSpace(displayName) != "" {
		subscription.DisplayName = &displayName
	}
	if err := s.db(ctx).Create(subscription).Error; err != nil {
		return err
	}
	s.logger().Info("app subscription created for order", "subscription", subscription.ID, "order", order.ID)
	return nil
}

// HandleSubscriptionOrder mirrors HandleSubscriptionOrder: a paid in-app
// order activates (or extends) its subscription placeholder.
func (s *Subscriptions) HandleSubscriptionOrder(ctx context.Context, order *WalletOrderEntity) (*WalletSubscriptionEntity, error) {
	if order.Status != OrderPaid {
		return nil, errors.New("Invalid order.")
	}
	meta := orderMeta(order)
	rawSubscriptionID, ok := metaString(meta, "subscription_id")
	if !ok {
		return nil, errors.New("Invalid order.")
	}
	subscriptionID, err := uuid.Parse(rawSubscriptionID)
	if err != nil || subscriptionID == uuid.Nil {
		return nil, errors.New("Invalid order.")
	}
	var subscription WalletSubscriptionEntity
	err = s.db(ctx).Where("id = ?", subscriptionID).First(&subscription).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.New("Invalid order.")
	}
	if err != nil {
		return nil, err
	}
	definition, err := s.GetDefinition(ctx, subscription.Identifier)
	if err != nil {
		return nil, err
	}
	if definition == nil {
		return nil, errors.New("Invalid order.")
	}
	cycle := s.DefaultSubscriptionDuration()
	if subscription.EndedAt != nil {
		cycle = subscription.EndedAt.Sub(subscription.BegunAt)
	}
	return s.applyPaidSubscription(ctx, applyPaidOptions{
		AccountID:     subscription.AccountID,
		Definition:    definition,
		PaymentMethod: subscription.PaymentMethod,
		PaymentDetails: PaymentDetails{
			Currency: definition.Currency,
			OrderID:  strPtr(order.ID.String()),
		},
		EffectiveFrom: time.Now().UTC(),
		Cycle:         cycle,
		Placeholder:   &subscription,
	})
}

// HandleGiftOrder mirrors HandleGiftOrder: a paid gift either redeems to its
// recipient or becomes redeemable.
func (s *Subscriptions) HandleGiftOrder(ctx context.Context, order *WalletOrderEntity) (*WalletGiftEntity, error) {
	if order.Status != OrderPaid {
		return nil, errors.New("Invalid order.")
	}
	meta := orderMeta(order)
	rawGiftID, ok := metaString(meta, "gift_id")
	if !ok {
		return nil, errors.New("Invalid order.")
	}
	giftID, err := uuid.Parse(rawGiftID)
	if err != nil || giftID == uuid.Nil {
		return nil, errors.New("Invalid order.")
	}
	var gift WalletGiftEntity
	err = s.db(ctx).Where("id = ?", giftID).First(&gift).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.New("Invalid order.")
	}
	if err != nil {
		return nil, err
	}
	if gift.Status != GiftCreated {
		return nil, errors.New("Gift is not in payable status.")
	}
	now := time.Now().UTC()
	if gift.RecipientID != nil {
		if _, err := s.redeemGiftToAccount(ctx, &gift, *gift.RecipientID, now); err != nil {
			return nil, err
		}
		return &gift, nil
	}
	gift.Status = GiftSent
	if err := s.db(ctx).Model(&WalletGiftEntity{}).Where("id = ?", gift.ID).
		Update("status", GiftSent).Error; err != nil {
		return nil, err
	}
	return &gift, nil
}

// applyPaidOptions carries ApplyPaidSubscriptionAsync's parameters.
type applyPaidOptions struct {
	AccountID      uuid.UUID
	Definition     *WalletSubscriptionDefinitionEntity
	PaymentMethod  string
	PaymentDetails PaymentDetails
	EffectiveFrom  time.Time
	Cycle          time.Duration
	IsTesting      bool
	Placeholder    *WalletSubscriptionEntity
	CouponID       *uuid.UUID
	Coupon         *WalletCouponEntity
	InboundOrderID *uuid.UUID
}

// applyPaidSubscription mirrors ApplyPaidSubscriptionAsync: it queues the
// subscription behind the group's tail and activates it when due.
func (s *Subscriptions) applyPaidSubscription(ctx context.Context, opts applyPaidOptions) (*WalletSubscriptionEntity, error) {
	if opts.Definition == nil {
		return nil, errors.New("subscription definition is required")
	}
	cycle := opts.Cycle
	if cycle <= 0 {
		cycle = s.DefaultSubscriptionDuration()
	}
	effectiveFrom := opts.EffectiveFrom
	if effectiveFrom.IsZero() {
		effectiveFrom = time.Now().UTC()
	}
	groupIdentifiers, err := s.GetGroupIdentifiers(ctx, opts.Definition.GroupIdentifier, opts.Definition.Identifier)
	if err != nil {
		return nil, err
	}

	var matchedByOrder *WalletSubscriptionEntity
	if opts.InboundOrderID != nil {
		var existing WalletSubscriptionEntity
		err := s.db(ctx).Where("inbound_order_id = ?", *opts.InboundOrderID).First(&existing).Error
		switch {
		case err == nil:
			matchedByOrder = &existing
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return nil, err
		}
	} else if opts.PaymentDetails.OrderID != nil && strings.TrimSpace(*opts.PaymentDetails.OrderID) != "" {
		var existing []WalletSubscriptionEntity
		if err := s.db(ctx).
			Where("account_id = ? AND is_testing = ? AND identifier IN ?", opts.AccountID, opts.IsTesting, groupIdentifiers).
			Order("begun_at DESC").
			Find(&existing).Error; err != nil {
			return nil, err
		}
		for i := range existing {
			details := parsePaymentDetails(existing[i].PaymentDetails)
			if details.OrderID != nil && strings.EqualFold(*details.OrderID, *opts.PaymentDetails.OrderID) {
				matchedByOrder = &existing[i]
				break
			}
		}
	}
	if matchedByOrder != nil {
		s.logger().Info("skipping duplicate subscription application",
			"inbound_order", opts.InboundOrderID, "subscription", matchedByOrder.ID, "status", matchedByOrder.Status)
		return matchedByOrder, nil
	}

	query := s.db(ctx).
		Where("account_id = ? AND is_active = ? AND status = ? AND identifier IN ? AND is_testing = ?",
			opts.AccountID, true, SubscriptionActive, groupIdentifiers, opts.IsTesting)
	if opts.Placeholder != nil {
		query = query.Where("id <> ?", opts.Placeholder.ID)
	}
	var activeOrQueued []WalletSubscriptionEntity
	if err := query.Order("begun_at ASC").Find(&activeOrQueued).Error; err != nil {
		return nil, err
	}

	effectivePerkLevel := opts.Definition.PerkLevel
	if opts.IsTesting {
		effectivePerkLevel = 0
	}
	begunAt := effectiveFrom
	for i := range activeOrQueued {
		tail := activeOrQueued[i].BegunAt
		if activeOrQueued[i].EndedAt != nil {
			tail = *activeOrQueued[i].EndedAt
		}
		if tail.After(begunAt) {
			begunAt = tail
		}
	}
	endedAt := begunAt.Add(cycle)

	subscription := opts.Placeholder
	if subscription == nil {
		subscription = &WalletSubscriptionEntity{ID: newID()}
	}
	subscription.BegunAt = begunAt
	subscription.EndedAt = &endedAt
	subscription.Identifier = opts.Definition.Identifier
	subscription.GroupIdentifier = opts.Definition.GroupIdentifier
	displayName := opts.Definition.DisplayName
	subscription.DisplayName = &displayName
	subscription.PerkLevel = effectivePerkLevel
	subscription.IsTesting = opts.IsTesting
	subscription.IsActive = true
	subscription.IsFreeTrial = false
	subscription.Status = SubscriptionActive
	subscription.PaymentMethod = opts.PaymentMethod
	subscription.PaymentDetails = paymentDetailsJSON(opts.PaymentDetails)
	subscription.InboundOrderID = opts.InboundOrderID
	subscription.BasePrice = opts.Definition.BasePrice
	subscription.CouponID = opts.CouponID
	subscription.AccountID = opts.AccountID
	policy := parsePaymentPolicy(opts.Definition.PaymentPolicy)
	if policy.AllowInternalWalletRenewal {
		renewal := endedAt
		subscription.RenewalAt = &renewal
	} else {
		subscription.RenewalAt = nil
	}

	if opts.Placeholder == nil {
		if err := s.db(ctx).Create(subscription).Error; err != nil {
			return nil, err
		}
	} else if err := s.db(ctx).Save(subscription).Error; err != nil {
		return nil, err
	}

	s.logger().Info("applied subscription",
		"subscription", subscription.ID, "identifier", opts.Definition.Identifier,
		"account", opts.AccountID, "payment_method", opts.PaymentMethod,
		"is_testing", opts.IsTesting, "begun_at", subscription.BegunAt, "ended_at", subscription.EndedAt)

	if !begunAt.After(time.Now().UTC()) {
		s.notifySubscriptionBegun(ctx, subscription)
		if s.IsSponsorRewardEligiblePaymentMethod(opts.PaymentMethod) {
			s.handleSponsorCurrencyUpdate(ctx, subscription, opts.Definition)
			s.handleSponsorBadgeSubscription(ctx, subscription)
		}
	}
	return subscription, nil
}

// ── provider orders ────────────────────────────────────────────────────────

// CreateSubscriptionFromOrder mirrors CreateSubscriptionFromOrder: a verified
// provider order activates its mapped subscription.
func (s *Subscriptions) CreateSubscriptionFromOrder(ctx context.Context, order ProviderOrder, inbound *InboundOrderEntity) (*WalletSubscriptionEntity, error) {
	if strings.TrimSpace(order.SubscriptionID) == "" {
		return nil, errors.New("Subscription identifier was missing from the payment payload.")
	}
	definition, err := s.GetDefinition(ctx, order.SubscriptionID)
	if err != nil {
		return nil, err
	}
	if definition == nil {
		definition, err = s.ResolveDefinition(ctx, order.Provider, order.SubscriptionID)
		if err != nil {
			return nil, err
		}
	}
	if definition == nil {
		return nil, fmt.Errorf("Subscription mapping %s was not found for provider %s.", order.SubscriptionID, order.Provider)
	}
	accountID, err := s.resolveAccountForOrder(ctx, order)
	if err != nil {
		return nil, err
	}
	if accountID == nil {
		return nil, fmt.Errorf("Account was not found with identifier %s", order.AccountID)
	}
	s.logger().Info("applying provider subscription order",
		"order", order.ID, "provider", order.Provider, "account", accountID,
		"subscription", order.SubscriptionID, "is_testing", order.IsTesting)

	effectiveFrom := order.BegunAt
	if effectiveFrom.IsZero() {
		effectiveFrom = time.Now().UTC()
	}
	cycle := order.Duration
	if cycle <= 0 {
		cycle = s.DefaultSubscriptionDuration()
	}
	var inboundOrderID *uuid.UUID
	if inbound != nil {
		inboundOrderID = &inbound.ID
	}
	return s.applyPaidSubscription(ctx, applyPaidOptions{
		AccountID:      *accountID,
		Definition:     definition,
		PaymentMethod:  order.Provider,
		PaymentDetails: PaymentDetails{Currency: definition.Currency},
		EffectiveFrom:  effectiveFrom,
		Cycle:          cycle,
		IsTesting:      order.IsTesting,
		InboundOrderID: inboundOrderID,
	})
}

// resolveAccountForOrder mirrors ResolveAccountForOrderAsync: the custom order
// id, then the provider connection, then a raw account UUID.
func (s *Subscriptions) resolveAccountForOrder(ctx context.Context, order ProviderOrder) (*uuid.UUID, error) {
	if strings.TrimSpace(order.CustomOrderID) != "" {
		if accountID, err := uuid.Parse(order.CustomOrderID); err == nil {
			exists, err := s.accountExists(ctx, accountID)
			if err != nil {
				return nil, err
			}
			if exists {
				return &accountID, nil
			}
		}
	}
	if strings.TrimSpace(order.Provider) != "" && strings.TrimSpace(order.AccountID) != "" {
		accountID, err := s.resolveConnectionAccount(ctx, order.Provider, order.AccountID)
		if err != nil {
			return nil, err
		}
		if accountID != nil {
			return accountID, nil
		}
	}
	if accountID, err := uuid.Parse(order.AccountID); err == nil {
		exists, err := s.accountExists(ctx, accountID)
		if err != nil {
			return nil, err
		}
		if exists {
			return &accountID, nil
		}
	}
	return nil, nil
}

// ApplyProviderOrder mirrors ApplyRestoredProviderOrderAsync: it deduplicates
// the provider order through inbound_orders, applies it (product or
// subscription) and sends the processed notification.
func (s *Subscriptions) ApplyProviderOrder(ctx context.Context, order ProviderOrder, extraMeta map[string]any) (any, error) {
	begunAt := order.BegunAt
	inbound, err := s.svc.CreateInboundOrder(ctx, InboundOrderInput{
		Provider:       order.Provider,
		ExternalID:     order.ID,
		CorrelationID:  order.CorrelationID,
		SubscriptionID: order.SubscriptionID,
		AccountID:      order.AccountID,
		BegunAt:        &begunAt,
		Duration:       order.Duration,
		IsTesting:      order.IsTesting,
	})
	if err != nil {
		return nil, err
	}

	var result any
	handledByProduct := false
	if s.Products != nil {
		handled, err := s.Products.HandlesProviderOrder(ctx, order)
		if err != nil {
			return nil, err
		}
		if handled {
			result, err = s.Products.ApplyProviderOrder(ctx, order, inbound, extraMeta)
			if err != nil {
				return nil, err
			}
			handledByProduct = true
		}
	}
	if !handledByProduct {
		subscription, err := s.CreateSubscriptionFromOrder(ctx, order, inbound)
		if err != nil {
			return nil, err
		}
		result = subscription
	}
	s.NotifyProviderOrderProcessed(ctx, order, inbound, result)
	return result, nil
}

// NotifyProviderOrderProcessed mirrors NotifyProviderOrderProcessedAsync. It
// is idempotent through inbound_orders.notification_sent_at.
func (s *Subscriptions) NotifyProviderOrderProcessed(ctx context.Context, order ProviderOrder, inbound *InboundOrderEntity, result any) {
	if inbound == nil || inbound.NotificationSentAt != nil {
		return
	}
	accountID := s.resolveProcessedOrderAccount(ctx, order, result)
	if accountID == nil {
		s.logger().Warn("cannot notify provider order: account could not be resolved",
			"provider", inbound.Provider, "external_id", inbound.ExternalID, "account", order.AccountID)
		return
	}
	product := order.SubscriptionID
	if strings.TrimSpace(product) == "" {
		if inbound.ProductIdentifier != nil {
			product = *inbound.ProductIdentifier
		} else {
			product = inbound.Provider
		}
	}
	title := "Order Processed"
	body := fmt.Sprintf("Your %s order has been processed successfully.", product)
	if order.IsTesting {
		title = "Sandbox Order Received"
		body = fmt.Sprintf("We received your %s order, but it will not affect your account because this was a sandbox purchase. No payment was collected.", product)
	}
	s.push(ctx, *accountID, title, body)

	now := time.Now().UTC()
	if err := s.db(ctx).Model(&InboundOrderEntity{}).Where("id = ?", inbound.ID).
		Update("notification_sent_at", now).Error; err != nil {
		s.logger().Warn("failed to record provider order notification", "inbound_order", inbound.ID, "error", err)
	}
}

func (s *Subscriptions) resolveProcessedOrderAccount(ctx context.Context, order ProviderOrder, result any) *uuid.UUID {
	switch typed := result.(type) {
	case *WalletSubscriptionEntity:
		if typed != nil {
			accountID := typed.AccountID
			return &accountID
		}
	case *WalletGiftEntity:
		if typed != nil {
			return nil
		}
	case *WalletOrderEntity:
		if typed != nil {
			meta := orderMeta(typed)
			if raw, ok := metaString(meta, "account_id"); ok {
				if accountID, err := uuid.Parse(raw); err == nil {
					return &accountID
				}
			}
		}
	}
	accountID, err := s.resolveAccountForOrder(ctx, order)
	if err != nil {
		s.logger().Warn("failed to resolve provider order account", "order", order.ID, "error", err)
		return nil
	}
	return accountID
}

// ── notifications ──────────────────────────────────────────────────────────

// push sends a Ring notification through the push port. The C# service used
// localized templates with a topic and metadata; the Stargate port carries the
// English copy because the push port has no topic/metadata fields.
func (s *Subscriptions) push(ctx context.Context, accountID uuid.UUID, title, body string) {
	if s.svc.Push == nil || !s.svc.Push.Available() {
		return
	}
	if err := s.svc.Push.SendToUser(ctx, accountID.String(), title, body); err != nil {
		s.logger().Warn("subscription notification failed", "account", accountID, "title", title, "error", err)
	}
}

// notifySubscriptionBegun mirrors NotifySubscriptionBegun.
func (s *Subscriptions) notifySubscriptionBegun(ctx context.Context, subscription *WalletSubscriptionEntity) {
	if subscription == nil {
		return
	}
	name := subscription.Identifier
	if subscription.DisplayName != nil && strings.TrimSpace(*subscription.DisplayName) != "" {
		name = *subscription.DisplayName
	}
	duration := "infinite"
	if subscription.EndedAt != nil {
		days := int(subscription.EndedAt.Sub(subscription.BegunAt).Hours() / 24)
		duration = fmt.Sprintf("%d", days)
	}
	body := fmt.Sprintf("Your subscription for %s days of %s has been applied", duration, name)
	s.push(ctx, subscription.AccountID, "Subscription Applied", body)
}

// handleSponsorCurrencyUpdate mirrors HandleSponsorCurrencyUpdateAsync: sponsor
// purchases credit golden points.
func (s *Subscriptions) handleSponsorCurrencyUpdate(ctx context.Context, subscription *WalletSubscriptionEntity, definition *WalletSubscriptionDefinitionEntity) {
	amount := decimal.Zero
	if subscription.PerkLevel > 0 {
		amount = decimal.NewFromInt(int64(subscription.PerkLevel * 10))
	} else if definition != nil && definition.GoldenPointReward != nil {
		amount = decimal.NewFromInt(int64(*definition.GoldenPointReward))
	}
	if amount.LessThanOrEqual(decimal.Zero) {
		return
	}
	accountID := subscription.AccountID
	remarks := "Stellar Program Golden Points"
	if _, err := s.svc.CreateTransactionWithAccounts(ctx, nil, &accountID, CurrencyGolds, amount, &remarks, TransactionSystem); err != nil {
		s.logger().Warn("unable to send golden points to member", "account", accountID, "error", err)
	}
}

// handleSponsorBadgeSubscription mirrors HandleSponsorBadgeSubscriptionAsync.
// Stargate's wallet module has no accounts/profile client port (the C# service
// called DyProfileService), so the sponsor badge grant is not available here;
// the subscription itself is unaffected, exactly like the C# catch block.
func (s *Subscriptions) handleSponsorBadgeSubscription(ctx context.Context, subscription *WalletSubscriptionEntity) {
	if subscription == nil {
		return
	}
	s.logger().Debug("sponsor badge grant skipped: no profile client port in the wallet module",
		"account", subscription.AccountID, "subscription", subscription.ID)
}

// ── pending activations / switching ────────────────────────────────────────

// GetPendingActivations mirrors GetPendingActivationsAsync.
func (s *Subscriptions) GetPendingActivations(ctx context.Context, accountID uuid.UUID, offset, take int) (int, []WalletSubscriptionEntity, error) {
	now := time.Now().UTC()
	base := func() *gorm.DB {
		return s.db(ctx).
			Model(&WalletSubscriptionEntity{}).
			Where("account_id = ? AND is_active = ? AND status = ? AND begun_at > ?", accountID, true, SubscriptionActive, now)
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		return 0, nil, err
	}
	var items []WalletSubscriptionEntity
	if err := base().Order("begun_at ASC").Offset(maxInt(offset, 0)).Limit(maxInt(take, 1)).Find(&items).Error; err != nil {
		return 0, nil, err
	}
	return int(total), items, nil
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

// SwitchSubscriptionActivation mirrors SwitchSubscriptionActivationAsync: the
// chosen subscription starts now and the rest of the group is re-queued.
func (s *Subscriptions) SwitchSubscriptionActivation(ctx context.Context, accountID uuid.UUID, groupIdentifier string, subscriptionID uuid.UUID) (*WalletSubscriptionEntity, error) {
	definitions, err := s.ListDefinitionsByGroup(ctx, groupIdentifier)
	if err != nil {
		return nil, err
	}
	if len(definitions) == 0 {
		return nil, fmt.Errorf("Subscription group %s was not found.", groupIdentifier)
	}
	identifiers := make([]string, 0, len(definitions))
	definitionMap := make(map[string]*WalletSubscriptionDefinitionEntity, len(definitions))
	for i := range definitions {
		identifiers = append(identifiers, definitions[i].Identifier)
		definitionMap[strings.ToLower(definitions[i].Identifier)] = &definitions[i]
	}
	var subscriptionsInGroup []WalletSubscriptionEntity
	if err := s.db(ctx).
		Where("account_id = ? AND identifier IN ? AND is_active = ? AND status = ?",
			accountID, identifiers, true, SubscriptionActive).
		Order("begun_at ASC").
		Find(&subscriptionsInGroup).Error; err != nil {
		return nil, err
	}
	var target *WalletSubscriptionEntity
	for i := range subscriptionsInGroup {
		if subscriptionsInGroup[i].ID == subscriptionID {
			target = &subscriptionsInGroup[i]
			break
		}
	}
	if target == nil {
		return nil, errors.New("Subscription was not found in this group.")
	}
	now := time.Now().UTC()
	if target.EndedAt != nil && !target.EndedAt.After(now) {
		return nil, errors.New("Subscription is no longer available for activation.")
	}

	var current *WalletSubscriptionEntity
	for i := range subscriptionsInGroup {
		candidate := &subscriptionsInGroup[i]
		if !subscriptionAvailable(candidate, now) {
			continue
		}
		if current == nil ||
			candidate.PerkLevel > current.PerkLevel ||
			(candidate.PerkLevel == current.PerkLevel && candidate.BegunAt.After(current.BegunAt)) {
			current = candidate
		}
	}
	alreadyActivated := map[uuid.UUID]struct{}{}
	for i := range subscriptionsInGroup {
		if !subscriptionsInGroup[i].BegunAt.After(now) {
			alreadyActivated[subscriptionsInGroup[i].ID] = struct{}{}
		}
	}
	if current != nil && current.ID == target.ID {
		return target, nil
	}

	targetDefinition := definitionMap[strings.ToLower(target.Identifier)]
	if targetDefinition == nil {
		return nil, errors.New("Subscription definition was not found.")
	}
	cursor := now
	applyDefinition := func(subscription *WalletSubscriptionEntity, definition *WalletSubscriptionDefinitionEntity, duration time.Duration) {
		subscription.BegunAt = cursor
		ended := cursor.Add(duration)
		subscription.EndedAt = &ended
		subscription.GroupIdentifier = definition.GroupIdentifier
		displayName := definition.DisplayName
		subscription.DisplayName = &displayName
		if subscription.IsTesting {
			subscription.PerkLevel = 0
		} else {
			subscription.PerkLevel = definition.PerkLevel
		}
		policy := parsePaymentPolicy(definition.PaymentPolicy)
		if policy.AllowInternalWalletRenewal {
			renewal := ended
			subscription.RenewalAt = &renewal
		} else {
			subscription.RenewalAt = nil
		}
		cursor = ended
	}
	applyDefinition(target, targetDefinition, subscriptionDuration(target))

	if current != nil {
		currentDefinition := definitionMap[strings.ToLower(current.Identifier)]
		if currentDefinition == nil {
			return nil, errors.New("Subscription definition was not found.")
		}
		applyDefinition(current, currentDefinition, remainingSubscriptionDuration(current, now))
	}

	for i := range subscriptionsInGroup {
		subscription := &subscriptionsInGroup[i]
		if subscription.ID == target.ID || (current != nil && subscription.ID == current.ID) {
			continue
		}
		definition := definitionMap[strings.ToLower(subscription.Identifier)]
		if definition == nil {
			return nil, errors.New("Subscription definition was not found.")
		}
		applyDefinition(subscription, definition, subscriptionDuration(subscription))
	}

	for i := range subscriptionsInGroup {
		subscription := &subscriptionsInGroup[i]
		if _, ok := alreadyActivated[subscription.ID]; ok && subscription.ID != target.ID && subscription.BegunAt.After(now) {
			subscription.UpdatedAt = subscription.BegunAt
		}
		if err := s.db(ctx).Save(subscription).Error; err != nil {
			return nil, err
		}
	}

	s.notifySubscriptionBegun(ctx, target)
	if s.IsSponsorRewardEligiblePaymentMethod(target.PaymentMethod) {
		s.handleSponsorCurrencyUpdate(ctx, target, targetDefinition)
		s.handleSponsorBadgeSubscription(ctx, target)
	}
	return target, nil
}

// subscriptionDuration mirrors GetSubscriptionDuration.
func subscriptionDuration(subscription *WalletSubscriptionEntity) time.Duration {
	if subscription.EndedAt != nil && subscription.EndedAt.After(subscription.BegunAt) {
		return subscription.EndedAt.Sub(subscription.BegunAt)
	}
	return 30 * 24 * time.Hour
}

// remainingSubscriptionDuration mirrors GetRemainingSubscriptionDuration.
func remainingSubscriptionDuration(subscription *WalletSubscriptionEntity, now time.Time) time.Duration {
	if subscription.EndedAt != nil && subscription.EndedAt.After(now) {
		return subscription.EndedAt.Sub(now)
	}
	return 0
}

// ── maintenance (jobs) ─────────────────────────────────────────────────────

// UpdateExpiredSubscriptions mirrors UpdateExpiredSubscriptionsAsync.
func (s *Subscriptions) UpdateExpiredSubscriptions(ctx context.Context, batchSize int) (int, error) {
	if batchSize <= 0 {
		batchSize = 100
	}
	now := time.Now().UTC()
	var subscriptions []WalletSubscriptionEntity
	if err := s.db(ctx).
		Where("is_active = ? AND status = ? AND ended_at IS NOT NULL AND ended_at < ?", true, SubscriptionActive, now).
		Limit(batchSize).
		Find(&subscriptions).Error; err != nil {
		return 0, err
	}
	if len(subscriptions) == 0 {
		return 0, nil
	}
	ids := make([]uuid.UUID, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		ids = append(ids, subscription.ID)
	}
	if err := s.db(ctx).Model(&WalletSubscriptionEntity{}).Where("id IN ?", ids).
		Update("status", SubscriptionExpired).Error; err != nil {
		return 0, err
	}
	return len(subscriptions), nil
}

// ActivatePendingSubscriptions mirrors ActivatePendingSubscriptionsAsync.
func (s *Subscriptions) ActivatePendingSubscriptions(ctx context.Context, batchSize int) (int, error) {
	if batchSize <= 0 {
		batchSize = 100
	}
	now := time.Now().UTC()
	var subscriptions []WalletSubscriptionEntity
	if err := s.db(ctx).
		Where("is_active = ? AND status = ? AND begun_at <= ? AND (ended_at IS NULL OR ended_at > ?) AND updated_at < begun_at",
			true, SubscriptionActive, now, now).
		Order("begun_at ASC").
		Limit(batchSize).
		Find(&subscriptions).Error; err != nil {
		return 0, err
	}
	if len(subscriptions) == 0 {
		return 0, nil
	}
	ids := make([]uuid.UUID, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		ids = append(ids, subscription.ID)
	}
	if err := s.db(ctx).Model(&WalletSubscriptionEntity{}).Where("id IN ?", ids).
		Update("updated_at", now).Error; err != nil {
		return 0, err
	}
	for i := range subscriptions {
		subscription := &subscriptions[i]
		s.notifySubscriptionBegun(ctx, subscription)
		if s.IsSponsorRewardEligiblePaymentMethod(subscription.PaymentMethod) {
			definition, err := s.GetDefinition(ctx, subscription.Identifier)
			if err != nil {
				return 0, err
			}
			s.handleSponsorCurrencyUpdate(ctx, subscription, definition)
			s.handleSponsorBadgeSubscription(ctx, subscription)
		}
	}
	return len(subscriptions), nil
}

// CancelUnavailableInAppWalletSubscriptions mirrors
// CancelUnavailableInAppWalletSubscriptionsAsync: definitions that no longer
// allow in-app wallet payments have their live subscriptions cancelled and
// the unused part of the cycle refunded.
func (s *Subscriptions) CancelUnavailableInAppWalletSubscriptions(ctx context.Context) (int, error) {
	now := time.Now().UTC()
	var subscriptions []WalletSubscriptionEntity
	if err := s.db(ctx).
		Where("is_active = ? AND status = ? AND payment_method = ? AND is_free_trial = ?",
			true, SubscriptionActive, PaymentInAppWallet, false).
		Where("begun_at > ?", time.Time{}).
		Where("ended_at IS NULL OR ended_at > ?", now).
		Order("begun_at ASC").
		Find(&subscriptions).Error; err != nil {
		return 0, err
	}
	cancelled := 0
	for i := range subscriptions {
		subscription := &subscriptions[i]
		definition, err := s.GetDefinition(ctx, subscription.Identifier)
		if err != nil {
			return cancelled, err
		}
		if definition == nil {
			s.logger().Warn("skipping legacy in-app subscription validation; definition was not found",
				"subscription", subscription.ID, "identifier", subscription.Identifier)
			continue
		}
		if parsePaymentPolicy(definition.PaymentPolicy).IsPaymentMethodAllowed(PaymentInAppWallet) {
			continue
		}
		refundAmount, refundCurrency, err := s.calculateLegacyInAppRefund(ctx, subscription, definition, now)
		if err != nil {
			s.logger().Error("failed to cancel legacy in-app subscription",
				"subscription", subscription.ID, "account", subscription.AccountID, "error", err)
			continue
		}
		// The ledger API is not transaction-scoped, so the refund is issued
		// before the subscription is cancelled: a failed update can never take
		// money without a refund.
		if refundAmount.GreaterThan(decimal.Zero) {
			remarks := fmt.Sprintf("Refund for discontinued in-app subscription %s", displayNameOf(subscription, definition))
			accountID := subscription.AccountID
			if _, err := s.svc.CreateTransactionWithAccounts(ctx, nil, &accountID, refundCurrency, refundAmount, &remarks, TransactionSystem); err != nil {
				s.logger().Error("failed to refund legacy in-app subscription",
					"subscription", subscription.ID, "account", subscription.AccountID, "error", err)
				continue
			}
		}
		endedAt := now
		if now.Before(subscription.BegunAt) {
			endedAt = subscription.BegunAt
		}
		if err := s.db(ctx).Model(&WalletSubscriptionEntity{}).Where("id = ?", subscription.ID).
			Updates(map[string]any{
				"is_active":  false,
				"status":     SubscriptionCancelled,
				"renewal_at": nil,
				"ended_at":   endedAt,
			}).Error; err != nil {
			s.logger().Error("failed to cancel legacy in-app subscription",
				"subscription", subscription.ID, "error", err)
			continue
		}
		name := displayNameOf(subscription, definition)
		body := fmt.Sprintf("Your %s in-app wallet subscription is no longer available and has been cancelled. Refunded: %s %s.",
			name, refundAmount.String(), refundCurrency)
		s.push(ctx, subscription.AccountID, "Subscription Cancelled", body)
		cancelled++
		s.logger().Info("cancelled legacy in-app subscription",
			"subscription", subscription.ID, "refund", refundAmount, "currency", refundCurrency)
	}
	return cancelled, nil
}

func displayNameOf(subscription *WalletSubscriptionEntity, definition *WalletSubscriptionDefinitionEntity) string {
	if definition != nil && strings.TrimSpace(definition.DisplayName) != "" {
		return definition.DisplayName
	}
	if subscription.DisplayName != nil && strings.TrimSpace(*subscription.DisplayName) != "" {
		return *subscription.DisplayName
	}
	return subscription.Identifier
}

// calculateLegacyInAppRefund mirrors CalculateLegacyInAppRefundAmountAsync.
func (s *Subscriptions) calculateLegacyInAppRefund(ctx context.Context, subscription *WalletSubscriptionEntity, definition *WalletSubscriptionDefinitionEntity, now time.Time) (decimal.Decimal, string, error) {
	chargedAmount, err := s.resolveSubscriptionChargeAmount(ctx, subscription)
	if err != nil {
		return decimal.Zero, "", err
	}
	currency := definition.Currency
	if details := parsePaymentDetails(subscription.PaymentDetails); strings.TrimSpace(details.Currency) != "" {
		currency = details.Currency
	}
	if chargedAmount.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, currency, nil
	}
	cycle := s.DefaultSubscriptionDuration()
	if subscription.EndedAt != nil {
		cycle = subscription.EndedAt.Sub(subscription.BegunAt)
	}
	if cycle <= 0 {
		return chargedAmount, currency, nil
	}
	var remaining time.Duration
	switch {
	case now.Before(subscription.BegunAt):
		remaining = cycle
	case subscription.EndedAt != nil && subscription.EndedAt.After(now):
		remaining = subscription.EndedAt.Sub(now)
	default:
		remaining = 0
	}
	if remaining <= 0 {
		return decimal.Zero, currency, nil
	}
	totalSeconds := decimal.NewFromFloat(cycle.Seconds())
	remainingSeconds := decimal.NewFromFloat(remaining.Seconds())
	if totalSeconds.LessThanOrEqual(decimal.Zero) {
		return chargedAmount, currency, nil
	}
	ratio := remainingSeconds.Div(totalSeconds)
	if ratio.LessThan(decimal.Zero) {
		ratio = decimal.Zero
	}
	if ratio.GreaterThan(decimal.NewFromInt(1)) {
		ratio = decimal.NewFromInt(1)
	}
	return truncate3(chargedAmount.Mul(ratio)), currency, nil
}

// resolveSubscriptionChargeAmount mirrors ResolveSubscriptionChargeAmountAsync.
func (s *Subscriptions) resolveSubscriptionChargeAmount(ctx context.Context, subscription *WalletSubscriptionEntity) (decimal.Decimal, error) {
	details := parsePaymentDetails(subscription.PaymentDetails)
	if details.OrderID != nil {
		if orderID, err := uuid.Parse(*details.OrderID); err == nil {
			var order WalletOrderEntity
			err := s.db(ctx).Where("id = ?", orderID).First(&order).Error
			if err == nil && order.Amount.GreaterThan(decimal.Zero) {
				return order.Amount, nil
			}
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return decimal.Zero, err
			}
		}
	}
	coupon, err := s.couponByID(ctx, subscription.CouponID)
	if err != nil {
		return decimal.Zero, err
	}
	return calculateFinalPrice(subscription.BasePrice, subscription.IsFreeTrial, coupon, subscription.BegunAt), nil
}

// RenewSubscriptions mirrors the SubscriptionRenewalJob auto-renewal pass. It
// returns how many subscriptions were renewed and how many failed.
func (s *Subscriptions) RenewSubscriptions(ctx context.Context, batchSize int) (int, int, error) {
	if batchSize <= 0 {
		batchSize = 100
	}
	now := time.Now().UTC()
	var subscriptions []WalletSubscriptionEntity
	if err := s.db(ctx).
		Where("renewal_at IS NOT NULL AND renewal_at <= ?", now).
		Where("status = ? AND is_active = ? AND is_free_trial = ?", SubscriptionActive, true, false).
		Where("begun_at <= ?", now).
		Order("renewal_at ASC").
		Limit(batchSize).
		Find(&subscriptions).Error; err != nil {
		return 0, 0, err
	}
	renewed, failed := 0, 0
	for i := range subscriptions {
		subscription := &subscriptions[i]
		definition, err := s.GetDefinition(ctx, subscription.Identifier)
		if err != nil {
			return renewed, failed, err
		}
		if definition == nil {
			s.logger().Warn("subscription definition missing for renewal", "identifier", subscription.Identifier)
			failed++
			continue
		}
		if subscription.RenewalAt == nil {
			if err := s.db(ctx).Model(&WalletSubscriptionEntity{}).Where("id = ?", subscription.ID).
				Update("status", SubscriptionCancelled).Error; err != nil {
				return renewed, failed, err
			}
			continue
		}
		if subscription.PaymentMethod != PaymentInAppWallet ||
			!parsePaymentPolicy(definition.PaymentPolicy).AllowInternalWalletRenewal {
			s.logger().Info("skipping renewal; provider must extend via webhook or renewal is disabled",
				"subscription", subscription.ID, "payment_method", subscription.PaymentMethod)
			continue
		}
		wallet, _, err := s.svc.GetAccountWallet(ctx, subscription.AccountID)
		if err != nil || wallet == nil {
			failed++
			continue
		}
		cycle := subscriptionDuration(subscription)
		price, err := s.subscriptionFinalPrice(ctx, subscription)
		if err != nil {
			failed++
			continue
		}
		productIdentifier := subscription.Identifier
		order, err := s.createUserOrder(ctx, subscription.AccountID, definition.Currency, price, "internal", &productIdentifier, map[string]any{
			"subscription_id":         subscription.ID.String(),
			"subscription_identifier": subscription.Identifier,
			"is_renewal":              true,
		})
		if err != nil {
			s.logger().Warn("failed to auto-renew subscription with wallet payment", "subscription", subscription.ID, "error", err)
			failed++
			continue
		}
		if _, err := s.svc.PayOrder(ctx, order.ID, wallet); err != nil {
			s.logger().Warn("failed to auto-renew subscription with wallet payment", "subscription", subscription.ID, "error", err)
			failed++
			continue
		}
		endedAt := subscription.BegunAt
		if subscription.EndedAt != nil {
			endedAt = *subscription.EndedAt
		}
		newEndedAt := endedAt.Add(cycle)
		if err := s.db(ctx).Model(&WalletSubscriptionEntity{}).Where("id = ?", subscription.ID).
			Updates(map[string]any{
				"begun_at":   endedAt,
				"ended_at":   newEndedAt,
				"renewal_at": newEndedAt,
			}).Error; err != nil {
			return renewed, failed, err
		}
		renewed++
		s.logger().Info("renewed subscription", "subscription", subscription.ID)
	}
	return renewed, failed, nil
}

// CleanupGifts mirrors GiftCleanupJob: unpaid gifts older than 24 hours are
// hard-deleted. It returns the number of removed gifts.
func (s *Subscriptions) CleanupGifts(ctx context.Context) (int, error) {
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	result := s.db(ctx).Unscoped().Where("status = ? AND created_at < ?", GiftCreated, cutoff).
		Delete(&WalletGiftEntity{})
	if result.Error != nil {
		return 0, result.Error
	}
	return int(result.RowsAffected), nil
}

// LegacyInAppAvailabilityValidationDelay mirrors the background service's
// startup delay.
func (s *Subscriptions) LegacyInAppAvailabilityValidationDelay() time.Duration {
	seconds := s.CatalogSettings().LegacyInAppAvailabilityValidationDelaySeconds
	if seconds < 0 {
		seconds = 0
	}
	return time.Duration(seconds) * time.Second
}

// ── gifts ──────────────────────────────────────────────────────────────────

// PurchaseGift mirrors PurchaseGiftAsync.
func (s *Subscriptions) PurchaseGift(ctx context.Context, gifter *model.Account, recipientID *uuid.UUID, subscriptionIdentifier, paymentMethod string, details PaymentDetails, message, coupon *string, giftDuration, cycleDuration *time.Duration) (*WalletGiftEntity, error) {
	definition, err := s.GetDefinition(ctx, subscriptionIdentifier)
	if err != nil {
		return nil, err
	}
	if definition == nil {
		return nil, fmt.Errorf("Subscription %s was not found.", subscriptionIdentifier)
	}
	if !parsePaymentPolicy(definition.PaymentPolicy).IsPaymentMethodAllowed(paymentMethod) {
		return nil, fmt.Errorf("Payment method %s is not allowed for gift purchase of %s.", paymentMethod, subscriptionIdentifier)
	}
	if gifter == nil {
		return nil, errors.New("account is required")
	}
	gifterID, err := uuid.Parse(gifter.Id)
	if err != nil {
		return nil, errors.New("account id is invalid")
	}
	giftPolicy := s.GetGiftPolicy(definition)
	if !giftPolicy.AllowPurchase {
		return nil, errors.New("Gift purchase is disabled for this subscription.")
	}
	if giftPolicy.MinimumAccountLevel != nil {
		level := 0
		if gifter.Profile != nil {
			level = gifter.Profile.Level
		}
		if level < *giftPolicy.MinimumAccountLevel {
			canBypass := giftPolicy.AllowPerkSubscriptionBypass && gifter.PerkLevel > 0
			if !canBypass {
				return nil, fmt.Errorf("Account level must be at least %d to purchase this gift.", *giftPolicy.MinimumAccountLevel)
			}
		}
	}
	if recipientID != nil {
		exists, err := s.accountExists(ctx, *recipientID)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, errors.New("Recipient account not found.")
		}
	}
	var couponData *WalletCouponEntity
	if coupon != nil && strings.TrimSpace(*coupon) != "" {
		couponData, err = s.findCoupon(ctx, *coupon)
		if err != nil {
			return nil, err
		}
		if couponData == nil {
			return nil, fmt.Errorf("Coupon %s was not found.", *coupon)
		}
	}

	giftDays := 30
	if giftPolicy.GiftDurationDays != nil {
		giftDays = *giftPolicy.GiftDurationDays
	}
	// The gift's own redemption window is what the record stores; the
	// subscription length is resolved at redemption from the gift policy,
	// exactly as in PurchaseGiftAsync.
	resolvedGiftDuration := time.Duration(giftDays) * 24 * time.Hour
	if giftDuration != nil {
		resolvedGiftDuration = *giftDuration
	}
	_ = cycleDuration

	now := time.Now().UTC()
	if err := s.enforceGiftPurchaseLimit(ctx, gifterID, subscriptionIdentifier, giftPolicy, now); err != nil {
		return nil, err
	}
	giftCode, err := s.generateUniqueGiftCode(ctx)
	if err != nil {
		return nil, err
	}
	finalPrice := calculateFinalPrice(definition.BasePrice, false, couponData, now)
	gift := &WalletGiftEntity{
		ID:                     newID(),
		GifterID:               gifterID,
		RecipientID:            recipientID,
		GiftCode:               giftCode,
		Message:                message,
		SubscriptionIdentifier: subscriptionIdentifier,
		BasePrice:              definition.BasePrice,
		FinalPrice:             finalPrice,
		Status:                 GiftCreated,
		ExpiresAt:              now.Add(resolvedGiftDuration),
		IsOpenGift:             recipientID == nil,
		PaymentMethod:          paymentMethod,
		PaymentDetails:         paymentDetailsJSON(details),
	}
	if couponData != nil {
		gift.CouponID = &couponData.ID
	}
	if err := s.db(ctx).Create(gift).Error; err != nil {
		return nil, err
	}
	return gift, nil
}

// enforceGiftPurchaseLimit mirrors EnforceGiftPurchaseLimitAsync.
func (s *Subscriptions) enforceGiftPurchaseLimit(ctx context.Context, gifterID uuid.UUID, subscriptionIdentifier string, policy SubscriptionGiftPolicy, now time.Time) error {
	if policy.RollingPurchaseLimit == nil || *policy.RollingPurchaseLimit <= 0 {
		return nil
	}
	windowDays := 30
	if policy.RollingWindowDays != nil {
		windowDays = *policy.RollingWindowDays
	}
	windowStart := now.AddDate(0, 0, -windowDays)
	var count int64
	if err := s.db(ctx).Model(&WalletGiftEntity{}).
		Where("gifter_id = ? AND subscription_identifier = ? AND created_at >= ?", gifterID, subscriptionIdentifier, windowStart).
		Count(&count).Error; err != nil {
		return err
	}
	if count >= int64(*policy.RollingPurchaseLimit) {
		return errors.New("Gift purchase limit reached for this subscription.")
	}
	return nil
}

const giftCodeAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// generateUniqueGiftCode mirrors GenerateUniqueGiftCodeAsync.
func (s *Subscriptions) generateUniqueGiftCode(ctx context.Context) (string, error) {
	for range 10 {
		code := randomCode(12)
		var count int64
		if err := s.db(ctx).Model(&WalletGiftEntity{}).Where("gift_code = ?", code).Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return code, nil
		}
	}
	return "", errors.New("Unable to generate unique gift code.")
}

func randomCode(length int) string {
	buffer := make([]byte, length)
	for i := range buffer {
		buffer[i] = giftCodeAlphabet[rand.IntN(len(giftCodeAlphabet))]
	}
	return string(buffer)
}

// RedeemGift mirrors RedeemGiftAsync.
func (s *Subscriptions) RedeemGift(ctx context.Context, redeemer *model.Account, giftCode string) (*WalletGiftEntity, *WalletSubscriptionEntity, error) {
	if redeemer == nil {
		return nil, nil, errors.New("account is required")
	}
	redeemerID, err := uuid.Parse(redeemer.Id)
	if err != nil {
		return nil, nil, errors.New("account id is invalid")
	}
	now := time.Now().UTC()
	gift, err := s.GetGiftByCode(ctx, giftCode)
	if err != nil {
		return nil, nil, err
	}
	if gift == nil {
		return nil, nil, errors.New("Gift code not found.")
	}
	if gift.Status != GiftSent {
		return nil, nil, errors.New("Gift is not available for redemption.")
	}
	if now.After(gift.ExpiresAt) {
		return nil, nil, errors.New("Gift has expired.")
	}
	if gift.GifterID == redeemerID {
		return nil, nil, errors.New("You cannot redeem your own gift.")
	}
	if !gift.IsOpenGift && (gift.RecipientID == nil || *gift.RecipientID != redeemerID) {
		return nil, nil, errors.New("This gift is not intended for you.")
	}
	subscription, err := s.redeemGiftToAccount(ctx, gift, redeemerID, now)
	if err != nil {
		return nil, nil, err
	}
	if gift.GifterID != redeemerID {
		s.notifyGiftClaimed(ctx, gift, subscription, redeemer)
	}
	return gift, subscription, nil
}

// redeemGiftToAccount mirrors RedeemGiftToAccountAsync.
func (s *Subscriptions) redeemGiftToAccount(ctx context.Context, gift *WalletGiftEntity, redeemerID uuid.UUID, now time.Time) (*WalletSubscriptionEntity, error) {
	definition, err := s.GetDefinition(ctx, gift.SubscriptionIdentifier)
	if err != nil {
		return nil, err
	}
	if definition == nil {
		return nil, errors.New("Invalid gift subscription type.")
	}
	giftPolicy := s.GetGiftPolicy(definition)
	subscriptionDays := s.CatalogSettings().DefaultSubscriptionDurationDays
	if giftPolicy.SubscriptionDurationDays != nil {
		subscriptionDays = *giftPolicy.SubscriptionDurationDays
	}
	cycle := time.Duration(subscriptionDays) * 24 * time.Hour
	coupon, err := s.couponByID(ctx, gift.CouponID)
	if err != nil {
		return nil, err
	}
	subscription, err := s.applyPaidSubscription(ctx, applyPaidOptions{
		AccountID:      redeemerID,
		Definition:     definition,
		PaymentMethod:  PaymentGift,
		PaymentDetails: PaymentDetails{Currency: definition.Currency, OrderID: strPtr(gift.ID.String())},
		EffectiveFrom:  now,
		Cycle:          cycle,
		CouponID:       gift.CouponID,
		Coupon:         coupon,
	})
	if err != nil {
		return nil, err
	}
	subscriptionID := subscription.ID
	redeemer := redeemerID
	if err := s.db(ctx).Model(&WalletGiftEntity{}).Where("id = ?", gift.ID).
		Updates(map[string]any{
			"status":          GiftRedeemed,
			"redeemed_at":     now,
			"redeemer_id":     redeemer,
			"subscription_id": subscriptionID,
		}).Error; err != nil {
		return nil, err
	}
	gift.Status = GiftRedeemed
	gift.RedeemedAt = &now
	gift.RedeemerID = &redeemer
	gift.SubscriptionID = &subscriptionID
	return subscription, nil
}

// notifyGiftClaimed mirrors NotifyGiftClaimedByRecipient.
func (s *Subscriptions) notifyGiftClaimed(ctx context.Context, gift *WalletGiftEntity, subscription *WalletSubscriptionEntity, redeemer *model.Account) {
	name := subscription.Identifier
	if subscription.DisplayName != nil && strings.TrimSpace(*subscription.DisplayName) != "" {
		name = *subscription.DisplayName
	}
	redeemerName := redeemer.Name
	if strings.TrimSpace(redeemerName) == "" {
		redeemerName = redeemer.Nick
	}
	body := fmt.Sprintf("Your gift of %s has been claimed by %s", name, redeemerName)
	s.push(ctx, gift.GifterID, "Gift Claimed", body)
}

// GetGiftByCode mirrors GetGiftByCodeAsync.
func (s *Subscriptions) GetGiftByCode(ctx context.Context, giftCode string) (*WalletGiftEntity, error) {
	var gift WalletGiftEntity
	err := s.db(ctx).Where("gift_code = ?", giftCode).First(&gift).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &gift, nil
}

// GetGiftsByGifter mirrors GetGiftsByGifterAsync: unpaid gifts are excluded.
func (s *Subscriptions) GetGiftsByGifter(ctx context.Context, gifterID uuid.UUID) ([]WalletGiftEntity, error) {
	var gifts []WalletGiftEntity
	err := s.db(ctx).Where("gifter_id = ? AND status <> ?", gifterID, GiftCreated).
		Order("created_at DESC").Find(&gifts).Error
	return gifts, err
}

// GetGiftsByRecipient mirrors GetGiftsByRecipientAsync.
func (s *Subscriptions) GetGiftsByRecipient(ctx context.Context, recipientID uuid.UUID) ([]WalletGiftEntity, error) {
	var gifts []WalletGiftEntity
	err := s.db(ctx).
		Where("recipient_id = ? OR (is_open_gift = ? AND redeemer_id = ?)", recipientID, true, recipientID).
		Order("created_at DESC").Find(&gifts).Error
	return gifts, err
}

// GetGift loads one gift by id.
func (s *Subscriptions) GetGift(ctx context.Context, giftID uuid.UUID) (*WalletGiftEntity, error) {
	var gift WalletGiftEntity
	err := s.db(ctx).Where("id = ?", giftID).First(&gift).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &gift, nil
}

// MarkGiftAsSent mirrors MarkGiftAsSentAsync.
func (s *Subscriptions) MarkGiftAsSent(ctx context.Context, giftID, gifterID uuid.UUID) (*WalletGiftEntity, error) {
	var gift WalletGiftEntity
	err := s.db(ctx).Where("id = ? AND gifter_id = ?", giftID, gifterID).First(&gift).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.New("Gift not found or access denied.")
	}
	if err != nil {
		return nil, err
	}
	if gift.Status != GiftCreated {
		return nil, errors.New("Gift cannot be marked as sent.")
	}
	if err := s.db(ctx).Model(&WalletGiftEntity{}).Where("id = ?", gift.ID).
		Update("status", GiftSent).Error; err != nil {
		return nil, err
	}
	gift.Status = GiftSent
	return &gift, nil
}

// CancelGift mirrors CancelGiftAsync.
func (s *Subscriptions) CancelGift(ctx context.Context, giftID, gifterID uuid.UUID) (*WalletGiftEntity, error) {
	var gift WalletGiftEntity
	err := s.db(ctx).Where("id = ? AND gifter_id = ?", giftID, gifterID).First(&gift).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.New("Gift not found or access denied.")
	}
	if err != nil {
		return nil, err
	}
	if gift.Status != GiftCreated && gift.Status != GiftSent {
		return nil, errors.New("Gift cannot be cancelled.")
	}
	if err := s.db(ctx).Model(&WalletGiftEntity{}).Where("id = ?", gift.ID).
		Update("status", GiftCancelled).Error; err != nil {
		return nil, err
	}
	gift.Status = GiftCancelled
	return &gift, nil
}
