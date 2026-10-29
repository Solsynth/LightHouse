package wallet

// Apple App Store payment provider. Ported from DysonNetwork.Wallet's
// Payment/PaymentHandlers/AppleStorePaymentHandler.cs.
//
// Configuration (wallet module settings):
//   payment.auth.appleStore.bundleId                     expected bundle id
//   payment.auth.appleStore.environment                  accepted environments
//   payment.auth.appleStore.trustedRootSha256Fingerprints
//                                                        extra trusted roots
//
// Apple sends compact JWS payloads signed by its own certificate chain. The
// chain is verified against the platform roots first; when the deployment does
// not trust Apple's root, the chain is rebuilt against the pinned root
// fingerprints, exactly like the C# handler.

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"
)

// appleKnownRootFingerprints mirrors KnownAppleRootFingerprints.
var appleKnownRootFingerprints = []string{
	"C2B9B042DD57830E7D117DAC55AC8AE19407D38E41D88F3215BC3A890444A050",
	"63343ABFB89A6A03EBB57E9B3F5FA7BE7C4F5C756F3017B3A8C488C3653E9179",
	"B0B1730ECBC7FF4505142C49F1295E6EDA6BCAED7E2C68C5BE91B5A11001F024",
}

// appleTransactionGrantingNotificationTypes mirrors
// TransactionGrantingNotificationTypes.
var appleTransactionGrantingNotificationTypes = map[string]struct{}{
	"SUBSCRIBED":        {},
	"DID_RENEW":         {},
	"DID_RECOVER":       {},
	"OFFER_REDEEMED":    {},
	"RENEWAL_EXTENDED":  {},
	"RENEWAL_EXTENSION": {},
	"ONE_TIME_CHARGE":   {},
}

// AppleProvider verifies Apple JWS payloads.
type AppleProvider struct {
	log          *slog.Logger
	bundleID     string
	environments map[string]struct{}
	trustedRoots map[string]struct{}
}

func newAppleProvider(settings map[string]any, log *slog.Logger) *AppleProvider {
	if log == nil {
		log = slog.Default()
	}
	provider := &AppleProvider{
		log:          log,
		bundleID:     settingString(settings, "payment.auth.appleStore.bundleId", "appleBundleId"),
		environments: map[string]struct{}{},
		trustedRoots: map[string]struct{}{},
	}
	for _, fingerprint := range appleKnownRootFingerprints {
		provider.trustedRoots[strings.ToUpper(fingerprint)] = struct{}{}
	}
	for _, fingerprint := range settingStrings(settings,
		"payment.auth.appleStore.trustedRootSha256Fingerprints",
		"appleTrustedRootSha256Fingerprints") {
		normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(fingerprint), " ", ""))
		if normalized != "" {
			provider.trustedRoots[normalized] = struct{}{}
		}
	}
	provider.environments = appleAcceptedEnvironments(settingString(settings,
		"payment.auth.appleStore.environment", "appleEnvironment"))
	return provider
}

// appleAcceptedEnvironments mirrors GetAcceptedEnvironments.
func appleAcceptedEnvironments(configured string) map[string]struct{} {
	defaults := map[string]struct{}{"SANDBOX": {}, "PROD": {}}
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return defaults
	}
	fields := strings.FieldsFunc(configured, func(r rune) bool {
		return r == ',' || r == ';' || r == ' '
	})
	if len(fields) == 0 {
		return defaults
	}
	accepted := map[string]struct{}{}
	for _, field := range fields {
		value := strings.ToUpper(strings.TrimSpace(field))
		switch value {
		case "BOTH", "ALL", "ANY":
			return defaults
		case "PRODUCTION":
			value = "PROD"
		}
		if value != "" {
			accepted[value] = struct{}{}
		}
	}
	if len(accepted) == 0 {
		return defaults
	}
	return accepted
}

// AppleJWSHeader mirrors AppleJwsHeader.
type AppleJWSHeader struct {
	Alg string   `json:"alg"`
	X5c []string `json:"x5c"`
}

// AppleNotificationPayload mirrors AppleNotificationPayload.
type AppleNotificationPayload struct {
	NotificationType *string                `json:"notificationType"`
	Subtype          *string                `json:"subtype"`
	Data             *AppleNotificationData `json:"data"`
}

// AppleNotificationData mirrors AppleNotificationData.
type AppleNotificationData struct {
	AppAppleID            *int64  `json:"appAppleId"`
	BundleID              *string `json:"bundleId"`
	BundleVersion         *string `json:"bundleVersion"`
	Environment           *string `json:"environment"`
	SignedRenewalInfo     *string `json:"signedRenewalInfo"`
	SignedTransactionInfo *string `json:"signedTransactionInfo"`
}

// AppleSignedTransactionPayload mirrors AppleSignedTransactionPayload.
type AppleSignedTransactionPayload struct {
	TransactionID               *string `json:"transactionId"`
	OriginalTransactionID       *string `json:"originalTransactionId"`
	WebOrderLineItemID          *string `json:"webOrderLineItemId"`
	BundleID                    *string `json:"bundleId"`
	ProductID                   *string `json:"productId"`
	Quantity                    *int    `json:"quantity"`
	SubscriptionGroupIdentifier *string `json:"subscriptionGroupIdentifier"`
	PurchaseDate                *int64  `json:"purchaseDate"`
	OriginalPurchaseDate        *int64  `json:"originalPurchaseDate"`
	ExpiresDate                 *int64  `json:"expiresDate"`
	SignedDate                  *int64  `json:"signedDate"`
	Environment                 *string `json:"environment"`
	AppAccountToken             *string `json:"appAccountToken"`
	OfferType                   *int    `json:"offerType"`
	OfferIdentifier             *string `json:"offerIdentifier"`
	InAppOwnershipType          *string `json:"inAppOwnershipType"`
	Type                        *string `json:"type"`
	RevocationDate              *int64  `json:"revocationDate"`
	RevocationReason            *int    `json:"revocationReason"`
}

// AppleAppStoreTransaction mirrors AppleAppStoreTransaction.
type AppleAppStoreTransaction struct {
	Payload               AppleSignedTransactionPayload
	SignedTransactionInfo string
}

func (t *AppleAppStoreTransaction) id() string {
	if t.Payload.TransactionID != nil && *t.Payload.TransactionID != "" {
		return *t.Payload.TransactionID
	}
	if t.Payload.OriginalTransactionID != nil {
		return *t.Payload.OriginalTransactionID
	}
	return ""
}

func (t *AppleAppStoreTransaction) subscriptionID() string {
	if t.Payload.ProductID != nil {
		return *t.Payload.ProductID
	}
	return ""
}

// quantity mirrors AppleAppStoreTransaction.Quantity.
func (t *AppleAppStoreTransaction) quantity() (int, error) {
	if t.Payload.Quantity == nil {
		return 1, nil
	}
	if *t.Payload.Quantity <= 0 {
		return 0, errors.New("Apple transaction quantity must be greater than zero.")
	}
	return *t.Payload.Quantity, nil
}

// begunAt mirrors AppleAppStoreTransaction.BegunAt.
func (t *AppleAppStoreTransaction) begunAt() time.Time {
	if t.Payload.PurchaseDate != nil {
		return time.UnixMilli(*t.Payload.PurchaseDate).UTC()
	}
	if t.Payload.ExpiresDate != nil {
		return time.UnixMilli(*t.Payload.ExpiresDate).UTC()
	}
	return time.UnixMilli(0).UTC()
}

// duration mirrors AppleAppStoreTransaction.Duration.
func (t *AppleAppStoreTransaction) duration() time.Duration {
	if t.Payload.PurchaseDate != nil && t.Payload.ExpiresDate != nil && *t.Payload.ExpiresDate > *t.Payload.PurchaseDate {
		return time.Duration(*t.Payload.ExpiresDate-*t.Payload.PurchaseDate) * time.Millisecond
	}
	return 30 * 24 * time.Hour
}

func (t *AppleAppStoreTransaction) accountID() string {
	if t.Payload.AppAccountToken != nil {
		return *t.Payload.AppAccountToken
	}
	return ""
}

func (t *AppleAppStoreTransaction) isTesting() bool {
	return t.Payload.Environment != nil && strings.EqualFold(*t.Payload.Environment, "Sandbox")
}

// ProviderOrder converts the transaction into the shared provider order.
func (t *AppleAppStoreTransaction) ProviderOrder() (ProviderOrder, error) {
	quantity, err := t.quantity()
	if err != nil {
		return ProviderOrder{}, err
	}
	order := ProviderOrder{
		ID:             t.id(),
		Provider:       PaymentAppleStore,
		SubscriptionID: t.subscriptionID(),
		AccountID:      t.accountID(),
		BegunAt:        t.begunAt(),
		Duration:       t.duration(),
		IsTesting:      t.isTesting(),
		Quantity:       quantity,
	}
	if t.Payload.OriginalTransactionID != nil {
		order.CorrelationID = *t.Payload.OriginalTransactionID
	}
	return order, nil
}

// AppleWebhookRequest mirrors AppleWebhookRequest.
type AppleWebhookRequest struct {
	SignedPayload string `json:"signedPayload"`
}

// ParseSignedTransaction mirrors ParseSignedTransaction.
func (p *AppleProvider) ParseSignedTransaction(signedTransactionInfo string) (*AppleAppStoreTransaction, error) {
	if strings.TrimSpace(signedTransactionInfo) == "" {
		return nil, errors.New("Signed transaction info is required.")
	}
	payloadBytes, signedAt, err := p.verifyJWS(signedTransactionInfo)
	if err != nil {
		return nil, err
	}
	var payload AppleSignedTransactionPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, fmt.Errorf("Apple JWS payload could not be parsed: %w", err)
	}
	_ = signedAt
	p.log.Info("parsed apple signed transaction",
		"transaction_id", transactionIDString(&payload), "product_id", stringValue(payload.ProductID),
		"environment", stringValue(payload.Environment), "app_account_token", stringValue(payload.AppAccountToken))
	if err := p.validateBundleID(stringValue(payload.BundleID)); err != nil {
		return nil, err
	}
	if err := p.validateEnvironment(stringValue(payload.Environment)); err != nil {
		return nil, err
	}
	return &AppleAppStoreTransaction{Payload: payload, SignedTransactionInfo: signedTransactionInfo}, nil
}

func transactionIDString(payload *AppleSignedTransactionPayload) string {
	if payload.TransactionID != nil {
		return *payload.TransactionID
	}
	if payload.OriginalTransactionID != nil {
		return *payload.OriginalTransactionID
	}
	return ""
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// HandleWebhook mirrors HandleWebhook. It reports whether the notification was
// accepted; callers answer 200 on success and 401 otherwise.
func (p *AppleProvider) HandleWebhook(ctx context.Context, body []byte, process func(context.Context, ProviderOrder) error) bool {
	p.log.Info("received webhook request from apple app store")
	if len(strings.TrimSpace(string(body))) == 0 {
		p.log.Warn("apple webhook request body is empty")
		return false
	}
	var request AppleWebhookRequest
	if err := json.Unmarshal(body, &request); err != nil {
		p.log.Error("failed to parse apple webhook request", "error", err)
		return false
	}
	if strings.TrimSpace(request.SignedPayload) == "" {
		p.log.Warn("apple webhook payload missing signedPayload")
		return false
	}
	payloadBytes, _, err := p.verifyJWS(request.SignedPayload)
	if err != nil {
		p.log.Error("failed to validate apple webhook payload", "error", err)
		return false
	}
	var notification AppleNotificationPayload
	if err := json.Unmarshal(payloadBytes, &notification); err != nil {
		p.log.Error("failed to parse apple notification", "error", err)
		return false
	}
	p.log.Info("apple notification decoded",
		"type", stringValue(notification.NotificationType), "subtype", stringValue(notification.Subtype),
		"environment", dataString(notification.Data, func(data *AppleNotificationData) *string { return data.Environment }),
		"bundle_id", dataString(notification.Data, func(data *AppleNotificationData) *string { return data.BundleID }))
	if err := p.validateBundleID(dataString(notification.Data, func(data *AppleNotificationData) *string { return data.BundleID })); err != nil {
		p.log.Error("failed to validate apple webhook payload", "error", err)
		return false
	}
	if err := p.validateEnvironment(dataString(notification.Data, func(data *AppleNotificationData) *string { return data.Environment })); err != nil {
		p.log.Error("failed to validate apple webhook payload", "error", err)
		return false
	}
	signedTransactionInfo := dataString(notification.Data, func(data *AppleNotificationData) *string { return data.SignedTransactionInfo })
	if strings.TrimSpace(signedTransactionInfo) == "" {
		p.log.Info("ignoring apple notification without signed transaction info",
			"type", stringValue(notification.NotificationType))
		return true
	}
	if !p.shouldProcessNotification(stringValue(notification.NotificationType)) {
		p.log.Info("ignoring unsupported apple notification type", "type", stringValue(notification.NotificationType))
		return true
	}
	transaction, err := p.ParseSignedTransaction(signedTransactionInfo)
	if err != nil {
		p.log.Error("failed to validate apple webhook payload", "error", err)
		return false
	}
	order, err := transaction.ProviderOrder()
	if err != nil {
		p.log.Error("failed to resolve apple transaction", "error", err)
		return false
	}
	p.log.Info("apple webhook transaction resolved",
		"transaction_id", order.ID, "product_id", order.SubscriptionID,
		"quantity", order.Quantity, "is_testing", order.IsTesting)
	if process != nil {
		if err := process(ctx, order); err != nil {
			p.log.Error("error processing apple transaction", "transaction", order.ID, "error", err)
		}
	}
	return true
}

func dataString(data *AppleNotificationData, getter func(*AppleNotificationData) *string) string {
	if data == nil {
		return ""
	}
	return stringValue(getter(data))
}

// shouldProcessNotification mirrors ShouldProcessNotification.
func (p *AppleProvider) shouldProcessNotification(notificationType string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(notificationType))
	if normalized == "" {
		return false
	}
	_, ok := appleTransactionGrantingNotificationTypes[normalized]
	return ok
}

// verifyJWS mirrors VerifyAndDecodeJws: it validates the x5c chain and the
// ES256 signature and returns the payload plus the signed date.
func (p *AppleProvider) verifyJWS(compactJWS string) ([]byte, time.Time, error) {
	parts := strings.Split(compactJWS, ".")
	if len(parts) != 3 {
		return nil, time.Time{}, errors.New("Apple signed payload must be a compact JWS with three parts.")
	}
	headerBytes, err := decodeBase64URL(parts[0])
	if err != nil {
		return nil, time.Time{}, err
	}
	payloadBytes, err := decodeBase64URL(parts[1])
	if err != nil {
		return nil, time.Time{}, err
	}
	signatureBytes, err := decodeBase64URL(parts[2])
	if err != nil {
		return nil, time.Time{}, err
	}
	var header AppleJWSHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, time.Time{}, errors.New("Apple JWS header could not be parsed.")
	}
	if !strings.EqualFold(header.Alg, "ES256") {
		return nil, time.Time{}, fmt.Errorf("Unsupported Apple JWS alg %s.", header.Alg)
	}
	if len(header.X5c) == 0 {
		return nil, time.Time{}, errors.New("Apple JWS header missing x5c certificate chain.")
	}
	signedAt := resolveAppleSignedAt(payloadBytes)

	certificates := make([]*x509.Certificate, 0, len(header.X5c))
	for _, encoded := range header.X5c {
		der, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("Apple JWS certificate could not be decoded: %w", err)
		}
		certificate, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("Apple JWS certificate could not be parsed: %w", err)
		}
		certificates = append(certificates, certificate)
	}
	leaf := certificates[0]
	if err := p.verifyCertificateChain(certificates, signedAt); err != nil {
		return nil, time.Time{}, err
	}
	if signedAt.Before(leaf.NotBefore) || signedAt.After(leaf.NotAfter) {
		return nil, time.Time{}, errors.New("Apple signing certificate was not valid at the signed date.")
	}
	publicKey, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, time.Time{}, errors.New("Apple signing certificate does not contain an ECDSA public key.")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(signatureBytes) != 64 {
		return nil, time.Time{}, errors.New("Apple JWS signature must be 64 bytes.")
	}
	r := new(big.Int).SetBytes(signatureBytes[:32])
	s := new(big.Int).SetBytes(signatureBytes[32:])
	if !ecdsa.Verify(publicKey, digest[:], r, s) {
		return nil, time.Time{}, errors.New("Apple JWS signature verification failed.")
	}
	return payloadBytes, signedAt, nil
}

// verifyCertificateChain mirrors BuildAppleCertificateChain: platform trust
// first, then a pinned-root rebuild.
func (p *AppleProvider) verifyCertificateChain(certificates []*x509.Certificate, signedAt time.Time) error {
	if len(certificates) == 0 {
		return errors.New("Apple certificate chain is empty")
	}
	leaf := certificates[0]
	intermediates := x509.NewCertPool()
	for _, certificate := range certificates[1:] {
		intermediates.AddCert(certificate)
	}
	options := x509.VerifyOptions{
		Intermediates: intermediates,
		CurrentTime:   signedAt,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}
	if roots, err := x509.SystemCertPool(); err == nil && roots != nil {
		options.Roots = roots
		if _, err := leaf.Verify(options); err == nil {
			p.log.Debug("apple certificate chain validated with platform trust", "subject", leaf.Subject.String())
			return nil
		}
	}
	if len(certificates) == 1 {
		return errors.New("Apple certificate chain validation failed: no root certificate in x5c")
	}
	root := certificates[len(certificates)-1]
	fingerprint := strings.ToUpper(hex.EncodeToString(sha256Bytes(root.Raw)))
	p.log.Warn("retrying apple certificate chain validation with custom root trust",
		"root_subject", root.Subject.String(), "fingerprint", fingerprint)
	if _, trusted := p.trustedRoots[fingerprint]; !trusted {
		return fmt.Errorf("Apple certificate root fingerprint %s is not in the trusted root list", fingerprint)
	}
	rootPool := x509.NewCertPool()
	rootPool.AddCert(root)
	customIntermediates := x509.NewCertPool()
	for _, certificate := range certificates[1 : len(certificates)-1] {
		customIntermediates.AddCert(certificate)
	}
	options.Roots = rootPool
	options.Intermediates = customIntermediates
	if _, err := leaf.Verify(options); err != nil {
		return fmt.Errorf("Apple certificate chain validation failed: %w", err)
	}
	p.log.Info("apple certificate chain custom-root validation succeeded")
	return nil
}

func sha256Bytes(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

// resolveAppleSignedAt mirrors ResolveSignedAt: the payload's signedDate in
// milliseconds, or now when absent.
func resolveAppleSignedAt(payloadBytes []byte) time.Time {
	var envelope struct {
		SignedDate json.RawMessage `json:"signedDate"`
	}
	if err := json.Unmarshal(payloadBytes, &envelope); err != nil || len(envelope.SignedDate) == 0 {
		return time.Now().UTC()
	}
	raw := strings.TrimSpace(string(envelope.SignedDate))
	if raw == "null" {
		return time.Now().UTC()
	}
	raw = strings.Trim(raw, `"`)
	milliseconds, err := parseInt64(raw)
	if err != nil {
		return time.Now().UTC()
	}
	return time.UnixMilli(milliseconds).UTC()
}

func parseInt64(value string) (int64, error) {
	var parsed int64
	_, err := fmt.Sscan(value, &parsed)
	return parsed, err
}

// decodeBase64URL mirrors DecodeBase64Url.
func decodeBase64URL(value string) ([]byte, error) {
	normalized := strings.ReplaceAll(value, "-", "+")
	normalized = strings.ReplaceAll(normalized, "_", "/")
	switch len(normalized) % 4 {
	case 2:
		normalized += "=="
	case 3:
		normalized += "="
	}
	return base64.StdEncoding.DecodeString(normalized)
}

// validateBundleID mirrors ValidateBundleId.
func (p *AppleProvider) validateBundleID(bundleID string) error {
	if strings.TrimSpace(p.bundleID) == "" || strings.TrimSpace(bundleID) == "" {
		return nil
	}
	if p.bundleID != bundleID {
		return fmt.Errorf("Apple bundle id mismatch. Expected %s, got %s.", p.bundleID, bundleID)
	}
	return nil
}

// validateEnvironment mirrors ValidateEnvironment.
func (p *AppleProvider) validateEnvironment(environment string) error {
	if strings.TrimSpace(environment) == "" {
		return nil
	}
	actual := strings.ToUpper(strings.TrimSpace(environment))
	if _, ok := p.environments[actual]; ok {
		return nil
	}
	if actual == "PRODUCTION" {
		if _, ok := p.environments["PROD"]; ok {
			return nil
		}
	}
	accepted := make([]string, 0, len(p.environments))
	for value := range p.environments {
		accepted = append(accepted, value)
	}
	return fmt.Errorf("Apple environment mismatch. Accepted environments: %s, got %s.",
		strings.Join(accepted, ", "), environment)
}
