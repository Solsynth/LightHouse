// Payment admin HTTP surface: the PaymentAdminController port.
//
// C# source:
//
//	../DysonNetwork/DysonNetwork.Wallet/Payment/PaymentAdminController.cs
//
// The C# listings hydrated the counterparty accounts through the account
// gRPC service. Stargate's wallet graph has no account client, so the wallet
// projections carry no account object; every other field, filter, error code
// and the X-Total header match the C# controller.
package wallet

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/permission"
)

// permAdminWalletsOrdersView is PermissionKeys.AdminWalletsOrdersView.
const permAdminWalletsOrdersView = "admin.wallets.orders.view"

type paymentAdminController struct {
	d *Deps
}

// registerPaymentAdminHTTP mounts /api/admin/payments.
func registerPaymentAdminHTTP(api *gin.RouterGroup, d *Deps) {
	c := &paymentAdminController{d: d}
	group := api.Group("/admin/payments", middleware.RequireAuth())
	group.GET("/transactions",
		requireWalletPerm(d, permission.WalletsTransactionsManage), c.listTransactions)
	group.GET("/transactions/:id",
		requireWalletPerm(d, permission.WalletsTransactionsManage), c.getTransaction)
	group.GET("/orders", requireWalletPerm(d, permAdminWalletsOrdersView), c.listOrders)
	group.GET("/orders/:id", requireWalletPerm(d, permAdminWalletsOrdersView), c.getOrder)
	group.GET("/inbound-orders", requireWalletPerm(d, permAdminWalletsOrdersView), c.listInboundOrders)
	group.GET("/inbound-orders/:id", requireWalletPerm(d, permAdminWalletsOrdersView), c.getInboundOrder)
	group.POST("/balance", requireWalletPerm(d, permission.WalletsBalanceModify), c.modifyBalance)
}

// adminQuery reads the first present query parameter. ASP.NET model binding is
// case insensitive and the C# clients send camelCase; both spellings work.
func adminQuery(ctx *gin.Context, names ...string) string {
	for _, name := range names {
		if value := ctx.Query(name); value != "" {
			return value
		}
	}
	return ""
}

func adminQueryUUID(ctx *gin.Context, names ...string) (*uuid.UUID, bool) {
	raw := strings.TrimSpace(adminQuery(ctx, names...))
	if raw == "" {
		return nil, true
	}
	parsed, err := uuid.Parse(raw)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
			names[0]: {fmt.Sprintf("The value '%s' is not valid.", raw)},
		}))
		return nil, false
	}
	return &parsed, true
}

func adminQueryInt(ctx *gin.Context, names ...string) (*int, bool) {
	raw := strings.TrimSpace(adminQuery(ctx, names...))
	if raw == "" {
		return nil, true
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
			names[0]: {fmt.Sprintf("The value '%s' is not valid.", raw)},
		}))
		return nil, false
	}
	return &parsed, true
}

func adminQueryBool(ctx *gin.Context, names ...string) (*bool, bool) {
	raw := strings.TrimSpace(adminQuery(ctx, names...))
	if raw == "" {
		return nil, true
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
			names[0]: {fmt.Sprintf("The value '%s' is not valid.", raw)},
		}))
		return nil, false
	}
	return &parsed, true
}

// adminPaginationParams mirrors `take = Math.Clamp(take, 1, 200)` after the
// shared PaginationValidationFilter already rejected take > 100.
func adminPaginationParams(ctx *gin.Context) (offset, take int, ok bool) {
	offset, take, ok = walletPaginationParams(ctx, 50)
	if !ok {
		return 0, 0, false
	}
	if take < 1 {
		take = 1
	}
	return offset, take, true
}

// resolveWalletIDs mirrors ResolveWalletIdsAsync.
func (c *paymentAdminController) resolveWalletIDs(ctx *gin.Context, accountID, walletID *uuid.UUID) ([]uuid.UUID, error) {
	db := c.d.Service.DB.WithContext(ctx.Request.Context())
	if walletID != nil {
		var wallet WalletEntity
		if err := db.First(&wallet, "id = ?", *walletID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, err
		}
		return []uuid.UUID{wallet.ID}, nil
	}
	if accountID == nil {
		return nil, nil
	}
	var ids []uuid.UUID
	if err := db.Model(&WalletEntity{}).Where("account_id = ?", *accountID).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// loadWallets loads the wallet rows behind a set of ids.
func (c *paymentAdminController) loadWallets(ctx *gin.Context, ids []uuid.UUID) (map[uuid.UUID]WalletEntity, error) {
	wallets := make(map[uuid.UUID]WalletEntity, len(ids))
	if len(ids) == 0 {
		return wallets, nil
	}
	var rows []WalletEntity
	if err := c.d.Service.DB.WithContext(ctx.Request.Context()).
		Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		wallets[row.ID] = row
	}
	return wallets, nil
}

// loadTransactions loads the transaction rows behind a set of ids.
func (c *paymentAdminController) loadTransactions(ctx *gin.Context, ids []uuid.UUID) (map[uuid.UUID]WalletTransactionEntity, error) {
	transactions := make(map[uuid.UUID]WalletTransactionEntity, len(ids))
	if len(ids) == 0 {
		return transactions, nil
	}
	var rows []WalletTransactionEntity
	if err := c.d.Service.DB.WithContext(ctx.Request.Context()).
		Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		transactions[row.ID] = row
	}
	return transactions, nil
}

// transactionWire renders a transaction with its wallet projections.
func (c *paymentAdminController) transactionWire(ctx *gin.Context, transaction *WalletTransactionEntity) (Transaction, error) {
	response := transactionModel(transaction)
	var ids []uuid.UUID
	if transaction.PayerWalletID != nil {
		ids = append(ids, *transaction.PayerWalletID)
	}
	if transaction.PayeeWalletID != nil {
		ids = append(ids, *transaction.PayeeWalletID)
	}
	wallets, err := c.loadWallets(ctx, ids)
	if err != nil {
		return response, err
	}
	if transaction.PayerWalletID != nil {
		if wallet, ok := wallets[*transaction.PayerWalletID]; ok {
			wire := walletModel(&wallet, nil)
			response.PayerWallet = &wire
		}
	}
	if transaction.PayeeWalletID != nil {
		if wallet, ok := wallets[*transaction.PayeeWalletID]; ok {
			wire := walletModel(&wallet, nil)
			response.PayeeWallet = &wire
		}
	}
	return response, nil
}

// GET /api/admin/payments/transactions
func (c *paymentAdminController) listTransactions(ctx *gin.Context) {
	offset, take, ok := adminPaginationParams(ctx)
	if !ok {
		return
	}
	accountID, ok := adminQueryUUID(ctx, "accountId", "account_id")
	if !ok {
		return
	}
	walletID, ok := adminQueryUUID(ctx, "walletId", "wallet_id")
	if !ok {
		return
	}
	status, ok := adminQueryInt(ctx, "status")
	if !ok {
		return
	}
	txType, ok := adminQueryInt(ctx, "type")
	if !ok {
		return
	}
	currency := strings.TrimSpace(adminQuery(ctx, "currency"))

	walletIDs, err := c.resolveWalletIDs(ctx, accountID, walletID)
	if err != nil {
		walletInternalError(ctx)
		return
	}
	if accountID != nil && len(walletIDs) == 0 {
		ctx.JSON(http.StatusNotFound, errs.New(walletErrCodeNotFound,
			"Wallet was not found for the specified account.", http.StatusNotFound))
		return
	}
	if walletID != nil && len(walletIDs) == 0 {
		ctx.JSON(http.StatusNotFound, errs.New(walletErrCodeNotFound, "Wallet was not found.", http.StatusNotFound))
		return
	}

	db := c.d.Service.DB.WithContext(ctx.Request.Context())
	query := db.Model(&WalletTransactionEntity{})
	if len(walletIDs) > 0 {
		query = query.Where("payer_wallet_id IN ? OR payee_wallet_id IN ?", walletIDs, walletIDs)
	}
	if status != nil {
		query = query.Where("status = ?", *status)
	}
	if txType != nil {
		query = query.Where("type = ?", *txType)
	}
	if currency != "" {
		query = query.Where("currency = ?", currency)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		walletInternalError(ctx)
		return
	}
	ctx.Header("X-Total", strconv.FormatInt(total, 10))

	var rows []WalletTransactionEntity
	if err := query.Order("created_at DESC").Offset(offset).Limit(take).Find(&rows).Error; err != nil {
		walletInternalError(ctx)
		return
	}
	walletSet := make([]uuid.UUID, 0, len(rows)*2)
	for _, row := range rows {
		if row.PayerWalletID != nil {
			walletSet = append(walletSet, *row.PayerWalletID)
		}
		if row.PayeeWalletID != nil {
			walletSet = append(walletSet, *row.PayeeWalletID)
		}
	}
	wallets, err := c.loadWallets(ctx, walletSet)
	if err != nil {
		walletInternalError(ctx)
		return
	}
	response := make([]Transaction, 0, len(rows))
	for i := range rows {
		wire := transactionModel(&rows[i])
		if rows[i].PayerWalletID != nil {
			if wallet, ok := wallets[*rows[i].PayerWalletID]; ok {
				projection := walletModel(&wallet, nil)
				wire.PayerWallet = &projection
			}
		}
		if rows[i].PayeeWalletID != nil {
			if wallet, ok := wallets[*rows[i].PayeeWalletID]; ok {
				projection := walletModel(&wallet, nil)
				wire.PayeeWallet = &projection
			}
		}
		response = append(response, wire)
	}
	ctx.JSON(http.StatusOK, response)
}

// GET /api/admin/payments/transactions/:id
func (c *paymentAdminController) getTransaction(ctx *gin.Context) {
	id, err := uuid.Parse(strings.TrimSpace(ctx.Param("id")))
	if err != nil {
		ctx.Status(http.StatusNotFound)
		return
	}
	var transaction WalletTransactionEntity
	if err := c.d.Service.DB.WithContext(ctx.Request.Context()).
		First(&transaction, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			ctx.JSON(http.StatusNotFound, errs.New(walletErrCodeTransactionNotFound,
				"Transaction was not found.", http.StatusNotFound))
			return
		}
		walletInternalError(ctx)
		return
	}
	response, err := c.transactionWire(ctx, &transaction)
	if err != nil {
		walletInternalError(ctx)
		return
	}
	ctx.JSON(http.StatusOK, response)
}

// orderWires renders a page of orders with the relations the C# listing
// included (Transaction, PayeeWallet, Items).
func (c *paymentAdminController) orderWires(ctx *gin.Context, orders []WalletOrderEntity) ([]Order, error) {
	db := c.d.Service.DB.WithContext(ctx.Request.Context())
	items, err := walletLoadOrderItems(ctx, db, orders)
	if err != nil {
		return nil, err
	}
	txIDs := make([]uuid.UUID, 0, len(orders))
	walletIDs := make([]uuid.UUID, 0, len(orders))
	for _, order := range orders {
		if order.TransactionID != nil {
			txIDs = append(txIDs, *order.TransactionID)
		}
		if order.PayeeWalletID != nil {
			walletIDs = append(walletIDs, *order.PayeeWalletID)
		}
	}
	transactions, err := c.loadTransactions(ctx, txIDs)
	if err != nil {
		return nil, err
	}
	wallets, err := c.loadWallets(ctx, walletIDs)
	if err != nil {
		return nil, err
	}
	response := make([]Order, 0, len(orders))
	for i := range orders {
		wire := orderModel(&orders[i], items[orders[i].ID])
		if orders[i].TransactionID != nil {
			if transaction, ok := transactions[*orders[i].TransactionID]; ok {
				projection := transactionModel(&transaction)
				wire.Transaction = &projection
			}
		}
		if orders[i].PayeeWalletID != nil {
			if wallet, ok := wallets[*orders[i].PayeeWalletID]; ok {
				projection := walletModel(&wallet, nil)
				wire.PayeeWallet = &projection
			}
		}
		response = append(response, wire)
	}
	return response, nil
}

// GET /api/admin/payments/orders
func (c *paymentAdminController) listOrders(ctx *gin.Context) {
	offset, take, ok := adminPaginationParams(ctx)
	if !ok {
		return
	}
	accountID, ok := adminQueryUUID(ctx, "accountId", "account_id")
	if !ok {
		return
	}
	walletID, ok := adminQueryUUID(ctx, "walletId", "wallet_id")
	if !ok {
		return
	}
	status, ok := adminQueryInt(ctx, "status")
	if !ok {
		return
	}
	appIdentifier := strings.TrimSpace(adminQuery(ctx, "appIdentifier", "app_identifier"))
	productIdentifier := strings.TrimSpace(adminQuery(ctx, "productIdentifier", "product_identifier"))
	currency := strings.TrimSpace(adminQuery(ctx, "currency"))

	walletIDs, err := c.resolveWalletIDs(ctx, accountID, walletID)
	if err != nil {
		walletInternalError(ctx)
		return
	}
	if accountID != nil && len(walletIDs) == 0 {
		ctx.JSON(http.StatusNotFound, errs.New(walletErrCodeNotFound,
			"Wallet was not found for the specified account.", http.StatusNotFound))
		return
	}
	if walletID != nil && len(walletIDs) == 0 {
		ctx.JSON(http.StatusNotFound, errs.New(walletErrCodeNotFound, "Wallet was not found.", http.StatusNotFound))
		return
	}

	db := c.d.Service.DB.WithContext(ctx.Request.Context())
	query := db.Model(&WalletOrderEntity{})
	if len(walletIDs) > 0 {
		transactionIDs := db.Model(&WalletTransactionEntity{}).Select("id").
			Where("payer_wallet_id IN ? OR payee_wallet_id IN ?", walletIDs, walletIDs)
		query = query.Where(
			"payer_wallet_id IN ? OR payee_wallet_id IN ? OR transaction_id IN (?)",
			walletIDs, walletIDs, transactionIDs,
		)
	}
	if status != nil {
		query = query.Where("status = ?", *status)
	}
	if appIdentifier != "" {
		query = query.Where("app_identifier = ?", appIdentifier)
	}
	if productIdentifier != "" {
		query = query.Where("product_identifier = ?", productIdentifier)
	}
	if currency != "" {
		query = query.Where("currency = ?", currency)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		walletInternalError(ctx)
		return
	}
	ctx.Header("X-Total", strconv.FormatInt(total, 10))

	var rows []WalletOrderEntity
	if err := query.Order("created_at DESC").Offset(offset).Limit(take).Find(&rows).Error; err != nil {
		walletInternalError(ctx)
		return
	}
	response, err := c.orderWires(ctx, rows)
	if err != nil {
		walletInternalError(ctx)
		return
	}
	ctx.JSON(http.StatusOK, response)
}

// GET /api/admin/payments/orders/:id
func (c *paymentAdminController) getOrder(ctx *gin.Context) {
	id, err := uuid.Parse(strings.TrimSpace(ctx.Param("id")))
	if err != nil {
		ctx.Status(http.StatusNotFound)
		return
	}
	var order WalletOrderEntity
	if err := c.d.Service.DB.WithContext(ctx.Request.Context()).
		First(&order, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			ctx.JSON(http.StatusNotFound, errs.New(walletErrCodeOrderNotFound,
				"Order was not found.", http.StatusNotFound))
			return
		}
		walletInternalError(ctx)
		return
	}
	response, err := c.orderWires(ctx, []WalletOrderEntity{order})
	if err != nil {
		walletInternalError(ctx)
		return
	}
	ctx.JSON(http.StatusOK, response[0])
}

// billingOrderSummary is BillingRecordResponse.BillingOrderSummary.
type billingOrderSummary struct {
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

func billingOrderSummaryModel(e *WalletOrderEntity) billingOrderSummary {
	return billingOrderSummary{
		ID:                uuidString(e.ID),
		Status:            e.Status,
		Currency:          e.Currency,
		Remarks:           e.Remarks,
		AppIdentifier:     e.AppIdentifier,
		ProductIdentifier: e.ProductIdentifier,
		Amount:            e.Amount,
		ExpiredAt:         timeValue(e.ExpiredAt),
		PayeeWalletID:     uuidPointer(e.PayeeWalletID),
		TransactionID:     uuidPointer(e.TransactionID),
		CreatedAt:         timeValue(e.CreatedAt),
		UpdatedAt:         timeValue(e.UpdatedAt),
	}
}

// billingSubscriptionSummary is BillingRecordResponse.BillingSubscriptionSummary.
type billingSubscriptionSummary struct {
	ID              string          `json:"id"`
	BegunAt         *model.Time     `json:"begun_at"`
	EndedAt         *model.Time     `json:"ended_at"`
	Identifier      string          `json:"identifier"`
	GroupIdentifier *string         `json:"group_identifier"`
	IsActive        bool            `json:"is_active"`
	IsFreeTrial     bool            `json:"is_free_trial"`
	Status          int             `json:"status"`
	PaymentMethod   string          `json:"payment_method"`
	BasePrice       decimal.Decimal `json:"base_price"`
	RenewalAt       *model.Time     `json:"renewal_at"`
	IsTesting       bool            `json:"is_testing"`
	CreatedAt       *model.Time     `json:"created_at"`
	UpdatedAt       *model.Time     `json:"updated_at"`
}

func billingSubscriptionSummaryModel(e *WalletSubscriptionEntity) billingSubscriptionSummary {
	return billingSubscriptionSummary{
		ID:              uuidString(e.ID),
		BegunAt:         timeValue(e.BegunAt),
		EndedAt:         timePointer(e.EndedAt),
		Identifier:      e.Identifier,
		GroupIdentifier: e.GroupIdentifier,
		IsActive:        e.IsActive,
		IsFreeTrial:     e.IsFreeTrial,
		Status:          e.Status,
		PaymentMethod:   e.PaymentMethod,
		BasePrice:       e.BasePrice,
		RenewalAt:       timePointer(e.RenewalAt),
		IsTesting:       e.IsTesting,
		CreatedAt:       timeValue(e.CreatedAt),
		UpdatedAt:       timeValue(e.UpdatedAt),
	}
}

// billingRecord is BillingRecordResponse.
type billingRecord struct {
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
	Orders              []billingOrderSummary        `json:"orders"`
	Subscriptions       []billingSubscriptionSummary `json:"subscriptions"`
}

// billingRecords builds BillingRecordResponse rows for a page of inbound
// orders, including their wallet orders and subscriptions.
func (c *paymentAdminController) billingRecords(ctx *gin.Context, inboundOrders []InboundOrderEntity) ([]billingRecord, error) {
	records := make([]billingRecord, 0, len(inboundOrders))
	if len(inboundOrders) == 0 {
		return records, nil
	}
	ids := make([]uuid.UUID, 0, len(inboundOrders))
	for _, inbound := range inboundOrders {
		ids = append(ids, inbound.ID)
	}
	db := c.d.Service.DB.WithContext(ctx.Request.Context())
	var orders []WalletOrderEntity
	if err := db.Where("inbound_order_id IN ?", ids).
		Order("created_at DESC").Find(&orders).Error; err != nil {
		return nil, err
	}
	var subscriptions []WalletSubscriptionEntity
	if err := db.Where("inbound_order_id IN ?", ids).
		Order("begun_at DESC").Find(&subscriptions).Error; err != nil {
		return nil, err
	}
	ordersByInbound := make(map[uuid.UUID][]billingOrderSummary)
	for i := range orders {
		if orders[i].InboundOrderID == nil {
			continue
		}
		ordersByInbound[*orders[i].InboundOrderID] = append(
			ordersByInbound[*orders[i].InboundOrderID], billingOrderSummaryModel(&orders[i]))
	}
	subscriptionsByInbound := make(map[uuid.UUID][]billingSubscriptionSummary)
	for i := range subscriptions {
		if subscriptions[i].InboundOrderID == nil {
			continue
		}
		subscriptionsByInbound[*subscriptions[i].InboundOrderID] = append(
			subscriptionsByInbound[*subscriptions[i].InboundOrderID], billingSubscriptionSummaryModel(&subscriptions[i]))
	}
	for i := range inboundOrders {
		inbound := &inboundOrders[i]
		recordOrders := ordersByInbound[inbound.ID]
		if recordOrders == nil {
			recordOrders = []billingOrderSummary{}
		}
		recordSubscriptions := subscriptionsByInbound[inbound.ID]
		if recordSubscriptions == nil {
			recordSubscriptions = []billingSubscriptionSummary{}
		}
		records = append(records, billingRecord{
			ID:                  uuidString(inbound.ID),
			Provider:            inbound.Provider,
			ExternalID:          inbound.ExternalID,
			CorrelationID:       inbound.CorrelationID,
			ProviderReferenceID: inbound.ProviderReferenceID,
			ProductIdentifier:   inbound.ProductIdentifier,
			AccountIdentifier:   inbound.AccountIdentifier,
			BegunAt:             timeValue(inbound.BegunAt),
			Duration:            formatInterval(inbound.Duration),
			IsTesting:           inbound.IsTesting,
			CreatedAt:           timeValue(inbound.CreatedAt),
			UpdatedAt:           timeValue(inbound.UpdatedAt),
			Orders:              recordOrders,
			Subscriptions:       recordSubscriptions,
		})
	}
	return records, nil
}

// GET /api/admin/payments/inbound-orders
func (c *paymentAdminController) listInboundOrders(ctx *gin.Context) {
	offset, take, ok := adminPaginationParams(ctx)
	if !ok {
		return
	}
	provider := strings.TrimSpace(adminQuery(ctx, "provider"))
	externalID := strings.TrimSpace(adminQuery(ctx, "externalId", "external_id"))
	accountIdentifier := strings.TrimSpace(adminQuery(ctx, "accountIdentifier", "account_identifier"))
	productIdentifier := strings.TrimSpace(adminQuery(ctx, "productIdentifier", "product_identifier"))
	orderStatus, ok := adminQueryInt(ctx, "orderStatus", "order_status")
	if !ok {
		return
	}
	subscriptionStatus, ok := adminQueryInt(ctx, "subscriptionStatus", "subscription_status")
	if !ok {
		return
	}
	isTesting, ok := adminQueryBool(ctx, "isTesting", "is_testing")
	if !ok {
		return
	}

	db := c.d.Service.DB.WithContext(ctx.Request.Context())
	query := db.Model(&InboundOrderEntity{})
	if provider != "" {
		query = query.Where("provider = ?", provider)
	}
	if externalID != "" {
		query = query.Where("external_id = ?", externalID)
	}
	if accountIdentifier != "" {
		query = query.Where("account_identifier = ?", accountIdentifier)
	}
	if productIdentifier != "" {
		orderMatch := db.Model(&WalletOrderEntity{}).Select("inbound_order_id").
			Where("product_identifier = ? AND inbound_order_id IS NOT NULL", productIdentifier)
		subscriptionMatch := db.Model(&WalletSubscriptionEntity{}).Select("inbound_order_id").
			Where("identifier = ? AND inbound_order_id IS NOT NULL", productIdentifier)
		query = query.Where(
			"product_identifier = ? OR id IN (?) OR id IN (?)",
			productIdentifier, orderMatch, subscriptionMatch,
		)
	}
	if orderStatus != nil {
		orderMatch := db.Model(&WalletOrderEntity{}).Select("inbound_order_id").
			Where("status = ? AND inbound_order_id IS NOT NULL", *orderStatus)
		query = query.Where("id IN (?)", orderMatch)
	}
	if subscriptionStatus != nil {
		subscriptionMatch := db.Model(&WalletSubscriptionEntity{}).Select("inbound_order_id").
			Where("status = ? AND inbound_order_id IS NOT NULL", *subscriptionStatus)
		query = query.Where("id IN (?)", subscriptionMatch)
	}
	if isTesting != nil {
		query = query.Where("is_testing = ?", *isTesting)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		walletInternalError(ctx)
		return
	}
	ctx.Header("X-Total", strconv.FormatInt(total, 10))

	var rows []InboundOrderEntity
	if err := query.Order("begun_at DESC").Offset(offset).Limit(take).Find(&rows).Error; err != nil {
		walletInternalError(ctx)
		return
	}
	response, err := c.billingRecords(ctx, rows)
	if err != nil {
		walletInternalError(ctx)
		return
	}
	ctx.JSON(http.StatusOK, response)
}

// GET /api/admin/payments/inbound-orders/:id
func (c *paymentAdminController) getInboundOrder(ctx *gin.Context) {
	id, err := uuid.Parse(strings.TrimSpace(ctx.Param("id")))
	if err != nil {
		ctx.Status(http.StatusNotFound)
		return
	}
	var inbound InboundOrderEntity
	if err := c.d.Service.DB.WithContext(ctx.Request.Context()).
		First(&inbound, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			ctx.JSON(http.StatusNotFound, errs.New(walletErrCodeInboundOrderNotFound,
				"Inbound order was not found.", http.StatusNotFound))
			return
		}
		walletInternalError(ctx)
		return
	}
	response, err := c.billingRecords(ctx, []InboundOrderEntity{inbound})
	if err != nil {
		walletInternalError(ctx)
		return
	}
	ctx.JSON(http.StatusOK, response[0])
}

// adminWalletBalanceRequest is PaymentAdminController.AdminWalletBalanceRequest.
type adminWalletBalanceRequest struct {
	Remark         *string         `json:"remark"`
	Amount         decimal.Decimal `json:"amount"`
	Currency       string          `json:"currency"`
	AccountID      *string         `json:"account_id"`
	WalletID       *string         `json:"wallet_id"`
	ForceOperation bool            `json:"force_operation"`
}

// balanceModifyMessage maps the domain errors onto the C# exception texts.
func balanceModifyMessage(err error) string {
	if errors.Is(err, ErrInsufficientFunds) {
		return "Insufficient funds"
	}
	switch err.Error() {
	case "cannot create transaction with negative or zero amount":
		return "Cannot create transaction with negative or zero amount."
	case "at least one wallet must be specified":
		return "At least one wallet must be specified."
	default:
		return err.Error()
	}
}

// POST /api/admin/payments/balance
func (c *paymentAdminController) modifyBalance(ctx *gin.Context) {
	var request adminWalletBalanceRequest
	if err := ctx.ShouldBindJSON(&request); err != nil && !errors.Is(err, io.EOF) {
		ctx.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
			"request": {"The request body is not valid."},
		}))
		return
	}
	if strings.TrimSpace(request.Currency) == "" {
		ctx.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
			"currency": {"The Currency field is required."},
		}))
		return
	}
	if request.WalletID == nil && request.AccountID == nil {
		ctx.JSON(http.StatusBadRequest, errs.New(walletErrCodeMissingTarget,
			"You must specify either WalletId or AccountId.", http.StatusBadRequest))
		return
	}

	var accountID, walletID *uuid.UUID
	if request.AccountID != nil && strings.TrimSpace(*request.AccountID) != "" {
		parsed, err := uuid.Parse(strings.TrimSpace(*request.AccountID))
		if err != nil {
			ctx.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
				"account_id": {fmt.Sprintf("The value '%s' is not valid.", *request.AccountID)},
			}))
			return
		}
		accountID = &parsed
	}
	if request.WalletID != nil && strings.TrimSpace(*request.WalletID) != "" {
		parsed, err := uuid.Parse(strings.TrimSpace(*request.WalletID))
		if err != nil {
			ctx.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
				"wallet_id": {fmt.Sprintf("The value '%s' is not valid.", *request.WalletID)},
			}))
			return
		}
		walletID = &parsed
	}
	if accountID == nil && walletID == nil {
		ctx.JSON(http.StatusBadRequest, errs.New(walletErrCodeMissingTarget,
			"You must specify either WalletId or AccountId.", http.StatusBadRequest))
		return
	}

	service := c.d.Service
	var wallet *WalletEntity
	var err error
	if accountID != nil {
		wallet, _, err = service.GetAccountWallet(ctx.Request.Context(), *accountID)
	} else {
		wallet, _, err = service.GetWallet(ctx.Request.Context(), *walletID)
	}
	if err != nil || wallet == nil {
		ctx.JSON(http.StatusNotFound, errs.New(walletErrCodeNotFound, "Wallet was not found.", http.StatusNotFound))
		return
	}

	input := TransactionInput{
		Currency: strings.TrimSpace(request.Currency),
		Remarks:  request.Remark,
		Type:     TransactionSystem,
		Force:    request.ForceOperation,
	}
	if request.Amount.IsNegative() {
		input.PayerWalletID = &wallet.ID
		input.Amount = request.Amount.Neg()
	} else {
		input.PayeeWalletID = &wallet.ID
		input.Amount = request.Amount
	}
	transaction, err := service.CreateTransaction(ctx.Request.Context(), input)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, errs.New(walletErrCodeBalanceModify,
			balanceModifyMessage(err), http.StatusBadRequest))
		return
	}
	response, err := c.transactionWire(ctx, transaction)
	if err != nil {
		walletInternalError(ctx)
		return
	}
	ctx.JSON(http.StatusOK, response)
}
