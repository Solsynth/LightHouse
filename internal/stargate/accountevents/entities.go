// Package accountevents is the ported Passport account-events domain: the
// daily check-in with its fortune tips, the user calendar (events,
// subscriptions, merged aggregation and countdowns), notable days/holidays and
// the public account timeline.
//
// Tables keep the legacy Passport names and columns so an existing deployment
// needs no data copy. The status and presence-activity tables stay core
// (internal/stargate/store); this package only reads them.
package accountevents

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

// CheckInResultEntity is `account_check_in_results` (SnCheckInResult).
type CheckInResultEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Level            int            `gorm:"column:level"`
	RewardPoints     *float64       `gorm:"column:reward_points;type:numeric"`
	RewardExperience *int           `gorm:"column:reward_experience"`
	Tips             datatypes.JSON `gorm:"column:tips;type:jsonb;not null"`
	FortuneReport    datatypes.JSON `gorm:"column:fortune_report;type:jsonb"`
	AccountID        uuid.UUID      `gorm:"column:account_id;type:uuid"`
	BackdatedFrom    *time.Time     `gorm:"column:backdated_from"`
}

func (CheckInResultEntity) TableName() string { return "account_check_in_results" }

// UserCalendarEventEntity is `user_calendar_events` (SnUserCalendarEvent).
type UserCalendarEventEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Title       string         `gorm:"column:title;size:256;not null"`
	Description *string        `gorm:"column:description;size:4096"`
	Location    *string        `gorm:"column:location;size:512"`
	StartTime   time.Time      `gorm:"column:start_time"`
	EndTime     time.Time      `gorm:"column:end_time"`
	IsAllDay    bool           `gorm:"column:is_all_day"`
	Visibility  int            `gorm:"column:visibility"`
	Recurrence  datatypes.JSON `gorm:"column:recurrence;type:jsonb"`
	Tags        datatypes.JSON `gorm:"column:tags;type:jsonb;not null"`
	Meta        datatypes.JSON `gorm:"column:meta;type:jsonb"`
	Icon        datatypes.JSON `gorm:"column:icon;type:jsonb"`
	Background  datatypes.JSON `gorm:"column:background;type:jsonb"`
	AccountID   uuid.UUID      `gorm:"column:account_id;type:uuid"`
}

func (UserCalendarEventEntity) TableName() string { return "user_calendar_events" }

// CalendarEventSubscriptionEntity is `calendar_event_subscriptions`
// (SnCalendarEventSubscription).
type CalendarEventSubscriptionEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	SubscriberID    uuid.UUID `gorm:"column:subscriber_id;type:uuid"`
	TargetAccountID uuid.UUID `gorm:"column:target_account_id;type:uuid"`
	Notify          bool      `gorm:"column:notify"`
}

func (CalendarEventSubscriptionEntity) TableName() string {
	return "calendar_event_subscriptions"
}

// NotableDayEntity is `notable_days` (SnNotableDay).
type NotableDayEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Name              string         `gorm:"column:name;size:256;not null"`
	Description       *string        `gorm:"column:description;size:4096"`
	LocalName         *string        `gorm:"column:local_name;size:256"`
	LocalizableKey    *string        `gorm:"column:localizable_key;size:256"`
	StartDate         time.Time      `gorm:"column:start_date"`
	EndDate           time.Time      `gorm:"column:end_date"`
	IsAllDay          bool           `gorm:"column:is_all_day"`
	Region            string         `gorm:"column:region;size:8;not null"`
	Tags              datatypes.JSON `gorm:"column:tags;type:jsonb;not null"`
	Meta              datatypes.JSON `gorm:"column:meta;type:jsonb"`
	IsRecurring       bool           `gorm:"column:is_recurring"`
	RecurrencePattern *string        `gorm:"column:recurrence_pattern;size:16"`
	IsPeriod          bool           `gorm:"column:is_period"`
	HolidayDays       datatypes.JSON `gorm:"column:holiday_days;type:jsonb"`
	DisplayOrder      *int           `gorm:"column:display_order"`
}

func (NotableDayEntity) TableName() string { return "notable_days" }
