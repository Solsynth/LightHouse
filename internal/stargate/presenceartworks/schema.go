package presenceartworks

import (
	"context"

	"gorm.io/gorm"
)

// Models is the AutoMigrate input for the module factory.
func Models() []any {
	return []any{&PresenceArtworkEntity{}}
}

// Hooks add the cleanup index. The hash is the primary key; the retention
// sweep scans last_referenced_at, which Passport never indexed.
func Hooks() []func(ctx context.Context, db *gorm.DB) error {
	return []func(ctx context.Context, db *gorm.DB) error{
		func(ctx context.Context, db *gorm.DB) error {
			return db.WithContext(ctx).Exec(`
				CREATE INDEX IF NOT EXISTS ix_presence_artworks_last_referenced_at
				ON presence_artworks (last_referenced_at)`).Error
		},
	}
}
