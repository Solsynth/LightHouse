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

// legacyUUIDColumns are the uuid columns Passport's EF model declared. GORM
// creates uuid columns for the tagged fields on a fresh database, but it does
// not alter primary keys on an existing database, and the first AutoMigrate
// pass stored uuid.UUID values as text. The hook below converts those columns
// back to uuid so a database created by either schema shape can import legacy
// rows and serve the same wire values.
var legacyUUIDColumns = [][2]string{
	{"realms", "id"}, {"realms", "account_id"},
	{"realm_members", "realm_id"}, {"realm_members", "account_id"}, {"realm_members", "label_id"},
	{"realm_labels", "id"}, {"realm_labels", "realm_id"}, {"realm_labels", "created_by_account_id"},
	{"realm_boost_contributions", "id"}, {"realm_boost_contributions", "realm_id"},
	{"realm_boost_contributions", "account_id"}, {"realm_boost_contributions", "order_id"},
	{"realm_boost_contributions", "transaction_id"},
	{"realm_experience_records", "id"}, {"realm_experience_records", "realm_id"},
	{"realm_experience_records", "account_id"},
	{"realm_role_permissions", "id"}, {"realm_role_permissions", "realm_id"},
	{"realm_user_permissions", "id"}, {"realm_user_permissions", "realm_id"},
	{"realm_user_permissions", "account_id"},
	{"realm_post_moderation_logs", "id"}, {"realm_post_moderation_logs", "realm_id"},
	{"realm_post_moderation_logs", "post_id"}, {"realm_post_moderation_logs", "moderator_account_id"},
}

// Hooks are the indexes Passport's EF model declared. They are created after
// AutoMigrate so a database migrated from Passport keeps identical
// constraints, including the partial uniqueness that lets a soft-deleted
// realm free its slug.
func Hooks() []func(ctx context.Context, db *gorm.DB) error {
	hooks := []func(ctx context.Context, db *gorm.DB) error{
		func(ctx context.Context, db *gorm.DB) error {
			for _, column := range legacyUUIDColumns {
				if err := ensureUUIDColumn(ctx, db, column[0], column[1]); err != nil {
					return err
				}
			}
			return nil
		},
	}
	return append(hooks, indexHooks()...)
}

func indexHooks() []func(ctx context.Context, db *gorm.DB) error {
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

// ensureUUIDColumn converts a text column holding UUID strings back to uuid.
// It is a no-op once the column already has the right type, so a boot does not
// rewrite the table.
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
