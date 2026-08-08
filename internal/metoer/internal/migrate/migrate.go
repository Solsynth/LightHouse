// Package migrate runs the embedded SQL migrations on boot.
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

	"src.solsynth.dev/sosys/metoer/internal/store"
)

//go:embed *.sql
var files embed.FS

// ErrUnsafeDatabase indicates a nonempty database without migration history.
var ErrUnsafeDatabase = errors.New("refusing to migrate nonempty database without schema_migrations")

// Run applies every embedded migration that has not been recorded yet.
func Run(ctx context.Context, database *gorm.DB) error {
	database = database.WithContext(ctx)
	tables, err := database.Migrator().GetTables()
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
		if err := database.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
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
		var migration store.SchemaMigrationEntity
		err := database.Where("version = ?", name).First(&migration).Error
		if err == nil {
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		content, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		err = database.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(string(content)).Error; err != nil {
				return fmt.Errorf("apply migration %s: %w", name, err)
			}
			return tx.Create(&store.SchemaMigrationEntity{Version: name, AppliedAt: time.Now().UTC()}).Error
		})
		if err != nil {
			return err
		}
	}
	return nil
}
