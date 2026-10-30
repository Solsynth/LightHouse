package wallet

// Subscription catalog: definitions, payment/gift policies and the
// configuration seed. Ported from DysonNetwork.Wallet's
// Payment/SubscriptionCatalogService.cs and Models/SubscriptionCatalog.cs.
//
// The C# service reads its catalog from the `Payment:SubscriptionCatalog`
// configuration section. Stargate stores that section in the wallet module's
// settings map (`[modules.settings."<wallet module id>"]`), so the same
// `subscriptionCatalog` table shape is accepted here; see settingsLookup for
// the key matching rules.

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/model"
)

// SubscriptionPaymentPolicy mirrors SubscriptionPaymentPolicy.
type SubscriptionPaymentPolicy struct {
	AllowInternalWallet        bool     `json:"allow_internal_wallet"`
	AllowExternal              bool     `json:"allow_external"`
	AllowInternalWalletRenewal bool     `json:"allow_internal_wallet_renewal"`
	AllowedMethods             []string `json:"allowed_methods"`
}

// SubscriptionDisplayConfig mirrors SubscriptionDisplayConfig.
type SubscriptionDisplayConfig struct {
	Color           *string `json:"color"`
	BackgroundColor *string `json:"background_color"`
	BadgeText       *string `json:"badge_text"`
}

// SubscriptionGiftPolicy mirrors SubscriptionGiftPolicy.
type SubscriptionGiftPolicy struct {
	AllowPurchase               bool `json:"allow_purchase"`
	MinimumAccountLevel         *int `json:"minimum_account_level"`
	AllowPerkSubscriptionBypass bool `json:"allow_perk_subscription_bypass"`
	RollingPurchaseLimit        *int `json:"rolling_purchase_limit"`
	RollingWindowDays           *int `json:"rolling_window_days"`
	GiftDurationDays            *int `json:"gift_duration_days"`
	SubscriptionDurationDays    *int `json:"subscription_duration_days"`
}

// Clone copies the policy so callers cannot mutate a shared default.
func (p SubscriptionGiftPolicy) Clone() SubscriptionGiftPolicy {
	clone := p
	clone.MinimumAccountLevel = copyInt(p.MinimumAccountLevel)
	clone.RollingPurchaseLimit = copyInt(p.RollingPurchaseLimit)
	clone.RollingWindowDays = copyInt(p.RollingWindowDays)
	clone.GiftDurationDays = copyInt(p.GiftDurationDays)
	clone.SubscriptionDurationDays = copyInt(p.SubscriptionDurationDays)
	return clone
}

// Merge overlays the definition's gift policy onto the configured defaults.
// A nil override keeps the default for every field, matching
// SubscriptionGiftPolicy.Merge.
func (p SubscriptionGiftPolicy) Merge(overrides *SubscriptionGiftPolicy) SubscriptionGiftPolicy {
	if overrides == nil {
		return p.Clone()
	}
	merged := SubscriptionGiftPolicy{
		AllowPurchase:               overrides.AllowPurchase,
		MinimumAccountLevel:         copyInt(overrides.MinimumAccountLevel),
		AllowPerkSubscriptionBypass: overrides.AllowPerkSubscriptionBypass,
		RollingPurchaseLimit:        copyInt(overrides.RollingPurchaseLimit),
		RollingWindowDays:           copyInt(overrides.RollingWindowDays),
		GiftDurationDays:            copyInt(overrides.GiftDurationDays),
		SubscriptionDurationDays:    copyInt(overrides.SubscriptionDurationDays),
	}
	if merged.MinimumAccountLevel == nil {
		merged.MinimumAccountLevel = copyInt(p.MinimumAccountLevel)
	}
	if merged.RollingPurchaseLimit == nil {
		merged.RollingPurchaseLimit = copyInt(p.RollingPurchaseLimit)
	}
	if merged.RollingWindowDays == nil {
		merged.RollingWindowDays = copyInt(p.RollingWindowDays)
	}
	if merged.GiftDurationDays == nil {
		merged.GiftDurationDays = copyInt(p.GiftDurationDays)
	}
	if merged.SubscriptionDurationDays == nil {
		merged.SubscriptionDurationDays = copyInt(p.SubscriptionDurationDays)
	}
	return merged
}

func copyInt(value *int) *int {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func defaultPaymentPolicy() SubscriptionPaymentPolicy {
	return SubscriptionPaymentPolicy{AllowInternalWallet: true, AllowExternal: true, AllowedMethods: []string{}}
}

func defaultGiftPolicy() SubscriptionGiftPolicy {
	return SubscriptionGiftPolicy{AllowPurchase: true, AllowPerkSubscriptionBypass: true}
}

// IsPaymentMethodAllowed mirrors SnWalletSubscriptionDefinition.IsPaymentMethodAllowed.
func (p SubscriptionPaymentPolicy) IsPaymentMethodAllowed(paymentMethod string) bool {
	if strings.TrimSpace(paymentMethod) == "" {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(paymentMethod))
	if len(p.AllowedMethods) > 0 && !containsFold(p.AllowedMethods, normalized) {
		return false
	}
	if normalized == PaymentInAppWallet {
		return p.AllowInternalWallet
	}
	if normalized == PaymentGift {
		return len(p.AllowedMethods) == 0 || containsFold(p.AllowedMethods, normalized)
	}
	return p.AllowExternal
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

// SubscriptionCatalogSeedSettings mirrors SubscriptionCatalogSeedSettings.
type SubscriptionCatalogSeedSettings struct {
	GiftPolicyDefaults                            SubscriptionGiftPolicy
	DefaultSubscriptionDurationDays               int
	LegacyInAppAvailabilityValidationDelaySeconds int
	SponsorRewardEligiblePaymentMethods           []string
}

func defaultSeedSettings() SubscriptionCatalogSeedSettings {
	return SubscriptionCatalogSeedSettings{
		GiftPolicyDefaults:                            defaultGiftPolicy(),
		DefaultSubscriptionDurationDays:               30,
		LegacyInAppAvailabilityValidationDelaySeconds: 180,
		SponsorRewardEligiblePaymentMethods:           []string{PaymentAfdian, PaymentPaddle, PaymentAppleStore},
	}
}

// SubscriptionCatalogSeedDefinition mirrors SubscriptionCatalogSeedDefinition.
type SubscriptionCatalogSeedDefinition struct {
	Identifier           string
	GroupIdentifier      *string
	DisplayName          string
	Currency             string
	BasePrice            decimal.Decimal
	PerkLevel            int
	MinimumAccountLevel  *int
	ExperienceMultiplier *decimal.Decimal
	GoldenPointReward    *int
	DisplayConfig        *SubscriptionDisplayConfig
	PaymentPolicy        SubscriptionPaymentPolicy
	GiftPolicy           *SubscriptionGiftPolicy
	ProviderMappings     map[string][]string
	AppIdentifier        *string
}

// SubscriptionCatalogSeedOptions mirrors SubscriptionCatalogSeedOptions.
type SubscriptionCatalogSeedOptions struct {
	Settings    SubscriptionCatalogSeedSettings
	Definitions []SubscriptionCatalogSeedDefinition
}

// SubscriptionDefinition is the wire shape of SnWalletSubscriptionDefinition.
type SubscriptionDefinition struct {
	ID                   string                     `json:"id"`
	Identifier           string                     `json:"identifier"`
	GroupIdentifier      *string                    `json:"group_identifier"`
	DisplayName          string                     `json:"display_name"`
	Currency             string                     `json:"currency"`
	BasePrice            decimal.Decimal            `json:"base_price"`
	PerkLevel            int                        `json:"perk_level"`
	MinimumAccountLevel  *int                       `json:"minimum_account_level"`
	ExperienceMultiplier *decimal.Decimal           `json:"experience_multiplier"`
	GoldenPointReward    *int                       `json:"golden_point_reward"`
	DisplayConfig        *SubscriptionDisplayConfig `json:"display_config"`
	PaymentPolicy        SubscriptionPaymentPolicy  `json:"payment_policy"`
	GiftPolicy           *SubscriptionGiftPolicy    `json:"gift_policy"`
	ProviderMappings     map[string][]string        `json:"provider_mappings"`
	AppIdentifier        *string                    `json:"app_identifier"`
	CreatedAt            *model.Time                `json:"created_at"`
	UpdatedAt            *model.Time                `json:"updated_at"`
	DeletedAt            *model.Time                `json:"deleted_at"`
}

// SubscriptionDefinitionInput is the admin upsert payload (snake_case wire,
// identical to UpsertSubscriptionDefinitionRequest).
type SubscriptionDefinitionInput struct {
	Identifier           string
	GroupIdentifier      *string
	DisplayName          string
	Currency             string
	BasePrice            decimal.Decimal
	PerkLevel            int
	MinimumAccountLevel  *int
	ExperienceMultiplier *decimal.Decimal
	GoldenPointReward    *int
	DisplayConfig        *SubscriptionDisplayConfig
	PaymentPolicy        SubscriptionPaymentPolicy
	GiftPolicy           *SubscriptionGiftPolicy
	ProviderMappings     map[string][]string
	AppIdentifier        *string
}

// SubscriptionDefinitionGroup is one group of definitions, keyed by the
// group identifier (or the identifier itself when the group is empty).
type SubscriptionDefinitionGroup struct {
	GroupIdentifier string
	Definitions     []WalletSubscriptionDefinitionEntity
}

// providerAliases mirrors SubscriptionCatalogService.ProviderAliases.
var providerAliases = map[string][]string{
	PaymentAppleStore:  {"AppleStore", "apple_store", "applestore", "apple", "app_store"},
	PaymentPaddle:      {"Paddle", "paddle"},
	PaymentAfdian:      {"Afdian", "afdian"},
	PaymentInAppWallet: {"solian.wallet", "wallet", "in_app_wallet", "inappwallet"},
	PaymentGift:        {"gift", "Gift"},
}

// The jsonb policy columns were written by EF Core with System.Text.Json's
// default (PascalCase) naming, so stored documents use PascalCase keys while
// the HTTP wire shape uses snake_case (the wallet controllers configure
// JsonNamingPolicy.SnakeCaseLower). Parsing therefore matches keys without
// case or separators; serialization keeps the C# EF layout so a legacy
// database and the new writer agree.

func parsePaymentPolicy(raw datatypes.JSON) SubscriptionPaymentPolicy {
	policy := defaultPaymentPolicy()
	table := jsonTable(raw)
	if value, ok := tableValue(table, "allowInternalWallet"); ok {
		if flag, ok := value.(bool); ok {
			policy.AllowInternalWallet = flag
		}
	}
	if value, ok := tableValue(table, "allowExternal"); ok {
		if flag, ok := value.(bool); ok {
			policy.AllowExternal = flag
		}
	}
	if value, ok := tableValue(table, "allowInternalWalletRenewal"); ok {
		if flag, ok := value.(bool); ok {
			policy.AllowInternalWalletRenewal = flag
		}
	}
	if value, ok := tableValue(table, "allowedMethods"); ok {
		if methods, ok := toStringSlice(value); ok {
			policy.AllowedMethods = methods
		}
	}
	if policy.AllowedMethods == nil {
		policy.AllowedMethods = []string{}
	}
	return policy
}

func paymentPolicyJSON(policy SubscriptionPaymentPolicy) datatypes.JSON {
	methods := policy.AllowedMethods
	if methods == nil {
		methods = []string{}
	}
	return datatypes.JSON(mustJSON(map[string]any{
		"AllowInternalWallet":        policy.AllowInternalWallet,
		"AllowExternal":              policy.AllowExternal,
		"AllowInternalWalletRenewal": policy.AllowInternalWalletRenewal,
		"AllowedMethods":             methods,
	}))
}

func parseDisplayConfig(raw datatypes.JSON) *SubscriptionDisplayConfig {
	table := jsonTable(raw)
	if table == nil {
		return nil
	}
	config := SubscriptionDisplayConfig{}
	if value, ok := tableValue(table, "color"); ok {
		if text, ok := toStringValue(value); ok {
			config.Color = stringPointer(text)
		}
	}
	if value, ok := tableValue(table, "backgroundColor"); ok {
		if text, ok := toStringValue(value); ok {
			config.BackgroundColor = stringPointer(text)
		}
	}
	if value, ok := tableValue(table, "badgeText"); ok {
		if text, ok := toStringValue(value); ok {
			config.BadgeText = stringPointer(text)
		}
	}
	return &config
}

func displayConfigJSON(config *SubscriptionDisplayConfig) datatypes.JSON {
	if config == nil {
		return nil
	}
	record := map[string]any{}
	if config.Color != nil {
		record["Color"] = *config.Color
	}
	if config.BackgroundColor != nil {
		record["BackgroundColor"] = *config.BackgroundColor
	}
	if config.BadgeText != nil {
		record["BadgeText"] = *config.BadgeText
	}
	return datatypes.JSON(mustJSON(record))
}

func parseGiftPolicy(raw datatypes.JSON) *SubscriptionGiftPolicy {
	table := jsonTable(raw)
	if table == nil {
		return nil
	}
	policy := defaultGiftPolicy()
	if value, ok := tableValue(table, "allowPurchase"); ok {
		if flag, ok := value.(bool); ok {
			policy.AllowPurchase = flag
		}
	}
	if value, ok := tableValue(table, "allowPerkSubscriptionBypass"); ok {
		if flag, ok := value.(bool); ok {
			policy.AllowPerkSubscriptionBypass = flag
		}
	}
	if value, ok := tableValue(table, "minimumAccountLevel"); ok {
		if parsed, ok := toInt(value); ok {
			policy.MinimumAccountLevel = &parsed
		}
	}
	if value, ok := tableValue(table, "rollingPurchaseLimit"); ok {
		if parsed, ok := toInt(value); ok {
			policy.RollingPurchaseLimit = &parsed
		}
	}
	if value, ok := tableValue(table, "rollingWindowDays"); ok {
		if parsed, ok := toInt(value); ok {
			policy.RollingWindowDays = &parsed
		}
	}
	if value, ok := tableValue(table, "giftDurationDays"); ok {
		if parsed, ok := toInt(value); ok {
			policy.GiftDurationDays = &parsed
		}
	}
	if value, ok := tableValue(table, "subscriptionDurationDays"); ok {
		if parsed, ok := toInt(value); ok {
			policy.SubscriptionDurationDays = &parsed
		}
	}
	return &policy
}

func giftPolicyJSON(policy *SubscriptionGiftPolicy) datatypes.JSON {
	if policy == nil {
		return nil
	}
	record := map[string]any{
		"AllowPurchase":               policy.AllowPurchase,
		"AllowPerkSubscriptionBypass": policy.AllowPerkSubscriptionBypass,
	}
	if policy.MinimumAccountLevel != nil {
		record["MinimumAccountLevel"] = *policy.MinimumAccountLevel
	}
	if policy.RollingPurchaseLimit != nil {
		record["RollingPurchaseLimit"] = *policy.RollingPurchaseLimit
	}
	if policy.RollingWindowDays != nil {
		record["RollingWindowDays"] = *policy.RollingWindowDays
	}
	if policy.GiftDurationDays != nil {
		record["GiftDurationDays"] = *policy.GiftDurationDays
	}
	if policy.SubscriptionDurationDays != nil {
		record["SubscriptionDurationDays"] = *policy.SubscriptionDurationDays
	}
	return datatypes.JSON(mustJSON(record))
}

// jsonTable decodes a jsonb document into a generic table. It returns nil for
// empty or non-object documents.
func jsonTable(raw datatypes.JSON) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var table map[string]any
	if err := json.Unmarshal(raw, &table); err != nil {
		return nil
	}
	return table
}

// tableValue resolves a key in a decoded document ignoring case and
// separators.
func tableValue(table map[string]any, key string) (any, bool) {
	wanted := normalizeSettingKey(key)
	for candidate, value := range table {
		if normalizeSettingKey(candidate) == wanted {
			return value, true
		}
	}
	return nil, false
}

func parseProviderMappings(raw datatypes.JSON) map[string][]string {
	mappings := map[string][]string{}
	if table := jsonTable(raw); table != nil {
		for key, value := range table {
			if list, ok := toStringSlice(value); ok {
				mappings[key] = list
			}
		}
	}
	return mappings
}

func providerMappingsJSON(mappings map[string][]string) datatypes.JSON {
	if mappings == nil {
		mappings = map[string][]string{}
	}
	return datatypes.JSON(mustJSON(mappings))
}

// definitionModel converts a definition entity into its wire shape.
func definitionModel(e *WalletSubscriptionDefinitionEntity) SubscriptionDefinition {
	return SubscriptionDefinition{
		ID:                   uuidString(e.ID),
		Identifier:           e.Identifier,
		GroupIdentifier:      e.GroupIdentifier,
		DisplayName:          e.DisplayName,
		Currency:             e.Currency,
		BasePrice:            e.BasePrice,
		PerkLevel:            e.PerkLevel,
		MinimumAccountLevel:  e.MinimumAccountLevel,
		ExperienceMultiplier: e.ExperienceMultiplier,
		GoldenPointReward:    e.GoldenPointReward,
		DisplayConfig:        parseDisplayConfig(e.DisplayConfig),
		PaymentPolicy:        parsePaymentPolicy(e.PaymentPolicy),
		GiftPolicy:           parseGiftPolicy(e.GiftPolicy),
		ProviderMappings:     parseProviderMappings(e.ProviderMappings),
		AppIdentifier:        e.AppIdentifier,
		CreatedAt:            timeValue(e.CreatedAt),
		UpdatedAt:            timeValue(e.UpdatedAt),
		DeletedAt:            timePointer(deletedTime(e.DeletedAt)),
	}
}

// ── settings ───────────────────────────────────────────────────────────────

// normalizeSettingKey folds case and drops separators so "plan_id",
// "planId" and "Plan-Id" all match.
func normalizeSettingKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, "_", "")
	return strings.ReplaceAll(key, "-", "")
}

// lookupValue walks a nested settings map by a dotted path. Segment matching
// ignores case and separators.
func lookupValue(root map[string]any, path string) (any, bool) {
	current := any(root)
	for _, segment := range strings.Split(path, ".") {
		table, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		wanted := normalizeSettingKey(segment)
		found := false
		for key, value := range table {
			if normalizeSettingKey(key) == wanted {
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

// settingString returns the first configured string among paths.
func settingString(settings map[string]any, paths ...string) string {
	for _, path := range paths {
		if value, ok := lookupValue(settings, path); ok {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				return text
			}
		}
	}
	return ""
}

// settingStrings returns the first configured string list among paths.
func settingStrings(settings map[string]any, paths ...string) []string {
	for _, path := range paths {
		if value, ok := lookupValue(settings, path); ok {
			if list, ok := toStringSlice(value); ok {
				return list
			}
		}
	}
	return nil
}

func toStringSlice(value any) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		return typed, true
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result, true
	default:
		return nil, false
	}
}

func settingsTable(value any) (map[string]any, bool) {
	table, ok := value.(map[string]any)
	return table, ok
}

func settingInt(table map[string]any, key string) (*int, bool) {
	value, ok := table[key]
	if !ok {
		return nil, false
	}
	parsed, ok := toInt(value)
	if !ok {
		return nil, false
	}
	return &parsed, true
}

func toInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case int32:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil
	default:
		return 0, false
	}
}

func toStringValue(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case json.Number:
		return typed.String(), true
	default:
		return "", false
	}
}

func toDecimalValue(value any) (decimal.Decimal, bool) {
	switch typed := value.(type) {
	case decimal.Decimal:
		return typed, true
	case float64:
		return decimal.NewFromFloat(typed), true
	case int:
		return decimal.NewFromInt(int64(typed)), true
	case int64:
		return decimal.NewFromInt(typed), true
	case string:
		parsed, err := decimal.NewFromString(typed)
		return parsed, err == nil
	case json.Number:
		parsed, err := decimal.NewFromString(typed.String())
		return parsed, err == nil
	default:
		return decimal.Zero, false
	}
}

func stringPointer(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

// seedValue resolves a key in a decoded settings table ignoring case and
// separators, so appsettings-style (PascalCase) and TOML-style keys both work.
func seedValue(table map[string]any, key string) any {
	value, _ := tableValue(table, key)
	return value
}

// parseSeedPaymentPolicy reads a policy table with the C# defaults applied.
func parseSeedPaymentPolicy(value any) SubscriptionPaymentPolicy {
	policy := defaultPaymentPolicy()
	table, ok := settingsTable(value)
	if !ok {
		return policy
	}
	if flag, ok := seedValue(table, "allowInternalWallet").(bool); ok {
		policy.AllowInternalWallet = flag
	}
	if flag, ok := seedValue(table, "allowExternal").(bool); ok {
		policy.AllowExternal = flag
	}
	if flag, ok := seedValue(table, "allowInternalWalletRenewal").(bool); ok {
		policy.AllowInternalWalletRenewal = flag
	}
	if methods, ok := toStringSlice(seedValue(table, "allowedMethods")); ok {
		policy.AllowedMethods = methods
	}
	return policy
}

func parseSeedGiftPolicy(value any) *SubscriptionGiftPolicy {
	table, ok := settingsTable(value)
	if !ok {
		return nil
	}
	policy := defaultGiftPolicy()
	if flag, ok := seedValue(table, "allowPurchase").(bool); ok {
		policy.AllowPurchase = flag
	}
	if flag, ok := seedValue(table, "allowPerkSubscriptionBypass").(bool); ok {
		policy.AllowPerkSubscriptionBypass = flag
	}
	if parsed, ok := settingInt(table, "minimumAccountLevel"); ok {
		policy.MinimumAccountLevel = parsed
	}
	if parsed, ok := settingInt(table, "rollingPurchaseLimit"); ok {
		policy.RollingPurchaseLimit = parsed
	}
	if parsed, ok := settingInt(table, "rollingWindowDays"); ok {
		policy.RollingWindowDays = parsed
	}
	if parsed, ok := settingInt(table, "giftDurationDays"); ok {
		policy.GiftDurationDays = parsed
	}
	if parsed, ok := settingInt(table, "subscriptionDurationDays"); ok {
		policy.SubscriptionDurationDays = parsed
	}
	return &policy
}

func parseSeedDisplayConfig(value any) *SubscriptionDisplayConfig {
	table, ok := settingsTable(value)
	if !ok {
		return nil
	}
	config := SubscriptionDisplayConfig{}
	if text, ok := toStringValue(seedValue(table, "color")); ok {
		config.Color = stringPointer(text)
	}
	if text, ok := toStringValue(seedValue(table, "backgroundColor")); ok {
		config.BackgroundColor = stringPointer(text)
	}
	if text, ok := toStringValue(seedValue(table, "badgeText")); ok {
		config.BadgeText = stringPointer(text)
	}
	return &config
}

func parseSeedProviderMappings(value any) map[string][]string {
	mappings := map[string][]string{}
	table, ok := settingsTable(value)
	if !ok {
		return mappings
	}
	for key, raw := range table {
		if list, ok := toStringSlice(raw); ok {
			mappings[key] = list
		}
	}
	return mappings
}

func parseSeedDefinitions(value any) []SubscriptionCatalogSeedDefinition {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	definitions := make([]SubscriptionCatalogSeedDefinition, 0, len(list))
	for _, raw := range list {
		table, ok := settingsTable(raw)
		if !ok {
			continue
		}
		identifier, _ := toStringValue(seedValue(table, "identifier"))
		if strings.TrimSpace(identifier) == "" {
			continue
		}
		displayName, _ := toStringValue(seedValue(table, "displayName"))
		currency, _ := toStringValue(seedValue(table, "currency"))
		definition := SubscriptionCatalogSeedDefinition{
			Identifier:       identifier,
			DisplayName:      displayName,
			Currency:         currency,
			PaymentPolicy:    defaultPaymentPolicy(),
			ProviderMappings: map[string][]string{},
		}
		if text, ok := toStringValue(seedValue(table, "groupIdentifier")); ok {
			definition.GroupIdentifier = stringPointer(text)
		}
		if amount, ok := toDecimalValue(seedValue(table, "basePrice")); ok {
			definition.BasePrice = amount
		}
		if level, ok := toInt(seedValue(table, "perkLevel")); ok {
			definition.PerkLevel = level
		}
		if parsed, ok := settingInt(table, "minimumAccountLevel"); ok {
			definition.MinimumAccountLevel = parsed
		}
		if multiplier, ok := toDecimalValue(seedValue(table, "experienceMultiplier")); ok {
			definition.ExperienceMultiplier = &multiplier
		}
		if reward, ok := toInt(seedValue(table, "goldenPointReward")); ok {
			definition.GoldenPointReward = &reward
		}
		definition.DisplayConfig = parseSeedDisplayConfig(seedValue(table, "displayConfig"))
		definition.PaymentPolicy = parseSeedPaymentPolicy(seedValue(table, "paymentPolicy"))
		definition.GiftPolicy = parseSeedGiftPolicy(seedValue(table, "giftPolicy"))
		definition.ProviderMappings = parseSeedProviderMappings(seedValue(table, "providerMappings"))
		if text, ok := toStringValue(seedValue(table, "appIdentifier")); ok {
			definition.AppIdentifier = stringPointer(text)
		}
		definitions = append(definitions, definition)
	}
	return definitions
}

// seedOptions reads Payment:SubscriptionCatalog from the wallet settings.
func (s *Subscriptions) seedOptions() (SubscriptionCatalogSeedOptions, bool) {
	options := SubscriptionCatalogSeedOptions{Settings: defaultSeedSettings()}
	raw, ok := lookupValue(s.Settings, "subscriptionCatalog")
	if !ok {
		return options, false
	}
	table, ok := settingsTable(raw)
	if !ok {
		return options, false
	}
	if settingsRaw, ok := settingsTable(seedValue(table, "settings")); ok {
		if giftRaw, ok := settingsTable(seedValue(settingsRaw, "giftPolicyDefaults")); ok {
			if policy := parseSeedGiftPolicy(giftRaw); policy != nil {
				options.Settings.GiftPolicyDefaults = *policy
			}
		}
		if days, ok := toInt(seedValue(settingsRaw, "defaultSubscriptionDurationDays")); ok && days > 0 {
			options.Settings.DefaultSubscriptionDurationDays = days
		}
		if delay, ok := toInt(seedValue(settingsRaw, "legacyInAppAvailabilityValidationDelaySeconds")); ok {
			options.Settings.LegacyInAppAvailabilityValidationDelaySeconds = delay
		}
		if methods, ok := toStringSlice(seedValue(settingsRaw, "sponsorRewardEligiblePaymentMethods")); ok {
			options.Settings.SponsorRewardEligiblePaymentMethods = methods
		}
	}
	options.Definitions = parseSeedDefinitions(seedValue(table, "definitions"))
	return options, true
}

// CatalogSettings returns the effective catalog settings (configured values
// over the C# defaults).
func (s *Subscriptions) CatalogSettings() SubscriptionCatalogSeedSettings {
	options, _ := s.seedOptions()
	return options.Settings
}

// DefaultSubscriptionDuration mirrors GetDefaultSubscriptionDuration.
func (s *Subscriptions) DefaultSubscriptionDuration() time.Duration {
	days := s.CatalogSettings().DefaultSubscriptionDurationDays
	if days <= 0 {
		days = 30
	}
	return time.Duration(days) * 24 * time.Hour
}

// IsSponsorRewardEligiblePaymentMethod mirrors
// IsSponsorRewardEligiblePaymentMethod.
func (s *Subscriptions) IsSponsorRewardEligiblePaymentMethod(paymentMethod string) bool {
	if strings.TrimSpace(paymentMethod) == "" {
		return false
	}
	return containsFold(s.CatalogSettings().SponsorRewardEligiblePaymentMethods, paymentMethod)
}

// ── catalog CRUD ───────────────────────────────────────────────────────────

func (s *Subscriptions) catalogDB(ctx context.Context) *gorm.DB {
	return s.svc.DB.WithContext(ctx)
}

// GetDefinition mirrors GetDefinitionAsync.
func (s *Subscriptions) GetDefinition(ctx context.Context, identifier string) (*WalletSubscriptionDefinitionEntity, error) {
	var definition WalletSubscriptionDefinitionEntity
	err := s.catalogDB(ctx).Where("identifier = ?", identifier).First(&definition).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &definition, nil
}

// ListDefinitions mirrors ListDefinitionsAsync (perk level, then display name).
func (s *Subscriptions) ListDefinitions(ctx context.Context) ([]WalletSubscriptionDefinitionEntity, error) {
	var definitions []WalletSubscriptionDefinitionEntity
	err := s.catalogDB(ctx).Order("perk_level ASC, display_name ASC").Find(&definitions).Error
	return definitions, err
}

// ListDefinitionGroups mirrors ListDefinitionGroupsAsync.
func (s *Subscriptions) ListDefinitionGroups(ctx context.Context) ([]SubscriptionDefinitionGroup, error) {
	definitions, err := s.ListDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	index := map[string]int{}
	groups := make([]SubscriptionDefinitionGroup, 0)
	for _, definition := range definitions {
		key := definition.Identifier
		if definition.GroupIdentifier != nil && strings.TrimSpace(*definition.GroupIdentifier) != "" {
			key = *definition.GroupIdentifier
		}
		position, ok := index[key]
		if !ok {
			position = len(groups)
			index[key] = position
			groups = append(groups, SubscriptionDefinitionGroup{GroupIdentifier: key})
		}
		groups[position].Definitions = append(groups[position].Definitions, definition)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		return strings.ToLower(groups[i].GroupIdentifier) < strings.ToLower(groups[j].GroupIdentifier)
	})
	return groups, nil
}

// ListDefinitionsByGroup mirrors ListDefinitionsByGroupAsync.
func (s *Subscriptions) ListDefinitionsByGroup(ctx context.Context, groupIdentifier string) ([]WalletSubscriptionDefinitionEntity, error) {
	if strings.TrimSpace(groupIdentifier) == "" {
		return []WalletSubscriptionDefinitionEntity{}, nil
	}
	var definitions []WalletSubscriptionDefinitionEntity
	err := s.catalogDB(ctx).
		Where("group_identifier = ? OR identifier = ?", groupIdentifier, groupIdentifier).
		Order("perk_level ASC, display_name ASC").
		Find(&definitions).Error
	return definitions, err
}

// ResolveDefinition maps a provider SKU to its definition, mirroring
// ResolveDefinitionAsync(provider, externalId).
func (s *Subscriptions) ResolveDefinition(ctx context.Context, provider, externalID string) (*WalletSubscriptionDefinitionEntity, error) {
	definitions, err := s.ListDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	for i := range definitions {
		mapped, ok := providerMappingsFor(definitions[i].ProviderMappings, provider)
		if !ok {
			continue
		}
		if containsFold(mapped, externalID) {
			return &definitions[i], nil
		}
	}
	return nil, nil
}

// GetProviderReference mirrors GetProviderReferenceAsync.
func (s *Subscriptions) GetProviderReference(ctx context.Context, identifier, provider string, preferred *string) (*string, error) {
	definition, err := s.GetDefinition(ctx, identifier)
	if err != nil || definition == nil {
		return nil, err
	}
	mapped, ok := providerMappingsFor(definition.ProviderMappings, provider)
	if !ok || len(mapped) == 0 {
		return nil, nil
	}
	if preferred != nil && strings.TrimSpace(*preferred) != "" {
		for _, candidate := range mapped {
			if strings.EqualFold(candidate, strings.TrimSpace(*preferred)) {
				value := candidate
				return &value, nil
			}
		}
		return nil, nil
	}
	if strings.EqualFold(provider, PaymentPaddle) {
		for _, candidate := range mapped {
			if strings.HasPrefix(strings.ToLower(candidate), "pri_") {
				value := candidate
				return &value, nil
			}
		}
	}
	value := mapped[0]
	return &value, nil
}

// GetGroupIdentifiers mirrors GetGroupIdentifiersAsync.
func (s *Subscriptions) GetGroupIdentifiers(ctx context.Context, groupIdentifier *string, fallbackIdentifier string) ([]string, error) {
	if groupIdentifier == nil || strings.TrimSpace(*groupIdentifier) == "" {
		return []string{fallbackIdentifier}, nil
	}
	var identifiers []string
	err := s.catalogDB(ctx).Model(&WalletSubscriptionDefinitionEntity{}).
		Where("group_identifier = ?", *groupIdentifier).
		Pluck("identifier", &identifiers).Error
	return identifiers, err
}

// GetGiftPolicy mirrors GetGiftPolicyAsync: configured defaults merged with
// the definition's own policy.
func (s *Subscriptions) GetGiftPolicy(definition *WalletSubscriptionDefinitionEntity) SubscriptionGiftPolicy {
	defaults := s.CatalogSettings().GiftPolicyDefaults.Clone()
	if definition == nil {
		return defaults
	}
	return defaults.Merge(parseGiftPolicy(definition.GiftPolicy))
}

// EnsureSeeded mirrors EnsureSeededAsync: create missing definitions from the
// configured catalog and update the ones that drifted.
func (s *Subscriptions) EnsureSeeded(ctx context.Context) error {
	options, ok := s.seedOptions()
	if !ok || len(options.Definitions) == 0 {
		return nil
	}
	identifiers := make([]string, 0, len(options.Definitions))
	for _, definition := range options.Definitions {
		identifiers = append(identifiers, definition.Identifier)
	}
	var existing []WalletSubscriptionDefinitionEntity
	if err := s.catalogDB(ctx).Where("identifier IN ?", identifiers).Find(&existing).Error; err != nil {
		return err
	}
	existingMap := make(map[string]*WalletSubscriptionDefinitionEntity, len(existing))
	for i := range existing {
		existingMap[existing[i].Identifier] = &existing[i]
	}
	changed := 0
	for _, seed := range options.Definitions {
		current, found := existingMap[seed.Identifier]
		if !found {
			entity := buildDefinitionEntity(seed)
			if err := s.catalogDB(ctx).Create(entity).Error; err != nil {
				return err
			}
			changed++
			continue
		}
		if applyDefinitionSeed(current, seed) {
			if err := s.catalogDB(ctx).Save(current).Error; err != nil {
				return err
			}
			changed++
		}
	}
	if changed > 0 {
		s.svc.Log.Info("synchronized subscription catalog definitions from settings", "changed", changed)
	}
	return nil
}

func buildDefinitionEntity(seed SubscriptionCatalogSeedDefinition) *WalletSubscriptionDefinitionEntity {
	return &WalletSubscriptionDefinitionEntity{
		ID:                   newID(),
		Identifier:           seed.Identifier,
		GroupIdentifier:      stringPointer(derefOrEmpty(seed.GroupIdentifier)),
		DisplayName:          seed.DisplayName,
		Currency:             seed.Currency,
		BasePrice:            seed.BasePrice,
		PerkLevel:            seed.PerkLevel,
		MinimumAccountLevel:  copyInt(seed.MinimumAccountLevel),
		ExperienceMultiplier: seed.ExperienceMultiplier,
		GoldenPointReward:    copyInt(seed.GoldenPointReward),
		DisplayConfig:        displayConfigJSON(seed.DisplayConfig),
		PaymentPolicy:        paymentPolicyJSON(seed.PaymentPolicy),
		GiftPolicy:           giftPolicyJSON(seed.GiftPolicy),
		ProviderMappings:     providerMappingsJSON(seed.ProviderMappings),
		AppIdentifier:        stringPointer(derefOrEmpty(seed.AppIdentifier)),
	}
}

func derefOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// applyDefinitionSeed copies every seed field onto the entity and reports
// whether anything changed, mirroring ApplyDefinitionUpdates.
func applyDefinitionSeed(entity *WalletSubscriptionDefinitionEntity, seed SubscriptionCatalogSeedDefinition) bool {
	changed := false
	group := stringPointer(derefOrEmpty(seed.GroupIdentifier))
	if !stringPointersEqual(entity.GroupIdentifier, group) {
		entity.GroupIdentifier = group
		changed = true
	}
	if entity.DisplayName != seed.DisplayName {
		entity.DisplayName = seed.DisplayName
		changed = true
	}
	if entity.Currency != seed.Currency {
		entity.Currency = seed.Currency
		changed = true
	}
	if !entity.BasePrice.Equal(seed.BasePrice) {
		entity.BasePrice = seed.BasePrice
		changed = true
	}
	if entity.PerkLevel != seed.PerkLevel {
		entity.PerkLevel = seed.PerkLevel
		changed = true
	}
	if !intPointersEqual(entity.MinimumAccountLevel, seed.MinimumAccountLevel) {
		entity.MinimumAccountLevel = copyInt(seed.MinimumAccountLevel)
		changed = true
	}
	if !decimalPointersEqual(entity.ExperienceMultiplier, seed.ExperienceMultiplier) {
		entity.ExperienceMultiplier = seed.ExperienceMultiplier
		changed = true
	}
	if !intPointersEqual(entity.GoldenPointReward, seed.GoldenPointReward) {
		entity.GoldenPointReward = copyInt(seed.GoldenPointReward)
		changed = true
	}
	if !displayConfigsEqual(parseDisplayConfig(entity.DisplayConfig), seed.DisplayConfig) {
		entity.DisplayConfig = displayConfigJSON(seed.DisplayConfig)
		changed = true
	}
	if !paymentPoliciesEqual(parsePaymentPolicy(entity.PaymentPolicy), seed.PaymentPolicy) {
		entity.PaymentPolicy = paymentPolicyJSON(seed.PaymentPolicy)
		changed = true
	}
	if !giftPoliciesEqual(parseGiftPolicy(entity.GiftPolicy), seed.GiftPolicy) {
		entity.GiftPolicy = giftPolicyJSON(seed.GiftPolicy)
		changed = true
	}
	if !providerMappingsEqual(parseProviderMappings(entity.ProviderMappings), seed.ProviderMappings) {
		entity.ProviderMappings = providerMappingsJSON(seed.ProviderMappings)
		changed = true
	}
	appIdentifier := stringPointer(derefOrEmpty(seed.AppIdentifier))
	if !stringPointersEqual(entity.AppIdentifier, appIdentifier) {
		entity.AppIdentifier = appIdentifier
		changed = true
	}
	return changed
}

func stringPointersEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func intPointersEqual(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func decimalPointersEqual(left, right *decimal.Decimal) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func displayConfigsEqual(left, right *SubscriptionDisplayConfig) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return stringPointersEqual(left.Color, right.Color) &&
		stringPointersEqual(left.BackgroundColor, right.BackgroundColor) &&
		stringPointersEqual(left.BadgeText, right.BadgeText)
}

func paymentPoliciesEqual(left, right SubscriptionPaymentPolicy) bool {
	if left.AllowInternalWallet != right.AllowInternalWallet ||
		left.AllowExternal != right.AllowExternal ||
		left.AllowInternalWalletRenewal != right.AllowInternalWalletRenewal ||
		len(left.AllowedMethods) != len(right.AllowedMethods) {
		return false
	}
	for i := range left.AllowedMethods {
		if !strings.EqualFold(left.AllowedMethods[i], right.AllowedMethods[i]) {
			return false
		}
	}
	return true
}

func giftPoliciesEqual(left, right *SubscriptionGiftPolicy) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.AllowPurchase == right.AllowPurchase &&
		intPointersEqual(left.MinimumAccountLevel, right.MinimumAccountLevel) &&
		left.AllowPerkSubscriptionBypass == right.AllowPerkSubscriptionBypass &&
		intPointersEqual(left.RollingPurchaseLimit, right.RollingPurchaseLimit) &&
		intPointersEqual(left.RollingWindowDays, right.RollingWindowDays) &&
		intPointersEqual(left.GiftDurationDays, right.GiftDurationDays) &&
		intPointersEqual(left.SubscriptionDurationDays, right.SubscriptionDurationDays)
}

func providerMappingsEqual(left, right map[string][]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, values := range left {
		other, ok := right[key]
		if !ok || len(other) != len(values) {
			return false
		}
		for i := range values {
			if !strings.EqualFold(values[i], other[i]) {
				return false
			}
		}
	}
	return true
}

// providerMappingsFor mirrors GetProviderMappings, including the alias
// expansion so "apple" finds an "AppleStore" key.
func providerMappingsFor(raw datatypes.JSON, provider string) ([]string, bool) {
	mappings := parseProviderMappings(raw)
	if len(mappings) == 0 {
		return nil, false
	}
	for _, lookupKey := range providerLookupKeys(provider) {
		if direct, ok := mappings[lookupKey]; ok {
			return direct, true
		}
		for key, values := range mappings {
			if strings.EqualFold(key, lookupKey) {
				return values, true
			}
		}
	}
	return nil, false
}

func providerLookupKeys(provider string) []string {
	if strings.TrimSpace(provider) == "" {
		return nil
	}
	seen := map[string]struct{}{}
	keys := make([]string, 0, 4)
	add := func(key string) {
		if strings.TrimSpace(key) == "" {
			return
		}
		normalized := strings.ToLower(key)
		if _, ok := seen[normalized]; ok {
			return
		}
		seen[normalized] = struct{}{}
		keys = append(keys, key)
	}
	add(provider)
	if aliases, ok := providerAliases[provider]; ok {
		for _, alias := range aliases {
			add(alias)
		}
	}
	for canonical, aliases := range providerAliases {
		matched := strings.EqualFold(canonical, provider)
		if !matched {
			for _, alias := range aliases {
				if strings.EqualFold(alias, provider) {
					matched = true
					break
				}
			}
		}
		if !matched {
			continue
		}
		add(canonical)
		for _, alias := range aliases {
			add(alias)
		}
	}
	return keys
}

// UpsertDefinition mirrors UpsertDefinitionAsync.
func (s *Subscriptions) UpsertDefinition(ctx context.Context, in SubscriptionDefinitionInput) (*WalletSubscriptionDefinitionEntity, bool, error) {
	if strings.TrimSpace(in.Identifier) == "" {
		return nil, false, errors.New("subscription identifier is required")
	}
	existing, err := s.GetDefinition(ctx, in.Identifier)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		existing.GroupIdentifier = stringPointer(derefOrEmpty(in.GroupIdentifier))
		existing.DisplayName = in.DisplayName
		existing.Currency = in.Currency
		existing.BasePrice = in.BasePrice
		existing.PerkLevel = in.PerkLevel
		existing.MinimumAccountLevel = copyInt(in.MinimumAccountLevel)
		existing.ExperienceMultiplier = in.ExperienceMultiplier
		existing.GoldenPointReward = copyInt(in.GoldenPointReward)
		existing.DisplayConfig = displayConfigJSON(in.DisplayConfig)
		existing.PaymentPolicy = paymentPolicyJSON(in.PaymentPolicy)
		existing.GiftPolicy = giftPolicyJSON(in.GiftPolicy)
		existing.ProviderMappings = providerMappingsJSON(in.ProviderMappings)
		existing.AppIdentifier = stringPointer(derefOrEmpty(in.AppIdentifier))
		if err := s.catalogDB(ctx).Save(existing).Error; err != nil {
			return nil, false, err
		}
		return existing, false, nil
	}
	created := buildDefinitionEntity(SubscriptionCatalogSeedDefinition{
		Identifier:           in.Identifier,
		GroupIdentifier:      in.GroupIdentifier,
		DisplayName:          in.DisplayName,
		Currency:             in.Currency,
		BasePrice:            in.BasePrice,
		PerkLevel:            in.PerkLevel,
		MinimumAccountLevel:  in.MinimumAccountLevel,
		ExperienceMultiplier: in.ExperienceMultiplier,
		GoldenPointReward:    in.GoldenPointReward,
		DisplayConfig:        in.DisplayConfig,
		PaymentPolicy:        in.PaymentPolicy,
		GiftPolicy:           in.GiftPolicy,
		ProviderMappings:     in.ProviderMappings,
		AppIdentifier:        in.AppIdentifier,
	})
	if err := s.catalogDB(ctx).Create(created).Error; err != nil {
		return nil, false, err
	}
	return created, true, nil
}

// DeleteDefinition mirrors DeleteDefinitionAsync (soft delete).
func (s *Subscriptions) DeleteDefinition(ctx context.Context, identifier string) (bool, error) {
	definition, err := s.GetDefinition(ctx, identifier)
	if err != nil {
		return false, err
	}
	if definition == nil {
		return false, nil
	}
	if err := s.catalogDB(ctx).Delete(definition).Error; err != nil {
		return false, err
	}
	return true, nil
}

// RegisterAppDefinition mirrors RegisterAppDefinitionAsync. It reports whether
// the definition was created.
func (s *Subscriptions) RegisterAppDefinition(ctx context.Context, identifier, appIdentifier, displayName, currency string, basePrice decimal.Decimal, groupIdentifier *string, cycleDurationDays int) (bool, error) {
	_ = cycleDurationDays // the C# signature carries it; duration is governed by the payment policy
	policy := SubscriptionPaymentPolicy{
		AllowInternalWallet:        true,
		AllowExternal:              false,
		AllowInternalWalletRenewal: true,
		AllowedMethods:             []string{PaymentInAppWallet},
	}
	existing, err := s.GetDefinition(ctx, identifier)
	if err != nil {
		return false, err
	}
	if existing != nil {
		existing.DisplayName = displayName
		existing.Currency = currency
		existing.BasePrice = basePrice
		existing.GroupIdentifier = stringPointer(derefOrEmpty(groupIdentifier))
		existing.AppIdentifier = stringPointer(appIdentifier)
		existing.PaymentPolicy = paymentPolicyJSON(policy)
		if err := s.catalogDB(ctx).Save(existing).Error; err != nil {
			return false, err
		}
		return false, nil
	}
	created := &WalletSubscriptionDefinitionEntity{
		ID:               newID(),
		Identifier:       identifier,
		DisplayName:      displayName,
		Currency:         currency,
		BasePrice:        basePrice,
		GroupIdentifier:  stringPointer(derefOrEmpty(groupIdentifier)),
		AppIdentifier:    stringPointer(appIdentifier),
		PaymentPolicy:    paymentPolicyJSON(policy),
		ProviderMappings: providerMappingsJSON(nil),
		PerkLevel:        0,
	}
	if err := s.catalogDB(ctx).Create(created).Error; err != nil {
		return false, err
	}
	return true, nil
}

// RemoveAppDefinition mirrors RemoveAppDefinitionAsync.
func (s *Subscriptions) RemoveAppDefinition(ctx context.Context, identifier, appIdentifier string) (bool, error) {
	var definition WalletSubscriptionDefinitionEntity
	err := s.catalogDB(ctx).Where("identifier = ? AND app_identifier = ?", identifier, appIdentifier).First(&definition).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := s.catalogDB(ctx).Delete(&definition).Error; err != nil {
		return false, err
	}
	return true, nil
}

// GetDefinitionsByApp mirrors GetDefinitionsByAppAsync.
func (s *Subscriptions) GetDefinitionsByApp(ctx context.Context, appIdentifier string) ([]WalletSubscriptionDefinitionEntity, error) {
	var definitions []WalletSubscriptionDefinitionEntity
	err := s.catalogDB(ctx).Where("app_identifier = ?", appIdentifier).Order("display_name ASC").Find(&definitions).Error
	return definitions, err
}
