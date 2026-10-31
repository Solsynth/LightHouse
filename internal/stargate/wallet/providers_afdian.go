package wallet

// Afdian (ifdian.net) payment provider. Ported from
// DysonNetwork.Wallet's Payment/PaymentHandlers/AfdianPaymentHandler.cs.
//
// Configuration (wallet module settings):
//   payment.auth.afdian.credential   "user_id:token"
//   payment.auth.afdian.planId       checkout plan id
//   payment.subscriptions.afdian.<key>  legacy subscription-key -> plan id map

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	afdianAPIURL         = "https://ifdian.net/api/open/query-order"
	afdianCheckoutURL    = "https://ifdian.net/order/create"
	afdianRequestTimeout = 20 * time.Second
	// Cloudflare fronts ifdian.net and answers 403 to Go's default
	// "Go-http-client/2.0" user agent. The C# handler sent no user agent at
	// all, which the WAF allows; identify as the application instead.
	afdianUserAgent = "DysonNetwork/1.0 (Lighthouse)"
)

// AfdianProvider calls the Afdian open API and parses its webhooks.
type AfdianProvider struct {
	client   *http.Client
	log      *slog.Logger
	settings map[string]any
	userID   string
	token    string
}

func newAfdianProvider(settings map[string]any, log *slog.Logger) *AfdianProvider {
	if log == nil {
		log = slog.Default()
	}
	credential := settingString(settings, "payment.auth.afdian.credential", "payment.auth.afdian", "afdianCredential")
	if strings.TrimSpace(credential) == "" {
		credential = "_:"
	}
	userID, token := credential, ""
	if parts := strings.SplitN(credential, ":", 2); len(parts) == 2 {
		userID, token = parts[0], parts[1]
	}
	return &AfdianProvider{
		client:   &http.Client{Timeout: afdianRequestTimeout},
		log:      log,
		settings: settings,
		userID:   userID,
		token:    token,
	}
}

// AfdianOrderResponse mirrors AfdianOrderResponse.
type AfdianOrderResponse struct {
	ErrorCode    int             `json:"ec"`
	ErrorMessage string          `json:"em"`
	Data         AfdianOrderData `json:"data"`
}

// AfdianOrderData mirrors AfdianOrderData.
type AfdianOrderData struct {
	Orders     []AfdianOrderItem     `json:"list"`
	TotalCount int                   `json:"total_count"`
	TotalPages int                   `json:"total_page"`
	Request    *AfdianRequestDetails `json:"request"`
}

// AfdianRequestDetails mirrors AfdianRequestDetails.
type AfdianRequestDetails struct {
	UserID    string `json:"user_id"`
	Params    string `json:"params"`
	Timestamp int64  `json:"ts"`
	Sign      string `json:"sign"`
}

// AfdianSkuDetailItem mirrors AfdianSkuDetailItem.
type AfdianSkuDetailItem struct {
	SkuID   string `json:"sku_id"`
	Count   int    `json:"count"`
	Name    string `json:"name"`
	AlbumID string `json:"album_id"`
	Picture string `json:"pic"`
}

// AfdianOrderItem mirrors AfdianOrderItem (the ISubscriptionOrder fields are
// resolved through providerOrder).
type AfdianOrderItem struct {
	TradeNumber    string                `json:"out_trade_no"`
	CustomOrderID  string                `json:"custom_order_id"`
	UserID         string                `json:"user_id"`
	PlanID         string                `json:"plan_id"`
	Months         int                   `json:"month"`
	TotalAmount    string                `json:"total_amount"`
	ShowAmount     string                `json:"show_amount"`
	Status         int                   `json:"status"`
	Remark         string                `json:"remark"`
	RedeemID       string                `json:"redeem_id"`
	ProductType    int                   `json:"product_type"`
	Discount       string                `json:"discount"`
	SkuDetail      []AfdianSkuDetailItem `json:"sku_detail"`
	CreateTime     int64                 `json:"create_time"`
	UserName       string                `json:"user_name"`
	PlanTitle      string                `json:"plan_title"`
	UserPrivateID  string                `json:"user_private_id"`
	AddressPerson  string                `json:"address_person"`
	AddressPhone   string                `json:"address_phone"`
	AddressAddress string                `json:"address_address"`
}

// PrimarySkuID mirrors AfdianOrderItem.PrimarySkuId.
func (o AfdianOrderItem) PrimarySkuID() string {
	for _, detail := range o.SkuDetail {
		if strings.TrimSpace(detail.SkuID) != "" {
			return detail.SkuID
		}
	}
	return ""
}

// providerOrder converts the Afdian order into the shared provider order.
func (o AfdianOrderItem) providerOrder(customOrderID string) ProviderOrder {
	subscriptionID := o.PrimarySkuID()
	if subscriptionID == "" {
		subscriptionID = o.PlanID
	}
	return ProviderOrder{
		ID:             o.TradeNumber,
		Provider:       PaymentAfdian,
		SubscriptionID: subscriptionID,
		AccountID:      o.UserID,
		BegunAt:        time.Unix(o.CreateTime, 0).UTC(),
		Duration:       time.Duration(o.Months) * 30 * 24 * time.Hour,
		IsTesting:      false,
		CustomOrderID:  customOrderID,
		Quantity:       1,
	}
}

// AfdianWebhookAfdianOrderDetails mirrors AfdianWebhookAfdianOrderDetails.
type AfdianWebhookAfdianOrderDetails struct {
	AfdianOrderItem
	CustomOrderID string `json:"custom_order_id"`
}

// AfdianWebhookOrderData mirrors AfdianWebhookOrderData.
type AfdianWebhookOrderData struct {
	Type  string                          `json:"type"`
	Order AfdianWebhookAfdianOrderDetails `json:"order"`
}

// AfdianWebhookRequest mirrors AfdianWebhookRequest.
type AfdianWebhookRequest struct {
	ErrorCode    int                    `json:"ec"`
	ErrorMessage string                 `json:"em"`
	Data         AfdianWebhookOrderData `json:"data"`
}

// AfdianWebhookResponse mirrors AfdianWebhookResponse.
type AfdianWebhookResponse struct {
	ErrorCode    int    `json:"ec"`
	ErrorMessage string `json:"em"`
}

func afdianWebhookSuccess() AfdianWebhookResponse {
	return AfdianWebhookResponse{ErrorCode: 200, ErrorMessage: ""}
}

// afdianSign mirrors CalculateSign: md5(token + "params" + paramsJSON + "ts" +
// ts + "user_id" + userId).
func afdianSign(token, userID, paramsJSON string, ts int64) string {
	sum := md5.Sum([]byte(token + "params" + paramsJSON + "ts" + strconv.FormatInt(ts, 10) + "user_id" + userID))
	return hex.EncodeToString(sum[:])
}

type afdianRequestPayload struct {
	UserID string `json:"user_id"`
	Params string `json:"params"`
	TS     int64  `json:"ts"`
	Sign   string `json:"sign"`
}

func (p *AfdianProvider) queryOrders(ctx context.Context, params any) (*AfdianOrderResponse, error) {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	ts := time.Now().Unix()
	payload := afdianRequestPayload{
		UserID: p.userID,
		Params: string(paramsJSON),
		TS:     ts,
		Sign:   afdianSign(p.token, p.userID, string(paramsJSON), ts),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, afdianAPIURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", afdianUserAgent)
	response, err := p.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		p.log.Error("afdian order query failed", "status", response.StatusCode, "body", string(raw))
		return nil, nil
	}
	var result AfdianOrderResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ListOrders mirrors ListOrderAsync.
func (p *AfdianProvider) ListOrders(ctx context.Context, page int) (*AfdianOrderResponse, error) {
	return p.queryOrders(ctx, map[string]any{"page": page})
}

// GetOrder mirrors GetOrderAsync: the order with the given out_trade_no.
func (p *AfdianProvider) GetOrder(ctx context.Context, orderID string) (*AfdianOrderItem, error) {
	if strings.TrimSpace(orderID) == "" {
		p.log.Warn("afdian order id cannot be empty")
		return nil, nil
	}
	result, err := p.queryOrders(ctx, map[string]any{"out_trade_no": orderID})
	if err != nil {
		return nil, err
	}
	if result == nil || len(result.Data.Orders) == 0 {
		p.log.Warn("no afdian order found", "order_id", orderID)
		return nil, nil
	}
	order := result.Data.Orders[0]
	return &order, nil
}

// GetOrderBatch mirrors GetOrderBatchAsync.
func (p *AfdianProvider) GetOrderBatch(ctx context.Context, orderIDs []string) ([]AfdianOrderItem, error) {
	if len(orderIDs) == 0 {
		return []AfdianOrderItem{}, nil
	}
	joined := strings.Join(orderIDs, ",")
	result, err := p.queryOrders(ctx, map[string]any{"out_trade_no": joined})
	if err != nil {
		return nil, err
	}
	if result == nil || len(result.Data.Orders) == 0 {
		p.log.Warn("no afdian orders found", "order_ids", joined)
		return []AfdianOrderItem{}, nil
	}
	return result.Data.Orders, nil
}

// HandleWebhook mirrors HandleWebhook. Afdian does not sign its callbacks, so
// the payload is only parsed; deduplication happens in inbound_orders when the
// order is applied. Afdian always receives ec=200 so it stops retrying.
func (p *AfdianProvider) HandleWebhook(ctx context.Context, body []byte, process func(context.Context, ProviderOrder) error) AfdianWebhookResponse {
	p.log.Info("received webhook request from afdian")
	if len(bytes.TrimSpace(body)) == 0 {
		p.log.Error("afdian webhook request body is empty")
		return AfdianWebhookResponse{ErrorCode: 400, ErrorMessage: "Empty request body"}
	}
	var webhook AfdianWebhookRequest
	if err := json.Unmarshal(body, &webhook); err != nil {
		p.log.Error("failed to parse afdian webhook", "error", err)
		return AfdianWebhookResponse{ErrorCode: 400, ErrorMessage: "Invalid webhook data"}
	}
	if webhook.Data.Type != "order" {
		p.log.Warn("unsupported afdian webhook type", "type", webhook.Data.Type)
		return afdianWebhookSuccess()
	}
	if process != nil {
		order := webhook.Data.Order.providerOrder(webhook.Data.Order.CustomOrderID)
		if err := process(ctx, order); err != nil {
			p.log.Error("error processing afdian order", "order", order.ID, "error", err)
		}
	}
	return afdianWebhookSuccess()
}

// CreateCheckoutURL mirrors CreateCheckoutUrl.
func (p *AfdianProvider) CreateCheckoutURL(planID, skuID string, customOrderID *string, quantity int) (string, error) {
	if strings.TrimSpace(planID) == "" {
		return "", errors.New("Payment:Auth:Afdian:PlanId is not configured.")
	}
	if strings.TrimSpace(skuID) == "" {
		return "", errors.New("Afdian sku id is required.")
	}
	if quantity <= 0 {
		return "", errors.New("Afdian quantity must be greater than zero.")
	}
	skuPayload, err := json.Marshal([]map[string]any{{"sku_id": skuID, "count": quantity}})
	if err != nil {
		return "", err
	}
	query := url.Values{}
	query.Set("product_type", "1")
	query.Set("plan_id", planID)
	query.Set("sku", string(skuPayload))
	if customOrderID != nil && strings.TrimSpace(*customOrderID) != "" {
		query.Set("custom_order_id", *customOrderID)
	}
	return afdianCheckoutURL + "?" + query.Encode(), nil
}

// GetCheckoutPlanID mirrors GetCheckoutPlanId.
func (p *AfdianProvider) GetCheckoutPlanID() (string, error) {
	planID := settingString(p.settings, "payment.auth.afdian.planId", "afdianPlanId", "payment.auth.afdian.plan")
	if strings.TrimSpace(planID) == "" {
		return "", errors.New("Payment:Auth:Afdian:PlanId is not configured.")
	}
	return planID, nil
}

// GetSubscriptionPlanID mirrors GetSubscriptionPlanId.
func (p *AfdianProvider) GetSubscriptionPlanID(subscriptionKey string) *string {
	planID := settingString(p.settings, "payment.subscriptions.afdian."+subscriptionKey)
	if strings.TrimSpace(planID) == "" {
		p.log.Warn("unknown afdian subscription key", "key", subscriptionKey)
		return nil
	}
	return &planID
}

// String renders the provider order for logs.
func (o ProviderOrder) String() string {
	return fmt.Sprintf("%s/%s", o.Provider, o.ID)
}
