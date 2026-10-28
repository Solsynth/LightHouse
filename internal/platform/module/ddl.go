package module

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// DDL returns a SchemaHook that executes the given statements in order. The
// statements are the module's own DDL and must be written to be idempotent
// (CREATE TABLE IF NOT EXISTS, CREATE INDEX IF NOT EXISTS, ...).
func DDL(statements ...string) SchemaHook {
	return func(ctx context.Context, db *gorm.DB) error {
		for _, statement := range statements {
			if err := db.WithContext(ctx).Exec(statement).Error; err != nil {
				return fmt.Errorf("apply ddl: %w", err)
			}
		}
		return nil
	}
}
