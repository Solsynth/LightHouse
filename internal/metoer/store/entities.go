package store

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type EntityBase struct {
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

type NotificationEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AccountID uuid.UUID      `gorm:"column:account_id"`
	AppID     *string        `gorm:"column:app_id"`
	Content   *string        `gorm:"column:content"`
	Meta      datatypes.JSON `gorm:"column:meta;type:jsonb"`
	Priority  int            `gorm:"column:priority"`
	PushType  *string        `gorm:"column:push_type"`
	Subtitle  *string        `gorm:"column:subtitle"`
	Title     *string        `gorm:"column:title"`
	Topic     string         `gorm:"column:topic"`
	ViewedAt  *time.Time     `gorm:"column:viewed_at"`
}

func (NotificationEntity) TableName() string { return "notifications" }

type PushSubscriptionEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AccountID      uuid.UUID  `gorm:"column:account_id"`
	AppID          *string    `gorm:"column:app_id"`
	CountDelivered int        `gorm:"column:count_delivered"`
	DeviceID       string     `gorm:"column:device_id"`
	DeviceName     *string    `gorm:"column:device_name"`
	DeviceToken    string     `gorm:"column:device_token"`
	IsActivated    bool       `gorm:"column:is_activated"`
	LastUsedAt     *time.Time `gorm:"column:last_used_at"`
	Provider       int        `gorm:"column:provider"`
}

func (PushSubscriptionEntity) TableName() string { return "push_subscriptions" }

type NotificationPreferenceEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AccountID  uuid.UUID `gorm:"column:account_id"`
	Preference int       `gorm:"column:preference"`
	Topic      string    `gorm:"column:topic"`
}

func (NotificationPreferenceEntity) TableName() string { return "notification_preferences" }

type EmailSendingPlanEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	SendingPlanKey         *string    `gorm:"column:sending_plan_key"`
	CreatedByAccountID     uuid.UUID  `gorm:"column:created_by_account_id"`
	Subject                string     `gorm:"column:subject"`
	HTMLBody               string     `gorm:"column:html_body"`
	BroadcastToAll         bool       `gorm:"column:broadcast_to_all"`
	RecipientCount         int        `gorm:"column:recipient_count"`
	MaxEmailsPerInterval   int        `gorm:"column:max_emails_per_interval"`
	IntervalMinutes        int        `gorm:"column:interval_minutes"`
	MaxEmailsPerDay        *int       `gorm:"column:max_emails_per_day"`
	Status                 int        `gorm:"column:status"`
	AdvancedIntervalsCount int        `gorm:"column:advanced_intervals_count"`
	PlannedStartAt         time.Time  `gorm:"column:planned_start_at"`
	NextIntervalAt         *time.Time `gorm:"column:next_interval_at"`
	LastAdvancedAt         *time.Time `gorm:"column:last_advanced_at"`
	PausedAt               *time.Time `gorm:"column:paused_at"`
	CompletedAt            *time.Time `gorm:"column:completed_at"`
}

func (EmailSendingPlanEntity) TableName() string { return "email_sending_plans" }

type EmailSendingPlanRecipientEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	PlanID                uuid.UUID  `gorm:"column:plan_id"`
	AccountID             uuid.UUID  `gorm:"column:account_id"`
	RecipientNameSnapshot *string    `gorm:"column:recipient_name_snapshot"`
	Status                int        `gorm:"column:status"`
	AttemptCount          int        `gorm:"column:attempt_count"`
	LastIntervalNumber    *int       `gorm:"column:last_interval_number"`
	LastResolvedEmail     *string    `gorm:"column:last_resolved_email"`
	LastError             *string    `gorm:"column:last_error"`
	ProcessedAt           *time.Time `gorm:"column:processed_at"`
}

func (EmailSendingPlanRecipientEntity) TableName() string { return "email_sending_plan_recipients" }

type EmailSendingPlanAdvanceEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	PlanID            uuid.UUID `gorm:"column:plan_id"`
	IntervalNumber    int       `gorm:"column:interval_number"`
	IsManual          bool      `gorm:"column:is_manual"`
	AttemptedCount    int       `gorm:"column:attempted_count"`
	SentCount         int       `gorm:"column:sent_count"`
	SkippedCount      int       `gorm:"column:skipped_count"`
	FailedCount       int       `gorm:"column:failed_count"`
	PendingCountAfter int       `gorm:"column:pending_count_after"`
	StartedAt         time.Time `gorm:"column:started_at"`
	CompletedAt       time.Time `gorm:"column:completed_at"`
}

func (EmailSendingPlanAdvanceEntity) TableName() string { return "email_sending_plan_advances" }

type EmailDeliveryRecordEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	DurationMilliseconds int64   `gorm:"column:duration_milliseconds"`
	Error                *string `gorm:"column:error"`
	Outcome              int     `gorm:"column:outcome"`
	Provider             string  `gorm:"column:provider"`
	Source               string  `gorm:"column:source"`
}

func (EmailDeliveryRecordEntity) TableName() string { return "email_delivery_records" }

type NotificationDeliveryRecordEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AppID                *string    `gorm:"column:app_id"`
	DurationMilliseconds int64      `gorm:"column:duration_milliseconds"`
	Error                *string    `gorm:"column:error"`
	NotificationID       *uuid.UUID `gorm:"column:notification_id"`
	Outcome              int        `gorm:"column:outcome"`
	Provider             string     `gorm:"column:provider"`
	PushType             *string    `gorm:"column:push_type"`
	SubscriptionID       *uuid.UUID `gorm:"column:subscription_id"`
	Topic                string     `gorm:"column:topic"`
}

func (NotificationDeliveryRecordEntity) TableName() string { return "notification_delivery_records" }

type NotificationSendRecordEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AppID    *string `gorm:"column:app_id"`
	PushType *string `gorm:"column:push_type"`
	Source   string  `gorm:"column:source"`
	Topic    string  `gorm:"column:topic"`
}

func (NotificationSendRecordEntity) TableName() string { return "notification_send_records" }

type SchemaMigrationEntity struct {
	Version   string    `gorm:"column:version;primaryKey"`
	AppliedAt time.Time `gorm:"column:applied_at"`
}

func (SchemaMigrationEntity) TableName() string { return "schema_migrations" }

var entityTables = []any{
	NotificationEntity{}, PushSubscriptionEntity{}, NotificationPreferenceEntity{},
	EmailSendingPlanEntity{}, EmailSendingPlanRecipientEntity{}, EmailSendingPlanAdvanceEntity{},
	EmailDeliveryRecordEntity{}, NotificationDeliveryRecordEntity{}, NotificationSendRecordEntity{},
}
