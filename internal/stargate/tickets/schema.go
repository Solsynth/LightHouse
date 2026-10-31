package tickets

import (
	"context"

	"gorm.io/gorm"
)

// Models is the AutoMigrate input for the module factory.
func Models() []any {
	return []any{
		&TicketEntity{},
		&TicketMessageEntity{},
		&OnCallAdminEntity{},
	}
}

// Hooks are the constraints AutoMigrate cannot express. Passport declared a
// unique index on account_id; the port keeps a live-row uniqueness so removing
// an on-call admin (a soft delete) frees the account for a later add.
func Hooks() []func(ctx context.Context, db *gorm.DB) error {
	return []func(ctx context.Context, db *gorm.DB) error{
		func(ctx context.Context, db *gorm.DB) error {
			if err := db.WithContext(ctx).Exec(`
				DROP INDEX IF EXISTS ix_ticket_on_call_admins_account_id`).Error; err != nil {
				return err
			}
			return db.WithContext(ctx).Exec(`
				CREATE UNIQUE INDEX IF NOT EXISTS ix_ticket_on_call_admins_account_id_live
				ON ticket_on_call_admins (account_id)
				WHERE deleted_at IS NULL`).Error
		},
	}
}
