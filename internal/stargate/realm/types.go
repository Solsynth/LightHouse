package realm

import (
	"database/sql/driver"
	"fmt"
	"math"
	"strings"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// Decimal is an exact decimal that serializes as a bare JSON number, which is
// what System.Text.Json does for the C# realm models (`boost_points`,
// `amount`). shopspring's decimal quotes its output by default, so unwrapping
// the type here keeps the wire shape without flipping the process-wide
// MarshalJSONWithoutQuotes switch that the wallet module relies on.
type Decimal struct{ decimal.Decimal }

// NewDecimal wraps an exact decimal.
func NewDecimal(d decimal.Decimal) Decimal { return Decimal{Decimal: d} }

// DecimalFromInt builds a Decimal from an integer.
func DecimalFromInt(n int64) Decimal { return NewDecimal(decimal.NewFromInt(n)) }

// MarshalJSON emits the number unquoted, like the C# decimal serializer.
func (d Decimal) MarshalJSON() ([]byte, error) {
	return []byte(d.String()), nil
}

// UnmarshalJSON accepts a JSON number or a quoted numeric string; the C#
// model accepts both after a round trip through some clients.
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

// Scan reads a numeric column (pgx hands back string, []byte or float64).
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
func (d Decimal) Value() (driver.Value, error) {
	return d.String(), nil
}

// RealmMemberRole mirrors the C# RealmMemberRole constants. Higher wins.
const (
	RoleNormal    = 0
	RoleModerator = 50
	RoleOwner     = 100
)

// BaseSocialCredit is the C# starting social credit.
const BaseSocialCredit = 100.0

// RealmBoostPolicy mirrors DysonNetwork.Shared.Models.RealmBoostPolicy.
const (
	DefaultCurrency = "golds"
	GoldsCurrency   = "golds"
	PointsCurrency  = "points"

	// ExpirationDays is the window a contribution increases the boost level.
	ExpirationDays = 30
)

var (
	goldsPerShare  = decimal.NewFromInt(1)
	pointsPerShare = decimal.NewFromInt(1000)
	level1Points   = decimal.NewFromInt(10)
	level2Points   = decimal.NewFromInt(25)
	level3Points   = decimal.NewFromInt(50)
)

// boostLevel maps boost points to the level (0-3) that gates labels and
// promoted roles.
func boostLevel(points decimal.Decimal) int {
	switch {
	case points.GreaterThanOrEqual(level3Points):
		return 3
	case points.GreaterThanOrEqual(level2Points):
		return 2
	case points.GreaterThanOrEqual(level1Points):
		return 1
	default:
		return 0
	}
}

// labelCap is the number of realm labels a boost level may define.
func labelCap(level int) int {
	switch {
	case level >= 3:
		return 9
	case level >= 2:
		return 6
	case level >= 1:
		return 3
	default:
		return 0
	}
}

// normalizeCurrency mirrors RealmBoostPolicy.NormalizeCurrency: empty means
// golds, anything else is rejected.
func normalizeCurrency(currency string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(currency)) {
	case "", GoldsCurrency:
		return GoldsCurrency, nil
	case PointsCurrency:
		return PointsCurrency, nil
	default:
		return "", fmt.Errorf("unsupported realm boost currency: %s", currency)
	}
}

// amountForShares is the price of shares in the given currency.
func amountForShares(currency string, shares int) (decimal.Decimal, error) {
	normalized, err := normalizeCurrency(currency)
	if err != nil {
		return decimal.Zero, err
	}
	perShare := goldsPerShare
	if normalized == PointsCurrency {
		perShare = pointsPerShare
	}
	return perShare.Mul(decimal.NewFromInt(int64(shares))), nil
}

// sharesForAmount is the inverse of amountForShares.
func sharesForAmount(currency string, amount decimal.Decimal) Decimal {
	normalized, err := normalizeCurrency(currency)
	if err != nil {
		return Decimal{}
	}
	perShare := goldsPerShare
	if normalized == PointsCurrency {
		perShare = pointsPerShare
	}
	if perShare.IsZero() {
		return Decimal{}
	}
	return NewDecimal(amount.Div(perShare))
}

// ─────────────────────────── leveling ───────────────────────────

// Realm leveling mirrors DysonNetwork.Shared.Models.RealmLeveling: the realm
// curve has a much larger base than the account curve (1000 vs 100).
const (
	realmMaxLevel = 120
	realmBaseExp  = 1000.0
	realmK        = 3.52
)

// realmExpForLevel is the single-level XP requirement (from L to L+1).
func realmExpForLevel(level int) float64 {
	if level < 1 {
		return 0
	}
	return realmBaseExp + realmK*math.Pow(float64(level-1), 2)
}

// realmTotalExpForLevel is the cumulative XP requirement to reach a level.
func realmTotalExpForLevel(level int) float64 {
	if level < 1 {
		return 0
	}
	l := float64(level)
	return realmBaseExp*l + realmK*((l-1)*l*(2*l-1))/6.0
}

// realmLevelFromExp mirrors RealmLeveling.GetLevelFromExp.
func realmLevelFromExp(xp int) int {
	if xp < 0 {
		return 0
	}
	level := 0
	for level < realmMaxLevel && realmTotalExpForLevel(level+1) <= float64(xp) {
		level++
	}
	return level
}

// realmLevelingProgress mirrors RealmLeveling.GetProgressToNextLevel.
func realmLevelingProgress(xp int) float64 {
	current := realmLevelFromExp(xp)
	if current >= realmMaxLevel {
		return 1.0
	}
	prev := realmTotalExpForLevel(current)
	next := realmTotalExpForLevel(current + 1)
	return (float64(xp) - prev) / (next - prev)
}

// ─────────────────────────── quota ───────────────────────────

// tieredQuota mirrors ResourceQuotaCalculator.GetTieredQuota, the realm quota
// every account gets from its level and perk level.
func tieredQuota(level, perkLevel int) int {
	base := 0
	switch {
	case level >= 60:
		base = 3
	case level >= 30:
		base = 2
	case level >= 10:
		base = 1
	}
	return base + perkLevel
}

// ─────────────────────────── permissions ───────────────────────────

// The eight realm permission flags, keyed by the wire strings
// RealmService.HasPermission accepts.
const (
	PermChatSend      = "chat.send"
	PermPostCreate    = "post.create"
	PermPostComment   = "post.comment"
	PermMediaUpload   = "media.upload"
	PermPostModerate  = "post.moderate"
	PermChatModerate  = "chat.moderate"
	PermMembersManage = "members.manage"
	PermRealmManage   = "realm.manage"
)

// ModuleID is the realm feature module identifier
// (app.solian.id.realm). It is referenced by the factory, the importer and the
// module documentation.
const ModuleID = "app.solian.id.realm"

// GormDBDataType declares the Postgres type for Decimal so GORM's schema
// parser accepts it as a field type (the numeric column keeps full precision
// through Decimal's Valuer/Scanner).
func (Decimal) GormDataType() string { return "numeric" }

// GormDBDataType is the column type GORM emits for a Decimal field.
func (Decimal) GormDBDataType(db *gorm.DB, field *schema.Field) string { return "numeric" }
