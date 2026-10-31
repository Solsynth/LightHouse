package wallet

// Subscription HTTP surface. Ported from DysonNetwork.Wallet's
// Payment/SubscriptionController.cs. Routes are mounted under /api/subscriptions
// with the same paths, query parameters, request/response field names
// (snake_case, matching JsonNamingPolicy.SnakeCaseLower), status codes and
// X-Total headers.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/permission"
)

// SubscriptionCatalogItem mirrors SubscriptionCatalogItem.
type SubscriptionCatalogItem struct {
	Identifier            string                     `json:"identifier"`
	GroupIdentifier       *string                    `json:"group_identifier"`
	DisplayName           string                     `json:"display_name"`
	Currency              string                     `json:"currency"`
	BasePrice             decimal.Decimal            `json:"base_price"`
	PerkLevel             int                        `json:"perk_level"`
	MinimumAccountLevel   *int                       `json:"minimum_account_level"`
	ExperienceMultiplier  *decimal.Decimal           `json:"experience_multiplier"`
	GoldenPointReward     *int                       `json:"golden_point_reward"`
	DisplayConfig         *SubscriptionDisplayConfig `json:"display_config"`
	AllowedPaymentMethods []string                   `json:"allowed_payment_methods"`
	ProviderMappings      map[string][]string        `json:"provider_mappings"`
}

// SubscriptionGroupCatalogItem mirrors SubscriptionGroupCatalogItem.
type SubscriptionGroupCatalogItem struct {
	GroupIdentifier string                     `json:"group_identifier"`
	DisplayName     string                     `json:"display_name"`
	MaxPerkLevel    int                        `json:"max_perk_level"`
	DisplayConfig   *SubscriptionDisplayConfig `json:"display_config"`
	Items           []SubscriptionCatalogItem  `json:"items"`
}

// SubscriptionReference mirrors SnSubscriptionReferenceObject.
type SubscriptionReference struct {
	ID                  string          `json:"id"`
	Identifier          string          `json:"identifier"`
	GroupIdentifier     *string         `json:"group_identifier"`
	DisplayName         *string         `json:"display_name"`
	PerkLevel           int             `json:"perk_level"`
	IsTesting           bool            `json:"is_testing"`
	BegunAt             *model.Time     `json:"begun_at"`
	EndedAt             *model.Time     `json:"ended_at"`
	IsActive            bool            `json:"is_active"`
	IsAvailable         bool            `json:"is_available"`
	IsPendingActivation bool            `json:"is_pending_activation"`
	IsFreeTrial         bool            `json:"is_free_trial"`
	Status              int             `json:"status"`
	BasePrice           decimal.Decimal `json:"base_price"`
	FinalPrice          decimal.Decimal `json:"final_price"`
	RenewalAt           *model.Time     `json:"renewal_at"`
	AccountID           string          `json:"account_id"`
	CreatedAt           *model.Time     `json:"created_at"`
	UpdatedAt           *model.Time     `json:"updated_at"`
}

// SubscriptionGroupStateItem mirrors SubscriptionGroupStateItem.
type SubscriptionGroupStateItem struct {
	Subscription SubscriptionReference    `json:"subscription"`
	Definition   *SubscriptionCatalogItem `json:"definition"`
}

// SubscriptionGroupStateResponse mirrors SubscriptionGroupStateResponse.
type SubscriptionGroupStateResponse struct {
	GroupIdentifier string                       `json:"group_identifier"`
	Catalog         SubscriptionGroupCatalogItem `json:"catalog"`
	Current         *SubscriptionGroupStateItem  `json:"current"`
	Next            *SubscriptionGroupStateItem  `json:"next"`
	Subscriptions   []SubscriptionGroupStateItem `json:"subscriptions"`
}

// PendingActivationListResponse mirrors PendingActivationListResponse.
type PendingActivationListResponse struct {
	TotalCount       int            `json:"total_count"`
	NextActivationAt *model.Time    `json:"next_activation_at"`
	Subscriptions    []Subscription `json:"subscriptions"`
}

// BillingOrderSummary mirrors BillingOrderSummary.
type BillingOrderSummary struct {
	ID                string          `json:"id"`
	Status            int             `json:"status"`
	Currency          string          `json:"currency"`
	Remarks           *string         `json:"remarks"`
	AppIdentifier     *string         `json:"app_identifier"`
	ProductIdentifier *string         `json:"product_identifier"`
	Amount            decimal.Decimal `json:"amount"`
	ExpiredAt         *model.Time     `json:"expired_at"`
	PayeeWalletID     *string         `json:"payee_wallet_id"`
	TransactionID     *string         `json:"transaction_id"`
	CreatedAt         *model.Time     `json:"created_at"`
	UpdatedAt         *model.Time     `json:"updated_at"`
}

// BillingSubscriptionSummary mirrors BillingSubscriptionSummary.
type BillingSubscriptionSummary struct {
	ID              string           `json:"id"`
	BegunAt         *model.Time      `json:"begun_at"`
	EndedAt         *model.Time      `json:"ended_at"`
	Identifier      string           `json:"identifier"`
	GroupIdentifier *string          `json:"group_identifier"`
	IsActive        bool             `json:"is_active"`
	IsFreeTrial     bool             `json:"is_free_trial"`
	Status          int              `json:"status"`
	PaymentMethod   *string          `json:"payment_method"`
	BasePrice       *decimal.Decimal `json:"base_price"`
	RenewalAt       *model.Time      `json:"renewal_at"`
	IsTesting       bool             `json:"is_testing"`
	CreatedAt       *model.Time      `json:"created_at"`
	UpdatedAt       *model.Time      `json:"updated_at"`
}

// BillingRecordResponse mirrors BillingRecordResponse.
type BillingRecordResponse struct {
	ID                  string                       `json:"id"`
	Provider            string                       `json:"provider"`
	ExternalID          string                       `json:"external_id"`
	CorrelationID       *string                      `json:"correlation_id"`
	ProviderReferenceID *string                      `json:"provider_reference_id"`
	ProductIdentifier   *string                      `json:"product_identifier"`
	AccountIdentifier   *string                      `json:"account_identifier"`
	BegunAt             *model.Time                  `json:"begun_at"`
	Duration            string                       `json:"duration"`
	IsTesting           bool                         `json:"is_testing"`
	CreatedAt           *model.Time                  `json:"created_at"`
	UpdatedAt           *model.Time                  `json:"updated_at"`
	Orders              []BillingOrderSummary        `json:"orders"`
	Subscriptions       []BillingSubscriptionSummary `json:"subscriptions"`
}

// ── helpers ────────────────────────────────────────────────────────────────

// requireWalletPermission mirrors [AskPermission]. Without a permission
// service (tests, degraded deployments) only superusers pass.
func requireWalletPermission(d *Deps, key string) gin.HandlerFunc {
	if d != nil && d.Perm != nil {
		return middleware.AskPermission(d.Perm, key)
	}
	return func(c *gin.Context) {
		user := middleware.CurrentUser(c.Request.Context())
		if user == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		if !user.IsSuperuser {
			c.AbortWithStatusJSON(http.StatusForbidden, errs.Forbidden("You do not have permission to perform this action."))
			return
		}
		c.Next()
	}
}

func failWith(c *gin.Context, code string, err error, status int) {
	message := err.Error()
	if apiErr := new(errs.ApiError); errors.As(err, &apiErr) {
		c.JSON(apiErr.Status, apiErr)
		return
	}
	c.JSON(status, errs.New(code, message, status))
}

// canonicalProviderKey maps a configured provider name to the payment method
// identifier used on the wire ("afdian", "paddle", "apple_store"). It mirrors
// providerAliases, which the catalog resolver already uses for lookups.
func canonicalProviderKey(key string) (string, bool) {
	normalized := normalizeSettingKey(key)
	for canonical, aliases := range providerAliases {
		for _, alias := range aliases {
			if normalizeSettingKey(alias) == normalized {
				return canonical, true
			}
		}
	}
	return "", false
}

// providerMappingsWire re-keys stored provider mappings by payment method.
// Definitions are stored with whatever spelling the seed or admin used
// ("AppleStore", "Paddle"), while clients key the catalog by payment method
// identifier and require all three lists to be present.
func providerMappingsWire(mappings map[string][]string) map[string][]string {
	wire := map[string][]string{
		PaymentAfdian:     {},
		PaymentPaddle:     {},
		PaymentAppleStore: {},
	}
	for key, references := range mappings {
		canonical, ok := canonicalProviderKey(key)
		if !ok {
			canonical = key
		}
		wire[canonical] = append(wire[canonical], references...)
	}
	return wire
}

// catalogItemFromDefinition mirrors MapCatalogItem.
func catalogItemFromDefinition(e *WalletSubscriptionDefinitionEntity) SubscriptionCatalogItem {
	policy := parsePaymentPolicy(e.PaymentPolicy)
	methods := policy.AllowedMethods
	if methods == nil {
		methods = []string{}
	}
	return SubscriptionCatalogItem{
		Identifier:            e.Identifier,
		GroupIdentifier:       e.GroupIdentifier,
		DisplayName:           e.DisplayName,
		Currency:              e.Currency,
		BasePrice:             e.BasePrice,
		PerkLevel:             e.PerkLevel,
		MinimumAccountLevel:   e.MinimumAccountLevel,
		ExperienceMultiplier:  e.ExperienceMultiplier,
		GoldenPointReward:     e.GoldenPointReward,
		DisplayConfig:         parseDisplayConfig(e.DisplayConfig),
		AllowedPaymentMethods: methods,
		ProviderMappings:      providerMappingsWire(parseProviderMappings(e.ProviderMappings)),
	}
}

// catalogGroupFromDefinitions mirrors MapCatalogGroup.
func catalogGroupFromDefinitions(definitions []WalletSubscriptionDefinitionEntity) SubscriptionGroupCatalogItem {
	ordered := make([]*WalletSubscriptionDefinitionEntity, 0, len(definitions))
	for i := range definitions {
		ordered = append(ordered, &definitions[i])
	}
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0; j-- {
			left, right := ordered[j-1], ordered[j]
			if left.PerkLevel < right.PerkLevel ||
				(left.PerkLevel == right.PerkLevel &&
					strings.ToLower(left.DisplayName) <= strings.ToLower(right.DisplayName)) {
				break
			}
			ordered[j-1], ordered[j] = ordered[j], ordered[j-1]
		}
	}
	group := SubscriptionGroupCatalogItem{Items: []SubscriptionCatalogItem{}}
	if len(ordered) == 0 {
		return group
	}
	primary := ordered[0]
	group.GroupIdentifier = primary.Identifier
	if primary.GroupIdentifier != nil && strings.TrimSpace(*primary.GroupIdentifier) != "" {
		group.GroupIdentifier = *primary.GroupIdentifier
	}
	group.DisplayName = primary.DisplayName
	group.DisplayConfig = parseDisplayConfig(primary.DisplayConfig)
	for _, definition := range ordered {
		if definition.PerkLevel > group.MaxPerkLevel {
			group.MaxPerkLevel = definition.PerkLevel
		}
		group.Items = append(group.Items, catalogItemFromDefinition(definition))
	}
	return group
}

// subscriptionReference mirrors ToReference.
func subscriptionReference(e *WalletSubscriptionEntity, coupon *WalletCouponEntity, now time.Time) SubscriptionReference {
	return SubscriptionReference{
		ID:                  uuidString(e.ID),
		Identifier:          e.Identifier,
		GroupIdentifier:     e.GroupIdentifier,
		DisplayName:         e.DisplayName,
		PerkLevel:           e.PerkLevel,
		IsTesting:           e.IsTesting,
		BegunAt:             timeValue(e.BegunAt),
		EndedAt:             timePointer(e.EndedAt),
		IsActive:            e.IsActive,
		IsAvailable:         subscriptionAvailable(e, now),
		IsPendingActivation: subscriptionPending(e, now),
		IsFreeTrial:         e.IsFreeTrial,
		Status:              e.Status,
		BasePrice:           e.BasePrice,
		FinalPrice:          calculateFinalPrice(e.BasePrice, e.IsFreeTrial, coupon, now),
		RenewalAt:           timePointer(e.RenewalAt),
		AccountID:           uuidString(e.AccountID),
		CreatedAt:           timeValue(e.CreatedAt),
		UpdatedAt:           timeValue(e.UpdatedAt),
	}
}

// subscriptionWire hydrates one subscription entity with its coupon for the
// C# entity serialization.
func subscriptionWire(e *WalletSubscriptionEntity, coupon *WalletCouponEntity) Subscription {
	out := subscriptionModel(e)
	if coupon != nil {
		couponWire := couponModel(coupon)
		out.Coupon = &couponWire
	}
	out.FinalPrice = calculateFinalPrice(e.BasePrice, e.IsFreeTrial, coupon, time.Now().UTC())
	return out
}

// couponsByID loads the coupons referenced by the entities.
func (s *Subscriptions) couponsByID(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]*WalletCouponEntity, error) {
	result := map[uuid.UUID]*WalletCouponEntity{}
	if len(ids) == 0 {
		return result, nil
	}
	var coupons []WalletCouponEntity
	if err := s.db(ctx).Where("id IN ?", ids).Find(&coupons).Error; err != nil {
		return nil, err
	}
	for i := range coupons {
		result[coupons[i].ID] = &coupons[i]
	}
	return result, nil
}

func couponIDsOf(subscriptions []WalletSubscriptionEntity) []uuid.UUID {
	seen := map[uuid.UUID]struct{}{}
	ids := make([]uuid.UUID, 0, len(subscriptions))
	for i := range subscriptions {
		id := subscriptions[i].CouponID
		if id == nil {
			continue
		}
		if _, ok := seen[*id]; ok {
			continue
		}
		seen[*id] = struct{}{}
		ids = append(ids, *id)
	}
	return ids
}

// subscriptionWires converts entities to their wire shape.
func (s *Subscriptions) subscriptionWires(ctx context.Context, subscriptions []WalletSubscriptionEntity) ([]Subscription, error) {
	coupons, err := s.couponsByID(ctx, couponIDsOf(subscriptions))
	if err != nil {
		return nil, err
	}
	out := make([]Subscription, 0, len(subscriptions))
	for i := range subscriptions {
		entity := &subscriptions[i]
		var coupon *WalletCouponEntity
		if entity.CouponID != nil {
			coupon = coupons[*entity.CouponID]
		}
		out = append(out, subscriptionWire(entity, coupon))
	}
	return out, nil
}

// ── registration ───────────────────────────────────────────────────────────

// registerSubscriptionHTTP mounts SubscriptionController's routes.
func registerSubscriptionHTTP(api *gin.RouterGroup, d *Deps) {
	if api == nil || d == nil || d.Service == nil {
		return
	}
	subs := NewSubscriptions(d.Service)
	subs.Configure(d.Settings, d.Log)
	if subs.Products == nil {
		subs.Products = NewProducts(d.Service)
	}

	group := api.Group("/subscriptions")

	// Public catalog and provider callbacks.
	group.GET("/catalog", subs.listCatalogHTTP)
	group.GET("/groups", subs.listCatalogGroupsHTTP)
	group.POST("/order/handle/afdian", subs.afdianWebhookHTTP)
	group.POST("/order/handle/paddle", subs.paddleWebhookHTTP)
	group.POST("/order/handle/apple", subs.appleWebhookHTTP)

	authed := group.Group("", middleware.RequireAuth())
	authed.GET("", subs.listSubscriptionsHTTP)
	authed.GET("/billing", subs.listBillingHTTP)
	authed.GET("/pending-activations", subs.listPendingActivationsHTTP)
	authed.GET("/fuzzy/:prefix", subs.getSubscriptionFuzzyHTTP)
	authed.GET("/groups/:groupIdentifier", subs.getSubscriptionGroupHTTP)
	authed.GET("/groups/:groupIdentifier/active", subs.getActiveSubscriptionGroupHTTP)
	authed.POST("/groups/:groupIdentifier/activate",
		requireWalletPermission(d, permission.SubscriptionsGroupsManage),
		subs.activateSubscriptionGroupHTTP)
	authed.GET("/:identifier", subs.getSubscriptionHTTP)
	authed.POST("", subs.createSubscriptionHTTP)
	authed.POST("/:identifier/cancel",
		requireWalletPermission(d, permission.SubscriptionsCancel),
		subs.cancelSubscriptionHTTP)
	authed.POST("/:identifier/order",
		requireWalletPermission(d, permission.SubscriptionsOrderManage),
		subs.createSubscriptionOrderHTTP)
	authed.POST("/order/restore/afdian", subs.restoreAfdianHTTP)
	authed.POST("/order/restore/paddle", subs.restorePaddleHTTP)
	authed.POST("/order/restore/apple", subs.restoreAppleHTTP)
	authed.POST("/:identifier/checkout/paddle",
		requireWalletPermission(d, permission.SubscriptionsCheckout),
		subs.createPaddleCheckoutHTTP)
	authed.POST("/:identifier/checkout/afdian",
		requireWalletPermission(d, permission.SubscriptionsCheckout),
		subs.createAfdianCheckoutHTTP)
}

// ── catalog endpoints ──────────────────────────────────────────────────────

// GET /api/subscriptions/catalog
func (s *Subscriptions) listCatalogHTTP(c *gin.Context) {
	definitions, err := s.ListDefinitions(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the subscription catalog.", http.StatusInternalServerError))
		return
	}
	items := make([]SubscriptionCatalogItem, 0, len(definitions))
	for i := range definitions {
		items = append(items, catalogItemFromDefinition(&definitions[i]))
	}
	c.JSON(http.StatusOK, items)
}

// GET /api/subscriptions/groups
func (s *Subscriptions) listCatalogGroupsHTTP(c *gin.Context) {
	groups, err := s.ListDefinitionGroups(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the subscription catalog.", http.StatusInternalServerError))
		return
	}
	items := make([]SubscriptionGroupCatalogItem, 0, len(groups))
	for _, group := range groups {
		items = append(items, catalogGroupFromDefinitions(group.Definitions))
	}
	c.JSON(http.StatusOK, items)
}

// ── account endpoints ──────────────────────────────────────────────────────

// GET /api/subscriptions
func (s *Subscriptions) listSubscriptionsHTTP(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	offset := maxInt(queryInt(c, "offset", 0), 0)
	take := queryInt(c, "take", 20)
	if take <= 0 {
		take = 20
	}
	base := func() *gorm.DB {
		return s.db(c.Request.Context()).Model(&WalletSubscriptionEntity{}).Where("account_id = ?", accountID)
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load subscriptions.", http.StatusInternalServerError))
		return
	}
	var subscriptions []WalletSubscriptionEntity
	if err := base().Order("begun_at DESC").Offset(offset).Limit(take).Find(&subscriptions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load subscriptions.", http.StatusInternalServerError))
		return
	}
	setTotal(c, total)
	wires, err := s.subscriptionWires(c.Request.Context(), subscriptions)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load subscriptions.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, wires)
}

// GET /api/subscriptions/billing
func (s *Subscriptions) listBillingHTTP(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	take := queryInt(c, "take", 20)
	if take < 1 {
		take = 1
	}
	if take > 200 {
		take = 200
	}
	offset := maxInt(queryInt(c, "offset", 0), 0)

	ctx := c.Request.Context()
	// Each subquery gets its own builder: a shared *gorm.DB is mutated by
	// BuildCondition, and reusing it would leak the previous statement.
	accountWallets := func() *gorm.DB {
		return s.db(ctx).Model(&WalletEntity{}).Select("id").Where("account_id = ?", accountID)
	}
	base := func() *gorm.DB {
		return s.db(ctx).Model(&InboundOrderEntity{}).Where(
			`account_identifier = ?
			 OR id IN (SELECT ws.inbound_order_id FROM wallet_subscriptions ws
			           WHERE ws.account_id = ? AND ws.inbound_order_id IS NOT NULL AND ws.deleted_at IS NULL)
			 OR id IN (SELECT po.inbound_order_id FROM payment_orders po
			           WHERE po.inbound_order_id IS NOT NULL AND (
			             po.payee_wallet_id IN (?)
			             OR po.transaction_id IN (
			               SELECT t.id FROM payment_transactions t
			               WHERE t.payer_wallet_id IN (?) OR t.payee_wallet_id IN (?))))`,
			accountID.String(), accountID, accountWallets(), accountWallets(), accountWallets())
	}

	var total int64
	if err := base().Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load billing records.", http.StatusInternalServerError))
		return
	}
	setTotal(c, total)

	var inboundOrders []InboundOrderEntity
	if err := base().Order("begun_at DESC").Offset(offset).Limit(take).Find(&inboundOrders).Error; err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load billing records.", http.StatusInternalServerError))
		return
	}
	ids := make([]uuid.UUID, 0, len(inboundOrders))
	for i := range inboundOrders {
		ids = append(ids, inboundOrders[i].ID)
	}
	ordersByInbound := map[uuid.UUID][]WalletOrderEntity{}
	subscriptionsByInbound := map[uuid.UUID][]WalletSubscriptionEntity{}
	if len(ids) > 0 {
		var orders []WalletOrderEntity
		if err := s.db(c.Request.Context()).Where("inbound_order_id IN ?", ids).Order("created_at DESC").Find(&orders).Error; err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load billing records.", http.StatusInternalServerError))
			return
		}
		for _, order := range orders {
			if order.InboundOrderID != nil {
				ordersByInbound[*order.InboundOrderID] = append(ordersByInbound[*order.InboundOrderID], order)
			}
		}
		var subscriptions []WalletSubscriptionEntity
		if err := s.db(c.Request.Context()).Where("inbound_order_id IN ?", ids).Order("begun_at DESC").Find(&subscriptions).Error; err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load billing records.", http.StatusInternalServerError))
			return
		}
		for _, subscription := range subscriptions {
			if subscription.InboundOrderID != nil {
				subscriptionsByInbound[*subscription.InboundOrderID] = append(subscriptionsByInbound[*subscription.InboundOrderID], subscription)
			}
		}
	}

	records := make([]BillingRecordResponse, 0, len(inboundOrders))
	for i := range inboundOrders {
		inbound := &inboundOrders[i]
		record := BillingRecordResponse{
			ID:                  uuidString(inbound.ID),
			Provider:            inbound.Provider,
			ExternalID:          inbound.ExternalID,
			CorrelationID:       inbound.CorrelationID,
			ProviderReferenceID: inbound.ProviderReferenceID,
			ProductIdentifier:   inbound.ProductIdentifier,
			BegunAt:             timeValue(inbound.BegunAt),
			Duration:            formatInterval(inbound.Duration),
			IsTesting:           inbound.IsTesting,
			CreatedAt:           timeValue(inbound.CreatedAt),
			UpdatedAt:           timeValue(inbound.UpdatedAt),
			Orders:              []BillingOrderSummary{},
			Subscriptions:       []BillingSubscriptionSummary{},
		}
		for _, order := range ordersByInbound[inbound.ID] {
			record.Orders = append(record.Orders, BillingOrderSummary{
				ID:                uuidString(order.ID),
				Status:            order.Status,
				Currency:          order.Currency,
				Remarks:           order.Remarks,
				AppIdentifier:     order.AppIdentifier,
				ProductIdentifier: order.ProductIdentifier,
				Amount:            order.Amount,
				ExpiredAt:         timeValue(order.ExpiredAt),
				PayeeWalletID:     uuidPointer(order.PayeeWalletID),
				TransactionID:     uuidPointer(order.TransactionID),
				CreatedAt:         timeValue(order.CreatedAt),
				UpdatedAt:         timeValue(order.UpdatedAt),
			})
		}
		for _, subscription := range subscriptionsByInbound[inbound.ID] {
			paymentMethod := subscription.PaymentMethod
			basePrice := subscription.BasePrice
			record.Subscriptions = append(record.Subscriptions, BillingSubscriptionSummary{
				ID:              uuidString(subscription.ID),
				BegunAt:         timeValue(subscription.BegunAt),
				EndedAt:         timePointer(subscription.EndedAt),
				Identifier:      subscription.Identifier,
				GroupIdentifier: subscription.GroupIdentifier,
				IsActive:        subscription.IsActive,
				IsFreeTrial:     subscription.IsFreeTrial,
				Status:          subscription.Status,
				PaymentMethod:   &paymentMethod,
				BasePrice:       &basePrice,
				RenewalAt:       timePointer(subscription.RenewalAt),
				IsTesting:       subscription.IsTesting,
				CreatedAt:       timeValue(subscription.CreatedAt),
				UpdatedAt:       timeValue(subscription.UpdatedAt),
			})
		}
		records = append(records, record)
	}
	c.JSON(http.StatusOK, records)
}

// GET /api/subscriptions/pending-activations
func (s *Subscriptions) listPendingActivationsHTTP(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	offset := maxInt(queryInt(c, "offset", 0), 0)
	take := maxInt(queryInt(c, "take", 20), 1)
	total, items, err := s.GetPendingActivations(c.Request.Context(), accountID, offset, take)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load pending activations.", http.StatusInternalServerError))
		return
	}
	response := PendingActivationListResponse{TotalCount: total, Subscriptions: []Subscription{}}
	if len(items) > 0 {
		next := items[0].BegunAt
		for _, item := range items[1:] {
			if item.BegunAt.Before(next) {
				next = item.BegunAt
			}
		}
		response.NextActivationAt = timeValue(next)
		wires, err := s.subscriptionWires(c.Request.Context(), items)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load pending activations.", http.StatusInternalServerError))
			return
		}
		response.Subscriptions = wires
	}
	c.JSON(http.StatusOK, response)
}

// ── group endpoints ────────────────────────────────────────────────────────

// GET /api/subscriptions/fuzzy/:prefix
func (s *Subscriptions) getSubscriptionFuzzyHTTP(c *gin.Context) {
	s.getSubscriptionGroup(c, c.Param("prefix"))
}

// GET /api/subscriptions/groups/:groupIdentifier
func (s *Subscriptions) getSubscriptionGroupHTTP(c *gin.Context) {
	s.getSubscriptionGroup(c, c.Param("groupIdentifier"))
}

func (s *Subscriptions) getSubscriptionGroup(c *gin.Context, groupIdentifier string) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	definitions, err := s.ListDefinitionsByGroup(ctx, groupIdentifier)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the subscription group.", http.StatusInternalServerError))
		return
	}
	if len(definitions) == 0 {
		c.JSON(http.StatusNotFound, errs.New("WALLET_SUBSCRIPTION_GROUP_NOT_FOUND",
			"Subscription group "+groupIdentifier+" was not found.", http.StatusNotFound))
		return
	}
	identifiers := make([]string, 0, len(definitions))
	definitionMap := make(map[string]*WalletSubscriptionDefinitionEntity, len(definitions))
	for i := range definitions {
		identifiers = append(identifiers, definitions[i].Identifier)
		definitionMap[strings.ToLower(definitions[i].Identifier)] = &definitions[i]
	}
	var subscriptionsInGroup []WalletSubscriptionEntity
	if err := s.db(ctx).
		Where("account_id = ? AND identifier IN ?", accountID, identifiers).
		Order("begun_at ASC").
		Find(&subscriptionsInGroup).Error; err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the subscription group.", http.StatusInternalServerError))
		return
	}
	if err := s.hydrateSubscriptionMetadata(ctx, subscriptionsInGroup, definitionMap); err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the subscription group.", http.StatusInternalServerError))
		return
	}
	coupons, err := s.couponsByID(ctx, couponIDsOf(subscriptionsInGroup))
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the subscription group.", http.StatusInternalServerError))
		return
	}
	now := time.Now().UTC()
	var current *WalletSubscriptionEntity
	var next *WalletSubscriptionEntity
	for i := range subscriptionsInGroup {
		subscription := &subscriptionsInGroup[i]
		if subscriptionAvailable(subscription, now) {
			if current == nil ||
				subscription.PerkLevel > current.PerkLevel ||
				(subscription.PerkLevel == current.PerkLevel && subscription.BegunAt.After(current.BegunAt)) {
				current = subscription
			}
		}
		if subscription.IsActive && subscription.Status == SubscriptionActive && subscription.BegunAt.After(now) {
			if next == nil || subscription.BegunAt.Before(next.BegunAt) {
				next = subscription
			}
		}
	}
	groupDefs := definitions
	groupCatalog := catalogGroupFromDefinitions(groupDefs)
	stateItem := func(subscription *WalletSubscriptionEntity) *SubscriptionGroupStateItem {
		coupon := (*WalletCouponEntity)(nil)
		if subscription.CouponID != nil {
			coupon = coupons[*subscription.CouponID]
		}
		item := &SubscriptionGroupStateItem{Subscription: subscriptionReference(subscription, coupon, now)}
		if definition, ok := definitionMap[strings.ToLower(subscription.Identifier)]; ok {
			catalogItem := catalogItemFromDefinition(definition)
			item.Definition = &catalogItem
		}
		return item
	}

	response := SubscriptionGroupStateResponse{
		GroupIdentifier: groupCatalog.GroupIdentifier,
		Catalog:         groupCatalog,
		Subscriptions:   []SubscriptionGroupStateItem{},
	}
	if current != nil {
		response.Current = stateItem(current)
	}
	if next != nil {
		response.Next = stateItem(next)
	}
	ordered := make([]WalletSubscriptionEntity, len(subscriptionsInGroup))
	copy(ordered, subscriptionsInGroup)
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0; j-- {
			if !ordered[j].BegunAt.After(ordered[j-1].BegunAt) {
				break
			}
			ordered[j-1], ordered[j] = ordered[j], ordered[j-1]
		}
	}
	for i := range ordered {
		response.Subscriptions = append(response.Subscriptions, *stateItem(&ordered[i]))
	}
	c.JSON(http.StatusOK, response)
}

// hydrateSubscriptionMetadata mirrors HydrateSubscriptionMetadataAsync.
func (s *Subscriptions) hydrateSubscriptionMetadata(ctx context.Context, subscriptions []WalletSubscriptionEntity, definitionMap map[string]*WalletSubscriptionDefinitionEntity) error {
	for i := range subscriptions {
		subscription := &subscriptions[i]
		definition, ok := definitionMap[strings.ToLower(subscription.Identifier)]
		if !ok {
			continue
		}
		updates := map[string]any{}
		if subscription.GroupIdentifier == nil && definition.GroupIdentifier != nil {
			subscription.GroupIdentifier = definition.GroupIdentifier
			updates["group_identifier"] = subscription.GroupIdentifier
		}
		if subscription.DisplayName == nil || strings.TrimSpace(*subscription.DisplayName) == "" {
			name := definition.DisplayName
			subscription.DisplayName = &name
			updates["display_name"] = name
		}
		if !subscription.IsTesting && subscription.PerkLevel == 0 && definition.PerkLevel > 0 {
			subscription.PerkLevel = definition.PerkLevel
			updates["perk_level"] = subscription.PerkLevel
		}
		if len(updates) > 0 {
			if err := s.db(ctx).Model(&WalletSubscriptionEntity{}).Where("id = ?", subscription.ID).
				Updates(updates).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// GET /api/subscriptions/groups/:groupIdentifier/active
func (s *Subscriptions) getActiveSubscriptionGroupHTTP(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	groupIdentifier := c.Param("groupIdentifier")
	definitions, err := s.ListDefinitionsByGroup(ctx, groupIdentifier)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the subscription group.", http.StatusInternalServerError))
		return
	}
	if len(definitions) == 0 {
		c.JSON(http.StatusNotFound, errs.New("WALLET_SUBSCRIPTION_GROUP_NOT_FOUND",
			"Subscription group "+groupIdentifier+" was not found.", http.StatusNotFound))
		return
	}
	identifiers := make([]string, 0, len(definitions))
	definitionMap := make(map[string]*WalletSubscriptionDefinitionEntity, len(definitions))
	for i := range definitions {
		identifiers = append(identifiers, definitions[i].Identifier)
		definitionMap[strings.ToLower(definitions[i].Identifier)] = &definitions[i]
	}
	var subscriptionsInGroup []WalletSubscriptionEntity
	if err := s.db(ctx).
		Where("account_id = ? AND identifier IN ?", accountID, identifiers).
		Find(&subscriptionsInGroup).Error; err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the subscription group.", http.StatusInternalServerError))
		return
	}
	if err := s.hydrateSubscriptionMetadata(ctx, subscriptionsInGroup, definitionMap); err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the subscription group.", http.StatusInternalServerError))
		return
	}
	now := time.Now().UTC()
	var active *WalletSubscriptionEntity
	for i := range subscriptionsInGroup {
		subscription := &subscriptionsInGroup[i]
		if !subscriptionAvailable(subscription, now) {
			continue
		}
		if active == nil ||
			subscription.PerkLevel > active.PerkLevel ||
			(subscription.PerkLevel == active.PerkLevel && subscription.BegunAt.After(active.BegunAt)) {
			active = subscription
		}
	}
	if active == nil {
		c.JSON(http.StatusNotFound, errs.New("WALLET_SUBSCRIPTION_NOT_FOUND",
			"No active subscription found in this group.", http.StatusNotFound))
		return
	}
	wires, err := s.subscriptionWires(ctx, []WalletSubscriptionEntity{*active})
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the subscription group.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, wires[0])
}

type activateSubscriptionRequest struct {
	SubscriptionID *uuid.UUID `json:"subscription_id"`
}

// POST /api/subscriptions/groups/:groupIdentifier/activate
func (s *Subscriptions) activateSubscriptionGroupHTTP(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	var request activateSubscriptionRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.SubscriptionID == nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
			"subscription_id": {"The SubscriptionId field is required."},
		}))
		return
	}
	target, err := s.SwitchSubscriptionActivation(c.Request.Context(), accountID, c.Param("groupIdentifier"), *request.SubscriptionID)
	if err != nil {
		failWith(c, "WALLET_SUBSCRIPTION_ACTIVATE_FAILED", err, http.StatusBadRequest)
		return
	}
	if target != nil {
		s.svc.LogRequestAction(c, accountID.String(), model.ActionLogSubscriptionActivate, map[string]any{
			"group_identifier":        c.Param("groupIdentifier"),
			"subscription_id":         target.ID.String(),
			"subscription_identifier": target.Identifier,
		})
	}
	s.getSubscriptionGroup(c, c.Param("groupIdentifier"))
}

// GET /api/subscriptions/:identifier
func (s *Subscriptions) getSubscriptionHTTP(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	identifier := c.Param("identifier")
	subscription, err := s.GetSubscription(c.Request.Context(), accountID, identifier)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the subscription.", http.StatusInternalServerError))
		return
	}
	if subscription == nil {
		c.JSON(http.StatusNotFound, errs.New("WALLET_SUBSCRIPTION_NOT_FOUND",
			"Subscription with identifier "+identifier+" was not found.", http.StatusNotFound))
		return
	}
	wires, err := s.subscriptionWires(c.Request.Context(), []WalletSubscriptionEntity{*subscription})
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the subscription.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, wires[0])
}

type createSubscriptionRequest struct {
	Identifier        string         `json:"identifier"`
	PaymentMethod     string         `json:"payment_method"`
	PaymentDetails    PaymentDetails `json:"payment_details"`
	Coupon            *string        `json:"coupon"`
	CycleDurationDays *int           `json:"cycle_duration_days"`
	IsFreeTrial       bool           `json:"is_free_trial"`
	IsAutoRenewal     *bool          `json:"is_auto_renewal"`
}

// POST /api/subscriptions
func (s *Subscriptions) createSubscriptionHTTP(c *gin.Context) {
	user, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	var request createSubscriptionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
			"identifier": {"The request body is invalid."},
		}))
		return
	}
	errorsByField := map[string][]string{}
	if strings.TrimSpace(request.Identifier) == "" {
		errorsByField["identifier"] = []string{"The Identifier field is required."}
	}
	if strings.TrimSpace(request.PaymentMethod) == "" {
		errorsByField["payment_method"] = []string{"The PaymentMethod field is required."}
	}
	if len(errorsByField) > 0 {
		c.JSON(http.StatusBadRequest, errs.Validation(errorsByField))
		return
	}
	var cycle *time.Duration
	if request.CycleDurationDays != nil {
		duration := time.Duration(*request.CycleDurationDays) * 24 * time.Hour
		cycle = &duration
	}
	coupon := ""
	if request.Coupon != nil {
		coupon = *request.Coupon
	}
	isAutoRenewal := true
	if request.IsAutoRenewal != nil {
		isAutoRenewal = *request.IsAutoRenewal
	}
	noop := strings.EqualFold(strings.TrimSpace(c.GetHeader("X-Noop")), "true")
	subscription, err := s.CreateSubscription(c.Request.Context(), user, request.Identifier, request.PaymentMethod,
		request.PaymentDetails, cycle, coupon, request.IsFreeTrial, isAutoRenewal, noop)
	if err != nil {
		if apiErr := new(errs.ApiError); errors.As(err, &apiErr) {
			c.JSON(apiErr.Status, apiErr)
			return
		}
		c.JSON(http.StatusBadRequest, errs.New("WALLET_SUBSCRIPTION_CREATE_FAILED", err.Error(), http.StatusBadRequest))
		return
	}
	s.svc.LogRequestAction(c, accountID.String(), model.ActionLogSubscriptionCreate, map[string]any{
		"subscription_identifier": request.Identifier,
		"payment_method":          request.PaymentMethod,
		"is_free_trial":           request.IsFreeTrial,
	})
	wires, err := s.subscriptionWires(c.Request.Context(), []WalletSubscriptionEntity{*subscription})
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the subscription.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, wires[0])
}

// POST /api/subscriptions/:identifier/cancel
func (s *Subscriptions) cancelSubscriptionHTTP(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	subscription, err := s.CancelSubscription(c.Request.Context(), accountID, c.Param("identifier"))
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_SUBSCRIPTION_CANCEL_FAILED", err.Error(), http.StatusBadRequest))
		return
	}
	s.svc.LogRequestAction(c, accountID.String(), model.ActionLogSubscriptionCancel, map[string]any{
		"subscription_identifier": c.Param("identifier"),
	})
	wires, err := s.subscriptionWires(c.Request.Context(), []WalletSubscriptionEntity{*subscription})
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the subscription.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, wires[0])
}

// POST /api/subscriptions/:identifier/order
func (s *Subscriptions) createSubscriptionOrderHTTP(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	order, err := s.CreateSubscriptionOrder(c.Request.Context(), accountID, c.Param("identifier"))
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_SUBSCRIPTION_ORDER_CREATE_FAILED", err.Error(), http.StatusBadRequest))
		return
	}
	c.JSON(http.StatusOK, orderModel(order, nil))
}

// ── restore endpoints ──────────────────────────────────────────────────────

type restorePurchaseRequest struct {
	OrderID string `json:"order_id"`
}

type restoreApplePurchaseRequest struct {
	SignedTransactionInfo string     `json:"signed_transaction_info"`
	WorkspaceID           *uuid.UUID `json:"workspace_id"`
}

// POST /api/subscriptions/order/restore/afdian
func (s *Subscriptions) restoreAfdianHTTP(c *gin.Context) {
	var request restorePurchaseRequest
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.OrderID) == "" {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
			"order_id": {"The OrderId field is required."},
		}))
		return
	}
	if s.Afdian == nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_SUBSCRIPTION_RESTORE_FAILED", "Afdian is not configured.", http.StatusBadRequest))
		return
	}
	order, err := s.Afdian.GetOrder(c.Request.Context(), request.OrderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to query the Afdian order.", http.StatusInternalServerError))
		return
	}
	if order == nil {
		c.JSON(http.StatusNotFound, errs.New("WALLET_ORDER_NOT_FOUND",
			"Order with ID "+request.OrderID+" was not found.", http.StatusNotFound))
		return
	}
	result, err := s.ApplyProviderOrder(c.Request.Context(), order.providerOrder(""), nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_SUBSCRIPTION_RESTORE_FAILED", err.Error(), http.StatusBadRequest))
		return
	}
	c.JSON(http.StatusOK, result)
}

// POST /api/subscriptions/order/restore/paddle
func (s *Subscriptions) restorePaddleHTTP(c *gin.Context) {
	var request restorePurchaseRequest
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.OrderID) == "" {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
			"order_id": {"The OrderId field is required."},
		}))
		return
	}
	if s.Paddle == nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_SUBSCRIPTION_RESTORE_FAILED", "Paddle is not configured.", http.StatusBadRequest))
		return
	}
	transaction, err := s.Paddle.GetTransaction(c.Request.Context(), request.OrderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to query the Paddle transaction.", http.StatusInternalServerError))
		return
	}
	if transaction == nil {
		c.JSON(http.StatusNotFound, errs.New("WALLET_TRANSACTION_NOT_FOUND",
			"Transaction with ID "+request.OrderID+" was not found.", http.StatusNotFound))
		return
	}
	result, err := s.ApplyProviderOrder(c.Request.Context(), transaction.ProviderOrder(), nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_SUBSCRIPTION_RESTORE_FAILED", err.Error(), http.StatusBadRequest))
		return
	}
	c.JSON(http.StatusOK, result)
}

// POST /api/subscriptions/order/restore/apple
func (s *Subscriptions) restoreAppleHTTP(c *gin.Context) {
	user, _, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	var request restoreApplePurchaseRequest
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.SignedTransactionInfo) == "" {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
			"signed_transaction_info": {"The SignedTransactionInfo field is required."},
		}))
		return
	}
	if s.Apple == nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_APPLE_TRANSACTION_PARSE_FAILED", "Apple App Store is not configured.", http.StatusBadRequest))
		return
	}
	transaction, err := s.Apple.ParseSignedTransaction(request.SignedTransactionInfo)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_APPLE_TRANSACTION_PARSE_FAILED", err.Error(), http.StatusBadRequest))
		return
	}
	if !strings.EqualFold(transaction.accountID(), user.Id) {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_APPLE_TRANSACTION_ACCOUNT_MISMATCH",
			"Apple transaction account token does not match the current user.", http.StatusBadRequest))
		return
	}
	order, err := transaction.ProviderOrder()
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_APPLE_TRANSACTION_PARSE_FAILED", err.Error(), http.StatusBadRequest))
		return
	}
	var extraMeta map[string]any
	if request.WorkspaceID != nil {
		extraMeta = map[string]any{"workspace_id": request.WorkspaceID.String()}
	}
	result, err := s.ApplyProviderOrder(c.Request.Context(), order, extraMeta)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_SUBSCRIPTION_RESTORE_FAILED", err.Error(), http.StatusBadRequest))
		return
	}
	c.JSON(http.StatusOK, result)
}

// ── checkout endpoints ─────────────────────────────────────────────────────

type createCheckoutRequest struct {
	ProviderReferenceID *string    `json:"provider_reference_id"`
	WorkspaceID         *uuid.UUID `json:"workspace_id"`
}

// PaddleCheckoutResponse mirrors PaddleCheckoutResponse.
type PaddleCheckoutResponse struct {
	TransactionID       string `json:"transaction_id"`
	CheckoutURL         string `json:"checkout_url"`
	ProviderReferenceID string `json:"provider_reference_id"`
}

// AfdianCheckoutResponse mirrors AfdianCheckoutResponse.
type AfdianCheckoutResponse struct {
	CheckoutURL         string `json:"checkout_url"`
	ProviderReferenceID string `json:"provider_reference_id"`
	PlanID              string `json:"plan_id"`
}

// POST /api/subscriptions/:identifier/checkout/paddle
func (s *Subscriptions) createPaddleCheckoutHTTP(c *gin.Context) {
	user, _, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	var request createCheckoutRequest
	_ = c.ShouldBindJSON(&request)
	ctx := c.Request.Context()

	definition, err := s.GetDefinition(ctx, c.Param("identifier"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the subscription.", http.StatusInternalServerError))
		return
	}
	if definition == nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_SUBSCRIPTION_CHECKOUT_FAILED",
			"Subscription "+c.Param("identifier")+" was not found.", http.StatusBadRequest))
		return
	}
	if !parsePaymentPolicy(definition.PaymentPolicy).IsPaymentMethodAllowed(PaymentPaddle) {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_SUBSCRIPTION_CHECKOUT_FAILED",
			"Payment method paddle is not allowed for subscription "+c.Param("identifier")+".", http.StatusBadRequest))
		return
	}
	reference, err := s.GetProviderReference(ctx, definition.Identifier, PaymentPaddle, request.ProviderReferenceID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to resolve the Paddle price.", http.StatusInternalServerError))
		return
	}
	if reference == nil || strings.TrimSpace(*reference) == "" {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_SUBSCRIPTION_CHECKOUT_FAILED",
			"No Paddle price mapping was configured for this subscription.", http.StatusBadRequest))
		return
	}
	if !strings.HasPrefix(strings.ToLower(*reference), "pri_") {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_SUBSCRIPTION_CHECKOUT_FAILED",
			"Configured Paddle reference "+*reference+" is not a price id. Checkout requires a Paddle price.", http.StatusBadRequest))
		return
	}
	if s.Paddle == nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_SUBSCRIPTION_CHECKOUT_FAILED", "Paddle is not configured.", http.StatusBadRequest))
		return
	}
	customData := map[string]any{
		"account_id":              user.Id,
		"subscription_identifier": definition.Identifier,
		"price_id":                *reference,
		"group_identifier":        derefOrEmpty(definition.GroupIdentifier),
	}
	if request.WorkspaceID != nil {
		customData["workspace_id"] = request.WorkspaceID.String()
	}
	session, err := s.Paddle.CreateCheckout(ctx, *reference, customData)
	if err != nil {
		failWith(c, "WALLET_SUBSCRIPTION_CHECKOUT_FAILED", err, http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusOK, PaddleCheckoutResponse{
		TransactionID:       session.TransactionID,
		CheckoutURL:         session.CheckoutURL,
		ProviderReferenceID: *reference,
	})
}

// POST /api/subscriptions/:identifier/checkout/afdian
func (s *Subscriptions) createAfdianCheckoutHTTP(c *gin.Context) {
	_, _, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	var request createCheckoutRequest
	_ = c.ShouldBindJSON(&request)
	ctx := c.Request.Context()

	definition, err := s.GetDefinition(ctx, c.Param("identifier"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the subscription.", http.StatusInternalServerError))
		return
	}
	if definition == nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_SUBSCRIPTION_CHECKOUT_FAILED",
			"Subscription "+c.Param("identifier")+" was not found.", http.StatusBadRequest))
		return
	}
	if !parsePaymentPolicy(definition.PaymentPolicy).IsPaymentMethodAllowed(PaymentAfdian) {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_SUBSCRIPTION_CHECKOUT_FAILED",
			"Payment method afdian is not allowed for subscription "+c.Param("identifier")+".", http.StatusBadRequest))
		return
	}
	reference, err := s.GetProviderReference(ctx, definition.Identifier, PaymentAfdian, request.ProviderReferenceID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to resolve the Afdian SKU.", http.StatusInternalServerError))
		return
	}
	if reference == nil || strings.TrimSpace(*reference) == "" {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_SUBSCRIPTION_CHECKOUT_FAILED",
			"No Afdian SKU mapping was configured for this subscription.", http.StatusBadRequest))
		return
	}
	if s.Afdian == nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_SUBSCRIPTION_CHECKOUT_FAILED", "Afdian is not configured.", http.StatusBadRequest))
		return
	}
	planID, err := s.Afdian.GetCheckoutPlanID()
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_SUBSCRIPTION_CHECKOUT_FAILED", err.Error(), http.StatusBadRequest))
		return
	}
	checkoutURL, err := s.Afdian.CreateCheckoutURL(planID, *reference, nil, 1)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_SUBSCRIPTION_CHECKOUT_FAILED", err.Error(), http.StatusBadRequest))
		return
	}
	c.JSON(http.StatusOK, AfdianCheckoutResponse{
		CheckoutURL:         checkoutURL,
		ProviderReferenceID: *reference,
		PlanID:              planID,
	})
}

// ── provider callbacks ─────────────────────────────────────────────────────

// POST /api/subscriptions/order/handle/afdian
func (s *Subscriptions) afdianWebhookHTTP(c *gin.Context) {
	body, err := readLimitedBody(c)
	if err != nil {
		c.JSON(http.StatusOK, afdianWebhookSuccess())
		return
	}
	provider := s.Afdian
	if provider == nil {
		c.JSON(http.StatusOK, afdianWebhookSuccess())
		return
	}
	response := provider.HandleWebhook(c.Request.Context(), body, func(ctx context.Context, order ProviderOrder) error {
		_, err := s.ApplyProviderOrder(ctx, order, nil)
		return err
	})
	c.JSON(http.StatusOK, response)
}

// POST /api/subscriptions/order/handle/paddle
func (s *Subscriptions) paddleWebhookHTTP(c *gin.Context) {
	body, err := readLimitedBody(c)
	if err != nil {
		c.Status(http.StatusUnauthorized)
		return
	}
	provider := s.Paddle
	if provider == nil {
		c.Status(http.StatusUnauthorized)
		return
	}
	ok := provider.HandleWebhook(c.Request.Context(), c.GetHeader("Paddle-Signature"), body,
		func(ctx context.Context, order ProviderOrder) error {
			_, err := s.ApplyProviderOrder(ctx, order, nil)
			return err
		})
	if !ok {
		c.Status(http.StatusUnauthorized)
		return
	}
	c.Status(http.StatusOK)
}

// POST /api/subscriptions/order/handle/apple
func (s *Subscriptions) appleWebhookHTTP(c *gin.Context) {
	body, err := readLimitedBody(c)
	if err != nil {
		c.Status(http.StatusUnauthorized)
		return
	}
	provider := s.Apple
	if provider == nil {
		c.Status(http.StatusUnauthorized)
		return
	}
	ok := provider.HandleWebhook(c.Request.Context(), body, func(ctx context.Context, order ProviderOrder) error {
		_, err := s.ApplyProviderOrder(ctx, order, nil)
		return err
	})
	if !ok {
		c.Status(http.StatusUnauthorized)
		return
	}
	c.Status(http.StatusOK)
}

// readLimitedBody reads a provider callback body (1 MiB cap, like the C#
// handler's stream read).
func readLimitedBody(c *gin.Context) ([]byte, error) {
	return io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
}
