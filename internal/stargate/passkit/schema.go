package passkit

import (
	"context"

	"gorm.io/gorm"
)

// Models returns the tables this module owns.
func Models() []any {
	return []any{
		&PassEntity{},
		&RegistrationEntity{},
	}
}

// Hooks restores the indexes Passport's EF model declared. The unique
// constraints are partial indexes over live rows: soft-deleted passes and
// registrations must not keep an account/serial/device pair occupied.
func Hooks() []func(ctx context.Context, db *gorm.DB) error {
	return []func(ctx context.Context, db *gorm.DB) error{
		func(ctx context.Context, db *gorm.DB) error {
			for _, statement := range []string{
				`CREATE UNIQUE INDEX IF NOT EXISTS ix_apple_passes_account_id_pass_type_identifier
				 ON apple_passes (account_id, pass_type_identifier) WHERE deleted_at IS NULL`,
				`CREATE UNIQUE INDEX IF NOT EXISTS ix_apple_passes_pass_type_identifier_serial_number
				 ON apple_passes (pass_type_identifier, serial_number) WHERE deleted_at IS NULL`,
				`CREATE UNIQUE INDEX IF NOT EXISTS ix_apple_pass_registrations_device_library_identifier_pass_id
				 ON apple_pass_registrations (device_library_identifier, pass_id) WHERE deleted_at IS NULL`,
			} {
				if err := db.WithContext(ctx).Exec(statement).Error; err != nil {
					return err
				}
			}
			return nil
		},
	}
}
