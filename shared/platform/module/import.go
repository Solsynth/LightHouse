package module

import "context"

// Importer copies legacy data into a module whose table layout changed during
// the Go port. Implementations must be idempotent, must never truncate the
// target, and must report how many rows they skipped and why.
type Importer interface {
	Module() string
	// Import copies rows from sourceDSN into targetDSN. A dry run reports the
	// same counts without writing.
	Import(ctx context.Context, sourceDSN, targetDSN string, dryRun bool) (ImportReport, error)
}

// ImportReport is the outcome of one importer run.
type ImportReport struct {
	Module  string
	Tables  []ImportTableReport
	Skipped []string
}

// ImportTableReport is the per-table row count of one importer run.
type ImportTableReport struct {
	Table     string
	Read      int64
	Written   int64
	Skipped   int64
	FirstSeen string
}

// TotalWritten is the sum of written rows across tables.
func (r ImportReport) TotalWritten() int64 {
	var total int64
	for _, table := range r.Tables {
		total += table.Written
	}
	return total
}
