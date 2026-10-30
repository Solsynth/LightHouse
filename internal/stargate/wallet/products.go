// Wallet products: the WalletProductService and AppProductService ports.
//
// C# sources:
//
//	../DysonNetwork/DysonNetwork.Wallet/Payment/WalletProductService.cs
//	../DysonNetwork/DysonNetwork.Wallet/Payment/AppProductService.cs
//	../DysonNetwork/DysonNetwork.Wallet/Models/WalletProductOptions.cs
//
// The wallet product is the golds resupply pack: a provider (Paddle/Afdian)
// purchase that credits golds to the buyer's account wallet. The C# service
// read its definition from configuration (Payment:Product:GoldCurrency) and
// called the Paddle/Afdian handlers directly; this port reads the same
// definition from the module settings and reaches the providers through the
// PaddleCheckoutProvider/AfdianCheckoutProvider interfaces wired with
// SetPaddle/SetAfdian.
package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// GoldsResupplyPackKey is WalletProductService.GoldsResupplyPackKey.
const GoldsResupplyPackKey = "golds_resupply_pack"

// internalAppIdentifier is SnWalletOrder.InternalAppIdentifier.
const internalAppIdentifier = "internal"

// Defaults from WalletProductDefinitionOptions.
const (
	defaultWalletProductIdentifier  = "wallet.golds_resupply_pack"
	defaultWalletProductDisplayName = "Golds Resupply Pack"
)

// ProviderMappings maps a provider name to its provider reference → golds
// amount table (WalletProductDefinitionOptions.ProviderMappings).
type ProviderMappings map[string]map[string]decimal.Decimal

// ProductDefinition is WalletProductDefinitionOptions.
type ProductDefinition struct {
	Identifier       string
	DisplayName      string
	Currency         string
	ProviderMappings ProviderMappings
}

// MappingsFor returns the provider's reference table, matched case
// insensitively like the C# FirstOrDefault on the provider key.
func (d ProductDefinition) MappingsFor(provider string) map[string]decimal.Decimal {
	for name, mappings := range d.ProviderMappings {
		if strings.EqualFold(name, provider) {
			return mappings
		}
	}
	return nil
}

// PaddleCheckoutProvider is the PaddleProvider surface the wallet product
// checkout needs (PaddlePaymentHandler.CreateCheckoutAsync).
type PaddleCheckoutProvider interface {
	CreateCheckout(ctx context.Context, priceID string, customData map[string]any) (*PaddleCheckoutSession, error)
}

// AfdianCheckoutProvider is the AfdianProvider surface the wallet product
// checkout needs.
type AfdianCheckoutProvider interface {
	GetCheckoutPlanID() (string, error)
	CreateCheckoutURL(planID, skuID string, customOrderID *string, quantity int) (string, error)
}

// Products implements the wallet product purchase flow. It also satisfies the
// Subscriptions ProviderOrderProcessor so provider webhooks can hand golds
// resupply pack purchases over to this service.
type Products struct {
	svc      *Service
	settings map[string]any
	paddle   PaddleCheckoutProvider
	afdian   AfdianCheckoutProvider
}

// Process-wide settings default. The module registry wires the wallet
// surfaces from Deps, which has no field for the product service, so the HTTP
// registration installs the module settings once at startup and every
// Products instance (the subscription provider-order processor) falls back to
// them.
var (
	walletProductDefaultsMu sync.RWMutex
	walletProductSettings   map[string]any
)

// SetWalletProductSettings installs the module settings every Products
// instance falls back to (C# Payment:Product).
func SetWalletProductSettings(settings map[string]any) {
	walletProductDefaultsMu.Lock()
	defer walletProductDefaultsMu.Unlock()
	walletProductSettings = settings
}

func defaultWalletProductSettings() map[string]any {
	walletProductDefaultsMu.RLock()
	defer walletProductDefaultsMu.RUnlock()
	return walletProductSettings
}

// NewProducts builds the wallet product service.
func NewProducts(svc *Service) *Products {
	return &Products{svc: svc}
}

// newProductsFromDeps builds the service with the module settings applied,
// installs them as the process default and constructs the checkout providers
// the same way the subscription graph does.
func newProductsFromDeps(d *Deps) *Products {
	products := NewProducts(d.Service)
	products.SetSettings(d.Settings)
	SetWalletProductSettings(d.Settings)
	products.SetPaddle(newPaddleProvider(d.Settings, d.Log))
	products.SetAfdian(newAfdianProvider(d.Settings, d.Log))
	return products
}

// SetSettings installs the module settings the product definition is read
// from (C# Payment:Product).
func (p *Products) SetSettings(settings map[string]any) { p.settings = settings }

// SetPaddle installs the Paddle checkout provider.
func (p *Products) SetPaddle(provider PaddleCheckoutProvider) { p.paddle = provider }

// SetAfdian installs the Afdian checkout provider.
func (p *Products) SetAfdian(provider AfdianCheckoutProvider) { p.afdian = provider }

// settingsTable resolves the instance settings or the process default.
func (p *Products) settingsTable() map[string]any {
	if p.settings != nil {
		return p.settings
	}
	return defaultWalletProductSettings()
}

// walletProductSettingKey folds a settings key for case/separator-insensitive
// lookups ("goldCurrency" == "gold_currency" == "GoldCurrency").
func walletProductSettingKey(key string) string {
	key = strings.ToLower(key)
	key = strings.ReplaceAll(key, "_", "")
	return strings.ReplaceAll(key, "-", "")
}

// walletProductLookup walks a nested settings map by a dotted path; segment
// matching ignores case and separators.
func walletProductLookup(root map[string]any, path string) (any, bool) {
	current := any(root)
	for _, segment := range strings.Split(path, ".") {
		table, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		wanted := walletProductSettingKey(segment)
		found := false
		for key, value := range table {
			if walletProductSettingKey(key) == wanted {
				current = value
				found = true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	return current, true
}

// walletProductDefinitionValue resolves one product option, trying the C#
// Payment:Product:GoldCurrency path first and then the shorter forms.
func walletProductDefinitionValue(root map[string]any, field string) (any, bool) {
	for _, prefix := range []string{
		"payment.product.goldCurrency.",
		"product.goldCurrency.",
		"goldCurrency.",
		"",
	} {
		if value, ok := walletProductLookup(root, prefix+field); ok {
			return value, true
		}
	}
	return nil, false
}

// toDecimal converts a decoded settings scalar to a decimal.
func toDecimal(value any) (decimal.Decimal, bool) {
	switch typed := value.(type) {
	case decimal.Decimal:
		return typed, true
	case float64:
		return decimal.NewFromFloat(typed), true
	case float32:
		return decimal.NewFromFloat(float64(typed)), true
	case int:
		return decimal.NewFromInt(int64(typed)), true
	case int32:
		return decimal.NewFromInt(int64(typed)), true
	case int64:
		return decimal.NewFromInt(typed), true
	case string:
		parsed, err := decimal.NewFromString(strings.TrimSpace(typed))
		if err != nil {
			return decimal.Zero, false
		}
		return parsed, true
	default:
		return decimal.Zero, false
	}
}

// parseWalletProductMappings decodes the settings ProviderMappings table.
func parseWalletProductMappings(raw any) ProviderMappings {
	outer, ok := raw.(map[string]any)
	if !ok {
		return ProviderMappings{}
	}
	mappings := make(ProviderMappings, len(outer))
	for provider, value := range outer {
		inner, ok := value.(map[string]any)
		if !ok {
			continue
		}
		table := make(map[string]decimal.Decimal, len(inner))
		for reference, amount := range inner {
			if parsed, ok := toDecimal(amount); ok {
				table[reference] = parsed
			}
		}
		mappings[provider] = table
	}
	return mappings
}

// GoldsResupplyPackDefinition returns the configured golds resupply pack
// (WalletProductService.GetGoldsResupplyPackDefinition).
func (p *Products) GoldsResupplyPackDefinition() ProductDefinition {
	definition := ProductDefinition{
		Identifier:       defaultWalletProductIdentifier,
		DisplayName:      defaultWalletProductDisplayName,
		Currency:         CurrencyGolds,
		ProviderMappings: ProviderMappings{},
	}

	table := p.settingsTable()
	if table == nil {
		return definition
	}
	if value, ok := walletProductDefinitionValue(table, "identifier"); ok {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			definition.Identifier = strings.TrimSpace(text)
		}
	}
	if value, ok := walletProductDefinitionValue(table, "displayName"); ok {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			definition.DisplayName = strings.TrimSpace(text)
		}
	}
	if value, ok := walletProductDefinitionValue(table, "currency"); ok {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			definition.Currency = strings.TrimSpace(text)
		}
	}
	if value, ok := walletProductDefinitionValue(table, "providerMappings"); ok {
		definition.ProviderMappings = parseWalletProductMappings(value)
	}
	return definition
}

// PreparePaddleGoldsResupplyPack resolves the Paddle price mapping.
func (p *Products) PreparePaddleGoldsResupplyPack(providerReference *string) (ProductDefinition, string, decimal.Decimal, error) {
	return p.resolveProductReference(PaymentPaddle, providerReference)
}

// PrepareAfdianGoldsResupplyPack resolves the Afdian plan mapping.
func (p *Products) PrepareAfdianGoldsResupplyPack(providerReference *string) (ProductDefinition, string, decimal.Decimal, error) {
	return p.resolveProductReference(PaymentAfdian, providerReference)
}

// resolveProductReference mirrors WalletProductService.ResolveProductReference.
func (p *Products) resolveProductReference(provider string, providerReference *string) (ProductDefinition, string, decimal.Decimal, error) {
	product := p.GoldsResupplyPackDefinition()
	mappings := product.MappingsFor(provider)
	if len(mappings) == 0 {
		return product, "", decimal.Zero,
			fmt.Errorf("No %s mapping was configured for %s.", provider, product.DisplayName)
	}
	if providerReference != nil && strings.TrimSpace(*providerReference) != "" {
		reference := strings.TrimSpace(*providerReference)
		for key, amount := range mappings {
			if strings.EqualFold(key, reference) {
				return product, key, amount, nil
			}
		}
		return product, "", decimal.Zero,
			fmt.Errorf("Provider reference %s was not configured for %s on %s.", reference, product.DisplayName, provider)
	}
	// C# takes the dictionary's first entry. A Go map has no order, so pick
	// the lexicographically smallest reference for a deterministic default.
	keys := make([]string, 0, len(mappings))
	for key := range mappings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	reference := keys[0]
	return product, reference, mappings[reference], nil
}

// IsGoldCurrencyPurchase reports whether a provider order is a golds resupply
// pack purchase (WalletProductService.IsGoldCurrencyPurchase).
func (p *Products) IsGoldCurrencyPurchase(order ProviderOrder) bool {
	_, _, _, err := p.resolveProductReference(order.Provider, &order.SubscriptionID)
	return err == nil
}

// HandlesProviderOrder implements the Subscriptions ProviderOrderProcessor.
func (p *Products) HandlesProviderOrder(_ context.Context, order ProviderOrder) (bool, error) {
	return p.IsGoldCurrencyPurchase(order), nil
}

// ApplyProviderOrder implements the Subscriptions ProviderOrderProcessor by
// crediting the purchase (WalletProductService
// .CreateOrApplyGoldsResupplyPackPurchaseAsync).
func (p *Products) ApplyProviderOrder(ctx context.Context, order ProviderOrder, inbound *InboundOrderEntity, _ map[string]any) (any, error) {
	return p.CreateOrApplyGoldsResupplyPackPurchase(ctx, order, inbound)
}

// CreateOrApplyGoldsResupplyPackPurchase records a paid provider order as a
// wallet order and credits the golds.
func (p *Products) CreateOrApplyGoldsResupplyPackPurchase(ctx context.Context, order ProviderOrder, inbound *InboundOrderEntity) (*WalletOrderEntity, error) {
	if inbound == nil {
		return nil, errors.New("Provider order was not recorded.")
	}
	product, _, baseAmount, err := p.resolveProductReference(order.Provider, &order.SubscriptionID)
	if err != nil {
		return nil, err
	}
	quantity := order.Quantity
	if quantity <= 0 {
		quantity = 1
	}
	amount := baseAmount.Mul(decimal.NewFromInt(int64(quantity)))

	accountID, err := resolveProviderAccountID(order)
	if err != nil {
		return nil, err
	}
	wallet, _, err := p.svc.GetAccountWallet(ctx, accountID)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		wallet, err = p.svc.CreateWallet(ctx, &accountID, nil, nil)
		if err != nil {
			return nil, err
		}
	}

	var existing WalletOrderEntity
	err = p.svc.DB.WithContext(ctx).
		Where("inbound_order_id = ? AND app_identifier = ? AND product_identifier = ?",
			inbound.ID, internalAppIdentifier, product.Identifier).
		First(&existing).Error
	switch {
	case err == nil:
		return p.applyPaidWalletProductOrder(ctx, &existing)
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, err
	}

	remarks := fmt.Sprintf("wallet-product:%s", product.Identifier)
	created, err := p.svc.CreateOrder(ctx, OrderInput{
		PayeeWalletID:     &wallet.ID,
		Currency:          product.Currency,
		Amount:            amount,
		AppIdentifier:     strPtr(internalAppIdentifier),
		ProductIdentifier: &product.Identifier,
		Remarks:           &remarks,
		Meta: map[string]any{
			"wallet_product":     product.Identifier,
			"wallet_product_key": GoldsResupplyPackKey,
			"account_id":         accountID.String(),
			"display_name":       product.DisplayName,
			"quantity":           quantity,
		},
		InboundOrderID: &inbound.ID,
	})
	if err != nil {
		return nil, err
	}
	if err := p.svc.DB.WithContext(ctx).Model(&WalletOrderEntity{}).Where("id = ?", created.ID).
		Update("status", OrderPaid).Error; err != nil {
		return nil, err
	}
	created.Status = OrderPaid
	return p.applyPaidWalletProductOrder(ctx, created)
}

// resolveProviderAccountID mirrors ResolveAccountIdForOrderAsync. The C#
// service first asked the account service for the account linked to the
// provider identity; Stargate has no account-service client in the wallet
// graph, so the Afdian custom_order_id and the UUID-shaped account identifier
// are the only resolution paths.
func resolveProviderAccountID(order ProviderOrder) (uuid.UUID, error) {
	if custom := strings.TrimSpace(order.CustomOrderID); custom != "" {
		if parsed, err := uuid.Parse(custom); err == nil {
			return parsed, nil
		}
	}
	if account := strings.TrimSpace(order.AccountID); account != "" {
		if parsed, err := uuid.Parse(account); err == nil {
			return parsed, nil
		}
	}
	return uuid.Nil, errors.New("Unable to resolve the account for this wallet product purchase.")
}

// ApplyPaidOrder is the OrderPaidHook entry point. The C# event listener only
// handled orders whose meta carried "wallet_product"; this hook uses the same
// marker plus the product identifier and ignores every other order.
func (p *Products) ApplyPaidOrder(ctx context.Context, order *WalletOrderEntity) error {
	if order == nil {
		return nil
	}
	product := p.GoldsResupplyPackDefinition()
	if !strings.EqualFold(deref(order.ProductIdentifier), product.Identifier) {
		return nil
	}
	if !hasWalletProductMeta(order.Meta) {
		return nil
	}
	_, err := p.applyPaidWalletProductOrder(ctx, order)
	return err
}

// hasWalletProductMeta reports whether an order carries the wallet-product
// marker the C# order-paid listener keyed on.
func hasWalletProductMeta(raw datatypes.JSON) bool {
	if len(raw) == 0 {
		return false
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		return false
	}
	_, ok := meta["wallet_product"]
	return ok
}

// ApplyPaidWalletProductOrder applies one paid wallet product order by id
// (WalletProductService.ApplyPaidWalletProductOrderAsync(Guid)).
func (p *Products) ApplyPaidWalletProductOrder(ctx context.Context, orderID uuid.UUID) (*WalletOrderEntity, error) {
	var order WalletOrderEntity
	if err := p.svc.DB.WithContext(ctx).First(&order, "id = ?", orderID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("Wallet product order not found.")
		}
		return nil, err
	}
	return p.applyPaidWalletProductOrder(ctx, &order)
}

// applyPaidWalletProductOrder credits the payee wallet exactly once and marks
// the order finished.
func (p *Products) applyPaidWalletProductOrder(ctx context.Context, order *WalletOrderEntity) (*WalletOrderEntity, error) {
	product := p.GoldsResupplyPackDefinition()
	if !strings.EqualFold(deref(order.ProductIdentifier), product.Identifier) {
		return nil, errors.New("Unsupported wallet product order.")
	}
	if order.Status == OrderFinished && order.TransactionID != nil {
		return order, nil
	}
	if order.Status != OrderPaid {
		return nil, errors.New("Wallet product order must be paid before it can be applied.")
	}
	if order.PayeeWalletID == nil {
		return nil, errors.New("Wallet product order is missing a payee wallet.")
	}

	if order.TransactionID == nil {
		remarks := fmt.Sprintf("%s deposit", product.DisplayName)
		transaction, err := p.svc.CreateTransaction(ctx, TransactionInput{
			PayeeWalletID: order.PayeeWalletID,
			Currency:      order.Currency,
			Amount:        order.Amount,
			Remarks:       &remarks,
			Type:          TransactionSystem,
			Silent:        true,
		})
		if err != nil {
			return nil, err
		}
		order.TransactionID = &transaction.ID
	}

	order.Status = OrderFinished
	if err := p.svc.DB.WithContext(ctx).Model(&WalletOrderEntity{}).Where("id = ?", order.ID).
		Updates(map[string]any{
			"status":         OrderFinished,
			"transaction_id": *order.TransactionID,
		}).Error; err != nil {
		return nil, err
	}
	p.notifyWalletProductApplied(ctx, order, product)
	return order, nil
}

// notifyWalletProductApplied mirrors NotifyWalletProductAppliedAsync. The C#
// push used localized strings; this port sends the English defaults because
// the wallet graph has no localization service.
func (p *Products) notifyWalletProductApplied(ctx context.Context, order *WalletOrderEntity, product ProductDefinition) {
	if order.PayeeWalletID == nil {
		return
	}
	var wallet WalletEntity
	if err := p.svc.DB.WithContext(ctx).First(&wallet, "id = ?", *order.PayeeWalletID).Error; err != nil {
		return
	}
	if wallet.AccountID == nil {
		return
	}
	balance := order.Amount
	var pocket WalletPocketEntity
	if err := p.svc.DB.WithContext(ctx).
		Where("wallet_id = ? AND currency = ?", *order.PayeeWalletID, order.Currency).
		First(&pocket).Error; err == nil {
		balance = pocket.Amount
	}

	p.svc.notifyAccount(ctx, wallet.AccountID, "wallets.products.applied", map[string]any{
		"order_id":           order.ID.String(),
		"product_identifier": product.Identifier,
		"amount":             order.Amount,
		"currency":           order.Currency,
		"balance":            balance,
	})

	if p.svc.Push == nil || !p.svc.Push.Available() {
		return
	}
	body := fmt.Sprintf(
		"Your purchase of %s has been applied: +%s %s. New balance: %s.",
		product.DisplayName, order.Amount.String(), order.Currency, balance.String(),
	)
	if err := p.svc.Push.SendToUser(ctx, wallet.AccountID.String(), "Wallet product applied", body); err != nil {
		p.svc.Log.Warn("wallet product push failed", "account", wallet.AccountID, "error", err)
	}
}
