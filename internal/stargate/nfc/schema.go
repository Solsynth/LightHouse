package nfc

import (
	"context"

	"gorm.io/gorm"
)

// Models returns the tables this module owns.
func Models() []any {
	return []any{
		&TagEntity{},
	}
}

// Hooks restores the index names Passport's EF model declared:
//
//	SnNfcTag: [Index(nameof(Uid), IsUnique = true)], [Index(nameof(AccountId))]
//
// The uid index stays a plain unique index over uid (not a partial index over
// live rows): that is the shape EF created and the one an existing database
// already carries.
func Hooks() []func(ctx context.Context, db *gorm.DB) error {
	return []func(ctx context.Context, db *gorm.DB) error{
		func(ctx context.Context, db *gorm.DB) error {
			for _, statement := range []string{
				`CREATE UNIQUE INDEX IF NOT EXISTS ix_nfc_tags_uid ON nfc_tags (uid)`,
				`CREATE INDEX IF NOT EXISTS ix_nfc_tags_account_id ON nfc_tags (account_id)`,
			} {
				if err := db.WithContext(ctx).Exec(statement).Error; err != nil {
					return err
				}
			}
			return nil
		},
	}
}
