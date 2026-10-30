package domaintrust

import (
	"context"

	"gorm.io/gorm"
)

// Models is the AutoMigrate input for the module factory.
func Models() []any {
	return []any{
		&DomainBlockEntity{},
		&DomainValidationMetricEntity{},
	}
}

// Hooks are the indexes Passport's EF model declared:
//
//	SnDomainBlock:           [Index(DomainPattern)], [Index(IsActive)]
//	SnDomainValidationMetric: [Index(Domain, IsUnique = true)]
//
// The metric index is a plain unique index over the domain (not over
// (domain, deleted_at)), which is the shape EF declared and the one the
// ON CONFLICT upsert relies on.
func Hooks() []func(ctx context.Context, db *gorm.DB) error {
	return []func(ctx context.Context, db *gorm.DB) error{
		func(ctx context.Context, db *gorm.DB) error {
			// Restore the uuid column types GORM does not alter on existing
			// tables (primary keys in particular).
			for _, column := range [][2]string{
				{"domain_blocks", "id"}, {"domain_blocks", "created_by_account_id"},
				{"domain_validation_metrics", "id"},
			} {
				if err := ensureUUIDColumn(ctx, db, column[0], column[1]); err != nil {
					return err
				}
			}
			return nil
		},
		func(ctx context.Context, db *gorm.DB) error {
			return db.WithContext(ctx).Exec(`
				CREATE INDEX IF NOT EXISTS ix_domain_blocks_domain_pattern
				ON domain_blocks (domain_pattern)`).Error
		},
		func(ctx context.Context, db *gorm.DB) error {
			return db.WithContext(ctx).Exec(`
				CREATE INDEX IF NOT EXISTS ix_domain_blocks_is_active
				ON domain_blocks (is_active)`).Error
		},
		func(ctx context.Context, db *gorm.DB) error {
			return db.WithContext(ctx).Exec(`
				CREATE UNIQUE INDEX IF NOT EXISTS ix_domain_validation_metrics_domain
				ON domain_validation_metrics (domain)`).Error
		},
	}
}

func ensureUUIDColumn(ctx context.Context, db *gorm.DB, table, column string) error {
	var dataType string
	if err := db.WithContext(ctx).Raw(`
		SELECT data_type FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = ? AND column_name = ?`,
		table, column).Scan(&dataType).Error; err != nil {
		return err
	}
	if dataType == "" || dataType == "uuid" {
		return nil
	}
	return db.WithContext(ctx).Exec(
		"ALTER TABLE " + table + " ALTER COLUMN " + column + " TYPE uuid USING " + column + "::uuid",
	).Error
}
