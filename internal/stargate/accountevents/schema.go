package accountevents

import (
	"context"

	"gorm.io/gorm"
)

// Models is the AutoMigrate input for the module factory.
func Models() []any {
	return []any{
		&CheckInResultEntity{},
		&UserCalendarEventEntity{},
		&CalendarEventSubscriptionEntity{},
		&NotableDayEntity{},
	}
}

// SchemaHook applies SQL AutoMigrate cannot express. Hooks must be idempotent.
type SchemaHook func(ctx context.Context, db *gorm.DB) error

// Hooks declares the query-supporting indexes. Passport's EF model declared
// no indexes on these tables; the ranges below are the ones every calendar
// read filters on, so they are added additively (never dropped).
func Hooks() []SchemaHook {
	return []SchemaHook{func(ctx context.Context, db *gorm.DB) error {
		statements := []string{
			`CREATE INDEX IF NOT EXISTS ix_account_check_in_results_account_created
			 ON account_check_in_results (account_id, created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS ix_user_calendar_events_account_start
			 ON user_calendar_events (account_id, start_time)`,
			`CREATE INDEX IF NOT EXISTS ix_calendar_event_subscriptions_subscriber
			 ON calendar_event_subscriptions (subscriber_id) WHERE deleted_at IS NULL`,
			`CREATE INDEX IF NOT EXISTS ix_calendar_event_subscriptions_target
			 ON calendar_event_subscriptions (target_account_id) WHERE deleted_at IS NULL`,
			`CREATE INDEX IF NOT EXISTS ix_notable_days_region
			 ON notable_days (lower(region), start_date) WHERE deleted_at IS NULL`,
		}
		for _, statement := range statements {
			if err := db.WithContext(ctx).Exec(statement).Error; err != nil {
				return err
			}
		}
		return nil
	}}
}
