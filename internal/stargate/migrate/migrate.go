// Package migrate runs the embedded SQL migrations on boot. The DDL mirrors
// the EF Core schema (snake_case naming) and is idempotent.
package migrate

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

//go:embed *.sql
var files embed.FS

// ErrUnsafeDatabase identifies a non-empty database with no migration ledger.
// Running the historical migrations there could execute destructive DROP
// statements against unrelated data, so boot must fail closed.
var ErrUnsafeDatabase = errors.New("migration safety: non-empty database has no schema_migrations ledger")

type schemaMigration struct {
	Version   string    `gorm:"column:version;primaryKey"`
	AppliedAt time.Time `gorm:"column:applied_at"`
}

func (schemaMigration) TableName() string { return "schema_migrations" }

// Run applies every embedded migration that has not been recorded yet.
func Run(ctx context.Context, database *gorm.DB) error {
	tables, err := database.WithContext(ctx).Migrator().GetTables()
	if err != nil {
		return fmt.Errorf("inspect database tables: %w", err)
	}
	hasLedger := false
	for _, table := range tables {
		if table == "schema_migrations" {
			hasLedger = true
			break
		}
	}
	if !hasLedger {
		if len(tables) != 0 {
			return ErrUnsafeDatabase
		}
		if err := database.WithContext(ctx).Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
			version text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`).Error; err != nil {
			return fmt.Errorf("create schema_migrations: %w", err)
		}
	}

	entries, err := files.ReadDir(".")
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var migration schemaMigration
		result := database.WithContext(ctx).Where("version = ?", name).First(&migration)
		if result.Error == nil {
			continue
		}
		if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return fmt.Errorf("check migration %s: %w", name, result.Error)
		}
		content, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		if err := database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(string(content)).Error; err != nil {
				return fmt.Errorf("apply migration %s: %w", name, err)
			}
			if err := tx.Create(&schemaMigration{Version: name}).Error; err != nil {
				return fmt.Errorf("record migration %s: %w", name, err)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
