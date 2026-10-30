// Package progression is the ported Passport progression domain: achievement
// and quest definitions, per-account progress, reward grants and the
// event-receipt ledger that makes redelivered action logs idempotent.
//
// Tables keep the legacy Passport names and columns so an existing deployment
// needs no data copy.
package progression

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// EntityBase mirrors the C# ModelBase.
type EntityBase struct {
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at"`
}

// AchievementDefinitionEntity is `achievement_definitions`.
type AchievementDefinitionEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Identifier        string         `gorm:"column:identifier;size:256;not null"`
	Title             string         `gorm:"column:title;size:256;not null"`
	Summary           string         `gorm:"column:summary;size:4096;not null"`
	Icon              *string        `gorm:"column:icon;size:256"`
	SeriesIdentifier  *string        `gorm:"column:series_identifier;size:256"`
	SeriesTitle       *string        `gorm:"column:series_title;size:256"`
	SeriesOrder       *int           `gorm:"column:series_order"`
	SortOrder         int            `gorm:"column:sort_order"`
	Hidden            bool           `gorm:"column:hidden"`
	IsEnabled         bool           `gorm:"column:is_enabled"`
	IsSeedManaged     bool           `gorm:"column:is_seed_managed"`
	IsProgressEnabled bool           `gorm:"column:is_progress_enabled"`
	AvailableFrom     *time.Time     `gorm:"column:available_from"`
	AvailableUntil    *time.Time     `gorm:"column:available_until"`
	TargetCount       int            `gorm:"column:target_count"`
	Trigger           datatypes.JSON `gorm:"column:trigger;type:jsonb"`
	Reward            datatypes.JSON `gorm:"column:reward;type:jsonb"`
}

func (AchievementDefinitionEntity) TableName() string { return "achievement_definitions" }

// QuestDefinitionEntity is `quest_definitions`.
type QuestDefinitionEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Identifier        string         `gorm:"column:identifier;size:256;not null"`
	Title             string         `gorm:"column:title;size:256;not null"`
	Summary           string         `gorm:"column:summary;size:4096;not null"`
	Icon              *string        `gorm:"column:icon;size:256"`
	SeriesIdentifier  *string        `gorm:"column:series_identifier;size:256"`
	SeriesTitle       *string        `gorm:"column:series_title;size:256"`
	SeriesOrder       *int           `gorm:"column:series_order"`
	SortOrder         int            `gorm:"column:sort_order"`
	Hidden            bool           `gorm:"column:hidden"`
	IsEnabled         bool           `gorm:"column:is_enabled"`
	IsSeedManaged     bool           `gorm:"column:is_seed_managed"`
	IsProgressEnabled bool           `gorm:"column:is_progress_enabled"`
	AvailableFrom     *time.Time     `gorm:"column:available_from"`
	AvailableUntil    *time.Time     `gorm:"column:available_until"`
	TargetCount       int            `gorm:"column:target_count"`
	Trigger           datatypes.JSON `gorm:"column:trigger;type:jsonb"`
	Schedule          datatypes.JSON `gorm:"column:schedule;type:jsonb"`
	Reward            datatypes.JSON `gorm:"column:reward;type:jsonb"`
}

func (QuestDefinitionEntity) TableName() string { return "quest_definitions" }

// AccountAchievementEntity is `account_achievements`. One row per
// (account, definition); achievements never reset.
type AccountAchievementEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	AccountID               uuid.UUID  `gorm:"column:account_id;type:uuid"`
	AchievementDefinitionID uuid.UUID  `gorm:"column:achievement_definition_id;type:uuid"`
	ProgressCount           int        `gorm:"column:progress_count"`
	CurrentStreak           int        `gorm:"column:current_streak"`
	BestStreak              int        `gorm:"column:best_streak"`
	LastProgressAt          *time.Time `gorm:"column:last_progress_at"`
	CompletedAt             *time.Time `gorm:"column:completed_at"`
	ClaimedAt               *time.Time `gorm:"column:claimed_at"`
	LastRewardToken         *string    `gorm:"column:last_reward_token;size:256"`
}

func (AccountAchievementEntity) TableName() string { return "account_achievements" }

// AccountQuestProgressEntity is `account_quest_progresses`: one row per
// (account, definition, period).
type AccountQuestProgressEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	AccountID            uuid.UUID  `gorm:"column:account_id;type:uuid"`
	QuestDefinitionID    uuid.UUID  `gorm:"column:quest_definition_id;type:uuid"`
	PeriodKey            string     `gorm:"column:period_key;size:128;not null"`
	ProgressCount        int        `gorm:"column:progress_count"`
	RepeatIterationCount int        `gorm:"column:repeat_iteration_count"`
	CompletedAt          *time.Time `gorm:"column:completed_at"`
	ClaimedAt            *time.Time `gorm:"column:claimed_at"`
	LastRewardToken      *string    `gorm:"column:last_reward_token;size:256"`
}

func (AccountQuestProgressEntity) TableName() string { return "account_quest_progresses" }

// RewardGrantEntity is `progress_reward_grants`: one completed definition, with
// a per-delivery timestamp for each reward type so a partial failure retries
// only what is missing.
type RewardGrantEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	AccountID             uuid.UUID      `gorm:"column:account_id;type:uuid"`
	DefinitionType        string         `gorm:"column:definition_type;size:32;not null"`
	DefinitionIdentifier  string         `gorm:"column:definition_identifier;size:256;not null"`
	DefinitionTitle       string         `gorm:"column:definition_title;size:256;not null"`
	RewardToken           string         `gorm:"column:reward_token;size:256;not null"`
	SourceEventID         uuid.UUID      `gorm:"column:source_event_id;type:uuid"`
	Reward                datatypes.JSON `gorm:"column:reward;type:jsonb"`
	PeriodKey             *string        `gorm:"column:period_key;size:64"`
	BadgeGrantedAt        *time.Time     `gorm:"column:badge_granted_at"`
	ExperienceGrantedAt   *time.Time     `gorm:"column:experience_granted_at"`
	SourcePointsGrantedAt *time.Time     `gorm:"column:source_points_granted_at"`
	NotificationSentAt    *time.Time     `gorm:"column:notification_sent_at"`
}

func (RewardGrantEntity) TableName() string { return "progress_reward_grants" }

// EventReceiptEntity is `progress_event_receipts`: the at-least-once guard on
// the action-log consumer. A redelivered event for the same definition and
// period is a no-op.
type EventReceiptEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	EventID              uuid.UUID `gorm:"column:event_id;type:uuid"`
	AccountID            uuid.UUID `gorm:"column:account_id;type:uuid"`
	DefinitionType       string    `gorm:"column:definition_type;size:32;not null"`
	DefinitionIdentifier string    `gorm:"column:definition_identifier;size:256;not null"`
	PeriodKey            string    `gorm:"column:period_key;size:64;not null"`
}

func (EventReceiptEntity) TableName() string { return "progress_event_receipts" }
