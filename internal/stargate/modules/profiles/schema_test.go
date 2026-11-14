package profiles_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/stargate/db"
	"src.solsynth.dev/sosys/stargate/dbtest"
	"src.solsynth.dev/sosys/stargate/migrate"
	"src.solsynth.dev/sosys/stargate/modules/standard"
)

// widenedColumns are the columns AutoMigrate rewrote on databases booted with
// the pre-repair entity tags (uuid -> text, integer -> bigint, varchar -> text).
// A text column cannot receive a uuid or bigint binary parameter, so the legacy
// importer failed on these tables; the pinned entity types must restore them,
// and their rows must survive.
var widenedColumns = [][2]string{
	{"account_statuses", "account_id"},
	{"account_statuses", "app_identifier"},
	{"account_statuses", "attitude"},
	{"account_statuses", "label"},
	{"account_statuses", "symbol"},
	{"account_statuses", "type"},
	{"presence_activities", "account_id"},
	{"presence_activities", "catalog_id"},
	{"presence_activities", "caption"},
	{"presence_activities", "visibility"},
	{"presence_activity_tags", "name"},
	{"presence_catalog_items", "account_id"},
	{"presence_catalog_items", "session_count"},
	{"presence_catalog_items", "catalog_key"},
	{"presence_catalog_tags", "name"},
}

func TestModuleSchemaRestoresWidenedColumns(t *testing.T) {
	baseDSN := os.Getenv("STARGATE_TEST_DSN")
	if baseDSN == "" {
		t.Skip("STARGATE_TEST_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dsn, cleanup, err := dbtest.NewDatabase(ctx, baseDSN)
	if err != nil {
		t.Skipf("cannot create dedicated database: %v", err)
	}
	t.Cleanup(cleanup)
	database, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(database) })
	if err := migrate.Run(ctx, database); err != nil {
		t.Fatal(err)
	}

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Real rows, inserted into the declared schema, must survive the repair.
	if _, err := conn.Exec(ctx, `
		INSERT INTO account_statuses (id, account_id, attitude, is_automated, type, created_at, updated_at)
		VALUES (gen_random_uuid(), gen_random_uuid(), 1, false, 2, now(), now());
		INSERT INTO presence_catalog_items (id, account_id, meta, queryable_terms, session_count, total_seconds, visibility, created_at, updated_at)
		VALUES (gen_random_uuid(), gen_random_uuid(), '{}', '{}', 3, 10, 0, now(), now());
		INSERT INTO presence_activities (id, lease_minutes, lease_expires_at, account_id, created_at, updated_at, queryable_terms, visibility)
		SELECT gen_random_uuid(), 5, now(), account_id, now(), now(), '{}', 0 FROM presence_catalog_items LIMIT 1;`); err != nil {
		_ = conn.Close(ctx)
		t.Fatal(err)
	}
	for _, target := range widenedColumns {
		if _, err := conn.Exec(ctx, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE text USING %s::text", target[0], target[1], target[1])); err != nil {
			_ = conn.Close(ctx)
			t.Fatalf("widen %s.%s: %v", target[0], target[1], err)
		}
	}
	if err := conn.Close(ctx); err != nil {
		t.Fatal(err)
	}

	resolution, err := module.Resolve(standard.Modules(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := module.MigrateSchema(ctx, database, resolution.Factories); err != nil {
		t.Fatal(err)
	}

	for _, target := range widenedColumns {
		var current string
		if err := database.Raw(`SELECT format_type(a.atttypid, a.atttypmod) FROM pg_attribute a
			WHERE a.attrelid = to_regclass(?) AND a.attname = ?`, target[0], target[1]).Scan(&current).Error; err != nil {
			t.Fatal(err)
		}
		if current == "text" {
			t.Errorf("%s.%s was left widened to text", target[0], target[1])
		}
	}
	for table, want := range map[string]int64{
		"account_statuses": 1, "presence_catalog_items": 1, "presence_activities": 1,
	} {
		var count int64
		if err := database.Raw("SELECT count(*) FROM " + table).Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Errorf("%s rows = %d, want %d", table, count, want)
		}
	}
}
