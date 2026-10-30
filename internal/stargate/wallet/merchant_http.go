// Merchant HTTP surface: the MerchantController port.
//
// C# source:
//
//	../DysonNetwork/DysonNetwork.Wallet/Payment/MerchantController.cs
//
// The C# controller resolved the `{merchant}` route value through the
// Publisher registry (RemotePublisherService.GetPublisherByName) and
// authorised callers through publisher membership. Stargate has no publisher
// port yet, so the route value resolves against the local merchant rows
// (merchant id, publisher id, then the publisher name stored on the merchant
// record) and ownership is granted to superusers or to the account that owns
// the merchant's payout wallet. Publisher Owner/Manager membership is
// therefore not enforced; see the package report.
package wallet

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/permission"
)

// walletPaginationMaxTake mirrors PaginationOptions.DefaultMaxTake (100).
const walletPaginationMaxTake = 100

// Wallet error codes shared by the merchant/product/admin controllers.
const (
	walletErrCodeNotFound             = "WALLET_NOT_FOUND"
	walletErrCodeMerchantNoPayWallet  = "WALLET_MERCHANT_NO_PAYMENT_WALLET"
	walletErrCodeMerchantProfile      = "MERCHANT_PROFILE_NOT_FOUND"
	walletErrCodePaginationTake       = "PAGINATION_TAKE_EXCEEDED"
	walletErrCodeProductCheckout      = "WALLET_PRODUCT_CHECKOUT_FAILED"
	walletErrCodeOrderApply           = "WALLET_ORDER_APPLY_FAILED"
	walletErrCodeBalanceModify        = "WALLET_BALANCE_MODIFY_FAILED"
	walletErrCodeMissingTarget        = "WALLET_MISSING_TARGET"
	walletErrCodeTransactionNotFound  = "WALLET_TRANSACTION_NOT_FOUND"
	walletErrCodeOrderNotFound        = "WALLET_ORDER_NOT_FOUND"
	walletErrCodeInboundOrderNotFound = "WALLET_INBOUND_ORDER_NOT_FOUND"
)

// requireWalletPerm mirrors Shared/Auth/RemotePermissionMiddleware: an
// unauthenticated caller gets 401, a superuser bypasses the check, otherwise
// every key must pass. A permission-service failure is a 500, never a denial.
func requireWalletPerm(d *Deps, keys ...string) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		user := middleware.CurrentUser(ctx.Request.Context())
		if user == nil {
			ctx.AbortWithStatusJSON(http.StatusUnauthorized,
				errs.Unauthorized("Authentication is required before permission checks can run."))
			return
		}
		if user.IsSuperuser {
			ctx.Next()
			return
		}
		accountID, err := uuid.Parse(user.Id)
		if err != nil {
			ctx.AbortWithStatusJSON(http.StatusUnauthorized,
				errs.Unauthorized("Authentication is required before permission checks can run."))
			return
		}
		if d.Perm == nil {
			ctx.AbortWithStatusJSON(http.StatusInternalServerError,
				errs.New("SERVER_ERROR", "Error checking permissions.", http.StatusInternalServerError))
			return
		}
		for _, key := range keys {
			allowed, err := d.Perm.HasPermission(ctx.Request.Context(), accountID, key)
			if err != nil {
				ctx.AbortWithStatusJSON(http.StatusInternalServerError,
					errs.New("SERVER_ERROR", "Error checking permissions.", http.StatusInternalServerError))
				return
			}
			if !allowed {
				ctx.AbortWithStatusJSON(http.StatusForbidden, errs.Forbidden("Permission "+key+" was required."))
				return
			}
		}
		ctx.Next()
	}
}

// walletInternalError writes the generic 500 used across the wallet surface.
func walletInternalError(ctx *gin.Context) {
	ctx.JSON(http.StatusInternalServerError,
		errs.New("SERVER_ERROR", "An internal server error occurred.", http.StatusInternalServerError))
}

// walletPaginationTakeExceeded mirrors PaginationValidationFilter's 400.
func walletPaginationTakeExceeded(take int) *errs.ApiError {
	return &errs.ApiError{
		Code:    walletErrCodePaginationTake,
		Message: "One or more validation errors occurred.",
		Status:  http.StatusBadRequest,
		Errors: map[string][]string{
			"take": {fmt.Sprintf("The 'take' parameter (%d) exceeds the maximum allowed value (%d).", take, walletPaginationMaxTake)},
		},
	}
}

// walletPaginationParams reads the C# `offset`/`take` query pair. A `take`
// above the shared maximum is rejected exactly like PaginationValidationFilter.
func walletPaginationParams(ctx *gin.Context, defaultTake int) (offset, take int, ok bool) {
	if raw := ctx.Query("offset"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			offset = value
		}
	}
	take = defaultTake
	if raw := ctx.Query("take"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil {
			take = value
		}
	}
	if take > walletPaginationMaxTake {
		ctx.JSON(http.StatusBadRequest, walletPaginationTakeExceeded(take))
		return 0, 0, false
	}
	if take < 0 {
		take = 0
	}
	return offset, take, true
}

// merchantParseQueryTime mirrors ASP.NET's DateTime? binding for the stats
// filters. Bare dates are read as UTC (the C# handler converted Unspecified
// kinds with ToUniversalTime, i.e. as server-local time).
func merchantParseQueryTime(raw string) (time.Time, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false, nil
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.9999999",
		"2006-01-02T15:04:05",
		"2006-01-02",
	} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC(), true, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("The value '%s' is not valid.", raw)
}

// merchantQueryInt parses an optional integer query parameter (enum filters).
func merchantQueryInt(ctx *gin.Context, name string) (*int, bool) {
	raw := strings.TrimSpace(ctx.Query(name))
	if raw == "" {
		return nil, true
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
			name: {fmt.Sprintf("The value '%s' is not valid.", raw)},
		}))
		return nil, false
	}
	return &parsed, true
}

// merchantProfileNotFound mirrors MerchantController.MerchantNotFound.
func merchantProfileNotFound(name string) *errs.ApiError {
	return errs.New(walletErrCodeMerchantProfile,
		fmt.Sprintf("Merchant profile for publisher '%s' was not found. Configure the publisher's merchant wallet first.", name),
		http.StatusNotFound)
}

func merchantNoPaymentWallet() *errs.ApiError {
	return errs.New(walletErrCodeMerchantNoPayWallet,
		"No payment wallet configured for this merchant.", http.StatusBadRequest)
}

func merchantWalletNotFound() *errs.ApiError {
	return errs.New(walletErrCodeNotFound, "Wallet was not found.", http.StatusBadRequest)
}

type merchantController struct {
	d *Deps
}

// registerMerchantHTTP mounts MerchantController under /api/merchants.
func registerMerchantHTTP(api *gin.RouterGroup, d *Deps) {
	c := &merchantController{d: d}
	group := api.Group("/merchants", middleware.RequireAuth())
	group.GET("/:merchant", c.getMerchant)
	group.GET("/:merchant/settlements", c.listSettlements)
	group.GET("/:merchant/settlements/pending", c.pendingSummary)
	group.POST("/:merchant/settlements/settle",
		requireWalletPerm(d, permission.MerchantsSettlementsManage), c.manualSettle)
	group.GET("/:merchant/orders", c.listOrders)
	group.PATCH("/:merchant/wallet", requireWalletPerm(d, permission.MerchantsManage), c.updateWallet)
	group.GET("/:merchant/stats/overview", c.overviewStats)
	group.GET("/:merchant/stats/incoming", c.periodIncoming)
	group.GET("/:merchant/stats/daily", c.dailyIncoming)
}

// findMerchant resolves the `{merchant}` route value against the local
// merchant rows: merchant id, publisher id, then the stored publisher name.
// A nil result means no merchant matched.
func (c *merchantController) findMerchant(ctx *gin.Context) (*MerchantEntity, error) {
	raw := strings.TrimSpace(ctx.Param("merchant"))
	db := c.d.Service.DB.WithContext(ctx.Request.Context())
	var merchant MerchantEntity
	if id, err := uuid.Parse(raw); err == nil {
		switch err := db.First(&merchant, "id = ?", id).Error; {
		case err == nil:
			return &merchant, nil
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return nil, err
		}
		switch err := db.First(&merchant, "publisher_id = ?", id).Error; {
		case err == nil:
			return &merchant, nil
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return nil, err
		}
		return nil, nil
	}
	switch err := db.First(&merchant, "name = ?", raw).Error; {
	case err == nil:
		return &merchant, nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, err
	}
	return nil, nil
}

// resolveMerchant resolves the `{merchant}` route value and writes the C# 404
// when nothing matches.
func (c *merchantController) resolveMerchant(ctx *gin.Context) (*MerchantEntity, bool) {
	merchant, err := c.findMerchant(ctx)
	if err != nil {
		walletInternalError(ctx)
		return nil, false
	}
	if merchant == nil {
		ctx.JSON(http.StatusNotFound, merchantProfileNotFound(strings.TrimSpace(ctx.Param("merchant"))))
		return nil, false
	}
	return merchant, true
}

// requireMerchantOwner mirrors IsMerchantOwner for the parts Stargate can
// evaluate: superusers always pass and the payout wallet's owner passes.
func (c *merchantController) requireMerchantOwner(ctx *gin.Context, merchant *MerchantEntity, message string) bool {
	user := middleware.CurrentUser(ctx.Request.Context())
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return false
	}
	accountID, err := uuid.Parse(user.Id)
	if err != nil {
		ctx.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return false
	}
	if user.IsSuperuser {
		return true
	}
	if merchant.PaymentWalletID != nil {
		var wallet WalletEntity
		if err := c.d.Service.DB.WithContext(ctx.Request.Context()).
			First(&wallet, "id = ?", *merchant.PaymentWalletID).Error; err == nil {
			if wallet.AccountID != nil && *wallet.AccountID == accountID {
				return true
			}
		}
	}
	ctx.JSON(http.StatusForbidden, errs.Forbidden(message))
	return false
}

// canManageWallet mirrors CanManageWalletAsync: the account owning the wallet,
// or a superuser. Realm wallets needed publisher Editor membership, which
// Stargate cannot resolve, so only superusers may manage them.
func (c *merchantController) canManageWallet(wallet *WalletEntity, accountID uuid.UUID, isSuperuser bool) bool {
	if isSuperuser {
		return true
	}
	return wallet.AccountID != nil && *wallet.AccountID == accountID
}

type merchantProfileResponse struct {
	ID              string      `json:"id"`
	PublisherID     string      `json:"publisher_id"`
	PaymentWalletID *string     `json:"payment_wallet_id"`
	Name            *string     `json:"name"`
	CreatedAt       *model.Time `json:"created_at"`
	UpdatedAt       *model.Time `json:"updated_at"`
}

func merchantProfile(merchant *MerchantEntity) merchantProfileResponse {
	return merchantProfileResponse{
		ID:              uuidString(merchant.ID),
		PublisherID:     uuidString(merchant.PublisherID),
		PaymentWalletID: uuidPointer(merchant.PaymentWalletID),
		Name:            merchant.Name,
		CreatedAt:       timeValue(merchant.CreatedAt),
		UpdatedAt:       timeValue(merchant.UpdatedAt),
	}
}

type merchantWalletResponse struct {
	ID              string  `json:"id"`
	PublisherID     string  `json:"publisher_id"`
	PaymentWalletID *string `json:"payment_wallet_id"`
	Name            *string `json:"name"`
}

// GET /api/merchants/:merchant
func (c *merchantController) getMerchant(ctx *gin.Context) {
	merchant, ok := c.resolveMerchant(ctx)
	if !ok {
		return
	}
	if !c.requireMerchantOwner(ctx, merchant, "You do not have access to this merchant.") {
		return
	}
	ctx.JSON(http.StatusOK, merchantProfile(merchant))
}

type settlementListItem struct {
	ID                      string          `json:"id"`
	MerchantID              string          `json:"merchant_id"`
	OrderID                 *string         `json:"order_id"`
	AwardID                 *string         `json:"award_id"`
	PaymentTransactionID    *string         `json:"payment_transaction_id"`
	PaymentWalletID         string          `json:"payment_wallet_id"`
	Currency                string          `json:"currency"`
	Amount                  decimal.Decimal `json:"amount"`
	Status                  int             `json:"status"`
	SettledBy               *int            `json:"settled_by"`
	SettledAt               *model.Time     `json:"settled_at"`
	SettlementTransactionID *string         `json:"settlement_transaction_id"`
	CreatedAt               *model.Time     `json:"created_at"`
	UpdatedAt               *model.Time     `json:"updated_at"`
}

func settlementListItemModel(e *MerchantSettlementEntity) settlementListItem {
	return settlementListItem{
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
	}
}

// GET /api/merchants/:merchant/settlements
func (c *merchantController) listSettlements(ctx *gin.Context) {
	merchant, ok := c.resolveMerchant(ctx)
	if !ok {
		return
	}
	if !c.requireMerchantOwner(ctx, merchant, "You do not have access to this merchant.") {
		return
	}
	offset, take, ok := walletPaginationParams(ctx, 20)
	if !ok {
		return
	}

	db := c.d.Service.DB.WithContext(ctx.Request.Context())
	query := db.Model(&MerchantSettlementEntity{}).Where("merchant_id = ?", merchant.ID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		walletInternalError(ctx)
		return
	}
	ctx.Header("X-Total", strconv.FormatInt(total, 10))

	var settlements []MerchantSettlementEntity
	if err := query.Order("created_at DESC").Offset(offset).Limit(take).Find(&settlements).Error; err != nil {
		walletInternalError(ctx)
		return
	}
	response := make([]settlementListItem, 0, len(settlements))
	for _, settlement := range settlements {
		response = append(response, settlementListItemModel(&settlement))
	}
	ctx.JSON(http.StatusOK, response)
}

type merchantOrderItem struct {
	ID                string          `json:"id"`
	OrderID           string          `json:"order_id"`
	ProductIdentifier string          `json:"product_identifier"`
	Quantity          int             `json:"quantity"`
	UnitPrice         decimal.Decimal `json:"unit_price"`
	Currency          string          `json:"currency"`
	CreatedAt         *model.Time     `json:"created_at"`
	UpdatedAt         *model.Time     `json:"updated_at"`
}

type merchantOrder struct {
	ID                string              `json:"id"`
	Status            int                 `json:"status"`
	Currency          string              `json:"currency"`
	Remarks           *string             `json:"remarks"`
	AppIdentifier     *string             `json:"app_identifier"`
	ProductIdentifier *string             `json:"product_identifier"`
	Meta              any                 `json:"meta"`
	Amount            decimal.Decimal     `json:"amount"`
	ExpiredAt         *model.Time         `json:"expired_at"`
	PayeeWalletID     *string             `json:"payee_wallet_id"`
	TransactionID     *string             `json:"transaction_id"`
	CreatedAt         *model.Time         `json:"created_at"`
	UpdatedAt         *model.Time         `json:"updated_at"`
	Items             []merchantOrderItem `json:"items"`
}

func merchantOrderModel(e *WalletOrderEntity, items []WalletOrderItemEntity) merchantOrder {
	out := merchantOrder{
		ID:                uuidString(e.ID),
		Status:            e.Status,
		Currency:          e.Currency,
		Remarks:           e.Remarks,
		AppIdentifier:     e.AppIdentifier,
		ProductIdentifier: e.ProductIdentifier,
		Meta:              jsonValue(e.Meta),
		Amount:            e.Amount,
		ExpiredAt:         timeValue(e.ExpiredAt),
		PayeeWalletID:     uuidPointer(e.PayeeWalletID),
		TransactionID:     uuidPointer(e.TransactionID),
		CreatedAt:         timeValue(e.CreatedAt),
		UpdatedAt:         timeValue(e.UpdatedAt),
		Items:             make([]merchantOrderItem, 0, len(items)),
	}
	for _, item := range items {
		out.Items = append(out.Items, merchantOrderItem{
			ID:                uuidString(item.ID),
			OrderID:           uuidString(item.OrderID),
			ProductIdentifier: item.ProductIdentifier,
			Quantity:          item.Quantity,
			UnitPrice:         item.UnitPrice,
			Currency:          item.Currency,
			CreatedAt:         timeValue(item.CreatedAt),
			UpdatedAt:         timeValue(item.UpdatedAt),
		})
	}
	return out
}

// GET /api/merchants/:merchant/orders
func (c *merchantController) listOrders(ctx *gin.Context) {
	merchant, ok := c.resolveMerchant(ctx)
	if !ok {
		return
	}
	if !c.requireMerchantOwner(ctx, merchant, "You do not have access to this merchant.") {
		return
	}
	status, ok := merchantQueryInt(ctx, "status")
	if !ok {
		return
	}
	if merchant.PaymentWalletID == nil {
		ctx.JSON(http.StatusBadRequest, merchantNoPaymentWallet())
		return
	}
	offset, take, ok := walletPaginationParams(ctx, 20)
	if !ok {
		return
	}

	db := c.d.Service.DB.WithContext(ctx.Request.Context())
	query := db.Model(&WalletOrderEntity{}).
		Where("payee_wallet_id = ? AND app_identifier IS NOT NULL", *merchant.PaymentWalletID)
	if status != nil {
		query = query.Where("status = ?", *status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		walletInternalError(ctx)
		return
	}
	ctx.Header("X-Total", strconv.FormatInt(total, 10))

	var orders []WalletOrderEntity
	if err := query.Order("created_at DESC").Offset(offset).Limit(take).Find(&orders).Error; err != nil {
		walletInternalError(ctx)
		return
	}
	itemsByOrder, err := walletLoadOrderItems(ctx, db, orders)
	if err != nil {
		walletInternalError(ctx)
		return
	}
	response := make([]merchantOrder, 0, len(orders))
	for i := range orders {
		response = append(response, merchantOrderModel(&orders[i], itemsByOrder[orders[i].ID]))
	}
	ctx.JSON(http.StatusOK, response)
}

// walletLoadOrderItems batches the order-item rows for a page of orders.
func walletLoadOrderItems(ctx *gin.Context, db *gorm.DB, orders []WalletOrderEntity) (map[uuid.UUID][]WalletOrderItemEntity, error) {
	result := make(map[uuid.UUID][]WalletOrderItemEntity, len(orders))
	if len(orders) == 0 {
		return result, nil
	}
	ids := make([]uuid.UUID, 0, len(orders))
	for _, order := range orders {
		ids = append(ids, order.ID)
	}
	var items []WalletOrderItemEntity
	if err := db.WithContext(ctx.Request.Context()).
		Where("order_id IN ?", ids).Order("created_at").Find(&items).Error; err != nil {
		return nil, err
	}
	for _, item := range items {
		result[item.OrderID] = append(result[item.OrderID], item)
	}
	return result, nil
}

type updateMerchantWalletRequest struct {
	WalletID *string `json:"wallet_id"`
}

// PATCH /api/merchants/:merchant/wallet
func (c *merchantController) updateWallet(ctx *gin.Context) {
	user := middleware.CurrentUser(ctx.Request.Context())
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	accountID, err := uuid.Parse(user.Id)
	if err != nil {
		ctx.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}

	var request updateMerchantWalletRequest
	if err := ctx.ShouldBindJSON(&request); err != nil && !errors.Is(err, io.EOF) {
		ctx.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
			"request": {"The request body is not valid."},
		}))
		return
	}
	var walletID *uuid.UUID
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

	raw := strings.TrimSpace(ctx.Param("merchant"))
	db := c.d.Service.DB.WithContext(ctx.Request.Context())
	merchant, lookupErr := c.findMerchant(ctx)
	if lookupErr != nil {
		walletInternalError(ctx)
		return
	}

	if merchant == nil {
		// Creation path: the C# controller resolved the publisher by name and
		// created the merchant for it. Without a publisher directory only a
		// superuser may create the record, keyed by the publisher UUID.
		if !user.IsSuperuser {
			ctx.JSON(http.StatusForbidden, errs.Forbidden("You do not have access to this merchant."))
			return
		}
		publisherID, err := uuid.Parse(raw)
		if err != nil {
			ctx.JSON(http.StatusNotFound, merchantProfileNotFound(raw))
			return
		}
		if walletID != nil {
			wallet, _, err := c.d.Service.GetWallet(ctx.Request.Context(), *walletID)
			if err != nil {
				ctx.JSON(http.StatusBadRequest, merchantWalletNotFound())
				return
			}
			if !c.canManageWallet(wallet, accountID, user.IsSuperuser) {
				ctx.JSON(http.StatusForbidden, errs.Forbidden("You do not have access to this wallet."))
				return
			}
		}
		created, err := c.d.Service.UpsertMerchant(ctx.Request.Context(), publisherID, walletID, nil)
		if err != nil {
			walletInternalError(ctx)
			return
		}
		ctx.JSON(http.StatusOK, merchantWalletResponse{
			ID:              uuidString(created.ID),
			PublisherID:     uuidString(created.PublisherID),
			PaymentWalletID: uuidPointer(created.PaymentWalletID),
			Name:            created.Name,
		})
		return
	}

	if !c.requireMerchantOwner(ctx, merchant, "You do not have access to this merchant.") {
		return
	}
	if walletID != nil {
		wallet, _, err := c.d.Service.GetWallet(ctx.Request.Context(), *walletID)
		if err != nil {
			ctx.JSON(http.StatusBadRequest, merchantWalletNotFound())
			return
		}
		if !c.canManageWallet(wallet, accountID, user.IsSuperuser) {
			ctx.JSON(http.StatusForbidden, errs.Forbidden("You do not have access to this wallet."))
			return
		}
	}
	if err := db.Model(&MerchantEntity{}).Where("id = ?", merchant.ID).
		Update("payment_wallet_id", walletID).Error; err != nil {
		walletInternalError(ctx)
		return
	}
	merchant.PaymentWalletID = walletID
	ctx.JSON(http.StatusOK, merchantWalletResponse{
		ID:              uuidString(merchant.ID),
		PublisherID:     uuidString(merchant.PublisherID),
		PaymentWalletID: uuidPointer(merchant.PaymentWalletID),
		Name:            merchant.Name,
	})
}

type pendingCurrency struct {
	Count int             `json:"count"`
	Total decimal.Decimal `json:"total"`
}

// GET /api/merchants/:merchant/settlements/pending
func (c *merchantController) pendingSummary(ctx *gin.Context) {
	merchant, ok := c.resolveMerchant(ctx)
	if !ok {
		return
	}
	if !c.requireMerchantOwner(ctx, merchant, "You do not have access to this merchant.") {
		return
	}
	if merchant.PaymentWalletID == nil {
		ctx.JSON(http.StatusBadRequest, merchantNoPaymentWallet())
		return
	}
	totals, err := c.d.Service.PendingTotals(ctx.Request.Context(), *merchant.PaymentWalletID)
	if err != nil {
		walletInternalError(ctx)
		return
	}
	pending := make(map[string]pendingCurrency, len(totals))
	for currency, stat := range totals {
		pending[currency] = pendingCurrency{Count: stat.Count, Total: stat.Total}
	}
	ctx.JSON(http.StatusOK, gin.H{
		"merchant_id": uuidString(merchant.ID),
		"wallet_id":   uuidPointer(merchant.PaymentWalletID),
		"pending":     pending,
	})
}

type settledTransaction struct {
	ID       string          `json:"id"`
	Currency string          `json:"currency"`
	Amount   decimal.Decimal `json:"amount"`
}

// POST /api/merchants/:merchant/settlements/settle
func (c *merchantController) manualSettle(ctx *gin.Context) {
	merchant, ok := c.resolveMerchant(ctx)
	if !ok {
		return
	}
	if !c.requireMerchantOwner(ctx, merchant, "You do not have permission to settle this merchant.") {
		return
	}
	if merchant.PaymentWalletID == nil {
		ctx.JSON(http.StatusBadRequest, merchantNoPaymentWallet())
		return
	}
	transactions, err := c.d.Service.SettleWallet(ctx.Request.Context(), *merchant.PaymentWalletID, SettlementManual)
	if err != nil {
		walletInternalError(ctx)
		return
	}
	if len(transactions) == 0 {
		ctx.JSON(http.StatusOK, gin.H{"message": "No pending funds to settle"})
		return
	}
	settled := make([]settledTransaction, 0, len(transactions))
	for _, transaction := range transactions {
		settled = append(settled, settledTransaction{
			ID:       uuidString(transaction.ID),
			Currency: transaction.Currency,
			Amount:   transaction.Amount,
		})
	}
	ctx.JSON(http.StatusOK, gin.H{
		"message":      fmt.Sprintf("Settled %d currency group(s)", len(transactions)),
		"transactions": settled,
	})
}

type currencyStatResponse struct {
	Count int             `json:"count"`
	Total decimal.Decimal `json:"total"`
}

func currencyStatMap(source map[string]CurrencyStat) map[string]currencyStatResponse {
	out := make(map[string]currencyStatResponse, len(source))
	for currency, stat := range source {
		out[currency] = currencyStatResponse{Count: stat.Count, Total: stat.Total}
	}
	return out
}

// GET /api/merchants/:merchant/stats/overview
func (c *merchantController) overviewStats(ctx *gin.Context) {
	merchant, ok := c.resolveMerchant(ctx)
	if !ok {
		return
	}
	if !c.requireMerchantOwner(ctx, merchant, "You do not have access to this merchant.") {
		return
	}
	stats, err := c.d.Service.OverviewStats(ctx.Request.Context(), merchant.ID)
	if err != nil {
		walletInternalError(ctx)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{
		"total_pending":  stats.TotalPending,
		"total_settled":  stats.TotalSettled,
		"total_all_time": stats.TotalAllTime,
		"pending":        currencyStatMap(stats.PendingByCurrency),
		"settled":        currencyStatMap(stats.SettledByCurrency),
		"this_month":     currencyStatMap(stats.ThisMonthByCurrency),
	})
}

// merchantSettlementStatusName renders MerchantSettlementStatus like the C#
// enum's ToString() under the snake_case dictionary key policy.
func merchantSettlementStatusName(status int) string {
	switch status {
	case SettlementPending:
		return "pending"
	case SettlementSettled:
		return "settled"
	case SettlementCancelled:
		return "cancelled"
	default:
		return strconv.Itoa(status)
	}
}

type periodSettlement struct {
	ID        string          `json:"id"`
	OrderID   *string         `json:"order_id"`
	AwardID   *string         `json:"award_id"`
	Currency  string          `json:"currency"`
	Amount    decimal.Decimal `json:"amount"`
	Status    int             `json:"status"`
	CreatedAt *model.Time     `json:"created_at"`
	SettledAt *model.Time     `json:"settled_at"`
	SettledBy *int            `json:"settled_by"`
}

// GET /api/merchants/:merchant/stats/incoming
func (c *merchantController) periodIncoming(ctx *gin.Context) {
	merchant, ok := c.resolveMerchant(ctx)
	if !ok {
		return
	}
	if !c.requireMerchantOwner(ctx, merchant, "You do not have access to this merchant.") {
		return
	}
	from, fromSet, err := merchantParseQueryTime(ctx.Query("from"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"from": {err.Error()}}))
		return
	}
	to, toSet, err := merchantParseQueryTime(ctx.Query("to"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"to": {err.Error()}}))
		return
	}
	now := time.Now().UTC()
	if !fromSet {
		from = addMonthsUTC(now, -1)
	}
	if !toSet {
		to = now
	}
	var currency *string
	if raw := strings.TrimSpace(ctx.Query("currency")); raw != "" {
		currency = &raw
	}

	result, err := c.d.Service.PeriodIncoming(ctx.Request.Context(), merchant.ID, from, to, currency)
	if err != nil {
		walletInternalError(ctx)
		return
	}

	byStatus := make(map[string]currencyStatResponse, len(result.ByStatus))
	for status, stat := range result.ByStatus {
		byStatus[merchantSettlementStatusName(status)] = currencyStatResponse{Count: stat.Count, Total: stat.Total}
	}
	settlements := make([]periodSettlement, 0, len(result.Settlements))
	for _, settlement := range result.Settlements {
		settlements = append(settlements, periodSettlement{
			ID:        settlement.ID,
			OrderID:   settlement.OrderID,
			AwardID:   settlement.AwardID,
			Currency:  settlement.Currency,
			Amount:    settlement.Amount,
			Status:    settlement.Status,
			CreatedAt: settlement.CreatedAt,
			SettledAt: settlement.SettledAt,
			SettledBy: settlement.SettledBy,
		})
	}
	ctx.JSON(http.StatusOK, gin.H{
		"from":         result.From,
		"to":           result.To,
		"currency":     result.Currency,
		"total_count":  result.TotalCount,
		"total_amount": result.TotalAmount,
		"by_status":    byStatus,
		"by_currency":  currencyStatMap(result.ByCurrency),
		"settlements":  settlements,
	})
}

type dailyEntry struct {
	Date       string                     `json:"date"`
	Count      int                        `json:"count"`
	Total      decimal.Decimal            `json:"total"`
	ByCurrency map[string]decimal.Decimal `json:"by_currency"`
}

// GET /api/merchants/:merchant/stats/daily
func (c *merchantController) dailyIncoming(ctx *gin.Context) {
	merchant, ok := c.resolveMerchant(ctx)
	if !ok {
		return
	}
	if !c.requireMerchantOwner(ctx, merchant, "You do not have access to this merchant.") {
		return
	}
	from, fromSet, err := merchantParseQueryTime(ctx.Query("from"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"from": {err.Error()}}))
		return
	}
	to, toSet, err := merchantParseQueryTime(ctx.Query("to"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"to": {err.Error()}}))
		return
	}
	now := time.Now().UTC()
	if !fromSet {
		from = addMonthsUTC(now, -1)
	}
	if !toSet {
		to = now
	}
	var currency *string
	if raw := strings.TrimSpace(ctx.Query("currency")); raw != "" {
		currency = &raw
	}

	stats, err := c.d.Service.DailyIncoming(ctx.Request.Context(), merchant.ID, from, to, currency)
	if err != nil {
		walletInternalError(ctx)
		return
	}
	merchantSortDailyStats(stats)
	daily := make([]dailyEntry, 0, len(stats))
	for _, stat := range stats {
		daily = append(daily, dailyEntry{
			Date:       stat.Date,
			Count:      stat.Count,
			Total:      stat.Total,
			ByCurrency: stat.ByCurrency,
		})
	}
	ctx.JSON(http.StatusOK, gin.H{
		"from":     timeValue(from),
		"to":       timeValue(to),
		"currency": currency,
		"daily":    daily,
	})
}

// merchantSortDailyStats orders the per-day buckets ascending like the C# LINQ
// OrderBy(g => g.Key).
func merchantSortDailyStats(stats []DailyStat) {
	for i := 1; i < len(stats); i++ {
		for j := i; j > 0 && stats[j].Date < stats[j-1].Date; j-- {
			stats[j], stats[j-1] = stats[j-1], stats[j]
		}
	}
}

// addMonthsUTC mirrors DateTime.AddMonths: the day-of-month is clamped to the
// target month's last day.
func addMonthsUTC(t time.Time, months int) time.Time {
	t = t.UTC()
	target := time.Date(t.Year(), t.Month()+time.Month(months), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
	lastDay := time.Date(target.Year(), target.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	day := t.Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(target.Year(), target.Month(), day, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}
