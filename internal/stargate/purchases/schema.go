package purchases

import (
	"context"

	"gorm.io/gorm"
)

// Models is the AutoMigrate input for the module factory. affiliation_spells
// and affiliation_results are already created by the core spell migration on
// an existing deployment; AutoMigrate reconciles them additively.
func Models() []any {
	return []any{
		&AffiliationSpellEntity{},
		&AffiliationResultEntity{},
		&AffiliationSpellPurchaseEntity{},
		&NameChangeCardPurchaseEntity{},
	}
}

// Hooks are the indexes Passport declared for the two purchase tables, made
// partial on deleted_at so a soft-deleted row frees its order id.
func Hooks() []func(ctx context.Context, db *gorm.DB) error {
	return []func(ctx context.Context, db *gorm.DB) error{
		func(ctx context.Context, db *gorm.DB) error {
			return db.WithContext(ctx).Exec(`
				CREATE UNIQUE INDEX IF NOT EXISTS ix_affiliation_spell_purchases_order_id
				ON affiliation_spell_purchases (order_id) WHERE deleted_at IS NULL`).Error
		},
		func(ctx context.Context, db *gorm.DB) error {
			return db.WithContext(ctx).Exec(`
				CREATE INDEX IF NOT EXISTS ix_affiliation_spell_purchases_account_id_created_at
				ON affiliation_spell_purchases (account_id, created_at)`).Error
		},
		func(ctx context.Context, db *gorm.DB) error {
			return db.WithContext(ctx).Exec(`
				CREATE UNIQUE INDEX IF NOT EXISTS ix_name_change_card_purchases_order_id
				ON name_change_card_purchases (order_id) WHERE deleted_at IS NULL`).Error
		},
		func(ctx context.Context, db *gorm.DB) error {
			return db.WithContext(ctx).Exec(`
				CREATE INDEX IF NOT EXISTS ix_name_change_card_purchases_account_id_created_at
				ON name_change_card_purchases (account_id, created_at)`).Error
		},
	}
}
