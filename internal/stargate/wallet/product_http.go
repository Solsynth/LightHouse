// Wallet product HTTP surface: the WalletProductController and
// WalletProductAdminController ports.
//
// C# sources:
//
//	../DysonNetwork/DysonNetwork.Wallet/Payment/WalletProductController.cs
//	../DysonNetwork/DysonNetwork.Wallet/Payment/WalletProductAdminController.cs
package wallet

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/permission"
)

// Admin wallet-product permission keys (DysonNetwork.Shared PermissionKeys).
// The shared Go permission registry does not declare the admin.wallets.*
// family yet, so they are spelled out here.
const (
	permAdminWalletsOrdersManage = "admin.wallets.orders.manage"
	permAdminWalletsProductsView = "admin.wallets.products.view"
)

type productController struct {
	d        *Deps
	products *Products
}

// registerProductHTTP mounts /api/wallet-products and /api/admin/wallet-products.
func registerProductHTTP(api *gin.RouterGroup, d *Deps) {
	products := newProductsFromDeps(d)
	c := &productController{d: d, products: products}

	group := api.Group("/wallet-products")
	group.GET("/catalog", c.catalog)

	checkout := api.Group("/wallet-products",
		middleware.RequireAuth(), requireWalletPerm(d, permission.OrdersCreate))
	checkout.POST("/golds-resupply-pack/checkout/paddle", c.paddleCheckout)
	checkout.POST("/golds-resupply-pack/checkout/afdian", c.afdianCheckout)

	admin := api.Group("/admin/wallet-products", middleware.RequireAuth())
	admin.GET("/golds-resupply-pack", requireWalletPerm(d, permAdminWalletsProductsView), c.adminGoldsResupplyPack)
	admin.POST("/orders/:orderId/apply", requireWalletPerm(d, permAdminWalletsOrdersManage), c.applyPaidOrder)
}

// walletProductSummary is WalletProductCatalogItem and
// WalletProductAdminSummary (identical wire shape).
type walletProductSummary struct {
	Key              string           `json:"key"`
	Identifier       string           `json:"identifier"`
	DisplayName      string           `json:"display_name"`
	Currency         string           `json:"currency"`
	ProviderMappings ProviderMappings `json:"provider_mappings"`
}

func walletProductSummaryModel(definition ProductDefinition) walletProductSummary {
	mappings := definition.ProviderMappings
	if mappings == nil {
		mappings = ProviderMappings{}
	}
	return walletProductSummary{
		Key:              GoldsResupplyPackKey,
		Identifier:       definition.Identifier,
		DisplayName:      definition.DisplayName,
		Currency:         definition.Currency,
		ProviderMappings: mappings,
	}
}

// GET /api/wallet-products/catalog
func (c *productController) catalog(ctx *gin.Context) {
	ctx.JSON(http.StatusOK, walletProductSummaryModel(c.products.GoldsResupplyPackDefinition()))
}

type paddleCheckoutResponse struct {
	TransactionID       string          `json:"transaction_id"`
	CheckoutURL         string          `json:"checkout_url"`
	ProviderReferenceID string          `json:"provider_reference_id"`
	GoldAmount          decimal.Decimal `json:"gold_amount"`
}

type afdianCheckoutResponse struct {
	CheckoutURL         string          `json:"checkout_url"`
	ProviderReferenceID string          `json:"provider_reference_id"`
	PlanID              string          `json:"plan_id"`
	GoldAmount          decimal.Decimal `json:"gold_amount"`
}

// providerCheckoutRequest is WalletProductController.ProviderCheckoutRequest.
type providerCheckoutRequest struct {
	ProviderReferenceID *string `json:"provider_reference_id"`
}

// bindProviderCheckoutRequest decodes the optional request body; an empty
// body is the C# `request = null` case.
func bindProviderCheckoutRequest(ctx *gin.Context) (*providerCheckoutRequest, bool) {
	var request providerCheckoutRequest
	if err := ctx.ShouldBindJSON(&request); err != nil && !errors.Is(err, io.EOF) {
		ctx.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
			"request": {"The request body is not valid."},
		}))
		return nil, false
	}
	return &request, true
}

func productCheckoutFailed(ctx *gin.Context, err error) {
	ctx.JSON(http.StatusBadRequest,
		errs.New(walletErrCodeProductCheckout, err.Error(), http.StatusBadRequest))
}

// POST /api/wallet-products/golds-resupply-pack/checkout/paddle
func (c *productController) paddleCheckout(ctx *gin.Context) {
	user := middleware.CurrentUser(ctx.Request.Context())
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	request, ok := bindProviderCheckoutRequest(ctx)
	if !ok {
		return
	}
	_, providerReference, amount, err := c.products.PreparePaddleGoldsResupplyPack(request.ProviderReferenceID)
	if err != nil {
		productCheckoutFailed(ctx, err)
		return
	}
	paddle := c.products.paddle
	if paddle == nil {
		productCheckoutFailed(ctx, errors.New("Paddle checkout is not configured."))
		return
	}
	session, err := paddle.CreateCheckout(ctx.Request.Context(), providerReference, map[string]any{
		"account_id":     user.Id,
		"wallet_product": GoldsResupplyPackKey,
		"price_id":       providerReference,
		"golds_amount":   amount,
	})
	if err != nil {
		productCheckoutFailed(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, paddleCheckoutResponse{
		TransactionID:       session.TransactionID,
		CheckoutURL:         session.CheckoutURL,
		ProviderReferenceID: providerReference,
		GoldAmount:          amount,
	})
}

// POST /api/wallet-products/golds-resupply-pack/checkout/afdian
func (c *productController) afdianCheckout(ctx *gin.Context) {
	user := middleware.CurrentUser(ctx.Request.Context())
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	request, ok := bindProviderCheckoutRequest(ctx)
	if !ok {
		return
	}
	_, providerReference, amount, err := c.products.PrepareAfdianGoldsResupplyPack(request.ProviderReferenceID)
	if err != nil {
		productCheckoutFailed(ctx, err)
		return
	}
	afdian := c.products.afdian
	if afdian == nil {
		productCheckoutFailed(ctx, errors.New("Afdian checkout is not configured."))
		return
	}
	planID, err := afdian.GetCheckoutPlanID()
	if err != nil {
		productCheckoutFailed(ctx, err)
		return
	}
	customOrderID := user.Id
	checkoutURL, err := afdian.CreateCheckoutURL(planID, providerReference, &customOrderID, 1)
	if err != nil {
		productCheckoutFailed(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, afdianCheckoutResponse{
		CheckoutURL:         checkoutURL,
		ProviderReferenceID: providerReference,
		PlanID:              planID,
		GoldAmount:          amount,
	})
}

// GET /api/admin/wallet-products/golds-resupply-pack
func (c *productController) adminGoldsResupplyPack(ctx *gin.Context) {
	ctx.JSON(http.StatusOK, walletProductSummaryModel(c.products.GoldsResupplyPackDefinition()))
}

// POST /api/admin/wallet-products/orders/:orderId/apply
func (c *productController) applyPaidOrder(ctx *gin.Context) {
	orderID, err := uuid.Parse(strings.TrimSpace(ctx.Param("orderId")))
	if err != nil {
		// The C# route carries a :guid constraint, so a malformed id does not
		// match any endpoint.
		ctx.Status(http.StatusNotFound)
		return
	}
	order, err := c.products.ApplyPaidWalletProductOrder(ctx.Request.Context(), orderID)
	if err != nil {
		ctx.JSON(http.StatusBadRequest,
			errs.New(walletErrCodeOrderApply, err.Error(), http.StatusBadRequest))
		return
	}
	ctx.JSON(http.StatusOK, c.orderWire(ctx, order))
}

// orderWire renders an order with the relations the C# apply endpoint
// included (Transaction and PayeeWallet).
func (c *productController) orderWire(ctx *gin.Context, order *WalletOrderEntity) Order {
	response := orderModel(order, nil)
	db := c.d.Service.DB.WithContext(ctx.Request.Context())
	if order.TransactionID != nil {
		var transaction WalletTransactionEntity
		if err := db.First(&transaction, "id = ?", *order.TransactionID).Error; err == nil {
			wire := transactionModel(&transaction)
			response.Transaction = &wire
		}
	}
	if order.PayeeWalletID != nil {
		var wallet WalletEntity
		if err := db.First(&wallet, "id = ?", *order.PayeeWalletID).Error; err == nil {
			wire := walletModel(&wallet, nil)
			response.PayeeWallet = &wire
		}
	}
	return response
}
