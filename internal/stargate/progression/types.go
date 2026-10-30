package progression

import (
	"errors"
	"strings"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// ModuleID is the progression feature module identifier.
const ModuleID = "app.solian.id.progression"

// Definition types (ProgressionDefinitionType).
const (
	TypeAchievement = "achievement"
	TypeQuest       = "quest"
)

// Trigger modes (ProgressionTriggerMode).
const (
	ModeCount      = "count"
	ModeStreak     = "streak"
	ModeDailyCount = "daily_count"
)

// Quest repeatability (QuestRepeatability).
const (
	RepeatNone    = "none"
	RepeatDaily   = "daily"
	RepeatWeekly  = "weekly"
	RepeatMonthly = "monthly"
)

// Completion packet type and default currency
// (WebSocketPacketType.ProgressionCompleted, WalletCurrency.SourcePoint).
const (
	CompletionPacketType = "progression.completed"
	SourcePointCurrency  = "points"
)

// Errors the HTTP and gRPC layers map to their codes.
var (
	// ErrNotFound reports a missing definition.
	ErrNotFound = errors.New("progression: definition not found")
	// ErrUnavailable reports a collaborator that is not configured.
	ErrUnavailable = errors.New("progression: collaborator unavailable")
)

// ProgressTrigger is SnProgressTriggerDefinition: the action-log filter and
// accumulation mode a definition matches on.
type ProgressTrigger struct {
	Actions    []string          `json:"actions"`
	MetaEquals map[string]string `json:"meta_equals,omitempty"`
	Mode       string            `json:"mode"`
}

// ModeOrDefault returns the trigger mode, defaulting to a plain count the way
// the C# definition initializer does.
func (t ProgressTrigger) ModeOrDefault() string {
	if strings.TrimSpace(t.Mode) == "" {
		return ModeCount
	}
	return t.Mode
}

// QuestSchedule is SnQuestScheduleConfig.
type QuestSchedule struct {
	Repeatability    string `json:"repeatability"`
	ActiveDaysOfWeek []int  `json:"active_days_of_week,omitempty"`
}

// RepeatabilityOrDefault returns the configured repeatability or RepeatNone.
func (s QuestSchedule) RepeatabilityOrDefault() string {
	if strings.TrimSpace(s.Repeatability) == "" {
		return RepeatNone
	}
	return s.Repeatability
}

// BadgeReward is SnProgressBadgeRewardDefinition.
type BadgeReward struct {
	Type    string         `json:"type"`
	Label   *string        `json:"label,omitempty"`
	Caption *string        `json:"caption,omitempty"`
	Meta    map[string]any `json:"meta,omitempty"`
}

// ProgressReward is SnProgressRewardDefinition.
type ProgressReward struct {
	Experience           int64        `json:"experience"`
	SourcePoints         int64        `json:"source_points"`
	SourcePointsCurrency string       `json:"source_points_currency,omitempty"`
	Badge                *BadgeReward `json:"badge,omitempty"`
}

// CurrencyOr returns the reward currency, defaulting to source points.
func (r ProgressReward) CurrencyOr() string {
	if strings.TrimSpace(r.SourcePointsCurrency) == "" {
		return SourcePointCurrency
	}
	return r.SourcePointsCurrency
}

// SeriesStage is one completed/remaining step of a series, as returned by the
// state listings.
type SeriesStage struct {
	Identifier  string     `json:"identifier"`
	Title       string     `json:"title"`
	SeriesOrder *int       `json:"series_order,omitempty"`
	TargetCount int        `json:"target_count"`
	IsCompleted bool       `json:"is_completed"`
	CompletedAt *TimeValue `json:"completed_at,omitempty"`
}

// RewardToken builds the idempotency key a grant carries:
// `{type}:{identifier}:{account}` for achievements and
// `{type}:{identifier}:{account}:{period}` for quests.
func RewardToken(definitionType, identifier string, accountID uuid.UUID, periodKey string) string {
	token := definitionType + ":" + identifier + ":" + accountID.String()
	if strings.TrimSpace(periodKey) != "" {
		token += ":" + periodKey
	}
	return token
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// datatypesJSON is the jsonb column type the entity triggers and rewards use.
type datatypesJSON = datatypes.JSON
