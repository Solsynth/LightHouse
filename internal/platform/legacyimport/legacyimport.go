// Package legacyimport copies the C# services' tables into a Lighthouse
// database. The Go entities keep the legacy table and column names, so an
// import is a straight copy: COPY the source rows into a staging table, then
// INSERT ... SELECT ... ON CONFLICT DO NOTHING. Importers are idempotent and
// never truncate.
package legacyimport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Report is the outcome of one import run.
type Report struct {
	Tables  []TableReport
	Skipped []string
}

// TableReport is one table's row count.
type TableReport struct {
	Table   string
	Written int64
}

// TotalWritten is the sum of written rows.
func (r Report) TotalWritten() int64 {
	var total int64
	for _, table := range r.Tables {
		total += table.Written
	}
	return total
}

// Run copies the named tables from sourceDSN into targetDSN. dryRun reports the
// rows that would be copied without writing them.
func Run(ctx context.Context, sourceDSN, targetDSN string, tables []string, dryRun bool) (Report, error) {
	source, err := pgx.Connect(ctx, sourceDSN)
	if err != nil {
		return Report{}, fmt.Errorf("connect source: %w", err)
	}
	defer source.Close(ctx)

	target, err := pgx.Connect(ctx, targetDSN)
	if err != nil {
		return Report{}, fmt.Errorf("connect target: %w", err)
	}
	defer target.Close(ctx)

	report := Report{}
	for _, table := range tables {
		exists, err := tableExists(ctx, source, table)
		if err != nil {
			return report, err
		}
		if !exists {
			report.Skipped = append(report.Skipped, table+" (absent in source)")
			continue
		}
		exists, err = tableExists(ctx, target, table)
		if err != nil {
			return report, err
		}
		if !exists {
			report.Skipped = append(report.Skipped, table+" (absent in target; boot the service once first)")
			continue
		}

		written, err := copyTable(ctx, source, target, table, dryRun)
		if err != nil {
			return report, err
		}
		report.Tables = append(report.Tables, TableReport{Table: table, Written: written})
	}
	return report, nil
}

func tableExists(ctx context.Context, conn *pgx.Conn, table string) (bool, error) {
	var exists bool
	err := conn.QueryRow(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM information_schema.tables
		   WHERE table_schema = 'public' AND table_name = $1
		 )`, table).Scan(&exists)
	return exists, err
}

// copyTable stages the source rows in a temp table shaped like the target and
// then inserts them, skipping rows the target already holds.
func copyTable(ctx context.Context, source, target *pgx.Conn, table string, dryRun bool) (int64, error) {
	columns, err := columnsOf(ctx, target, table)
	if err != nil {
		return 0, err
	}
	columnList := strings.Join(quoteAll(columns), ", ")
	stage := "stage_" + table

	if _, err := target.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", quote(stage))); err != nil {
		return 0, fmt.Errorf("%s: drop stale staging table: %w", table, err)
	}
	if _, err := target.Exec(ctx, fmt.Sprintf(
		"CREATE TEMP TABLE %s AS SELECT %s FROM %s WITH NO DATA",
		quote(stage), columnList, quote(table))); err != nil {
		return 0, fmt.Errorf("%s: create staging table: %w", table, err)
	}
	defer func() {
		_, _ = target.Exec(context.WithoutCancel(ctx), fmt.Sprintf("DROP TABLE IF EXISTS %s", quote(stage)))
	}()

	// Stream the source rows into the staging table without buffering the table
	// in memory: COPY TO STDOUT on the source feeds COPY FROM STDIN on the
	// target through a pipe.
	reader, writer := io.Pipe()
	copyErr := make(chan error, 1)
	go func() {
		_, err := source.PgConn().CopyTo(ctx, writer,
			fmt.Sprintf("COPY (SELECT %s FROM %s) TO STDOUT", columnList, quote(table)))
		_ = writer.CloseWithError(err)
		copyErr <- err
	}()
	_, err = target.PgConn().CopyFrom(ctx, reader,
		fmt.Sprintf("COPY %s (%s) FROM STDIN", quote(stage), columnList))
	_ = reader.CloseWithError(err)
	if sourceErr := <-copyErr; sourceErr != nil {
		return 0, fmt.Errorf("%s: read source rows: %w", table, sourceErr)
	}
	if err != nil {
		return 0, fmt.Errorf("%s: copy rows: %w", table, err)
	}

	var staged int64
	if err := target.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", quote(stage))).Scan(&staged); err != nil {
		return 0, fmt.Errorf("%s: count staged rows: %w", table, err)
	}
	if dryRun {
		return staged, nil
	}

	tag, err := target.Exec(ctx, fmt.Sprintf(
		"INSERT INTO %s (%s) SELECT %s FROM %s ON CONFLICT DO NOTHING",
		quote(table), columnList, columnList, quote(stage)))
	if err != nil {
		return 0, fmt.Errorf("%s: insert rows: %w", table, err)
	}
	return tag.RowsAffected(), nil
}

// columnsOf lists the target table's columns so the copy is explicit and
// survives a source table with extra columns.
func columnsOf(ctx context.Context, conn *pgx.Conn, table string) ([]string, error) {
	rows, err := conn.Query(ctx,
		`SELECT column_name FROM information_schema.columns
		 WHERE table_schema = 'public' AND table_name = $1
		 ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, fmt.Errorf("%s: read columns: %w", table, err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, errors.New(table + ": no columns found")
	}
	return columns, nil
}

func quote(identifier string) string { return `"` + identifier + `"` }

func quoteAll(identifiers []string) []string {
	out := make([]string, len(identifiers))
	for i, identifier := range identifiers {
		out[i] = quote(identifier)
	}
	return out
}
