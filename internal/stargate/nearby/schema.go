package nearby

import (
	"context"

	"gorm.io/gorm"
)

// Models is the AutoMigrate input for the module factory.
func Models() []any {
	return []any{
		&NearbyDeviceEntity{},
		&NearbyPresenceTokenEntity{},
	}
}

// Hooks create the indexes Passport's EF model declared plus the live-device
// uniqueness the C# model intended: a partial unique index over
// (user_id, device_id) for non-deleted rows, so a re-registered device row
// never conflicts with a tombstoned one.
func Hooks() []func(ctx context.Context, db *gorm.DB) error {
	return []func(ctx context.Context, db *gorm.DB) error{
		func(ctx context.Context, db *gorm.DB) error {
			return db.WithContext(ctx).Exec(`
				CREATE UNIQUE INDEX IF NOT EXISTS ix_nearby_devices_user_device_live
				ON nearby_devices (user_id, device_id)
				WHERE deleted_at IS NULL`).Error
		},
		func(ctx context.Context, db *gorm.DB) error {
			return db.WithContext(ctx).Exec(`
				CREATE INDEX IF NOT EXISTS ix_nearby_presence_tokens_token_hash_slot
				ON nearby_presence_tokens (token_hash, slot)`).Error
		},
		func(ctx context.Context, db *gorm.DB) error {
			return db.WithContext(ctx).Exec(`
				CREATE INDEX IF NOT EXISTS ix_nearby_presence_tokens_device_slot
				ON nearby_presence_tokens (device_id, slot)`).Error
		},
	}
}
