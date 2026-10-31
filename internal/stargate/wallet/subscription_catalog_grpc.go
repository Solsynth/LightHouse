// DySubscriptionCatalogService: the app-owned catalog surface. Ported from
// DysonNetwork.Wallet/Payment/SubscriptionCatalogServiceGrpc.cs.
//
// The wire payload is a JSON-carried SubscriptionCatalogSeedDefinition. The C#
// service serialized it with System.Text.Json's defaults (PascalCase keys,
// unquoted decimals), so this port accepts keys case-insensitively and emits
// PascalCase to stay byte-compatible with existing callers.
package wallet

import (
	"context"
	"encoding/json"

	"fmt"
	"strings"

	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

// unquotedDecimal marshals as a JSON number and accepts either a number or a
// quoted decimal, matching System.Text.Json's decimal encoding.
type unquotedDecimal struct{ decimal.Decimal }

func (d unquotedDecimal) MarshalJSON() ([]byte, error) {
	return []byte(d.Decimal.String()), nil
}

func (d *unquotedDecimal) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if text == "null" || text == "" {
		d.Decimal = decimal.Zero
		return nil
	}
	text = strings.Trim(text, `"`)
	parsed, err := decimal.NewFromString(text)
	if err != nil {
		return err
	}
	d.Decimal = parsed
	return nil
}

// seedDefinitionWire is SubscriptionCatalogSeedDefinition on the wire.
type seedDefinitionWire struct {
	Identifier           string              `json:"Identifier"`
	GroupIdentifier      *string             `json:"GroupIdentifier"`
	DisplayName          string              `json:"DisplayName"`
	Currency             string              `json:"Currency"`
	BasePrice            unquotedDecimal     `json:"BasePrice"`
	PerkLevel            int                 `json:"PerkLevel"`
	MinimumAccountLevel  *int                `json:"MinimumAccountLevel"`
	ExperienceMultiplier *unquotedDecimal    `json:"ExperienceMultiplier"`
	GoldenPointReward    *int                `json:"GoldenPointReward"`
	DisplayConfig        *displayConfigWire  `json:"DisplayConfig"`
	PaymentPolicy        *paymentPolicyWire  `json:"PaymentPolicy"`
	GiftPolicy           *giftPolicyWire     `json:"GiftPolicy"`
	ProviderMappings     map[string][]string `json:"ProviderMappings"`
	AppIdentifier        *string             `json:"AppIdentifier"`
}

type displayConfigWire struct {
	Color           *string `json:"Color"`
	BackgroundColor *string `json:"BackgroundColor"`
	BadgeText       *string `json:"BadgeText"`
}

// paymentPolicyWire keeps every flag optional so omitted keys take the C#
// property-initializer defaults (AllowInternalWallet/AllowExternal = true).
type paymentPolicyWire struct {
	AllowInternalWallet        *bool    `json:"AllowInternalWallet"`
	AllowExternal              *bool    `json:"AllowExternal"`
	AllowInternalWalletRenewal *bool    `json:"AllowInternalWalletRenewal"`
	AllowedMethods             []string `json:"AllowedMethods"`
}

// giftPolicyWire mirrors SubscriptionGiftPolicy; omitted booleans default true.
type giftPolicyWire struct {
	AllowPurchase               *bool `json:"AllowPurchase"`
	MinimumAccountLevel         *int  `json:"MinimumAccountLevel"`
	AllowPerkSubscriptionBypass *bool `json:"AllowPerkSubscriptionBypass"`
	RollingPurchaseLimit        *int  `json:"RollingPurchaseLimit"`
	RollingWindowDays           *int  `json:"RollingWindowDays"`
	GiftDurationDays            *int  `json:"GiftDurationDays"`
	SubscriptionDurationDays    *int  `json:"SubscriptionDurationDays"`
}

// seedDefinitionValues flattens the wire shape into the domain seed type,
// applying the C# defaults for omitted policy fields.
func (w seedDefinitionWire) toSeed() SubscriptionCatalogSeedDefinition {
	policy := defaultPaymentPolicy()
	if w.PaymentPolicy != nil {
		policy = SubscriptionPaymentPolicy{
			AllowInternalWallet:        boolDefault(w.PaymentPolicy.AllowInternalWallet, true),
			AllowExternal:              boolDefault(w.PaymentPolicy.AllowExternal, true),
			AllowInternalWalletRenewal: boolDefault(w.PaymentPolicy.AllowInternalWalletRenewal, false),
			AllowedMethods:             w.PaymentPolicy.AllowedMethods,
		}
		if policy.AllowedMethods == nil {
			policy.AllowedMethods = []string{}
		}
	}
	var gift *SubscriptionGiftPolicy
	if w.GiftPolicy != nil {
		gift = &SubscriptionGiftPolicy{
			AllowPurchase:               boolDefault(w.GiftPolicy.AllowPurchase, true),
			MinimumAccountLevel:         w.GiftPolicy.MinimumAccountLevel,
			AllowPerkSubscriptionBypass: boolDefault(w.GiftPolicy.AllowPerkSubscriptionBypass, true),
			RollingPurchaseLimit:        w.GiftPolicy.RollingPurchaseLimit,
			RollingWindowDays:           w.GiftPolicy.RollingWindowDays,
			GiftDurationDays:            w.GiftPolicy.GiftDurationDays,
			SubscriptionDurationDays:    w.GiftPolicy.SubscriptionDurationDays,
		}
	}
	var display *SubscriptionDisplayConfig
	if w.DisplayConfig != nil {
		display = &SubscriptionDisplayConfig{
			Color:           w.DisplayConfig.Color,
			BackgroundColor: w.DisplayConfig.BackgroundColor,
			BadgeText:       w.DisplayConfig.BadgeText,
		}
	}
	var multiplier *decimal.Decimal
	if w.ExperienceMultiplier != nil {
		value := w.ExperienceMultiplier.Decimal
		multiplier = &value
	}
	mappings := w.ProviderMappings
	if mappings == nil {
		mappings = map[string][]string{}
	}
	return SubscriptionCatalogSeedDefinition{
		Identifier:           w.Identifier,
		GroupIdentifier:      w.GroupIdentifier,
		DisplayName:          w.DisplayName,
		Currency:             w.Currency,
		BasePrice:            w.BasePrice.Decimal,
		PerkLevel:            w.PerkLevel,
		MinimumAccountLevel:  w.MinimumAccountLevel,
		ExperienceMultiplier: multiplier,
		GoldenPointReward:    w.GoldenPointReward,
		DisplayConfig:        display,
		PaymentPolicy:        policy,
		GiftPolicy:           gift,
		ProviderMappings:     mappings,
		AppIdentifier:        w.AppIdentifier,
	}
}

func boolDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

// definitionToSeedDefinition mirrors SubscriptionCatalogServiceGrpc.ToSeedDefinition.
func definitionToSeedDefinition(e *WalletSubscriptionDefinitionEntity) seedDefinitionWire {
	wire := seedDefinitionWire{
		Identifier:          e.Identifier,
		GroupIdentifier:     e.GroupIdentifier,
		DisplayName:         e.DisplayName,
		Currency:            e.Currency,
		BasePrice:           unquotedDecimal{e.BasePrice},
		PerkLevel:           e.PerkLevel,
		MinimumAccountLevel: e.MinimumAccountLevel,
		GoldenPointReward:   e.GoldenPointReward,
		ProviderMappings:    parseProviderMappings(e.ProviderMappings),
		AppIdentifier:       e.AppIdentifier,
	}
	if e.ExperienceMultiplier != nil {
		wire.ExperienceMultiplier = &unquotedDecimal{*e.ExperienceMultiplier}
	}
	if display := parseDisplayConfig(e.DisplayConfig); display != nil {
		wire.DisplayConfig = &displayConfigWire{
			Color:           display.Color,
			BackgroundColor: display.BackgroundColor,
			BadgeText:       display.BadgeText,
		}
	}
	policy := parsePaymentPolicy(e.PaymentPolicy)
	wire.PaymentPolicy = &paymentPolicyWire{
		AllowInternalWallet:        &policy.AllowInternalWallet,
		AllowExternal:              &policy.AllowExternal,
		AllowInternalWalletRenewal: &policy.AllowInternalWalletRenewal,
		AllowedMethods:             policy.AllowedMethods,
	}
	if gift := parseGiftPolicy(e.GiftPolicy); gift != nil {
		wire.GiftPolicy = &giftPolicyWire{
			AllowPurchase:               &gift.AllowPurchase,
			MinimumAccountLevel:         gift.MinimumAccountLevel,
			AllowPerkSubscriptionBypass: &gift.AllowPerkSubscriptionBypass,
			RollingPurchaseLimit:        gift.RollingPurchaseLimit,
			RollingWindowDays:           gift.RollingWindowDays,
			GiftDurationDays:            gift.GiftDurationDays,
			SubscriptionDurationDays:    gift.SubscriptionDurationDays,
		}
	}
	return wire
}

// decodeSeedDefinition accepts the C# PascalCase payload and any casing a
// caller might send. encoding/json matches struct tags exactly, so a case
// mismatch would silently drop fields; keys are normalized first instead.
func decodeSeedDefinition(raw string) (seedDefinitionWire, error) {
	var wire seedDefinitionWire
	var table map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &table); err != nil {
		return wire, err
	}
	canonical := map[string]json.RawMessage{}
	for key, value := range table {
		canonical[normalizeSettingKey(key)] = value
	}
	lookup := func(key string) (json.RawMessage, bool) {
		value, ok := canonical[normalizeSettingKey(key)]
		return value, ok
	}
	assign := func(key string, target any) error {
		value, ok := lookup(key)
		if !ok {
			return nil
		}
		if err := json.Unmarshal(value, target); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		return nil
	}

	var display map[string]json.RawMessage
	var policy map[string]json.RawMessage
	var gift map[string]json.RawMessage
	var mappings map[string][]string
	for key, target := range map[string]any{
		"Identifier":           &wire.Identifier,
		"GroupIdentifier":      &wire.GroupIdentifier,
		"DisplayName":          &wire.DisplayName,
		"Currency":             &wire.Currency,
		"BasePrice":            &wire.BasePrice,
		"PerkLevel":            &wire.PerkLevel,
		"MinimumAccountLevel":  &wire.MinimumAccountLevel,
		"ExperienceMultiplier": &wire.ExperienceMultiplier,
		"GoldenPointReward":    &wire.GoldenPointReward,
		"AppIdentifier":        &wire.AppIdentifier,
		"ProviderMappings":     &mappings,
		"DisplayConfig":        &display,
		"PaymentPolicy":        &policy,
		"GiftPolicy":           &gift,
	} {
		if err := assign(key, target); err != nil {
			return wire, err
		}
	}
	wire.ProviderMappings = mappings
	if display != nil {
		wire.DisplayConfig = &displayConfigWire{}
		for key, target := range map[string]any{
			"Color":           &wire.DisplayConfig.Color,
			"BackgroundColor": &wire.DisplayConfig.BackgroundColor,
			"BadgeText":       &wire.DisplayConfig.BadgeText,
		} {
			if value, ok := nestedValue(display, key); ok {
				if err := json.Unmarshal(value, target); err != nil {
					return wire, err
				}
			}
		}
	}
	if policy != nil {
		wire.PaymentPolicy = &paymentPolicyWire{}
		for key, target := range map[string]any{
			"AllowInternalWallet":        &wire.PaymentPolicy.AllowInternalWallet,
			"AllowExternal":              &wire.PaymentPolicy.AllowExternal,
			"AllowInternalWalletRenewal": &wire.PaymentPolicy.AllowInternalWalletRenewal,
			"AllowedMethods":             &wire.PaymentPolicy.AllowedMethods,
		} {
			if value, ok := nestedValue(policy, key); ok {
				if err := json.Unmarshal(value, target); err != nil {
					return wire, err
				}
			}
		}
	}
	if gift != nil {
		wire.GiftPolicy = &giftPolicyWire{}
		for key, target := range map[string]any{
			"AllowPurchase":               &wire.GiftPolicy.AllowPurchase,
			"MinimumAccountLevel":         &wire.GiftPolicy.MinimumAccountLevel,
			"AllowPerkSubscriptionBypass": &wire.GiftPolicy.AllowPerkSubscriptionBypass,
			"RollingPurchaseLimit":        &wire.GiftPolicy.RollingPurchaseLimit,
			"RollingWindowDays":           &wire.GiftPolicy.RollingWindowDays,
			"GiftDurationDays":            &wire.GiftPolicy.GiftDurationDays,
			"SubscriptionDurationDays":    &wire.GiftPolicy.SubscriptionDurationDays,
		} {
			if value, ok := nestedValue(gift, key); ok {
				if err := json.Unmarshal(value, target); err != nil {
					return wire, err
				}
			}
		}
	}
	return wire, nil
}

// nestedValue resolves a key in a decoded object ignoring case and separators.
func nestedValue(table map[string]json.RawMessage, key string) (json.RawMessage, bool) {
	wanted := normalizeSettingKey(key)
	for candidate, value := range table {
		if normalizeSettingKey(candidate) == wanted {
			return value, true
		}
	}
	return nil, false
}

type dySubscriptionCatalogGRPC struct {
	gen.UnimplementedDySubscriptionCatalogServiceServer
	subs *Subscriptions
}

func (s *dySubscriptionCatalogGRPC) UpsertDefinition(ctx context.Context, req *gen.DyUpsertSubscriptionDefinitionRequest) (*gen.DyUpsertSubscriptionDefinitionResponse, error) {
	if strings.TrimSpace(req.AppIdentifier) == "" {
		return nil, status.Error(codes.InvalidArgument, "app_identifier is required.")
	}
	if strings.TrimSpace(req.DefinitionJson) == "" {
		return nil, status.Error(codes.InvalidArgument, "definition_json is required.")
	}
	if s.subs == nil {
		return nil, status.Error(codes.Unimplemented, "subscription catalog is unavailable")
	}

	wire, err := decodeSeedDefinition(req.DefinitionJson)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "definition_json is invalid: %s", err.Error())
	}
	if strings.TrimSpace(wire.Identifier) == "" ||
		strings.TrimSpace(wire.DisplayName) == "" ||
		strings.TrimSpace(wire.Currency) == "" {
		return nil, status.Error(codes.InvalidArgument, "definition_json must include identifier, display_name, and currency.")
	}

	// Ownership: a service may only manage definitions bearing its own app
	// identifier. The stored AppIdentifier is always the claimed one; anything
	// inside the JSON is ignored.
	seed := wire.toSeed()
	seed.AppIdentifier = &req.AppIdentifier

	existing, err := s.subs.GetDefinition(ctx, seed.Identifier)
	if err != nil {
		return nil, grpcError(err)
	}
	if existing != nil {
		stored := ""
		if existing.AppIdentifier != nil {
			stored = *existing.AppIdentifier
		}
		if stored != req.AppIdentifier {
			owner := stored
			if owner == "" {
				owner = "platform"
			}
			return nil, status.Errorf(codes.FailedPrecondition,
				"Definition %s is managed by app %s.", seed.Identifier, owner)
		}
	}

	_, created, err := s.subs.UpsertDefinition(ctx, seedDefinitionInput(seed))
	if err != nil {
		return nil, grpcError(err)
	}
	return &gen.DyUpsertSubscriptionDefinitionResponse{Created: created, Identifier: seed.Identifier}, nil
}

func (s *dySubscriptionCatalogGRPC) ListDefinitions(ctx context.Context, req *gen.DyListSubscriptionDefinitionsRequest) (*gen.DyListSubscriptionDefinitionsResponse, error) {
	if strings.TrimSpace(req.AppIdentifier) == "" {
		return nil, status.Error(codes.InvalidArgument, "app_identifier is required.")
	}
	if s.subs == nil {
		return nil, status.Error(codes.Unimplemented, "subscription catalog is unavailable")
	}
	definitions, err := s.subs.GetDefinitionsByApp(ctx, req.AppIdentifier)
	if err != nil {
		return nil, grpcError(err)
	}
	response := &gen.DyListSubscriptionDefinitionsResponse{}
	for i := range definitions {
		encoded, marshalErr := json.Marshal(definitionToSeedDefinition(&definitions[i]))
		if marshalErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to encode definition %s: %s", definitions[i].Identifier, marshalErr.Error())
		}
		response.DefinitionsJson = append(response.DefinitionsJson, string(encoded))
	}
	return response, nil
}

// seedDefinitionInput flattens a seed definition into the admin input shape.
func seedDefinitionInput(seed SubscriptionCatalogSeedDefinition) SubscriptionDefinitionInput {
	return SubscriptionDefinitionInput{
		Identifier:           seed.Identifier,
		GroupIdentifier:      seed.GroupIdentifier,
		DisplayName:          seed.DisplayName,
		Currency:             seed.Currency,
		BasePrice:            seed.BasePrice,
		PerkLevel:            seed.PerkLevel,
		MinimumAccountLevel:  seed.MinimumAccountLevel,
		ExperienceMultiplier: seed.ExperienceMultiplier,
		GoldenPointReward:    seed.GoldenPointReward,
		DisplayConfig:        seed.DisplayConfig,
		PaymentPolicy:        seed.PaymentPolicy,
		GiftPolicy:           seed.GiftPolicy,
		ProviderMappings:     seed.ProviderMappings,
		AppIdentifier:        seed.AppIdentifier,
	}
}
