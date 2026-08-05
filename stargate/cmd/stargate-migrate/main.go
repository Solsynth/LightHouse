// stargate-migrate copies the existing Dyson Network data into the Stargate
// database. It reads from the legacy dyson_padlock and dyson_passport
// databases (schema-parity targets) and preserves every UUID, bcrypt hash
// and bytea blob, so sessions, tokens and credentials keep working after
// cutover.
//
// Usage:
//
//	stargate-migrate \
//	  --padlock-dsn "postgres://.../dyson_padlock" \
//	  --passport-dsn "postgres://.../dyson_passport" \
//	  --target-dsn "postgres://.../dyson_stargate"
//
// Target DSN defaults to the STARGATE config [database] section.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/config"
)

// padlockTables is the copy order for the Padlock database (FK-safe).
var padlockTables = []string{
	"accounts",
	"account_auth_factors",
	"account_passkeys",
	"punishments",
	"authorized_apps",
	"account_connections",
	"account_contacts",
	"auth_clients",
	"auth_sessions",
	"auth_challenges",
	"api_keys",
	"action_logs",
	"permission_groups",
	"permission_nodes",
	"permission_group_members",
	"e2ee_devices",
	"e2ee_key_bundles",
	"e2ee_one_time_pre_keys",
	"e2ee_sessions",
	"e2ee_envelopes",
	"mls_key_packages",
	"mls_group_states",
	"mls_device_memberships",
}

// passportTables is the copy order for the Passport profile tables.
var passportTables = []string{
	"account_profiles",
	"account_board_items",
	"account_relationships",
}

func main() {
	var padlockDSN, passportDSN, targetDSN string
	var force, incremental bool
	flag.StringVar(&padlockDSN, "padlock-dsn", os.Getenv("PADLOCK_DSN"), "source dyson_padlock postgres DSN")
	flag.StringVar(&passportDSN, "passport-dsn", os.Getenv("PASSPORT_DSN"), "source dyson_passport postgres DSN")
	flag.StringVar(&targetDSN, "target-dsn", os.Getenv("TARGET_DSN"), "target dyson_stargate DSN (defaults to config)")
	flag.BoolVar(&force, "force", false, "proceed even though the target database already contains data")
	flag.BoolVar(&incremental, "incremental", false, "resume: skip fully-copied tables and only insert rows missing from the target (no truncate)")
	flag.Parse()

	if targetDSN == "" {
		cfg, err := config.Load("")
		if err != nil {
			log.Fatalf("load config: %v", err)
		}
		targetDSN = cfg.Database.DSN
	}
	if padlockDSN == "" {
		log.Fatal("--padlock-dsn is required")
	}

	ctx := context.Background()
	// A single connection for all target writes: trigger toggling and COPY
	// must share one session.
	target, err := pgx.Connect(ctx, targetDSN)
	if err != nil {
		log.Fatalf("connect target: %v", err)
	}
	defer target.Close(ctx)

	if err := ensureStateTable(ctx, target); err != nil {
		log.Fatalf("ensure migration state table: %v", err)
	}
	if !incremental {
		// Safety: the migration TRUNCATEs every table in the target. If the
		// target already holds migrated data (e.g. a wrong DSN pointing at a
		// live database, or a previous migration run), refuse unless --force.
		// The boot-time permission seed is expected and does not trip this.
		if nonEmpty, table, err := targetHasData(ctx, target); err != nil {
			log.Fatalf("check target data: %v", err)
		} else if nonEmpty && !force {
			log.Printf("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
			log.Printf("!!  WARNING: target database already contains data (%s).    !!", table)
			log.Printf("!!  Running will TRUNCATE all tables (CASCADE) and re-copy. !!")
			log.Printf("!!  Verify TARGET_DSN points at the NEW stargate database,  !!")
			log.Printf("!!  not a live database. Pass --force or --incremental to   !!")
			log.Printf("!!  proceed.                                                !!")
			log.Printf("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
			log.Fatal("aborting: target database is not empty (use --force or --incremental to override)")
		}
	}

	// Passport DSN is optional; the profile tables are skipped when absent.
	var passport *pgxpool.Pool
	if passportDSN != "" {
		passport, err = pgxpool.New(ctx, passportDSN)
		if err != nil {
			log.Fatalf("connect passport: %v", err)
		}
		defer passport.Close()
	}

	padlock, err := pgxpool.New(ctx, padlockDSN)
	if err != nil {
		log.Fatalf("connect padlock: %v", err)
	}
	defer padlock.Close()

	// Truncate target tables first (CASCADE), preserving the migration table.
	if !incremental {
		if _, err := target.Exec(ctx, `TRUNCATE TABLE
			mls_device_memberships, mls_group_states, mls_key_packages,
			e2ee_envelopes, e2ee_sessions, e2ee_one_time_pre_keys, e2ee_key_bundles, e2ee_devices,
			permission_group_members, permission_nodes, permission_groups,
			action_logs, auth_challenges, auth_sessions, auth_clients, api_keys,
			account_contacts, account_connections, authorized_apps, punishments,
			account_passkeys, account_auth_factors, account_relationships, account_board_items,
			account_profiles, accounts CASCADE`); err != nil {
			log.Fatalf("truncate target: %v", err)
		}
	}

	total := int64(0)
	skipped := 0
	copyOne := func(source *pgxpool.Pool, table string) {
		if incremental && !force {
			done, err := tableDone(ctx, target, table)
			if err != nil {
				log.Fatalf("check state for %s: %v", table, err)
			}
			if done {
				log.Printf("skip %s: already migrated (--incremental)", table)
				skipped++
				return
			}
		}
		n, err := copyTable(ctx, source, target, table, incremental)
		if err != nil {
			log.Fatalf("copy %s: %v", table, err)
		}
		if err := markTableDone(ctx, target, table, n); err != nil {
			log.Fatalf("record state for %s: %v", table, err)
		}
		log.Printf("padlock.%-26s %d rows", table, n)
		total += n
	}
	for _, table := range padlockTables {
		copyOne(padlock, table)
	}
	if passport != nil {
		for _, table := range passportTables {
			copyOne(passport, table)
		}
	}
	log.Printf("done: %d rows copied (%d tables skipped as already migrated)", total, skipped)
}

// migration_state tracks per-table completion for --incremental runs.
func ensureStateTable(ctx context.Context, dst *pgx.Conn) error {
	_, err := dst.Exec(ctx, `CREATE TABLE IF NOT EXISTS migration_state (
		table_name text PRIMARY KEY,
		rows_copied bigint NOT NULL,
		completed_at timestamptz NOT NULL DEFAULT now()
	)`)
	return err
}

func tableDone(ctx context.Context, dst *pgx.Conn, table string) (bool, error) {
	var exists bool
	err := dst.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM migration_state WHERE table_name = $1)`, table).Scan(&exists)
	return exists, err
}

func markTableDone(ctx context.Context, dst *pgx.Conn, table string, rowsCopied int64) error {
	_, err := dst.Exec(ctx, `INSERT INTO migration_state (table_name, rows_copied) VALUES ($1, $2)
		ON CONFLICT (table_name) DO UPDATE SET rows_copied = EXCLUDED.rows_copied, completed_at = now()`,
		table, rowsCopied)
	return err
}

// querier is the minimal query surface shared by pgx.Conn and pgxpool.Pool.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// targetHasData reports whether any table the migration copies into already
// holds rows. The permission_* tables are excluded: the boot-time permission
// seed populates them on a fresh database, so their presence is expected and
// not a sign of prior data.
func targetHasData(ctx context.Context, db querier) (bool, string, error) {
	var tables []string
	rows, err := db.Query(ctx, `SELECT tablename FROM pg_tables
		WHERE schemaname = 'public' AND tablename = ANY($1)`,
		[]string{
			"accounts", "account_profiles", "account_board_items", "account_relationships",
			"account_contacts", "account_connections", "account_auth_factors", "account_passkeys",
			"punishments", "auth_sessions", "auth_challenges", "auth_clients", "api_keys",
			"authorized_apps", "action_logs", "e2ee_devices", "e2ee_key_bundles", "e2ee_one_time_pre_keys",
			"e2ee_sessions", "e2ee_envelopes", "mls_key_packages", "mls_group_states",
			"mls_device_memberships",
		})
	if err != nil {
		return false, "", err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return false, "", err
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, "", err
	}

	for _, table := range tables {
		var count int64
		if err := db.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, table)).Scan(&count); err != nil {
			// Missing table (schema not applied yet) is fine.
			continue
		}
		if count > 0 {
			return true, table, nil
		}
	}
	return false, "", nil
}

// sourceQueryOverrides replaces the default `SELECT <cols> FROM <table>`
// with a custom query for tables whose source rows reference soft-deleted
// rows (EF soft-delete is app-level, so FK orphans exist in production
// data). auth_sessions is self-referencing: the reachable-from-root CTE
// keeps only sessions whose parent chain terminates at a NULL parent; the
// client_id/account_id EXISTS conditions drop rows whose device or account
// is missing (the generic FK filter does not apply to overrides). The outer
// ORDER BY guarantees parents insert before children with FK triggers
// active — no superuser needed.
var sourceQueryOverrides = map[string]string{
	"auth_sessions": `SELECT <COLS> FROM (
		WITH RECURSIVE reachable(id, __depth) AS (
			SELECT id, 0 FROM auth_sessions WHERE parent_session_id IS NULL
			UNION ALL
			SELECT s.id, r.__depth + 1 FROM auth_sessions s JOIN reachable r ON s.parent_session_id = r.id
		)
		SELECT s.*, r.__depth FROM auth_sessions s JOIN reachable r ON r.id = s.id
		WHERE (s.client_id IS NULL OR EXISTS (SELECT 1 FROM auth_clients c WHERE c.id = s.client_id))
		  AND (s.account_id IS NULL OR EXISTS (SELECT 1 FROM accounts a WHERE a.id = s.account_id))
	) AS src ORDER BY src.__depth`,
	"permission_nodes": `SELECT <COLS> FROM permission_nodes n WHERE n.group_id IS NULL OR EXISTS (SELECT 1 FROM permission_groups g WHERE g.id = n.group_id)`,
}

// fkRef describes one outgoing foreign-key column of a table.
type fkRef struct {
	col      string
	refTable string
	refCol   string
}

// outgoingFKs lists the table's foreign keys via the catalog, without
// requiring superuser.
func outgoingFKs(ctx context.Context, db querier, table string) ([]fkRef, error) {
	rows, err := db.Query(ctx, `
		SELECT a.attname, c.confrelid::regclass::text, af.attname
		FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid AND t.relname = $1
		JOIN pg_namespace n ON n.oid = t.relnamespace AND n.nspname = 'public'
		JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord) ON true
		JOIN LATERAL unnest(c.confkey) WITH ORDINALITY AS fk(attnum, ord) ON fk.ord = k.ord
		JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = k.attnum
		JOIN pg_attribute af ON af.attrelid = c.confrelid AND af.attnum = fk.attnum
		WHERE c.contype = 'f'`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var fks []fkRef
	for rows.Next() {
		var fk fkRef
		if err := rows.Scan(&fk.col, &fk.refTable, &fk.refCol); err != nil {
			return nil, err
		}
		fk.refTable = strings.TrimPrefix(fk.refTable, "public.")
		fks = append(fks, fk)
	}
	return fks, rows.Err()
}

// copyTable streams every row of the source table into the target table,
// reconciling schema drift: columns present in both are copied verbatim;
// target-only NOT NULL columns (e.g. epoch, added by newer migrations) are
// copyTable streams every row of the source table into the target table,
// reconciling schema drift: columns present in both are copied verbatim;
// target-only NOT NULL columns (e.g. epoch, added by newer migrations) are
// zero-filled by type. Rows are deduplicated against every unique index the
// TARGET declares (the live source may predate them and carry duplicates);
// in incremental mode the target's existing keys seed the dedupe so a
// re-run only inserts missing rows. A missing source table is skipped
// (returns 0) so shared-DB deployments where some tables were never migrated
// do not abort.
func copyTable(ctx context.Context, src *pgxpool.Pool, dst *pgx.Conn, table string, incremental bool) (int64, error) {
	var exists bool
	if err := src.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_tables WHERE schemaname = 'public' AND tablename = $1)`, table).Scan(&exists); err != nil {
		return 0, err
	}
	if !exists {
		log.Printf("skip %s: not present in source", table)
		return 0, nil
	}

	// Column inventory on both sides.
	srcCols, err := tableColumns(ctx, src, table)
	if err != nil {
		return 0, err
	}
	dstCols, err := tableColumns(ctx, dst, table)
	if err != nil {
		return 0, err
	}
	srcSet := map[string]struct{}{}
	for _, c := range srcCols {
		srcSet[c.name] = struct{}{}
	}
	var copyCols []string
	var fillCols []columnInfo // target-only columns to zero-fill
	var selectCols []string   // columns present in BOTH sides
	for _, c := range dstCols {
		if _, ok := srcSet[c.name]; ok {
			copyCols = append(copyCols, c.name)
			selectCols = append(selectCols, c.name)
		} else if c.notNull {
			fillCols = append(fillCols, c)
			copyCols = append(copyCols, c.name)
		}
	}
	if len(copyCols) == 0 {
		return 0, nil
	}
	colPos := make(map[string]int, len(copyCols))
	for i, col := range copyCols {
		colPos[col] = i
	}

	query := fmt.Sprintf(`SELECT %s FROM %s`, quoteCols(selectCols), table)
	if override, ok := sourceQueryOverrides[table]; ok {
		query = strings.ReplaceAll(override, "<COLS>", quoteCols(selectCols))
	} else if fks, err := outgoingFKs(ctx, dst, table); err != nil {
		return 0, err
	} else if len(fks) > 0 {
		// The source data can carry orphaned FK references (EF soft-delete
		// is app-level), and the live source database may predate the FK
		// constraints (or even the columns) entirely — so the constraint set
		// is read from the TARGET, which is what the COPY must satisfy, and
		// only applied to columns the source actually has. Filter orphaned
		// rows out at the source: keep rows whose referenced key exists.
		// Self-referencing FKs get the depth-ordered reachable-set treatment
		// (see sourceQueryOverrides).
		srcColSet := make(map[string]struct{}, len(srcCols))
		for _, c := range srcCols {
			srcColSet[c.name] = struct{}{}
		}
		var conds []string
		for _, fk := range fks {
			if _, ok := srcColSet[fk.col]; !ok {
				continue // source predates this column; cannot reference it
			}
			conds = append(conds, fmt.Sprintf(`(%s.%s IS NULL OR EXISTS (SELECT 1 FROM %s WHERE %s = %s.%s))`,
				table, fk.col, fk.refTable, fk.refCol, table, fk.col))
		}
		if len(conds) > 0 {
			query += ` WHERE ` + strings.Join(conds, " AND ")
		}
	}

	// Unique-index dedupe: the target's unique constraints are the contract
	// the COPY must satisfy. In incremental mode, seed the seen sets from
	// the target's existing rows so re-runs only insert what is missing.
	uniques, err := uniqueIndexes(ctx, dst, table)
	if err != nil {
		return 0, err
	}
	seen := make([]map[string]struct{}, len(uniques))
	for i := range seen {
		seen[i] = map[string]struct{}{}
	}
	if incremental {
		for i, ui := range uniques {
			cols := ui.cols
			if len(cols) == 0 || !allIn(colPos, cols) {
				continue
			}
			sel := quoteCols(cols)
			tRows, err := dst.Query(ctx, fmt.Sprintf(`SELECT %s FROM %s`, sel, table))
			if err != nil {
				return 0, err
			}
			for tRows.Next() {
				vals, err := tRows.Values()
				if err != nil {
					tRows.Close()
					return 0, err
				}
				seen[i][keyOf(vals, cols)] = struct{}{}
			}
			tRows.Close()
			if err := tRows.Err(); err != nil {
				return 0, err
			}
		}
	}

	rows, err := src.Query(ctx, query)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	fields := rows.FieldDescriptions()
	fieldSet := map[string]int{}
	for i, f := range fields {
		fieldSet[f.Name] = i
	}
	// Target column → source field index or -1 for zero-fill.
	idx := make([]int, len(copyCols))
	for i, col := range copyCols {
		if j, ok := fieldSet[col]; ok {
			idx[i] = j
		} else {
			idx[i] = -1
		}
	}
	fillValue := make([]any, len(copyCols))
	for i := range fillValue {
		if idx[i] == -1 {
			fillValue[i] = zeroValue(fillColsFor(copyCols[i], fillCols))
		}
	}

	var copied, deduped int64
	batch := make([][]any, 0, 500)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		n, err := dst.CopyFrom(ctx, pgx.Identifier{table}, copyCols, pgx.CopyFromRows(batch))
		if err != nil {
			return err
		}
		copied += n
		batch = batch[:0]
		return nil
	}

	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return copied, err
		}
		row := make([]any, len(copyCols))
		for i, fi := range idx {
			if fi == -1 {
				row[i] = fillValue[i]
			} else {
				row[i] = values[fi]
			}
		}
		if len(uniques) > 0 {
			dup := false
			for i, ui := range uniques {
				if len(ui.cols) == 0 || !allIn(colPos, ui.cols) {
					continue
				}
				k := keyOfRow(row, colPos, ui.cols)
				if _, ok := seen[i][k]; ok {
					dup = true
					break
				}
				seen[i][k] = struct{}{}
			}
			if dup {
				deduped++
				continue
			}
		}
		batch = append(batch, row)
		if len(batch) >= 500 {
			if err := flush(); err != nil {
				return copied, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return copied, err
	}
	if err := flush(); err != nil {
		return copied, err
	}
	if deduped > 0 {
		log.Printf("note: %s dropped %d duplicate rows (target unique constraints)", table, deduped)
	}
	return copied, nil
}

// uniqueIndex describes one unique index's key columns.
type uniqueIndex struct{ cols []string }

// uniqueIndexes lists the target table's unique indexes (PK-backed and plain
// UNIQUE indexes), read from the catalog like outgoingFKs.
func uniqueIndexes(ctx context.Context, db querier, table string) ([]uniqueIndex, error) {
	rows, err := db.Query(ctx, `
		SELECT i.indexrelid::text, a.attname
		FROM pg_index i
		JOIN pg_class t ON t.oid = i.indrelid AND t.relname = $1
		JOIN pg_namespace n ON n.oid = t.relnamespace AND n.nspname = 'public'
		JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord) ON true
		JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = k.attnum
		WHERE i.indisunique AND i.indisvalid AND i.indpred IS NULL
		ORDER BY i.indexrelid::text, k.ord`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var uniques []uniqueIndex
	byID := map[string]*uniqueIndex{}
	for rows.Next() {
		var id, col string
		if err := rows.Scan(&id, &col); err != nil {
			return nil, err
		}
		ui, ok := byID[id]
		if !ok {
			ui = &uniqueIndex{}
			byID[id] = ui
			uniques = append(uniques, *ui)
		}
		ui.cols = append(ui.cols, col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// byID holds pointers to copies; rebuild properly.
	out := make([]uniqueIndex, 0, len(byID))
	for _, ui := range byID {
		out = append(out, *ui)
	}
	return out, nil
}

func allIn(colPos map[string]int, cols []string) bool {
	for _, c := range cols {
		if _, ok := colPos[c]; !ok {
			return false
		}
	}
	return true
}

// keyOf builds a dedupe key from raw scanned values.
func keyOf(vals []any, cols []string) string {
	var b strings.Builder
	for _, v := range vals {
		if v == nil {
			b.WriteString("∅")
		} else {
			fmt.Fprintf(&b, "%v|", v)
		}
	}
	return b.String()
}

// keyOfRow builds a dedupe key from a copy row using column positions.
func keyOfRow(row []any, colPos map[string]int, cols []string) string {
	var b strings.Builder
	for _, col := range cols {
		pos := colPos[col]
		if pos >= len(row) || row[pos] == nil {
			b.WriteString("∅")
			continue
		}
		fmt.Fprintf(&b, "%v|", row[pos])
	}
	return b.String()
}

type columnInfo struct {
	name    string
	notNull bool
	typ     string
}

// tableColumns lists a table's public columns with nullability and type.
func tableColumns(ctx context.Context, db querier, table string) ([]columnInfo, error) {
	rows, err := db.Query(ctx, `SELECT a.attname, NOT a.attnotnull, pg_catalog.format_type(a.atttypid, a.atttypmod)
		FROM pg_attribute a
		WHERE a.attrelid = $1::regclass AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []columnInfo
	for rows.Next() {
		var c columnInfo
		var nullable bool
		if err := rows.Scan(&c.name, &nullable, &c.typ); err != nil {
			return nil, err
		}
		c.notNull = !nullable
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

func quoteCols(cols []string) string {
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = `"` + c + `"`
	}
	return joinStrings(quoted)
}

func joinStrings(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

func fillColsFor(name string, fillCols []columnInfo) columnInfo {
	for _, c := range fillCols {
		if c.name == name {
			return c
		}
	}
	return columnInfo{name: name}
}

// zeroValue maps a PostgreSQL type to a zero fill value.
func zeroValue(c columnInfo) any {
	switch {
	case c.typ == "integer", c.typ == "smallint", c.typ == "bigint":
		return int64(0)
	case c.typ == "boolean":
		return false
	case c.typ == "double precision", c.typ == "real", c.typ == "numeric":
		return float64(0)
	case c.typ == "jsonb", c.typ == "json":
		return []byte("{}")
	case c.typ == "uuid":
		return "00000000-0000-0000-0000-000000000000"
	default:
		return ""
	}
}
