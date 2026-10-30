package wallet

// Paddle payment provider. Ported from DysonNetwork.Wallet's
// Payment/PaymentHandlers/PaddlePaymentHandler.cs.
//
// Configuration (wallet module settings):
//   payment.auth.paddle.apiKey         API key (Bearer token)
//   payment.auth.paddle.webhookSecret  Paddle-Signature HMAC secret
//   payment.auth.paddle.environment    "sandbox" | "live" | "production"
//   payment.subscriptions.paddle.<priceId>  legacy price -> plan map

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	paddleLiveBaseURL     = "https://api.paddle.com"
	paddleSandboxBaseURL  = "https://sandbox-api.paddle.com"
	paddleRequestTimeout  = 20 * time.Second
	paddleSignatureWindow = 30 * time.Second
)

// PaddleProvider calls the Paddle API and verifies its webhooks.
type PaddleProvider struct {
	client        *http.Client
	log           *slog.Logger
	settings      map[string]any
	apiKey        string
	webhookSecret string
	environment   string
}

func newPaddleProvider(settings map[string]any, log *slog.Logger) *PaddleProvider {
	if log == nil {
		log = slog.Default()
	}
	return &PaddleProvider{
		client:        &http.Client{Timeout: paddleRequestTimeout},
		log:           log,
		settings:      settings,
		apiKey:        settingString(settings, "payment.auth.paddle.apiKey", "paddleApiKey"),
		webhookSecret: settingString(settings, "payment.auth.paddle.webhookSecret", "paddleWebhookSecret"),
		environment:   settingString(settings, "payment.auth.paddle.environment", "paddleEnvironment"),
	}
}

// PaddleTransaction mirrors PaddleTransaction.
type PaddleTransaction struct {
	ID                   string                  `json:"id"`
	Status               string                  `json:"status"`
	PaddleSubscriptionID *string                 `json:"subscription_id"`
	CustomData           map[string]any          `json:"custom_data"`
	Items                []PaddleTransactionItem `json:"items"`
	BilledAt             *time.Time              `json:"billed_at"`
	CreatedAt            time.Time               `json:"created_at"`
	Checkout             *PaddleCheckoutInfo     `json:"checkout"`
}

// PaddleTransactionItem mirrors PaddleTransactionItem.
type PaddleTransactionItem struct {
	Price *PaddlePrice `json:"price"`
}

// PaddlePrice mirrors PaddlePrice.
type PaddlePrice struct {
	ID           string              `json:"id"`
	BillingCycle *PaddleBillingCycle `json:"billing_cycle"`
}

// PaddleBillingCycle mirrors PaddleBillingCycle.
type PaddleBillingCycle struct {
	Interval  string `json:"interval"`
	Frequency int    `json:"frequency"`
}

// ToDuration mirrors PaddleBillingCycle.ToDuration.
func (c *PaddleBillingCycle) ToDuration() time.Duration {
	interval := strings.ToLower(strings.TrimSpace(c.Interval))
	frequency := c.Frequency
	if frequency <= 0 {
		frequency = 1
	}
	switch interval {
	case "day":
		return time.Duration(frequency) * 24 * time.Hour
	case "week":
		return time.Duration(frequency) * 7 * 24 * time.Hour
	case "month":
		return time.Duration(frequency) * 30 * 24 * time.Hour
	case "year":
		return time.Duration(frequency) * 365 * 24 * time.Hour
	default:
		return 30 * 24 * time.Hour
	}
}

// PaddleCheckoutInfo mirrors PaddleCheckoutInfo.
type PaddleCheckoutInfo struct {
	URL *string `json:"url"`
}

// PaddleCheckoutSession mirrors PaddleCheckoutSession.
type PaddleCheckoutSession struct {
	TransactionID string
	CheckoutURL   string
}

// PaddleWebhookEvent mirrors PaddleWebhookEvent.
type PaddleWebhookEvent struct {
	EventID        string             `json:"event_id"`
	EventType      string             `json:"event_type"`
	OccurredAt     *time.Time         `json:"occurred_at"`
	NotificationID string             `json:"notification_id"`
	Data           *PaddleTransaction `json:"data"`
}

type paddleEntityResponse struct {
	Data json.RawMessage `json:"data"`
}

// customDataString mirrors GetCustomDataString.
func (t *PaddleTransaction) customDataString(key string) *string {
	if t.CustomData == nil {
		return nil
	}
	value, ok := t.CustomData[key]
	if !ok {
		return nil
	}
	var text string
	switch typed := value.(type) {
	case string:
		text = typed
	case json.Number:
		text = typed.String()
	case float64:
		text = strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		text = strconv.FormatBool(typed)
	case nil:
		return nil
	default:
		text = fmt.Sprint(typed)
	}
	return &text
}

// subscriptionIdentifier mirrors PaddleTransaction.SubscriptionId.
func (t *PaddleTransaction) subscriptionIdentifier() string {
	if value := t.customDataString("subscription_identifier"); value != nil && strings.TrimSpace(*value) != "" {
		return *value
	}
	if value := t.customDataString("price_id"); value != nil && strings.TrimSpace(*value) != "" {
		return *value
	}
	for _, item := range t.Items {
		if item.Price != nil && strings.TrimSpace(item.Price.ID) != "" {
			return item.Price.ID
		}
	}
	if t.PaddleSubscriptionID != nil {
		return *t.PaddleSubscriptionID
	}
	return ""
}

// accountIdentifier mirrors PaddleTransaction.AccountId.
func (t *PaddleTransaction) accountIdentifier() string {
	if value := t.customDataString("account_id"); value != nil && strings.TrimSpace(*value) != "" {
		return *value
	}
	if value := t.customDataString("user_id"); value != nil {
		return *value
	}
	return ""
}

// begunAt mirrors PaddleTransaction.BegunAt.
func (t *PaddleTransaction) begunAt() time.Time {
	if t.BilledAt != nil {
		return t.BilledAt.UTC()
	}
	return t.CreatedAt.UTC()
}

// duration mirrors PaddleTransaction.Duration.
func (t *PaddleTransaction) duration() time.Duration {
	for _, item := range t.Items {
		if item.Price != nil && item.Price.BillingCycle != nil {
			return item.Price.BillingCycle.ToDuration()
		}
	}
	return 30 * 24 * time.Hour
}

// ProviderOrder converts the transaction into the shared provider order.
func (t *PaddleTransaction) ProviderOrder() ProviderOrder {
	order := ProviderOrder{
		ID:             t.ID,
		Provider:       PaymentPaddle,
		SubscriptionID: t.subscriptionIdentifier(),
		AccountID:      t.accountIdentifier(),
		BegunAt:        t.begunAt(),
		Duration:       t.duration(),
		IsTesting:      false,
		Quantity:       1,
	}
	if t.PaddleSubscriptionID != nil {
		order.CorrelationID = *t.PaddleSubscriptionID
	}
	return order
}

// baseAPIURL mirrors GetBaseApiUrl.
func (p *PaddleProvider) baseAPIURL() (string, error) {
	configured := strings.ToLower(strings.TrimSpace(p.environment))
	switch configured {
	case "sandbox":
		return paddleSandboxBaseURL, nil
	case "live", "production":
		return paddleLiveBaseURL, nil
	case "":
		if strings.Contains(strings.ToLower(p.apiKey), "_sdbx_") {
			return paddleSandboxBaseURL, nil
		}
		return paddleLiveBaseURL, nil
	default:
		return "", errors.New("Payment:Auth:Paddle:Environment must be 'sandbox', 'live', or 'production'.")
	}
}

func (p *PaddleProvider) do(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	if strings.TrimSpace(p.apiKey) == "" {
		return nil, 0, errors.New("Payment:Auth:Paddle:ApiKey is not configured.")
	}
	baseURL, err := p.baseAPIURL()
	if err != nil {
		return nil, 0, err
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Authorization", "Bearer "+p.apiKey)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := p.client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, response.StatusCode, err
	}
	return raw, response.StatusCode, nil
}

// CreateCheckout mirrors CreateCheckoutAsync.
func (p *PaddleProvider) CreateCheckout(ctx context.Context, priceID string, customData map[string]any) (*PaddleCheckoutSession, error) {
	if strings.TrimSpace(priceID) == "" {
		return nil, errors.New("Paddle price id is required.")
	}
	payload := map[string]any{
		"collection_mode": "automatic",
		"items":           []map[string]any{{"price_id": priceID, "quantity": 1}},
		"custom_data":     customData,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	raw, status, err := p.do(ctx, http.MethodPost, "/transactions?include=checkout", body)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		p.log.Error("failed to create paddle checkout", "price_id", priceID, "status", status, "body", string(raw))
		return nil, errors.New("Unable to create Paddle checkout transaction.")
	}
	var response paddleEntityResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	var transaction PaddleTransaction
	if err := json.Unmarshal(response.Data, &transaction); err != nil {
		return nil, err
	}
	if transaction.Checkout == nil || transaction.Checkout.URL == nil {
		return nil, errors.New("Paddle checkout URL was not returned by the transaction API.")
	}
	return &PaddleCheckoutSession{TransactionID: transaction.ID, CheckoutURL: *transaction.Checkout.URL}, nil
}

// GetTransaction mirrors GetTransactionAsync.
func (p *PaddleProvider) GetTransaction(ctx context.Context, transactionID string) (*PaddleTransaction, error) {
	if strings.TrimSpace(transactionID) == "" {
		p.log.Warn("paddle transaction id cannot be empty")
		return nil, nil
	}
	raw, status, err := p.do(ctx, http.MethodGet, "/transactions/"+transactionID, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		p.log.Error("failed to fetch paddle transaction", "transaction", transactionID, "status", status, "body", string(raw))
		return nil, nil
	}
	var response paddleEntityResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	var transaction PaddleTransaction
	if err := json.Unmarshal(response.Data, &transaction); err != nil {
		return nil, err
	}
	return &transaction, nil
}

// HandleWebhook mirrors HandleWebhook. It reports whether the notification was
// accepted; callers answer 200 on success and 401 otherwise.
func (p *PaddleProvider) HandleWebhook(ctx context.Context, signatureHeader string, body []byte, process func(context.Context, ProviderOrder) error) bool {
	p.log.Info("received webhook request from paddle")
	if strings.TrimSpace(signatureHeader) == "" {
		p.log.Warn("missing Paddle-Signature header")
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		p.log.Warn("paddle webhook request body is empty")
		return false
	}
	if err := p.verifySignature(signatureHeader, body); err != nil {
		p.log.Warn("paddle webhook signature verification failed", "error", err)
		return false
	}
	var webhook PaddleWebhookEvent
	if err := json.Unmarshal(body, &webhook); err != nil {
		p.log.Error("failed to parse paddle webhook", "error", err)
		return false
	}
	if webhook.Data == nil {
		p.log.Warn("paddle webhook payload missing data")
		return false
	}
	if !strings.EqualFold(webhook.EventType, "transaction.completed") {
		p.log.Info("ignoring unsupported paddle event", "event_type", webhook.EventType)
		return true
	}
	if !strings.EqualFold(webhook.Data.Status, "completed") {
		p.log.Info("ignoring paddle transaction with non-completed status",
			"transaction", webhook.Data.ID, "status", webhook.Data.Status)
		return true
	}
	if process != nil {
		if err := process(ctx, webhook.Data.ProviderOrder()); err != nil {
			p.log.Error("error processing paddle transaction", "transaction", webhook.Data.ID, "error", err)
		}
	}
	return true
}

// verifySignature mirrors VerifySignature: ts=...;h1=... with a 30 second
// tolerance and an HMAC-SHA256 of "<ts>:<body>".
func (p *PaddleProvider) verifySignature(signatureHeader string, body []byte) error {
	secret := strings.TrimSpace(p.webhookSecret)
	if secret == "" {
		return errors.New("Payment:Auth:Paddle:WebhookSecret is not configured.")
	}
	pairs := map[string]string{}
	for _, part := range strings.Split(signatureHeader, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		pieces := strings.SplitN(part, "=", 2)
		if len(pieces) != 2 {
			continue
		}
		pairs[strings.ToLower(strings.TrimSpace(pieces[0]))] = strings.TrimSpace(pieces[1])
	}
	ts, ok := pairs["ts"]
	if !ok {
		return errors.New("paddle signature is missing ts")
	}
	h1, ok := pairs["h1"]
	if !ok {
		return errors.New("paddle signature is missing h1")
	}
	timestamp, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return errors.New("paddle signature timestamp is invalid")
	}
	skew := time.Now().Unix() - timestamp
	if skew < 0 {
		skew = -skew
	}
	if time.Duration(skew)*time.Second > paddleSignatureWindow {
		return fmt.Errorf("paddle webhook timestamp outside tolerance window: %d", timestamp)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + ":"))
	mac.Write(body)
	computed := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(computed), []byte(strings.ToLower(h1))) {
		return errors.New("paddle signature mismatch")
	}
	return nil
}

// GetSubscriptionPlanID mirrors GetSubscriptionPlanId.
func (p *PaddleProvider) GetSubscriptionPlanID(priceID string) *string {
	if strings.TrimSpace(priceID) == "" {
		return nil
	}
	planID := settingString(p.settings, "payment.subscriptions.paddle."+priceID)
	if strings.TrimSpace(planID) == "" {
		p.log.Warn("unknown paddle price id", "price_id", priceID)
		return nil
	}
	return &planID
}
