package realm

import (
	"context"

	"gorm.io/gorm"
)

// Models is the AutoMigrate input for the module factory.
func Models() []any {
	return []any{
		&RealmEntity{},
		&RealmMemberEntity{},
		&RealmLabelEntity{},
		&RealmBoostContributionEntity{},
		&RealmExperienceRecordEntity{},
		&RealmRolePermissionEntity{},
		&RealmUserPermissionEntity{},
		&RealmPostModerationLogEntity{},
	}
}

// Hooks are the indexes Passport's EF model declared. They are created after
// AutoMigrate so a database migrated from Passport keeps identical
// constraints, including the partial uniqueness that lets a soft-deleted
// realm free its slug.
func Hooks() []func(ctx context.Context, db *gorm.DB) error {
	return []func(ctx context.Context, db *gorm.DB) error{
		func(ctx context.Context, db *gorm.DB) error {
			return db.WithContext(ctx).Exec(`
				CREATE UNIQUE INDEX IF NOT EXISTS ix_realms_slug_deleted_at
				ON realms (slug, deleted_at)`).Error
		},
		func(ctx context.Context, db *gorm.DB) error {
			return db.WithContext(ctx).Exec(`
				CREATE INDEX IF NOT EXISTS ix_realm_labels_realm_id
				ON realm_labels (realm_id)`).Error
		},
		func(ctx context.Context, db *gorm.DB) error {
			return db.WithContext(ctx).Exec(`
				CREATE INDEX IF NOT EXISTS ix_realm_members_label_id
				ON realm_members (label_id)`).Error
		},
		func(ctx context.Context, db *gorm.DB) error {
			return db.WithContext(ctx).Exec(`
				CREATE UNIQUE INDEX IF NOT EXISTS ix_realm_role_permissions_realm_id_role_level
				ON realm_role_permissions (realm_id, role_level)`).Error
		},
		func(ctx context.Context, db *gorm.DB) error {
			return db.WithContext(ctx).Exec(`
				CREATE UNIQUE INDEX IF NOT EXISTS ix_realm_user_permissions_realm_id_account_id
				ON realm_user_permissions (realm_id, account_id)`).Error
		},
		func(ctx context.Context, db *gorm.DB) error {
			return db.WithContext(ctx).Exec(`
				CREATE INDEX IF NOT EXISTS ix_realm_post_moderation_logs_realm_id
				ON realm_post_moderation_logs (realm_id)`).Error
		},
		func(ctx context.Context, db *gorm.DB) error {
			// Boost state is read per realm inside a 30-day window and XP
			// idempotency probes (reason_type, reason); both are hot paths.
			if err := db.WithContext(ctx).Exec(`
				CREATE INDEX IF NOT EXISTS ix_realm_boost_contributions_realm_created
				ON realm_boost_contributions (realm_id, created_at)`).Error; err != nil {
				return err
			}
			return db.WithContext(ctx).Exec(`
				CREATE INDEX IF NOT EXISTS ix_realm_experience_records_lookup
				ON realm_experience_records (realm_id, account_id, reason_type)`).Error
		},
	}
}
