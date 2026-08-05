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
	flag.StringVar(&padlockDSN, "padlock-dsn", "", "source dyson_padlock postgres DSN")
	flag.StringVar(&passportDSN, "passport-dsn", "", "source dyson_passport postgres DSN")
	flag.StringVar(&targetDSN, "target-dsn", "", "target dyson_stargate DSN (defaults to config)")
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
	target, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		log.Fatalf("connect target: %v", err)
	}
	defer target.Close()

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

	total := int64(0)
	for _, table := range padlockTables {
		n, err := copyTable(ctx, padlock, target, table)
		if err != nil {
			log.Fatalf("copy %s: %v", table, err)
		}
		log.Printf("padlock.%-26s %d rows", table, n)
		total += n
	}
	if passport != nil {
		for _, table := range passportTables {
			n, err := copyTable(ctx, passport, target, table)
			if err != nil {
				log.Fatalf("copy %s: %v", table, err)
			}
			log.Printf("passport.%-25s %d rows", table, n)
			total += n
		}
	}
	log.Printf("done: %d rows copied", total)
}

// sourceQueryOverrides replaces the default `SELECT <cols> FROM <table>`
// with a custom query for tables whose source rows reference soft-deleted
// rows (EF soft-delete is app-level, so FK orphans exist in production
// data). Orphan rows are dropped.
var sourceQueryOverrides = map[string]string{
	"permission_nodes": `SELECT <COLS> FROM permission_nodes n WHERE n.group_id IS NULL OR EXISTS (SELECT 1 FROM permission_groups g WHERE g.id = n.group_id)`,
}

// copyTable streams every row of the source table into the target table,
// reconciling schema drift: columns present in both are copied verbatim;
// target-only NOT NULL columns (e.g. epoch, added by newer migrations) are
// zero-filled by type. A missing source table is skipped (returns 0) so
// shared-DB deployments where some tables were never migrated do not abort.
func copyTable(ctx context.Context, src, dst *pgxpool.Pool, table string) (int64, error) {
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

	query := fmt.Sprintf(`SELECT %s FROM %s`, quoteCols(selectCols), table)
	if override, ok := sourceQueryOverrides[table]; ok {
		query = strings.ReplaceAll(override, "<COLS>", quoteCols(selectCols))
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

	var copied int64
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
	return copied, nil
}

type columnInfo struct {
	name    string
	notNull bool
	typ     string
}

// tableColumns lists a table's public columns with nullability and type.
func tableColumns(ctx context.Context, pool *pgxpool.Pool, table string) ([]columnInfo, error) {
	rows, err := pool.Query(ctx, `SELECT a.attname, NOT a.attnotnull, pg_catalog.format_type(a.atttypid, a.atttypmod)
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
