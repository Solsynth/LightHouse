package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/dbtest"
)

// newPair returns a source and a target connection, each in its own dedicated
// database, plus the migration_state table on the target.
func newPair(t *testing.T, ctx context.Context) (*pgxpool.Pool, *pgx.Conn, string, string) {
	t.Helper()
	baseDSN := os.Getenv("STARGATE_TEST_DSN")
	if baseDSN == "" {
		t.Skip("STARGATE_TEST_DSN is not configured")
	}
	srcDSN, cleanupSrc, err := dbtest.NewDatabase(ctx, baseDSN)
	if err != nil {
		t.Skipf("cannot create source database: %v", err)
	}
	t.Cleanup(cleanupSrc)
	dstDSN, cleanupDst, err := dbtest.NewDatabase(ctx, baseDSN)
	if err != nil {
		t.Skipf("cannot create target database: %v", err)
	}
	t.Cleanup(cleanupDst)

	src, err := pgxpool.New(ctx, srcDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(src.Close)
	dst, err := pgx.Connect(ctx, dstDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dst.Close(ctx) })
	if err := ensureStateTable(ctx, dst); err != nil {
		t.Fatal(err)
	}
	return src, dst, srcDSN, dstDSN
}

const demoSchema = `CREATE TABLE demo (id uuid PRIMARY KEY, name text NOT NULL)`

func createTable(t *testing.T, ctx context.Context, dsn, statement string) {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, statement); err != nil {
		t.Fatalf("create table: %v", err)
	}
}

func tableRowCount(t *testing.T, ctx context.Context, dsn, table string) int64 {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var count int64
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func stateRecorded(t *testing.T, ctx context.Context, dst *pgx.Conn, table string) (bool, int64) {
	t.Helper()
	var rows int64
	err := dst.QueryRow(ctx, `SELECT rows_copied FROM migration_state WHERE table_name = $1`, table).Scan(&rows)
	if err == pgx.ErrNoRows {
		return false, 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return true, rows
}

// A table the source does not have used to be recorded as complete with 0
// rows, so every later --incremental run skipped it long after the source
// table appeared. Nothing may be recorded for a table that was not copied.
func TestSkippedTablesAreNotRecordedAsComplete(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	src, dst, srcDSN, dstDSN := newPair(t, ctx)

	createTable(t, ctx, dstDSN, demoSchema)

	if copied, skipped := copyOne(ctx, src, dst, "test", "demo", true, true); copied != 0 || !skipped {
		t.Fatalf("copyOne = (%d, %v), want (0, true)", copied, skipped)
	}
	if recorded, _ := stateRecorded(t, ctx, dst, "demo"); recorded {
		t.Fatal("a table missing from the source must not be recorded as migrated")
	}

	// The legacy table appears later: the next run must copy it.
	createTable(t, ctx, srcDSN, demoSchema)
	if _, err := src.Exec(ctx, `INSERT INTO demo (id, name) VALUES (gen_random_uuid(), 'a'), (gen_random_uuid(), 'b')`); err != nil {
		t.Fatal(err)
	}
	copied, skipped := copyOne(ctx, src, dst, "test", "demo", true, true)
	if copied != 2 || skipped {
		t.Fatalf("copyOne = (%d, %v), want (2, false)", copied, skipped)
	}
	if rows := tableRowCount(t, ctx, dstDSN, "demo"); rows != 2 {
		t.Fatalf("target rows = %d, want 2", rows)
	}
	if recorded, rows := stateRecorded(t, ctx, dst, "demo"); !recorded || rows != 2 {
		t.Fatalf("state = (%v, %d), want (true, 2)", recorded, rows)
	}

	// A completed table is skipped, not copied twice.
	if copied, skipped := copyOne(ctx, src, dst, "test", "demo", true, true); copied != 0 || !skipped {
		t.Fatalf("second copyOne = (%d, %v), want skip", copied, skipped)
	}
}

// The reverse direction: the target table is missing because the owning
// module is disabled. It must not be recorded either; enabling the module and
// re-running copies it.
func TestMissingTargetTableIsNotRecorded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	src, dst, srcDSN, dstDSN := newPair(t, ctx)

	createTable(t, ctx, srcDSN, demoSchema)
	if _, err := src.Exec(ctx, `INSERT INTO demo (id, name) VALUES (gen_random_uuid(), 'a')`); err != nil {
		t.Fatal(err)
	}

	if copied, skipped := copyOne(ctx, src, dst, "test", "demo", true, true); copied != 0 || !skipped {
		t.Fatalf("copyOne = (%d, %v), want (0, true)", copied, skipped)
	}
	if recorded, _ := stateRecorded(t, ctx, dst, "demo"); recorded {
		t.Fatal("a table missing from the target must not be recorded as migrated")
	}

	createTable(t, ctx, dstDSN, demoSchema)
	if copied, skipped := copyOne(ctx, src, dst, "test", "demo", true, true); copied != 1 || skipped {
		t.Fatalf("copyOne after enabling = (%d, %v), want (1, false)", copied, skipped)
	}
	if rows := tableRowCount(t, ctx, dstDSN, "demo"); rows != 1 {
		t.Fatalf("target rows = %d, want 1", rows)
	}
}

// A completion record left behind by an older run (or by a --force run whose
// TRUNCATE emptied the table) must not make --incremental skip a table that
// holds no rows.
func TestStaleCompletionWithEmptyTargetIsRecopied(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	src, dst, srcDSN, dstDSN := newPair(t, ctx)

	createTable(t, ctx, srcDSN, demoSchema)
	createTable(t, ctx, dstDSN, demoSchema)
	if _, err := src.Exec(ctx, `INSERT INTO demo (id, name) VALUES (gen_random_uuid(), 'a')`); err != nil {
		t.Fatal(err)
	}
	if err := markTableDone(ctx, dst, "demo", 0); err != nil {
		t.Fatal(err)
	}

	copied, skipped := copyOne(ctx, src, dst, "test", "demo", true, true)
	if copied != 1 || skipped {
		t.Fatalf("copyOne = (%d, %v), want (1, false)", copied, skipped)
	}
	if rows := tableRowCount(t, ctx, dstDSN, "demo"); rows != 1 {
		t.Fatalf("target rows = %d, want 1", rows)
	}
}

// Without --force, --incremental still deduplicates against the target rows
// instead of failing on the primary key.
func TestIncrementalDedupesExistingRows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	src, dst, srcDSN, dstDSN := newPair(t, ctx)

	createTable(t, ctx, srcDSN, demoSchema)
	createTable(t, ctx, dstDSN, demoSchema)
	if _, err := src.Exec(ctx, `INSERT INTO demo (id, name) VALUES ('11111111-1111-1111-1111-111111111111', 'a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.Exec(ctx, `INSERT INTO demo (id, name) VALUES ('11111111-1111-1111-1111-111111111111', 'a')`); err != nil {
		t.Fatal(err)
	}

	copied, skipped := copyOne(ctx, src, dst, "test", "demo", true, true)
	if copied != 0 || skipped {
		t.Fatalf("copyOne = (%d, %v), want (0, false) with the row deduplicated", copied, skipped)
	}
	if rows := tableRowCount(t, ctx, dstDSN, "demo"); rows != 1 {
		t.Fatalf("target rows = %d, want 1", rows)
	}
}

// A target whose column types drifted from the source (a module entity tag
// once let AutoMigrate widen uuid to text and integer to bigint) must still
// accept the COPY: the source columns are cast to the target types instead of
// failing with "cannot find encode plan".
func TestCopyCastsToTargetColumnTypes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	src, dst, srcDSN, dstDSN := newPair(t, ctx)

	createTable(t, ctx, srcDSN, `CREATE TABLE drifted (
		id uuid PRIMARY KEY,
		account_id uuid NOT NULL,
		attitude integer NOT NULL,
		label varchar(1024) NULL)`)
	// The widened shape the old build produced.
	createTable(t, ctx, dstDSN, `CREATE TABLE drifted (
		id text PRIMARY KEY,
		account_id text NOT NULL,
		attitude bigint NOT NULL,
		label text NULL)`)
	if _, err := src.Exec(ctx, `INSERT INTO drifted (id, account_id, attitude, label)
		VALUES ('11111111-1111-1111-1111-111111111111', '22222222-2222-2222-2222-222222222222', 3, 'busy')`); err != nil {
		t.Fatal(err)
	}

	copied, skipped := copyOne(ctx, src, dst, "test", "drifted", false, false)
	if copied != 1 || skipped {
		t.Fatalf("copyOne = (%d, %v), want (1, false)", copied, skipped)
	}
	var account, label string
	if err := dst.QueryRow(ctx, `SELECT account_id, label FROM drifted`).Scan(&account, &label); err != nil {
		t.Fatal(err)
	}
	if account != "22222222-2222-2222-2222-222222222222" || label != "busy" {
		t.Fatalf("row = (%q, %q), want the source values", account, label)
	}
}
