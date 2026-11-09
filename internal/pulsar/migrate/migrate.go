package migrate

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// Run applies core prerequisite database extensions.
func Run(ctx context.Context, database *gorm.DB) error {
	for _, statement := range []string{
		`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`,
		`CREATE EXTENSION IF NOT EXISTS pg_trgm`,
	} {
		if err := database.WithContext(ctx).Exec(statement).Error; err != nil {
			return fmt.Errorf("pulsar migrate %q: %w", statement, err)
		}
	}
	return nil
}
