package wallet

import (
	"context"

	"gorm.io/gorm"
)

// SchemaModels is the AutoMigrate input for the module factory. The tables
// keep the C# wallet names, so stargate-migrate can copy a legacy wallet
// database without renaming columns.
func SchemaModels() []any { return entities() }

// SchemaHooks are the raw-SQL schema hooks the factory applies after
// AutoMigrate.
func SchemaHooks() []func(ctx context.Context, db *gorm.DB) error {
	hooks := make([]func(ctx context.Context, db *gorm.DB) error, 0, len(schemaHooks))
	for _, hook := range schemaHooks {
		hooks = append(hooks, func(ctx context.Context, db *gorm.DB) error {
			return hook(db.WithContext(ctx))
		})
	}
	return hooks
}
