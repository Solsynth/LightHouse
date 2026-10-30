package wallet

// Order HTTP routes: the port of Payment/OrderController.cs.
//
// C# source:
//	../DysonNetwork/DysonNetwork.Wallet/Payment/OrderController.cs
//
// Two auth models coexist, exactly as in C#: order creation, status updates,
// metrics and payouts authenticate the calling Develop app with a client
// id/secret pair, while the payer-facing routes use the bearer account.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/go/pkg/models"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/permission"
)

// orderInternalAppIdentifier is SnWalletOrder.InternalAppIdentifier.
const orderInternalAppIdentifier = "internal"

// customAppStatusProduction is CustomAppStatus.Production: the wire enum, not
// the offset Develop proto enum.
const customAppStatusProduction = 2

// ProductStockMode values carried in the product state JSON.
const (
	orderStockUnlimited = iota
	orderStockDaily
	orderStockWeekly
	orderStockMonthly
	orderStockYearly
	orderStockManual
)

// ── request DTOs ───────────────────────────────────────────────────────────

// orderCreateRequest mirrors CreateOrderRequest. Currency/Amount are the
// legacy item-less fields; Items is the line-item variant.
type orderCreateRequest struct {
	Currency          *string            `json:"currency"`
	Amount            *decimal.Decimal   `json:"amount"`
	Remarks           *string            `json:"remarks"`
	ProductIdentifier *string            `json:"product_identifier"`
	Meta              map[string]any     `json:"meta"`
	DurationHours     *int               `json:"duration_hours"`
	PayeeWalletID     *uuid.UUID         `json:"payee_wallet_id"`
	Items             []orderItemRequest `json:"items"`
	ClientID          string             `json:"client_id"`
	ClientSecret      string             `json:"client_secret"`
}

// orderItemRequest mirrors OrderItemRequest; Quantity defaults to 1.
type orderItemRequest struct {
	ProductIdentifier string `json:"product_identifier"`
	Quantity          *int   `json:"quantity"`
}

// orderPayRequest mirrors PayOrderRequest. PinCode has no effect, exactly as
// in C#, where the value is read into an unused local.
type orderPayRequest struct {
	PayerWalletID *uuid.UUID `json:"payer_wallet_id"`
	PinCode       *string    `json:"pin_code"`
}

// orderStatusRequest mirrors UpdateOrderStatusRequest.
type orderStatusRequest struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Status       int    `json:"status"`
}

// orderMetricsRequest mirrors AppOrderMetricsRequest.
type orderMetricsRequest struct {
	ClientID     string      `json:"client_id"`
	ClientSecret string      `json:"client_secret"`
	StartDate    *model.Time `json:"start_date"`
	EndDate      *model.Time `json:"end_date"`
}

// orderPayoutRequest mirrors AppPayoutRequest.
type orderPayoutRequest struct {
	ClientID       string          `json:"client_id"`
	ClientSecret   string          `json:"client_secret"`
	PayeeAccountID uuid.UUID       `json:"payee_account_id"`
	Currency       string          `json:"currency"`
	Amount         decimal.Decimal `json:"amount"`
	Remarks        *string         `json:"remarks"`
}

// orderMetricsResponse mirrors AppOrderMetricsResponse.
type orderMetricsResponse struct {
	AppIdentifier          string                     `json:"app_identifier"`
	TotalOrders            int                        `json:"total_orders"`
	PaidOrders             int                        `json:"paid_orders"`
	UnpaidOrders           int                        `json:"unpaid_orders"`
	FinishedOrders         int                        `json:"finished_orders"`
	CancelledOrders        int                        `json:"cancelled_orders"`
	ExpiredOrders          int                        `json:"expired_orders"`
	TotalIncomingAmount    decimal.Decimal            `json:"total_incoming_amount"`
	PaidIncomingAmount     decimal.Decimal            `json:"paid_incoming_amount"`
	ProductIncomingAmounts map[string]decimal.Decimal `json:"product_incoming_amounts"`
	ProductOrderCounts     map[string]int             `json:"product_order_counts"`
}

// orderMetricRow is the projection the metrics endpoint aggregates.
type orderMetricRow struct {
	Status            int             `gorm:"column:status"`
	Amount            decimal.Decimal `gorm:"column:amount"`
	ProductIdentifier *string         `gorm:"column:product_identifier"`
}

// paymentOrderResponse mirrors PaymentOrderResponse.
type paymentOrderResponse struct {
	ID                string          `json:"id"`
	Status            int             `json:"status"`
	Currency          string          `json:"currency"`
	Remarks           *string         `json:"remarks"`
	AppIdentifier     *string         `json:"app_identifier"`
	ProductIdentifier *string         `json:"product_identifier"`
	Meta              any             `json:"meta"`
	Amount            decimal.Decimal `json:"amount"`
	CreatedAt         *model.Time     `json:"created_at"`
	UpdatedAt         *model.Time     `json:"updated_at"`
	ExpiredAt         *model.Time     `json:"expired_at"`
	PayeeWalletID     *string         `json:"payee_wallet_id"`
	PayerWalletID     *string         `json:"payer_wallet_id"`
	TransactionID     *string         `json:"transaction_id"`
	Items             []OrderItem     `json:"items"`
	App               *orderApp       `json:"app"`
	Developer         *orderDeveloper `json:"developer"`
	IsSystemApp       bool            `json:"is_system_app"`
}

// orderApp is the wire shape of SnCustomApp embedded in an order response.
// Board widgets are [JsonIgnore] in C# and therefore absent here too.
type orderApp struct {
	ID              string                            `json:"id"`
	Slug            string                            `json:"slug"`
	Name            string                            `json:"name"`
	Description     *string                           `json:"description"`
	Status          int                               `json:"status"`
	Picture         *model.SnCloudFileReferenceObject `json:"picture"`
	Background      *model.SnCloudFileReferenceObject `json:"background"`
	Verification    *model.SnVerificationMark         `json:"verification"`
	OauthConfig     *orderAppOauthConfig              `json:"oauth_config"`
	Links           *orderAppLinks                    `json:"links"`
	PaymentWalletID *string                           `json:"payment_wallet_id"`
	ProjectID       string                            `json:"project_id"`
	CreatedAt       *model.Time                       `json:"created_at"`
	UpdatedAt       *model.Time                       `json:"updated_at"`
	DeletedAt       *model.Time                       `json:"deleted_at"`
}

// orderAppLinks mirrors SnCustomAppLinks.
type orderAppLinks struct {
	HomePage       *string `json:"home_page"`
	PrivacyPolicy  *string `json:"privacy_policy"`
	TermsOfService *string `json:"terms_of_service"`
}

// orderAppOauthConfig mirrors SnCustomAppOauthConfig.
type orderAppOauthConfig struct {
	ClientURI              *string  `json:"client_uri"`
	RedirectURIs           []string `json:"redirect_uris"`
	PostLogoutRedirectURIs []string `json:"post_logout_redirect_uris"`
	AllowedScopes          []string `json:"allowed_scopes"`
	AllowedGrantTypes      []string `json:"allowed_grant_types"`
	RequirePkce            bool     `json:"require_pkce"`
	AllowOfflineAccess     bool     `json:"allow_offline_access"`
	IsPublicClient         bool     `json:"is_public_client"`
}

// orderDeveloper mirrors AppDeveloperResponse.
type orderDeveloper struct {
	ID            string  `json:"id"`
	PublisherID   string  `json:"publisher_id"`
	PublisherName *string `json:"publisher_name"`
}

// ── routes ─────────────────────────────────────────────────────────────────

// registerOrderHTTP mounts /api/orders.
func registerOrderHTTP(api *gin.RouterGroup, d *Deps) {
	orders := api.Group("/orders")
	orders.POST("", func(c *gin.Context) { createOrderHTTP(c, d) })
	orders.GET("/mine", middleware.RequireAuth(), func(c *gin.Context) { listMyOrdersHTTP(c, d) })
	orders.GET("/mine/:id", middleware.RequireAuth(), func(c *gin.Context) { getMyOrderHTTP(c, d) })
	orders.GET("/:id", func(c *gin.Context) { getOrderHTTP(c, d) })
	orders.POST("/:id/pay", middleware.RequireAuth(), func(c *gin.Context) { payOrderHTTP(c, d) })
	orders.PATCH("/:id/status", func(c *gin.Context) { updateOrderStatusHTTP(c, d) })
	orders.POST("/metrics", func(c *gin.Context) { orderMetricsHTTP(c, d) })
	orders.POST("/payouts", func(c *gin.Context) { orderPayoutsHTTP(c, d) })
}

// ── POST /api/orders ───────────────────────────────────────────────────────

// createOrderHTTP mirrors CreateOrder: the caller is a Develop app, which
// names the payee wallet (explicit, or the merchant payout wallet).
func createOrderHTTP(c *gin.Context, d *Deps) {
	svc := d.Service
	ctx := c.Request.Context()

	var req orderCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Invalid request body."}}))
		return
	}

	client := orderValidateClient(ctx, d, req.ClientID, req.ClientSecret)
	if client == nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_ORDER_INVALID_CLIENT", "Invalid client credentials.", http.StatusBadRequest))
		return
	}
	appIdentifier := "developer.app:" + client.GetId()

	var payeeWalletID *uuid.UUID
	if req.PayeeWalletID != nil {
		var count int64
		if err := svc.DB.WithContext(ctx).Model(&WalletEntity{}).Where("id = ?", *req.PayeeWalletID).Count(&count).Error; err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load payee wallet.", http.StatusInternalServerError))
			return
		}
		if count == 0 {
			c.JSON(http.StatusBadRequest, errs.New("WALLET_NOT_FOUND", "Payee wallet was not found.", http.StatusBadRequest))
			return
		}
		payeeWalletID = req.PayeeWalletID
	} else {
		payeeWalletID = orderMerchantWalletID(ctx, d, client)
		if payeeWalletID == nil {
			c.JSON(http.StatusBadRequest, errs.New("WALLET_MERCHANT_NO_PAYOUT_WALLET", "Merchant payout wallet was not configured.", http.StatusBadRequest))
			return
		}
	}

	expiration := time.Duration(24) * time.Hour
	if req.DurationHours != nil {
		expiration = time.Duration(*req.DurationHours) * time.Hour
	}

	if len(req.Items) > 0 {
		orderCreateWithItemsHTTP(c, svc, d, &req, client, appIdentifier, payeeWalletID, expiration)
		return
	}

	// Legacy: order without line items.
	if req.Currency == nil || req.Amount == nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
			"currency": {"The Currency field is required."},
			"amount":   {"The Amount field is required."},
		}))
		return
	}
	order, err := svc.CreateOrder(ctx, OrderInput{
		PayeeWalletID:     payeeWalletID,
		Currency:          *req.Currency,
		Amount:            *req.Amount,
		Expiration:        &expiration,
		AppIdentifier:     &appIdentifier,
		ProductIdentifier: req.ProductIdentifier,
		Remarks:           req.Remarks,
		Meta:              req.Meta,
		Reuseable:         true,
	})
	if err != nil {
		svc.Log.Error("creating order failed", "app", appIdentifier, "error", err)
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to create order.", http.StatusInternalServerError))
		return
	}
	items, err := orderLoadItems(ctx, svc, []uuid.UUID{order.ID})
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load order items.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, orderModel(order, items[order.ID]))
}

// orderCreateWithItemsHTTP handles the product line-item order variant.
func orderCreateWithItemsHTTP(c *gin.Context, svc *Service, d *Deps, req *orderCreateRequest, client *gen.DyCustomApp, appIdentifier string, payeeWalletID *uuid.UUID, expiration time.Duration) {
	ctx := c.Request.Context()
	if err := svc.ExpireOrders(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to expire overdue orders.", http.StatusInternalServerError))
		return
	}

	items := make([]OrderItemInput, 0, len(req.Items))
	products := make([]orderProduct, 0, len(req.Items))
	totalAmount := decimal.Zero
	var currency string

	for _, item := range req.Items {
		quantity := 1
		if item.Quantity != nil {
			quantity = *item.Quantity
		}
		if quantity <= 0 {
			c.JSON(http.StatusBadRequest, errs.New("WALLET_ORDER_INVALID_QUANTITY", "Item quantity must be greater than zero.", http.StatusBadRequest))
			return
		}

		resp, err := d.Clients.Develop.GetAppProduct(ctx, &gen.DyGetAppProductRequest{
			AppId:      client.GetId(),
			Identifier: item.ProductIdentifier,
		})
		if err != nil || resp.GetProduct() == nil {
			c.JSON(http.StatusBadRequest, errs.New("WALLET_ORDER_PRODUCT_NOT_FOUND",
				fmt.Sprintf("Product '%s' not found for this app.", item.ProductIdentifier), http.StatusBadRequest))
			return
		}
		product, err := orderProductFromProto(resp.GetProduct())
		if err != nil {
			svc.Log.Error("reading product failed", "product", item.ProductIdentifier, "error", err)
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to read product.", http.StatusInternalServerError))
			return
		}
		if !product.enabled {
			c.JSON(http.StatusBadRequest, errs.New("WALLET_ORDER_PRODUCT_NOT_SELLING",
				fmt.Sprintf("Product '%s' is not selling right now.", item.ProductIdentifier), http.StatusBadRequest))
			return
		}
		if currency == "" {
			currency = product.currency
		} else if currency != product.currency {
			c.JSON(http.StatusBadRequest, errs.New("WALLET_ORDER_CURRENCY_MISMATCH", "All items must use the same currency.", http.StatusBadRequest))
			return
		}

		stockError, err := svc.orderStockError(ctx, appIdentifier, item.ProductIdentifier, product, quantity)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to validate product stock.", http.StatusInternalServerError))
			return
		}
		if stockError != "" {
			c.JSON(http.StatusBadRequest, errs.New("WALLET_ORDER_STOCK_ERROR", stockError, http.StatusBadRequest))
			return
		}

		items = append(items, OrderItemInput{
			ProductIdentifier: item.ProductIdentifier,
			Quantity:          quantity,
			UnitPrice:         product.price,
			Currency:          product.currency,
		})
		products = append(products, product)
		totalAmount = totalAmount.Add(product.price.Mul(decimal.NewFromInt(int64(quantity))))
	}

	meta, err := orderBuildFulfillmentMeta(req.Meta, products)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_ORDER_FULFILLMENT_META_FAILED", err.Error(), http.StatusBadRequest))
		return
	}

	// Single-currency order: the recurring metadata follows the first product.
	first := products[0]
	if first.recurrence != "" {
		cycleDays := 30
		switch first.recurrence {
		case "weekly":
			cycleDays = 7
		case "monthly":
			cycleDays = 30
		case "yearly":
			cycleDays = 365
		}
		groupIdentifier := ""
		if first.groupIdentifier != nil {
			groupIdentifier = *first.groupIdentifier
		}
		meta["app_subscription"] = map[string]any{
			"identifier":          first.identifier,
			"display_name":        first.displayName,
			"currency":            first.currency,
			"base_price":          first.price.String(),
			"group_identifier":    groupIdentifier,
			"cycle_duration_days": cycleDays,
			"app_identifier":      appIdentifier,
			"payment_method":      PaymentInAppWallet,
		}
	}

	order, err := svc.CreateOrder(ctx, OrderInput{
		PayeeWalletID: payeeWalletID,
		Currency:      currency,
		Amount:        totalAmount,
		Expiration:    &expiration,
		AppIdentifier: &appIdentifier,
		Remarks:       req.Remarks,
		Meta:          meta,
		Reuseable:     true,
		Items:         items,
	})
	if err != nil {
		svc.Log.Error("creating itemised order failed", "app", appIdentifier, "error", err)
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to create order.", http.StatusInternalServerError))
		return
	}
	rows, err := orderLoadItems(ctx, svc, []uuid.UUID{order.ID})
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load order items.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, orderModel(order, rows[order.ID]))
}

// ── GET /api/orders/mine ───────────────────────────────────────────────────

// listMyOrdersHTTP mirrors ListMyOrders: the caller's orders, matched by the
// order's payer wallet or, for orders still unclaimed, the paying wallet of
// its transaction.
func listMyOrdersHTTP(c *gin.Context, d *Deps) {
	svc := d.Service
	ctx := c.Request.Context()
	accountID, ok := orderCurrentAccount(c)
	if !ok {
		return
	}

	offset, take, ok := orderPagination(c)
	if !ok {
		return
	}
	var status *int
	if raw := c.Query("status"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"status": {"The value is not a valid order status."}}))
			return
		}
		status = &value
	}
	appIdentifier := orderQueryValue(c, "appIdentifier")
	productIdentifier := orderQueryValue(c, "productIdentifier")

	if err := svc.ExpireOrders(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to expire overdue orders.", http.StatusInternalServerError))
		return
	}
	walletIDs, err := orderAccountWalletIDs(ctx, svc, accountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load wallets.", http.StatusInternalServerError))
		return
	}
	if len(walletIDs) == 0 {
		c.JSON(http.StatusOK, make([]Order, 0))
		return
	}

	orders := func() *gorm.DB {
		query := orderPayerQuery(ctx, svc, walletIDs)
		if status != nil {
			query = query.Where("status = ?", *status)
		}
		if appIdentifier != nil {
			query = query.Where("app_identifier = ?", *appIdentifier)
		}
		if productIdentifier != nil {
			query = query.Where("product_identifier = ?", *productIdentifier)
		}
		return query
	}

	var total int64
	if err := orders().Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to count orders.", http.StatusInternalServerError))
		return
	}
	var rows []WalletOrderEntity
	if err := orders().Order("created_at DESC").Offset(offset).Limit(take).Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load orders.", http.StatusInternalServerError))
		return
	}

	c.Header("X-Total", strconv.FormatInt(total, 10))
	ordersWire, err := orderWireList(ctx, svc, rows)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load order details.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, ordersWire)
}

// ── GET /api/orders/mine/{id} ──────────────────────────────────────────────

// getMyOrderHTTP mirrors GetMyOrder.
func getMyOrderHTTP(c *gin.Context, d *Deps) {
	svc := d.Service
	ctx := c.Request.Context()
	accountID, ok := orderCurrentAccount(c)
	if !ok {
		return
	}
	orderID, ok := orderPathID(c)
	if !ok {
		c.JSON(http.StatusNotFound, errs.New("WALLET_ORDER_NOT_FOUND", "Order was not found.", http.StatusNotFound))
		return
	}
	walletIDs, err := orderAccountWalletIDs(ctx, svc, accountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load wallets.", http.StatusInternalServerError))
		return
	}

	var rows []WalletOrderEntity
	if len(walletIDs) > 0 {
		if err := orderPayerQuery(ctx, svc, walletIDs).Where("id = ?", orderID).Limit(1).Find(&rows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load order.", http.StatusInternalServerError))
			return
		}
	}
	if len(rows) == 0 {
		c.JSON(http.StatusNotFound, errs.New("WALLET_ORDER_NOT_FOUND", "Order was not found.", http.StatusNotFound))
		return
	}
	wire, err := orderWireList(ctx, svc, rows)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load order details.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, wire[0])
}

// ── GET /api/orders/{id} ───────────────────────────────────────────────────

// getOrderHTTP mirrors GetOrderById: a public read that enriches the order
// with the owning Develop app.
func getOrderHTTP(c *gin.Context, d *Deps) {
	svc := d.Service
	ctx := c.Request.Context()
	if err := svc.ExpireOrders(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to expire overdue orders.", http.StatusInternalServerError))
		return
	}
	orderID, ok := orderPathID(c)
	if !ok {
		c.JSON(http.StatusNotFound, errs.New("WALLET_ORDER_NOT_FOUND", "Order was not found.", http.StatusNotFound))
		return
	}
	var order WalletOrderEntity
	if err := svc.DB.WithContext(ctx).First(&order, "id = ?", orderID).Error; err != nil {
		c.JSON(http.StatusNotFound, errs.New("WALLET_ORDER_NOT_FOUND", "Order was not found.", http.StatusNotFound))
		return
	}
	itemsByOrder, err := orderLoadItems(ctx, svc, []uuid.UUID{order.ID})
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load order items.", http.StatusInternalServerError))
		return
	}

	response := paymentOrderResponse{
		ID:                uuidString(order.ID),
		Status:            order.Status,
		Currency:          order.Currency,
		Remarks:           order.Remarks,
		AppIdentifier:     order.AppIdentifier,
		ProductIdentifier: order.ProductIdentifier,
		Meta:              jsonValue(order.Meta),
		Amount:            order.Amount,
		CreatedAt:         timeValue(order.CreatedAt),
		UpdatedAt:         timeValue(order.UpdatedAt),
		ExpiredAt:         timeValue(order.ExpiredAt),
		PayeeWalletID:     uuidPointer(order.PayeeWalletID),
		PayerWalletID:     uuidPointer(order.PayerWalletID),
		TransactionID:     uuidPointer(order.TransactionID),
		Items:             make([]OrderItem, 0, len(itemsByOrder[order.ID])),
	}
	for i := range itemsByOrder[order.ID] {
		response.Items = append(response.Items, orderItemModel(&itemsByOrder[order.ID][i]))
	}

	if order.AppIdentifier != nil && *order.AppIdentifier == orderInternalAppIdentifier {
		slug := orderInternalAppIdentifier
		name := "Solar Network"
		description := "Built-in platform services"
		response.App = &orderApp{
			ID:          uuid.Nil.String(),
			Slug:        slug,
			Name:        name,
			Description: &description,
			Status:      customAppStatusProduction,
			ProjectID:   uuid.Nil.String(),
			CreatedAt:   timeValue(order.CreatedAt),
			UpdatedAt:   timeValue(order.UpdatedAt),
		}
		response.IsSystemApp = true
	} else if order.AppIdentifier != nil && strings.TrimSpace(*order.AppIdentifier) != "" {
		if appID, err := uuid.Parse(strings.TrimPrefix(*order.AppIdentifier, "developer.app:")); err == nil {
			// The app or developer may have been removed; skip enrichment then.
			if d.Clients != nil && d.Clients.Develop != nil {
				devResp, err := d.Clients.Develop.GetAppDeveloper(ctx, &gen.DyGetAppDeveloperRequest{AppId: appID.String()})
				if err == nil {
					if devResp.GetApp() != nil {
						app := orderAppFromProto(devResp.GetApp())
						response.App = &app
					}
					if devResp.GetDeveloper() != nil {
						developer := orderDeveloperFromProto(devResp.GetDeveloper())
						response.Developer = &developer
					}
				}
			}
		}
	}

	c.JSON(http.StatusOK, response)
}

// ── POST /api/orders/{id}/pay ──────────────────────────────────────────────

// payOrderHTTP mirrors PayOrder. The caller pays from their primary wallet
// unless they name another wallet they may use.
func payOrderHTTP(c *gin.Context, d *Deps) {
	svc := d.Service
	ctx := c.Request.Context()
	accountID, ok := orderCurrentAccount(c)
	if !ok {
		return
	}
	if !orderRequirePermission(c, d, permission.OrdersPay) {
		return
	}
	var req orderPayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Invalid request body."}}))
		return
	}

	wallet, apiErr := orderPayerWallet(ctx, svc, accountID, middleware.CurrentUser(ctx).IsSuperuser, req.PayerWalletID)
	if apiErr != nil {
		c.JSON(apiErr.Status, apiErr)
		return
	}
	orderID, ok := orderPathID(c)
	if !ok {
		c.JSON(http.StatusNotFound, errs.New("WALLET_ORDER_NOT_FOUND", "Order was not found.", http.StatusNotFound))
		return
	}
	var order WalletOrderEntity
	if err := svc.DB.WithContext(ctx).First(&order, "id = ?", orderID).Error; err != nil {
		c.JSON(http.StatusNotFound, errs.New("WALLET_ORDER_NOT_FOUND", "Order was not found.", http.StatusNotFound))
		return
	}

	// The two conditions below are PaymentService.PayOrderAsync guards; they
	// are checked here so the C# messages reach the client verbatim.
	if order.PayerWalletID != nil && *order.PayerWalletID != wallet.ID {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_ORDER_PAY_FAILED", "Order is reserved for a different wallet.", http.StatusBadRequest))
		return
	}
	if order.Status == OrderPaid && wallet.AccountID == nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_ORDER_PAY_FAILED", "Payer wallet is not account-owned.", http.StatusBadRequest))
		return
	}
	if order.Status != OrderUnpaid && order.Status != OrderPaid {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_ORDER_PAY_FAILED",
			"Order is in invalid status: "+orderStatusName(order.Status), http.StatusBadRequest))
		return
	}

	if err := svc.orderValidateFulfillment(ctx, accountID, &order); err != nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_ORDER_PAY_FAILED", err.Error(), http.StatusBadRequest))
		return
	}
	if _, err := svc.PayOrder(ctx, orderID, wallet); err != nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_ORDER_PAY_FAILED", orderPayError(err), http.StatusBadRequest))
		return
	}

	var paid WalletOrderEntity
	if err := svc.DB.WithContext(ctx).First(&paid, "id = ?", orderID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load order.", http.StatusInternalServerError))
		return
	}
	response := orderModel(&paid, nil)
	if paid.TransactionID != nil {
		var tx WalletTransactionEntity
		if err := svc.DB.WithContext(ctx).First(&tx, "id = ?", *paid.TransactionID).Error; err == nil {
			txWire := transactionModel(&tx)
			response.Transaction = &txWire
		}
	}
	// PayOrderAsync includes the payee wallet in its response.
	if paid.PayeeWalletID != nil {
		if payee, pockets, err := svc.GetWallet(ctx, *paid.PayeeWalletID); err == nil {
			payeeWire := walletModel(payee, pockets)
			response.PayeeWallet = &payeeWire
		}
	}
	c.JSON(http.StatusOK, response)
}

// ── PATCH /api/orders/{id}/status ──────────────────────────────────────────

// updateOrderStatusHTTP mirrors UpdateOrderStatus: an app may only move its
// own order to Finished or Cancelled, and cancelling a paid order refunds the
// payer and drops the pending settlements.
func updateOrderStatusHTTP(c *gin.Context, d *Deps) {
	svc := d.Service
	ctx := c.Request.Context()

	var req orderStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Invalid request body."}}))
		return
	}
	client := orderValidateClient(ctx, d, req.ClientID, req.ClientSecret)
	if client == nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_ORDER_INVALID_CLIENT", "Invalid client credentials.", http.StatusBadRequest))
		return
	}
	orderID, ok := orderPathID(c)
	if !ok {
		c.JSON(http.StatusNotFound, errs.New("WALLET_ORDER_NOT_FOUND", "Order was not found.", http.StatusNotFound))
		return
	}
	var order WalletOrderEntity
	if err := svc.DB.WithContext(ctx).First(&order, "id = ?", orderID).Error; err != nil {
		c.JSON(http.StatusNotFound, errs.New("WALLET_ORDER_NOT_FOUND", "Order was not found.", http.StatusNotFound))
		return
	}
	if order.AppIdentifier == nil || *order.AppIdentifier != "developer.app:"+client.GetId() {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_ORDER_CLIENT_MISMATCH", "Order does not belong to this client.", http.StatusBadRequest))
		return
	}
	if req.Status != OrderFinished && req.Status != OrderCancelled {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_ORDER_INVALID_STATUS",
			"Invalid status. Available statuses are Finished, Cancelled.", http.StatusBadRequest))
		return
	}

	if req.Status == OrderCancelled {
		if err := svc.CancelSettlementsByOrder(ctx, order.ID); err != nil {
			svc.Log.Error("cancelling settlements failed", "order", order.ID, "error", err)
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to cancel settlements.", http.StatusInternalServerError))
			return
		}
		// A paid order is refunded from the wallet that received it; an
		// escrowed payment returns from the system (the payee was never
		// credited).
		if order.Status == OrderPaid && order.TransactionID != nil {
			var original WalletTransactionEntity
			if err := svc.DB.WithContext(ctx).First(&original, "id = ?", *order.TransactionID).Error; err == nil && original.PayerWalletID != nil {
				remarks := fmt.Sprintf("Refund for cancelled order #%s", order.ID)
				if _, err := svc.CreateTransaction(ctx, TransactionInput{
					PayerWalletID: original.PayeeWalletID,
					PayeeWalletID: original.PayerWalletID,
					Currency:      order.Currency,
					Amount:        order.Amount,
					Remarks:       &remarks,
					Type:          TransactionSystem,
					Silent:        true,
				}); err != nil {
					svc.Log.Error("refunding cancelled order failed", "order", order.ID, "error", err)
					c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to refund the cancelled order.", http.StatusInternalServerError))
					return
				}
			}
		}
	}

	now := time.Now().UTC()
	if err := svc.DB.WithContext(ctx).Model(&WalletOrderEntity{}).Where("id = ?", order.ID).
		Updates(map[string]any{"status": req.Status, "updated_at": now}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to update order.", http.StatusInternalServerError))
		return
	}
	order.Status = req.Status
	order.UpdatedAt = now
	c.JSON(http.StatusOK, orderModel(&order, nil))
}

// ── POST /api/orders/metrics ───────────────────────────────────────────────

// orderMetricsHTTP mirrors GetAppOrderMetrics.
func orderMetricsHTTP(c *gin.Context, d *Deps) {
	svc := d.Service
	ctx := c.Request.Context()

	var req orderMetricsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Invalid request body."}}))
		return
	}
	client := orderValidateClient(ctx, d, req.ClientID, req.ClientSecret)
	if client == nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_ORDER_INVALID_CLIENT", "Invalid client credentials.", http.StatusBadRequest))
		return
	}
	appIdentifier := "developer.app:" + client.GetId()

	query := func() *gorm.DB {
		q := svc.DB.WithContext(ctx).Model(&WalletOrderEntity{}).Where("app_identifier = ?", appIdentifier)
		if req.StartDate != nil {
			q = q.Where("created_at >= ?", req.StartDate.Time())
		}
		if req.EndDate != nil {
			q = q.Where("created_at <= ?", req.EndDate.Time())
		}
		return q
	}
	var rows []orderMetricRow
	if err := query().Select("status, amount, product_identifier").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load orders.", http.StatusInternalServerError))
		return
	}

	response := orderMetricsResponse{
		AppIdentifier:          appIdentifier,
		TotalOrders:            len(rows),
		ProductIncomingAmounts: make(map[string]decimal.Decimal),
		ProductOrderCounts:     make(map[string]int),
	}
	for _, row := range rows {
		switch row.Status {
		case OrderPaid:
			response.PaidOrders++
		case OrderUnpaid:
			response.UnpaidOrders++
		case OrderFinished:
			response.FinishedOrders++
		case OrderCancelled:
			response.CancelledOrders++
		case OrderExpired:
			response.ExpiredOrders++
		}
		response.TotalIncomingAmount = response.TotalIncomingAmount.Add(row.Amount)
		if row.Status == OrderPaid || row.Status == OrderFinished {
			response.PaidIncomingAmount = response.PaidIncomingAmount.Add(row.Amount)
		}
		key := ""
		if row.ProductIdentifier != nil {
			key = *row.ProductIdentifier
		}
		response.ProductIncomingAmounts[key] = response.ProductIncomingAmounts[key].Add(row.Amount)
		response.ProductOrderCounts[key]++
	}

	c.JSON(http.StatusOK, response)
}

// ── POST /api/orders/payouts ───────────────────────────────────────────────

// orderPayoutsHTTP mirrors CreateAppPayout: the app's merchant wallet pays an
// account's wallet.
func orderPayoutsHTTP(c *gin.Context, d *Deps) {
	svc := d.Service
	ctx := c.Request.Context()

	var req orderPayoutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Invalid request body."}}))
		return
	}
	client := orderValidateClient(ctx, d, req.ClientID, req.ClientSecret)
	if client == nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_ORDER_INVALID_CLIENT", "Invalid client credentials.", http.StatusBadRequest))
		return
	}
	merchantWalletID := orderMerchantWalletID(ctx, d, client)
	if merchantWalletID == nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_MERCHANT_NO_PAYOUT_WALLET", "Merchant payout wallet was not configured.", http.StatusBadRequest))
		return
	}

	var payerWallet WalletEntity
	if err := svc.DB.WithContext(ctx).First(&payerWallet, "id = ?", *merchantWalletID).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the merchant payout wallet.", http.StatusInternalServerError))
			return
		}
		c.JSON(http.StatusBadRequest, errs.New("WALLET_NOT_FOUND", "Merchant payout wallet was not found.", http.StatusBadRequest))
		return
	}
	if payerWallet.AccountID != nil && *payerWallet.AccountID == req.PayeeAccountID {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_PAYOUT_SELF_FORBIDDEN", "Cannot pay the payout wallet owner.", http.StatusBadRequest))
		return
	}

	var payeeWallet WalletEntity
	if err := svc.DB.WithContext(ctx).Where("account_id = ?", req.PayeeAccountID).First(&payeeWallet).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusBadRequest, errs.New("WALLET_PAYOUT_FAILED", "Failed to load the payee wallet.", http.StatusBadRequest))
			return
		}
		c.JSON(http.StatusBadRequest, errs.New("WALLET_NOT_FOUND", "Payee wallet was not found.", http.StatusBadRequest))
		return
	}

	transaction, err := svc.CreateTransaction(ctx, TransactionInput{
		PayerWalletID: &payerWallet.ID,
		PayeeWalletID: &payeeWallet.ID,
		Currency:      req.Currency,
		Amount:        req.Amount,
		Remarks:       req.Remarks,
		Type:          TransactionTransfer,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_PAYOUT_FAILED", orderTransactionMessage(err), http.StatusBadRequest))
		return
	}
	c.JSON(http.StatusOK, transactionModel(transaction))
}

// ── request helpers ────────────────────────────────────────────────────────

// orderCurrentAccount resolves the authenticated account, answering 401 when
// it is absent.
func orderCurrentAccount(c *gin.Context) (uuid.UUID, bool) {
	user := middleware.CurrentUser(c.Request.Context())
	if user == nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return uuid.Nil, false
	}
	accountID, err := uuid.Parse(user.Id)
	if err != nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return uuid.Nil, false
	}
	return accountID, true
}

// orderPathID parses the {id:guid} route value; the C# route constraint turns
// a malformed id into a 404 before the action runs.
func orderPathID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// orderRequirePermission mirrors [AskPermission]: superusers bypass the check,
// which is where the C# middleware short-circuits before calling Padlock.
func orderRequirePermission(c *gin.Context, d *Deps, key string) bool {
	ctx := c.Request.Context()
	user := middleware.CurrentUser(ctx)
	if user != nil && user.IsSuperuser {
		return true
	}
	if user != nil && d.Perm != nil {
		if accountID, err := uuid.Parse(user.Id); err == nil {
			if allowed, err := d.Perm.HasPermission(ctx, accountID, key); err == nil && allowed {
				return true
			}
		}
	}
	c.JSON(http.StatusForbidden, errs.Forbidden("Permission "+key+" was required."))
	return false
}

// orderPagination reads the C# offset/take query pair and applies its clamp.
func orderPagination(c *gin.Context) (int, int, bool) {
	offset := 0
	if raw := c.Query("offset"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"offset": {"The value is not a valid integer."}}))
			return 0, 0, false
		}
		offset = value
	}
	if offset < 0 {
		offset = 0
	}
	take := 50
	if raw := c.Query("take"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"take": {"The value is not a valid integer."}}))
			return 0, 0, false
		}
		take = value
	}
	if take < 1 {
		take = 1
	}
	if take > 200 {
		take = 200
	}
	return offset, take, true
}

// orderQueryValue reads a query parameter and applies the C# IsNullOrWhiteSpace
// guard: blank means "no filter". ASP.NET binds query names case-insensitively,
// so the lower-case spelling is accepted too.
func orderQueryValue(c *gin.Context, name string) *string {
	value := c.Query(name)
	if strings.TrimSpace(value) == "" {
		value = c.Query(strings.ToLower(name))
	}
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

// orderAccountWalletIDs lists the wallets the account owns.
func orderAccountWalletIDs(ctx context.Context, svc *Service, accountID uuid.UUID) ([]uuid.UUID, error) {
	wallets, err := svc.GetAccountWallets(ctx, accountID)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(wallets))
	for _, wallet := range wallets {
		ids = append(ids, wallet.ID)
	}
	return ids, nil
}

// orderPayerQuery selects the orders the given wallets paid for: the order's
// payer wallet, or the paying wallet of its transaction when the order itself
// is still unclaimed.
func orderPayerQuery(ctx context.Context, svc *Service, walletIDs []uuid.UUID) *gorm.DB {
	transactionIDs := svc.DB.WithContext(ctx).Model(&WalletTransactionEntity{}).
		Select("id").
		Where("payer_wallet_id IN ?", walletIDs)
	return svc.DB.WithContext(ctx).Model(&WalletOrderEntity{}).
		Where("(payer_wallet_id IN ? OR transaction_id IN (?))", walletIDs, transactionIDs)
}

// orderLoadItems groups an order set's line items by order id.
func orderLoadItems(ctx context.Context, svc *Service, orderIDs []uuid.UUID) (map[uuid.UUID][]WalletOrderItemEntity, error) {
	grouped := make(map[uuid.UUID][]WalletOrderItemEntity, len(orderIDs))
	if len(orderIDs) == 0 {
		return grouped, nil
	}
	var rows []WalletOrderItemEntity
	if err := svc.DB.WithContext(ctx).Where("order_id IN ?", orderIDs).Order("created_at ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		grouped[row.OrderID] = append(grouped[row.OrderID], row)
	}
	return grouped, nil
}

// orderLoadTransactions loads transactions by id.
func orderLoadTransactions(ctx context.Context, svc *Service, ids []uuid.UUID) (map[uuid.UUID]WalletTransactionEntity, error) {
	loaded := make(map[uuid.UUID]WalletTransactionEntity, len(ids))
	if len(ids) == 0 {
		return loaded, nil
	}
	var rows []WalletTransactionEntity
	if err := svc.DB.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		loaded[row.ID] = row
	}
	return loaded, nil
}

// orderWireList renders orders as SnWalletOrder wire models, with their items
// and — as ListMyOrders includes it — their transaction.
func orderWireList(ctx context.Context, svc *Service, rows []WalletOrderEntity) ([]Order, error) {
	out := make([]Order, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	orderIDs := make([]uuid.UUID, 0, len(rows))
	transactionIDs := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		orderIDs = append(orderIDs, row.ID)
		if row.TransactionID != nil {
			transactionIDs = append(transactionIDs, *row.TransactionID)
		}
	}
	itemsByOrder, err := orderLoadItems(ctx, svc, orderIDs)
	if err != nil {
		return nil, err
	}
	transactions, err := orderLoadTransactions(ctx, svc, transactionIDs)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		order := orderModel(&rows[i], itemsByOrder[rows[i].ID])
		if rows[i].TransactionID != nil {
			if transaction, ok := transactions[*rows[i].TransactionID]; ok {
				txWire := transactionModel(&transaction)
				order.Transaction = &txWire
			}
		}
		out = append(out, order)
	}
	return out, nil
}

// ── Develop client helpers ─────────────────────────────────────────────────

// orderValidateClient mirrors ValidateClientAsync: resolve the app by slug and
// check the presented secret. Every failure — including an unreachable Develop
// service — reads as invalid credentials, matching the C# catch.
func orderValidateClient(ctx context.Context, d *Deps, clientID, clientSecret string) *gen.DyCustomApp {
	if d.Clients == nil || d.Clients.Develop == nil {
		return nil
	}
	clients := d.Clients.Develop
	appResp, err := clients.GetCustomApp(ctx, &gen.DyGetCustomAppRequest{
		Query: &gen.DyGetCustomAppRequest_Slug{Slug: clientID},
	})
	if err != nil || appResp.GetApp() == nil {
		return nil
	}
	app := appResp.GetApp()
	secretResp, err := clients.CheckCustomAppSecret(ctx, &gen.DyCheckCustomAppSecretRequest{
		SecretIdentifier: &gen.DyCheckCustomAppSecretRequest_AppId{AppId: app.GetId()},
		Secret:           clientSecret,
	})
	if err != nil || !secretResp.GetValid() {
		return nil
	}
	return app
}

// orderMerchantWalletID mirrors ResolveMerchantWalletIdAsync: the publisher
// behind the app names the merchant whose wallet receives the funds.
func orderMerchantWalletID(ctx context.Context, d *Deps, app *gen.DyCustomApp) *uuid.UUID {
	if d.Clients == nil || d.Clients.Develop == nil {
		return nil
	}
	devResp, err := d.Clients.Develop.GetAppDeveloper(ctx, &gen.DyGetAppDeveloperRequest{AppId: app.GetId()})
	if err != nil {
		return nil
	}
	publisherID, err := uuid.Parse(devResp.GetDeveloper().GetPublisherId())
	if err != nil {
		return nil
	}
	merchant, err := d.Service.GetMerchantByPublisher(ctx, publisherID)
	if err != nil || merchant.PaymentWalletID == nil {
		return nil
	}
	return merchant.PaymentWalletID
}

// orderPayerWallet mirrors ResolveAccessiblePayerWalletAsync. Publisher
// membership has no Stargate port, so a realm wallet is only usable by a
// superuser; every other caller is refused.
func orderPayerWallet(ctx context.Context, svc *Service, accountID uuid.UUID, superuser bool, walletID *uuid.UUID) (*WalletEntity, *errs.ApiError) {
	notFound := errs.New("WALLET_NOT_FOUND", "Wallet was not found.", http.StatusBadRequest)
	if walletID == nil {
		wallet, _, err := svc.GetAccountWallet(ctx, accountID)
		if err != nil {
			if !errors.Is(err, ErrNotFound) && !errors.Is(err, gorm.ErrRecordNotFound) {
				svc.Log.Warn("loading account wallet failed", "account", accountID, "error", err)
			}
			return nil, notFound
		}
		return wallet, nil
	}
	wallet, _, err := svc.GetWallet(ctx, *walletID)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) && !errors.Is(err, ErrNotFound) {
			svc.Log.Warn("loading payer wallet failed", "wallet", *walletID, "error", err)
		}
		return nil, notFound
	}
	if wallet.AccountID != nil && *wallet.AccountID == accountID {
		return wallet, nil
	}
	if wallet.RealmID == nil {
		return nil, notFound
	}
	if superuser {
		return wallet, nil
	}
	return nil, errs.Forbidden("Realm wallet payments require publisher membership.")
}

// ── product helpers ────────────────────────────────────────────────────────

// orderProduct is the slice of SnAppProduct this controller needs.
type orderProduct struct {
	identifier      string
	displayName     string
	currency        string
	price           decimal.Decimal
	groupIdentifier *string
	recurrence      string
	enabled         bool
	stockMode       int
	stockQuantity   *int
	lastRestockedAt *time.Time
	requiresAddress bool
}

// orderProductStateJSON is ProductStateDto as Develop serializes it: the proto
// carries the default JsonSerializer output, so the keys are PascalCase.
type orderProductStateJSON struct {
	IsEnabled                  bool   `json:"IsEnabled"`
	StockMode                  int    `json:"StockMode"`
	StockQuantity              *int   `json:"StockQuantity"`
	LastRestockedAtUnixSeconds *int64 `json:"LastRestockedAtUnixSeconds"`
}

// orderProductFulfillmentJSON is SnAppProductFulfillment.
type orderProductFulfillmentJSON struct {
	IsAddressRequired bool `json:"IsAddressRequired"`
}

// orderProductFromProto mirrors SnAppProduct.FromProto.
func orderProductFromProto(product *gen.DyAppProduct) (orderProduct, error) {
	out := orderProduct{
		identifier:  product.GetIdentifier(),
		displayName: product.GetDisplayName(),
		currency:    product.GetCurrency(),
		recurrence:  product.GetRecurrence(),
	}
	price, err := decimal.NewFromString(product.GetPrice())
	if err != nil {
		return orderProduct{}, fmt.Errorf("product %s has an invalid price: %w", out.identifier, err)
	}
	out.price = price
	if group := product.GetGroupIdentifier(); group != "" {
		out.groupIdentifier = &group
	}
	if raw := product.GetStateJson(); strings.TrimSpace(raw) != "" {
		var state orderProductStateJSON
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			return orderProduct{}, fmt.Errorf("product %s has invalid state: %w", out.identifier, err)
		}
		out.enabled = state.IsEnabled
		out.stockMode = state.StockMode
		out.stockQuantity = state.StockQuantity
		if state.LastRestockedAtUnixSeconds != nil {
			restocked := time.Unix(*state.LastRestockedAtUnixSeconds, 0).UTC()
			out.lastRestockedAt = &restocked
		}
	}
	if raw := product.GetFulfillmentJson(); strings.TrimSpace(raw) != "" {
		var fulfillment orderProductFulfillmentJSON
		if err := json.Unmarshal([]byte(raw), &fulfillment); err == nil {
			out.requiresAddress = fulfillment.IsAddressRequired
		}
	}
	return out, nil
}

// orderStockError mirrors ValidateStockAsync: the returned message is the C#
// text and an empty message means the requested quantity is available.
func (s *Service) orderStockError(ctx context.Context, appIdentifier, productIdentifier string, product orderProduct, quantity int) (string, error) {
	if !product.enabled {
		return fmt.Sprintf("Product '%s' is not selling right now.", product.identifier), nil
	}
	if product.stockMode == orderStockUnlimited {
		return "", nil
	}
	if product.stockQuantity == nil || *product.stockQuantity < 0 {
		return fmt.Sprintf("Product '%s' is missing stock configuration.", product.identifier), nil
	}

	// Expired unpaid orders are already excluded by ExpireOrders; paid and
	// finished orders still consume stock.
	query := s.DB.WithContext(ctx).Table("wallet_order_items AS i").
		Joins("JOIN payment_orders AS o ON o.id = i.order_id").
		Where("i.deleted_at IS NULL AND o.deleted_at IS NULL").
		Where("o.app_identifier = ?", appIdentifier).
		Where("o.status NOT IN ?", []int{OrderCancelled, OrderExpired}).
		Where("i.product_identifier = ?", productIdentifier)
	if windowStart := orderStockWindowStart(product); windowStart != nil {
		query = query.Where("o.created_at >= ?", *windowStart)
	}
	var reserved int64
	if err := query.Select("COALESCE(SUM(i.quantity), 0)").Scan(&reserved).Error; err != nil {
		return "", err
	}
	if int64(*product.stockQuantity)-reserved >= int64(quantity) {
		return "", nil
	}
	return fmt.Sprintf("Product '%s' does not have enough stock.", product.identifier), nil
}

// orderStockWindowStart mirrors GetStockWindowStart.
func orderStockWindowStart(product orderProduct) *time.Time {
	now := time.Now().UTC()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	switch product.stockMode {
	case orderStockDaily:
		return &midnight
	case orderStockWeekly:
		// Microsoft DayOfWeek and Go Weekday both start at Sunday.
		daysSinceMonday := (7 + (int(now.Weekday()) - 1)) % 7
		start := midnight.AddDate(0, 0, -daysSinceMonday)
		return &start
	case orderStockMonthly:
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		return &start
	case orderStockYearly:
		start := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		return &start
	case orderStockManual:
		return product.lastRestockedAt
	}
	return nil
}

// ── fulfillment helpers ────────────────────────────────────────────────────

// orderBuildFulfillmentMeta mirrors BuildFulfillmentMeta: an order that ships
// to the buyer keeps the address contact id and a snapshot of its content.
func orderBuildFulfillmentMeta(requestMeta map[string]any, products []orderProduct) (map[string]any, error) {
	meta := make(map[string]any, len(requestMeta)+1)
	for key, value := range requestMeta {
		meta[key] = value
	}
	requiresAddress := false
	for _, product := range products {
		if product.requiresAddress {
			requiresAddress = true
			break
		}
	}
	if !requiresAddress {
		return meta, nil
	}
	contactID, err := uuid.Parse(orderMetaString(requestMeta, "address_contact_id"))
	if err != nil {
		return nil, errors.New("Address contact id is required for this order.")
	}
	snapshot := orderMetaString(requestMeta, "address_snapshot")
	if strings.TrimSpace(snapshot) == "" {
		return nil, errors.New("Address snapshot is required for this order.")
	}
	meta["fulfillment"] = map[string]any{
		"address_contact_id": contactID.String(),
		"address_snapshot":   snapshot,
		"requires_address":   true,
	}
	return meta, nil
}

// orderMetaString mirrors TryGetString on a decoded jsonb meta object.
func orderMetaString(meta map[string]any, key string) string {
	raw, ok := meta[key]
	if !ok || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	default:
		return fmt.Sprint(value)
	}
}

// orderValidateFulfillment mirrors ValidateOrderFulfillmentContactsAsync: an
// address-bearing order is revalidated against the account's Address contacts
// and its snapshot refreshed.
func (s *Service) orderValidateFulfillment(ctx context.Context, accountID uuid.UUID, order *WalletOrderEntity) error {
	if len(order.Meta) == 0 {
		return nil
	}
	var meta map[string]any
	if err := json.Unmarshal(order.Meta, &meta); err != nil {
		return nil
	}
	raw, ok := meta["fulfillment"]
	if !ok {
		return nil
	}
	var contactIDText string
	switch fulfillment := raw.(type) {
	case map[string]any:
		contactIDText = orderMetaString(fulfillment, "address_contact_id")
	case string:
		var nested map[string]any
		if json.Unmarshal([]byte(fulfillment), &nested) == nil {
			contactIDText = orderMetaString(nested, "address_contact_id")
		}
	}
	contactID, err := uuid.Parse(contactIDText)
	if err != nil {
		return errors.New("Order is missing a valid address contact id.")
	}

	contact := s.orderAddressContact(ctx, accountID, contactID)
	if contact == nil {
		return errors.New("Address contact was not found for the current account.")
	}
	meta["fulfillment"] = map[string]any{
		"address_contact_id": contact.ID.String(),
		"address_snapshot":   contact.Content,
		"requires_address":   true,
	}
	now := time.Now().UTC()
	if err := s.DB.WithContext(ctx).Model(&WalletOrderEntity{}).Where("id = ?", order.ID).
		Updates(map[string]any{"meta": mustJSON(meta), "updated_at": now}).Error; err != nil {
		return err
	}
	order.Meta = mustJSON(meta)
	order.UpdatedAt = now
	return nil
}

// orderAddressContact resolves one of the account's Address contacts. The C#
// service listed them from Padlock over gRPC; Stargate owns account_contacts,
// so the lookup runs against that table instead. A failed lookup reads as "not
// found", which is what the C# registry helper returned when the RPC failed.
func (s *Service) orderAddressContact(ctx context.Context, accountID, contactID uuid.UUID) *orderContactRow {
	var contact orderContactRow
	err := s.DB.WithContext(ctx).Table("account_contacts").
		Select("id, content").
		Where("id = ? AND account_id = ? AND type = ?", contactID, accountID, int(model.ContactTypeAddress)).
		Where("deleted_at IS NULL").
		Take(&contact).Error
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			s.Log.Warn("loading account contacts failed", "account", accountID, "error", err)
		}
		return nil
	}
	return &contact
}

// orderContactRow is one account_contacts row.
type orderContactRow struct {
	ID      uuid.UUID `gorm:"column:id"`
	Content string    `gorm:"column:content"`
}

// ── wire helpers ───────────────────────────────────────────────────────────

// orderAppFromProto mirrors SnCustomApp.FromProtoValue. Board widgets are
// [JsonIgnore] in C# and therefore not part of the wire shape.
func orderAppFromProto(app *gen.DyCustomApp) orderApp {
	out := orderApp{
		ID:              app.GetId(),
		Slug:            app.GetSlug(),
		Name:            app.GetName(),
		Description:     orderOptionalString(app.GetDescription()),
		Status:          orderAppStatus(app.GetStatus()),
		PaymentWalletID: orderOptionalString(app.GetPaymentWalletId()),
		ProjectID:       app.GetProjectId(),
		CreatedAt:       orderProtoTime(app.GetCreatedAt()),
		UpdatedAt:       orderProtoTime(app.GetUpdatedAt()),
	}
	if out.ProjectID == "" {
		out.ProjectID = uuid.Nil.String()
	}
	if app.GetPicture() != nil {
		picture := models.FromProtoValue(app.GetPicture())
		out.Picture = &picture
	}
	if app.GetBackground() != nil {
		background := models.FromProtoValue(app.GetBackground())
		out.Background = &background
	}
	if verification := app.GetVerification(); verification != nil {
		out.Verification = &model.SnVerificationMark{
			Type:        orderVerificationMarkType(verification.GetType()),
			Title:       orderOptionalString(verification.GetTitle()),
			Description: orderOptionalString(verification.GetDescription()),
			VerifiedBy:  orderOptionalString(verification.GetVerifiedBy()),
		}
	}
	if links := app.GetLinks(); links != nil {
		out.Links = &orderAppLinks{
			HomePage:       orderOptionalString(links.GetHomePage()),
			PrivacyPolicy:  orderOptionalString(links.GetPrivacyPolicy()),
			TermsOfService: orderOptionalString(links.GetTermsOfService()),
		}
	}
	if oauth := app.GetOauthConfig(); oauth != nil {
		out.OauthConfig = &orderAppOauthConfig{
			ClientURI:              orderOptionalString(oauth.GetClientUri()),
			RedirectURIs:           oauth.GetRedirectUris(),
			PostLogoutRedirectURIs: oauth.GetPostLogoutRedirectUris(),
			AllowedScopes:          oauth.GetAllowedScopes(),
			AllowedGrantTypes:      oauth.GetAllowedGrantTypes(),
			RequirePkce:            oauth.GetRequirePkce(),
			AllowOfflineAccess:     oauth.GetAllowOfflineAccess(),
			IsPublicClient:         oauth.GetIsPublicClient(),
		}
	}
	return out
}

// orderAppStatus maps the offset Develop proto enum back to CustomAppStatus.
func orderAppStatus(status gen.DyCustomAppStatus) int {
	switch status {
	case gen.DyCustomAppStatus_DY_STAGING:
		return 1
	case gen.DyCustomAppStatus_DY_PRODUCTION:
		return 2
	case gen.DyCustomAppStatus_DY_SUSPENDED:
		return 3
	}
	return 0
}

// orderDeveloperFromProto mirrors AppDeveloperResponse.FromProto.
func orderDeveloperFromProto(developer *gen.DyDeveloper) orderDeveloper {
	return orderDeveloper{
		ID:            developer.GetId(),
		PublisherID:   developer.GetPublisherId(),
		PublisherName: orderOptionalString(developer.GetPublisherName()),
	}
}

// orderVerificationMarkType maps the offset proto enum to VerificationMarkType.
func orderVerificationMarkType(mark gen.DyVerificationMarkType) int {
	switch mark {
	case gen.DyVerificationMarkType_DY_OFFICIAL:
		return 0
	case gen.DyVerificationMarkType_DY_INDIVIDUAL:
		return 1
	case gen.DyVerificationMarkType_DY_ORGANIZATION:
		return 2
	case gen.DyVerificationMarkType_DY_GOVERNMENT:
		return 3
	case gen.DyVerificationMarkType_DY_CREATOR:
		return 4
	case gen.DyVerificationMarkType_DY_DEVELOPER:
		return 5
	case gen.DyVerificationMarkType_DY_PARODY:
		return 6
	}
	return 1
}

// orderProtoTime converts a protobuf timestamp to the wire time.
func orderProtoTime(ts *timestamppb.Timestamp) *model.Time {
	if ts == nil {
		return nil
	}
	return model.NewTime(ts.AsTime())
}

// orderOptionalString keeps "" out of optional wire strings (the C#
// IsNullOrEmpty ? null pattern).
func orderOptionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// orderStatusName renders OrderStatus the way C# interpolates the enum
// (undefined values print as their number).
func orderStatusName(status int) string {
	switch status {
	case OrderUnpaid:
		return "Unpaid"
	case OrderPaid:
		return "Paid"
	case OrderCancelled:
		return "Cancelled"
	case OrderFinished:
		return "Finished"
	case OrderExpired:
		return "Expired"
	}
	return strconv.Itoa(status)
}

// orderPayError renders a pay failure. The C# PaymentService reported the same
// conditions through InvalidOperationException; the Go service uses its own
// wording, so the two texts that reach clients are restored here.
func orderPayError(err error) string {
	switch {
	case errors.Is(err, ErrInsufficientFunds):
		return "Insufficient funds"
	case err.Error() == "order has expired":
		return "Order has expired"
	}
	return err.Error()
}

// orderTransactionMessage renders a transaction failure (C# threw
// InvalidOperationException from CreateTransactionAsync).
func orderTransactionMessage(err error) string {
	if errors.Is(err, ErrInsufficientFunds) {
		return "Insufficient funds"
	}
	return err.Error()
}
