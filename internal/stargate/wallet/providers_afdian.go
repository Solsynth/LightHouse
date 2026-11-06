package wallet

// Afdian (ifdian.net) payment provider. Ported from
// DysonNetwork.Wallet's Payment/PaymentHandlers/AfdianPaymentHandler.cs.
//
// Configuration (wallet module settings):
//   payment.auth.afdian.credential        "user_id:token"
//   payment.auth.afdian.planId            checkout plan id
//   payment.auth.afdian.webhookPublicKey  optional override of the platform
//                                         webhook signing key
//   payment.subscriptions.afdian.<key>    legacy subscription-key -> plan id map
//
// Webhooks are signed since 2025-07-01 (RSA/SHA-256 over out_trade_no,
// user_id, plan_id and total_amount). The signature does not cover month,
// status, sku_detail or custom_order_id, so those fields are always re-read
// from the open API before anything is granted.

import (
	"bytes"
	"context"
	"crypto"
	"crypto/md5"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
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
	// afdianOrderStatusPaid is the only status Afdian reports for a completed
	// payment ("status 2 为交易成功"). Anything else is not a sale.
	afdianOrderStatusPaid = 2
)

// afdianWebhookPublicKeyPEM is the platform key Afdian publishes for callback
// signatures (guide.afdian.com/creator/developer, "Webhook 签名"). The
// payment.auth.afdian.webhookPublicKey setting overrides it.
const afdianWebhookPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAwwdaCg1Bt+UKZKs0R54y
lYnuANma49IpgoOwNmk3a0rhg/PQuhUJ0EOZSowIC44l0K3+fqGns3Ygi4AfmEfS
4EKbdk1ahSxu7Zkp2rHMt+R9GarQFQkwSS/5x1dYiHNVMiR8oIXDgjmvxuNes2Cr
8fw9dEF0xNBKdkKgG2qAawcN1nZrdyaKWtPVT9m2Hl0ddOO9thZmVLFOb9NVzgYf
jEgI+KWX6aY19Ka/ghv/L4t1IXmz9pctablN5S0CRWpJW3Cn0k6zSXgjVdKm4uN7
jRlgSRaf/Ind46vMCm3N2sgwxu/g3bnooW+db0iLo13zzuvyn727Q3UDQ0MmZcEW
MQIDAQAB
-----END PUBLIC KEY-----`

// AfdianProvider calls the Afdian open API and parses its webhooks.
type AfdianProvider struct {
	client     *http.Client
	log        *slog.Logger
	settings   map[string]any
	userID     string
	token      string
	webhookKey *rsa.PublicKey
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
	keyPEM := settingString(settings, "payment.auth.afdian.webhookPublicKey")
	if strings.TrimSpace(keyPEM) == "" {
		keyPEM = afdianWebhookPublicKeyPEM
	}
	webhookKey, err := parseAfdianWebhookKey(keyPEM)
	if err != nil {
		log.Error("afdian webhook signing key is unusable", "error", err)
	}
	return &AfdianProvider{
		client:     &http.Client{Timeout: afdianRequestTimeout},
		log:        log,
		settings:   settings,
		userID:     userID,
		token:      token,
		webhookKey: webhookKey,
	}
}

// parseAfdianWebhookKey loads the PEM public key Afdian signs callbacks with.
func parseAfdianWebhookKey(value string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(value)))
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("webhook key is not an RSA public key")
	}
	return key, nil
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

// IsPaid reports whether Afdian settled the order. Callers must not grant
// anything for an unpaid or refunded order.
func (o AfdianOrderItem) IsPaid() bool {
	return o.Status == afdianOrderStatusPaid
}

// providerOrder converts the Afdian order into the shared provider order.
func (o AfdianOrderItem) providerOrder(customOrderID string) ProviderOrder {
	subscriptionID := o.PrimarySkuID()
	if subscriptionID == "" {
		subscriptionID = o.PlanID
	}
	// Afdian omits create_time from both the webhook and the query-order
	// payloads, so a missing value must stay zero: time.Unix(0, 0) is not
	// time.Time.IsZero() and would backdate the subscription to 1970.
	var begunAt time.Time
	if o.CreateTime > 0 {
		begunAt = time.Unix(o.CreateTime, 0).UTC()
	}
	return ProviderOrder{
		ID:             o.TradeNumber,
		Provider:       PaymentAfdian,
		SubscriptionID: subscriptionID,
		AccountID:      o.UserID,
		BegunAt:        begunAt,
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

// AfdianWebhookOrderData mirrors AfdianWebhookOrderData. The platform puts
// the callback signature next to the order, inside data.
type AfdianWebhookOrderData struct {
	Type  string                          `json:"type"`
	Order AfdianWebhookAfdianOrderDetails `json:"order"`
	Sign  string                          `json:"sign"`
}

// AfdianWebhookRequest mirrors AfdianWebhookRequest. Sign is also accepted at
// the top level in case the platform moves it out of data.
type AfdianWebhookRequest struct {
	ErrorCode    int                    `json:"ec"`
	ErrorMessage string                 `json:"em"`
	Data         AfdianWebhookOrderData `json:"data"`
	Sign         string                 `json:"sign"`
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

// HandleWebhook validates and applies an Afdian callback.
//
// The endpoint is public and Afdian does not expose a per-shop webhook
// secret, so nothing in the body is trusted on its own:
//
//   - A signature (added 2025-07-01) is required when present and is verified
//     against the platform key over out_trade_no, user_id, plan_id and
//     total_amount. A wrong signature is rejected without any lookup.
//   - The signature does not cover month, status, sku_detail or
//     custom_order_id, so the order is re-read from the open API with the
//     shop credential and only the API's copy is applied. Unknown, unpaid or
//     id-mismatched orders are dropped.
//   - Deduplication still happens in inbound_orders.
//
// A lookup failure answers non-200 so Afdian retries; a rejected order
// answers 200 so Afdian stops.
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
	callbackOrder := webhook.Data.Order.AfdianOrderItem
	sign := strings.TrimSpace(webhook.Data.Sign)
	if sign == "" {
		sign = strings.TrimSpace(webhook.Sign)
	}
	if err := p.verifyWebhookSignature(&callbackOrder, sign); err != nil {
		if sign != "" {
			p.log.Warn("afdian webhook signature verification failed",
				"order", callbackOrder.TradeNumber, "error", err)
			return AfdianWebhookResponse{ErrorCode: 401, ErrorMessage: "Invalid signature"}
		}
		p.log.Warn("afdian webhook is unsigned, relying on the order lookup",
			"order", callbackOrder.TradeNumber)
	}
	tradeNumber := strings.TrimSpace(webhook.Data.Order.TradeNumber)
	if len(tradeNumber) == 0 || len(tradeNumber) > 64 {
		p.log.Warn("afdian webhook carried no usable order id", "length", len(tradeNumber))
		return AfdianWebhookResponse{ErrorCode: 400, ErrorMessage: "Invalid webhook data"}
	}
	order, err := p.GetOrder(ctx, tradeNumber)
	if err != nil {
		p.log.Error("failed to verify afdian order", "order", tradeNumber, "error", err)
		return AfdianWebhookResponse{ErrorCode: 500, ErrorMessage: "Order verification failed"}
	}
	if order == nil {
		p.log.Warn("afdian webhook named an order the shop does not own", "order", tradeNumber)
		return afdianWebhookSuccess()
	}
	if !strings.EqualFold(strings.TrimSpace(order.TradeNumber), tradeNumber) {
		p.log.Warn("afdian webhook order id mismatch",
			"order", tradeNumber, "resolved", order.TradeNumber)
		return afdianWebhookSuccess()
	}
	if !order.IsPaid() {
		p.log.Warn("afdian webhook order is not paid", "order", tradeNumber, "status", order.Status)
		return afdianWebhookSuccess()
	}
	// The account binding is part of our checkout URL, so the API's echo of it
	// wins; the callback copy is only a fallback for API payloads that omit it.
	customOrderID := strings.TrimSpace(order.CustomOrderID)
	if custom := strings.TrimSpace(webhook.Data.Order.CustomOrderID); custom != "" {
		if customOrderID == "" {
			customOrderID = custom
		} else if !strings.EqualFold(customOrderID, custom) {
			p.log.Warn("afdian webhook custom_order_id disagrees with the order",
				"order", tradeNumber, "callback", custom, "verified", customOrderID)
		}
	}
	if process != nil {
		if err := process(ctx, order.providerOrder(customOrderID)); err != nil {
			p.log.Error("error processing afdian order", "order", order.TradeNumber, "error", err)
		}
	}
	return afdianWebhookSuccess()
}

// verifyWebhookSignature checks the platform signature over the fields Afdian
// signs, in order and without separators: out_trade_no, user_id, plan_id,
// total_amount. The algorithm is RSA/SHA-256 with PKCS#1 v1.5 padding and a
// base64 signature, matching the reference PHP verifier.
func (p *AfdianProvider) verifyWebhookSignature(order *AfdianOrderItem, sign string) error {
	if strings.TrimSpace(sign) == "" {
		return errors.New("signature is missing")
	}
	if p.webhookKey == nil {
		return errors.New("webhook public key is not usable")
	}
	raw, err := base64.StdEncoding.DecodeString(sign)
	if err != nil {
		return fmt.Errorf("signature is not base64: %w", err)
	}
	signed := order.TradeNumber + order.UserID + order.PlanID + order.TotalAmount
	digest := sha256.Sum256([]byte(signed))
	return rsa.VerifyPKCS1v15(p.webhookKey, crypto.SHA256, digest[:], raw)
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
