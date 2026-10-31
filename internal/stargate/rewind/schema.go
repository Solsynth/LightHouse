package rewind

import (
	"context"

	"gorm.io/gorm"
)

// Models is the AutoMigrate input for the module factory.
func Models() []any {
	return []any{&RewindPointEntity{}}
}

// Hooks add the lookup indexes. The sharable code is only unique among live
// rows: a cleared code is NULL (and NULLs never collide), and a soft-deleted
// point must not reserve its old code forever.
func Hooks() []func(ctx context.Context, db *gorm.DB) error {
	return []func(ctx context.Context, db *gorm.DB) error{
		func(ctx context.Context, db *gorm.DB) error {
			return db.WithContext(ctx).Exec(`
				CREATE UNIQUE INDEX IF NOT EXISTS ix_rewind_points_sharable_code
				ON rewind_points (sharable_code)
				WHERE sharable_code IS NOT NULL AND deleted_at IS NULL`).Error
		},
		func(ctx context.Context, db *gorm.DB) error {
			return db.WithContext(ctx).Exec(`
				CREATE INDEX IF NOT EXISTS ix_rewind_points_account_year
				ON rewind_points (account_id, year)`).Error
		},
	}
}
