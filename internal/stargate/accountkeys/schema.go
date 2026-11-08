package accountkeys

import (
	"context"

	"gorm.io/gorm"
)

// Models is the module's AutoMigrate input. Both tables are module-owned: a
// deployment that disables app.solian.id.keys never creates them.
func Models() []any {
	return []any{
		&SSHKeyEntity{},
		&PGPKeyEntity{},
	}
}

// Indexes are the constraints AutoMigrate cannot express. Fingerprints are
// unique per account among live rows; a deleted key can be added again.
func Indexes(ctx context.Context, db *gorm.DB) error {
	for _, statement := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS ix_account_ssh_keys_account_fingerprint
		 ON account_ssh_keys (account_id, fingerprint) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS ix_account_ssh_keys_account_id
		 ON account_ssh_keys (account_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ix_account_pgp_keys_account_fingerprint
		 ON account_pgp_keys (account_id, fingerprint) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS ix_account_pgp_keys_account_id
		 ON account_pgp_keys (account_id)`,
	} {
		if err := db.WithContext(ctx).Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}
