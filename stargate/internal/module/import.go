package module

import (
	"context"

	"gorm.io/gorm"
)

// ImportOptions controls a module data import.
type ImportOptions struct {
	DryRun    bool
	BatchSize int
}

// ImportStats reports one module import run.
type ImportStats struct {
	Module  string
	Table   string
	Read    int64
	Written int64
	Skipped int64
}

// Importer copies legacy Padlock/Passport rows into a module's new schema.
// Implementations must be idempotent, must not truncate the target, and must
// honor DryRun.
type Importer interface {
	Import(ctx context.Context, source, target *gorm.DB, opts ImportOptions) (ImportStats, error)
}

// ImporterFor finds the importer for a module ID.
func ImporterFor(factories []Factory, id string) (Importer, bool) {
	for _, factory := range factories {
		if factory.ID != id || factory.Importer == nil {
			continue
		}
		importer := factory.Importer()
		if importer == nil {
			return nil, false
		}
		return importer, true
	}
	return nil, false
}
