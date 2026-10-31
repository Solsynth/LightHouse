// Package purchases owns the Passport affiliation-spell and name-change-card
// domains: marketing/registration invites, their wallet purchases, and the
// purchase + consumption ledger for name change cards.
//
// The affiliation_spells and affiliation_results tables are shared with the
// core spell service (registration consumes a registration invite through the
// same rows); the two purchase tables are owned by this module.
package purchases

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"src.solsynth.dev/sosys/stargate/model"
)

// ModuleID is the compiled feature module identifier.
const ModuleID = "app.solian.id.purchases"

// Product and wallet identifiers, copied from the C# services.
const (
	ProductAffiliationRegistrationInvite = "affiliations.registration-invite"
	ProductNameChangeCard                = "passport.name-change-card"

	CurrencyPoints        = "points"
	AppIdentifierInternal = "internal"
)

// Name change card target types (NameChangeCardService constants).
const (
	TargetAccount   = "account"
	TargetRealm     = "realm"
	TargetPublisher = "publisher"
)

// AffiliationSpellType values mirror the C# enum (stored as int).
const (
	AffiliationSpellTypeRegistrationInvite = 0
)

// Decimal is an exact decimal that serializes as a bare JSON number, matching
// System.Text.Json's output for the C# purchase models. shopspring's decimal
// quotes its output by default, so the wrapper keeps the legacy wire shape.
type Decimal struct{ decimal.Decimal }

// NewDecimal wraps an exact decimal.
func NewDecimal(d decimal.Decimal) Decimal { return Decimal{Decimal: d} }

// DecimalFromInt builds a Decimal from an integer.
func DecimalFromInt(n int64) Decimal { return NewDecimal(decimal.NewFromInt(n)) }

// MarshalJSON emits the number unquoted, like the C# decimal serializer.
func (d Decimal) MarshalJSON() ([]byte, error) {
	return []byte(d.String()), nil
}

// UnmarshalJSON accepts a JSON number or a quoted numeric string.
func (d *Decimal) UnmarshalJSON(raw []byte) error {
	text := strings.TrimSpace(string(raw))
	if text == "null" {
		d.Decimal = decimal.Zero
		return nil
	}
	text = strings.Trim(text, `"`)
	parsed, err := decimal.NewFromString(text)
	if err != nil {
		return fmt.Errorf("invalid decimal %q: %w", text, err)
	}
	d.Decimal = parsed
	return nil
}

// Scan reads a numeric column (pgx hands back string, []byte or a number).
func (d *Decimal) Scan(value any) error {
	switch v := value.(type) {
	case nil:
		d.Decimal = decimal.Zero
	case string:
		return d.UnmarshalJSON([]byte(v))
	case []byte:
		return d.UnmarshalJSON(v)
	case int64:
		d.Decimal = decimal.NewFromInt(v)
	case float64:
		d.Decimal = decimal.NewFromFloat(v)
	default:
		parsed, err := decimal.NewFromString(fmt.Sprint(v))
		if err != nil {
			return err
		}
		d.Decimal = parsed
	}
	return nil
}

// Value writes the exact numeric text so Postgres keeps full precision.
func (d Decimal) Value() (driver.Value, error) { return d.String(), nil }

// ── wire models (snake_case, matching the C# models) ───────────────────────

// AffiliationSpell mirrors SnAffiliationSpell. Unlike magic spells, the spell
// word is a shareable invite code and is part of the payload in C#.
type AffiliationSpell struct {
	Id         string         `json:"id"`
	Spell      string         `json:"spell"`
	Type       int            `json:"type"`
	ExpiresAt  *model.Time    `json:"expires_at,omitempty"`
	AffectedAt *model.Time    `json:"affected_at,omitempty"`
	Meta       map[string]any `json:"meta"`
	AccountId  *string        `json:"account_id,omitempty"`
	CreatedAt  *model.Time    `json:"created_at"`
	UpdatedAt  *model.Time    `json:"updated_at"`
	DeletedAt  *model.Time    `json:"deleted_at"`
}

// AffiliationResult mirrors SnAffiliationResult. Its Spell navigation is
// [JsonIgnore] in C#, so the wire payload carries only SpellId.
type AffiliationResult struct {
	Id                 string      `json:"id"`
	ResourceIdentifier string      `json:"resource_identifier"`
	SpellId            string      `json:"spell_id"`
	CreatedAt          *model.Time `json:"created_at"`
	UpdatedAt          *model.Time `json:"updated_at"`
	DeletedAt          *model.Time `json:"deleted_at"`
}

// AffiliationSpellPurchase mirrors SnAffiliationSpellPurchase.
type AffiliationSpellPurchase struct {
	Id          string      `json:"id"`
	AccountId   string      `json:"account_id"`
	OrderId     string      `json:"order_id"`
	Amount      Decimal     `json:"amount"`
	SpellId     *string     `json:"spell_id,omitempty"`
	FulfilledAt *model.Time `json:"fulfilled_at,omitempty"`
	CreatedAt   *model.Time `json:"created_at"`
	UpdatedAt   *model.Time `json:"updated_at"`
	DeletedAt   *model.Time `json:"deleted_at"`
}

// NameChangeCardPurchase mirrors SnNameChangeCardPurchase.
type NameChangeCardPurchase struct {
	Id          string      `json:"id"`
	AccountId   string      `json:"account_id"`
	OrderId     string      `json:"order_id"`
	Amount      Decimal     `json:"amount"`
	FulfilledAt *model.Time `json:"fulfilled_at,omitempty"`
	ConsumedAt  *model.Time `json:"consumed_at,omitempty"`
	TargetType  *string     `json:"target_type,omitempty"`
	TargetId    *string     `json:"target_id,omitempty"`
	OldName     *string     `json:"old_name,omitempty"`
	NewName     *string     `json:"new_name,omitempty"`
	CreatedAt   *model.Time `json:"created_at"`
	UpdatedAt   *model.Time `json:"updated_at"`
	DeletedAt   *model.Time `json:"deleted_at"`
}

// ── settings ──────────────────────────────────────────────────────────────

// Options is one purchase domain's configuration. Defaults mirror the C#
// option classes; the maximum purchase count follows the module contract.
type Options struct {
	Enabled            bool
	PricePoints        decimal.Decimal
	PurchasePeriodDays int
	MaxPurchases       int
}

// Settings carries both purchase domains.
type Settings struct {
	Affiliation    Options
	NameChangeCard Options
}

// DefaultSettings is the built-in configuration.
func DefaultSettings() Settings {
	return Settings{
		Affiliation:    defaultOptions(),
		NameChangeCard: defaultOptions(),
	}
}

func defaultOptions() Options {
	return Options{
		Enabled:            true,
		PricePoints:        decimal.NewFromInt(100),
		PurchasePeriodDays: 30,
		MaxPurchases:       1,
	}
}

// ParseSettings decodes the module settings subtree. It accepts both nested
// tables (`affiliation = { enabled = true }`, camelCase or snake_case section
// names) and flat top-level keys (`affiliation_enabled`, `affiliation.pricePoints`,
// `nameChangeCardMaxPurchases`, ...). Unprefixed top-level values act as the
// default for both domains, which keeps a single flat configuration usable.
func ParseSettings(raw map[string]any) Settings {
	settings := DefaultSettings()
	settings.Affiliation = parseOptions(raw,
		[]string{"affiliation", "affiliations", "affiliationPurchase", "affiliation_purchase"},
		defaultOptions())
	settings.NameChangeCard = parseOptions(raw,
		[]string{"nameChangeCard", "name_change_card", "nameChangeCardPurchase", "name_change_card_purchase", "nameChange", "name_change"},
		defaultOptions())
	return settings
}

var optionKeys = struct {
	Enabled  []string
	Price    []string
	Period   []string
	MaxCount []string
}{
	Enabled:  []string{"enabled"},
	Price:    []string{"pricePoints", "price_points", "price"},
	Period:   []string{"purchasePeriodDays", "purchase_period_days", "periodDays", "period_days"},
	MaxCount: []string{"maxPurchases", "max_purchases", "max"},
}

func parseOptions(raw map[string]any, sections []string, fallback Options) Options {
	options := fallback
	if value, ok := sectionLookup(raw, sections, optionKeys.Enabled); ok {
		if parsed, ok := toBool(value); ok {
			options.Enabled = parsed
		}
	} else if value, ok := globalLookup(raw, optionKeys.Enabled); ok {
		if parsed, ok := toBool(value); ok {
			options.Enabled = parsed
		}
	}
	if value, ok := sectionLookup(raw, sections, optionKeys.Price); ok {
		if parsed, ok := toDecimal(value); ok {
			options.PricePoints = parsed
		}
	} else if value, ok := globalLookup(raw, optionKeys.Price); ok {
		if parsed, ok := toDecimal(value); ok {
			options.PricePoints = parsed
		}
	}
	if value, ok := sectionLookup(raw, sections, optionKeys.Period); ok {
		if parsed, ok := toInt(value); ok {
			options.PurchasePeriodDays = parsed
		}
	} else if value, ok := globalLookup(raw, optionKeys.Period); ok {
		if parsed, ok := toInt(value); ok {
			options.PurchasePeriodDays = parsed
		}
	}
	if value, ok := sectionLookup(raw, sections, optionKeys.MaxCount); ok {
		if parsed, ok := toInt(value); ok {
			options.MaxPurchases = parsed
		}
	} else if value, ok := globalLookup(raw, optionKeys.MaxCount); ok {
		if parsed, ok := toInt(value); ok {
			options.MaxPurchases = parsed
		}
	}
	return options
}

// sectionLookup checks a nested table under any section name, then flat keys
// built from the section name (`affiliation.enabled`, `affiliation_enabled`,
// `affiliationEnabled`, ...). The original map is also checked with
// case-insensitive section names.
func sectionLookup(raw map[string]any, sections, keys []string) (any, bool) {
	if raw == nil {
		return nil, false
	}
	for _, section := range sections {
		if table, ok := tableFor(raw, section); ok {
			for _, key := range keys {
				if value, ok := table[key]; ok {
					return value, true
				}
				if value, ok := table[strings.ToLower(key)]; ok {
					return value, true
				}
			}
		}
	}
	for _, section := range sections {
		for _, key := range keys {
			candidates := []string{
				section + "." + key,
				section + "_" + key,
				section + key,
			}
			for _, candidate := range candidates {
				if value, ok := raw[candidate]; ok {
					return value, true
				}
				if value, ok := raw[strings.ToLower(candidate)]; ok {
					return value, true
				}
			}
		}
	}
	return nil, false
}

func tableFor(raw map[string]any, section string) (map[string]any, bool) {
	if value, ok := raw[section]; ok {
		if table, ok := value.(map[string]any); ok {
			return table, true
		}
	}
	for key, value := range raw {
		if strings.EqualFold(key, section) {
			table, ok := value.(map[string]any)
			return table, ok
		}
	}
	return nil, false
}

func globalLookup(raw map[string]any, keys []string) (any, bool) {
	if raw == nil {
		return nil, false
	}
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			return value, true
		}
	}
	return nil, false
}

func toBool(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		return false, false
	}
}

func toInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case uint:
		return int(typed), true
	case uint64:
		if typed > math.MaxInt {
			return 0, false
		}
		return int(typed), true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed < math.MinInt || typed > math.MaxInt {
			return 0, false
		}
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		return 0, false
	}
}

func toDecimal(value any) (decimal.Decimal, bool) {
	switch typed := value.(type) {
	case decimal.Decimal:
		return typed, true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return decimal.Zero, false
		}
		return decimal.NewFromFloat(typed), true
	case int:
		return decimal.NewFromInt(int64(typed)), true
	case int64:
		return decimal.NewFromInt(typed), true
	case json.Number:
		parsed, err := decimal.NewFromString(typed.String())
		return parsed, err == nil
	case string:
		parsed, err := decimal.NewFromString(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		return decimal.Zero, false
	}
}
