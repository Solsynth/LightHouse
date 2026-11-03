package migrate

import (
	"context"
	"os"
	"testing"
	"time"

	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/stargate/db"
	"src.solsynth.dev/sosys/stargate/dbtest"
	"src.solsynth.dev/sosys/stargate/modules/standard"
)

func TestModuleSchemaOwnership(t *testing.T) {
	baseDSN := os.Getenv("STARGATE_TEST_DSN")
	if baseDSN == "" {
		t.Skip("STARGATE_TEST_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	newDatabase := func(t *testing.T) *gorm.DB {
		t.Helper()
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
		return database
	}

	moduleTable := "account_relationships"
	realmTables := []string{
		"realms", "realm_members", "realm_labels", "realm_boost_contributions",
		"realm_experience_records", "realm_role_permissions",
		"realm_user_permissions", "realm_post_moderation_logs",
	}

	t.Run("relationships module disabled skips table", func(t *testing.T) {
		database := newDatabase(t)
		if err := Run(ctx, database); err != nil {
			t.Fatal(err)
		}
		factories := standard.Modules()
		resolution, err := module.Resolve(factories, []string{"app.solian.id.auth", "app.solian.id.accounts", "app.solian.id.profiles"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if resolution.Enabled["app.solian.id.relationships"] {
			t.Fatal("relationships should be disabled by the allowlist")
		}
		if err := module.MigrateSchema(ctx, database, resolution.Factories); err != nil {
			t.Fatal(err)
		}
		if database.Migrator().HasTable(moduleTable) {
			t.Fatalf("table %s should not exist when relationships module is disabled", moduleTable)
		}
	})

	t.Run("relationships module enabled creates table", func(t *testing.T) {
		database := newDatabase(t)
		if err := Run(ctx, database); err != nil {
			t.Fatal(err)
		}
		factories := standard.Modules()
		resolution, err := module.Resolve(factories, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !resolution.Enabled["app.solian.id.relationships"] {
			t.Fatal("relationships should be enabled by default")
		}
		if err := module.MigrateSchema(ctx, database, resolution.Factories); err != nil {
			t.Fatal(err)
		}
		if !database.Migrator().HasTable(moduleTable) {
			t.Fatalf("table %s should exist when relationships module is enabled", moduleTable)
		}
	})

	t.Run("legacy database preserves module table across disable", func(t *testing.T) {
		database := newDatabase(t)
		if err := Run(ctx, database); err != nil {
			t.Fatal(err)
		}
		factories := standard.Modules()
		enabled, err := module.Resolve(factories, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := module.MigrateSchema(ctx, database, enabled.Factories); err != nil {
			t.Fatal(err)
		}
		if !database.Migrator().HasTable(moduleTable) {
			t.Fatalf("table %s should exist before disable", moduleTable)
		}
		disabled, err := module.Resolve(factories, nil, []string{"app.solian.id.relationships"})
		if err != nil {
			t.Fatal(err)
		}
		if err := module.MigrateSchema(ctx, database, disabled.Factories); err != nil {
			t.Fatal(err)
		}
		if !database.Migrator().HasTable(moduleTable) {
			t.Fatalf("table %s must survive a disable cycle", moduleTable)
		}
	})

	t.Run("realm module disabled skips every realm table", func(t *testing.T) {
		database := newDatabase(t)
		if err := Run(ctx, database); err != nil {
			t.Fatal(err)
		}
		resolution, err := module.Resolve(standard.Modules(),
			[]string{"app.solian.id.auth", "app.solian.id.accounts", "app.solian.id.profiles"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if resolution.Enabled["app.solian.id.realm"] {
			t.Fatal("realm should be disabled by the allowlist")
		}
		if err := module.MigrateSchema(ctx, database, resolution.Factories); err != nil {
			t.Fatal(err)
		}
		for _, table := range realmTables {
			if database.Migrator().HasTable(table) {
				t.Fatalf("table %s should not exist when realm is disabled", table)
			}
		}
	})

	t.Run("realm module enabled creates tables and the legacy indexes", func(t *testing.T) {
		database := newDatabase(t)
		if err := Run(ctx, database); err != nil {
			t.Fatal(err)
		}
		resolution, err := module.Resolve(standard.Modules(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !resolution.Enabled["app.solian.id.realm"] {
			t.Fatal("realm should be enabled by default")
		}
		if err := module.MigrateSchema(ctx, database, resolution.Factories); err != nil {
			t.Fatal(err)
		}
		for _, table := range realmTables {
			if !database.Migrator().HasTable(table) {
				t.Fatalf("table %s should exist when realm is enabled", table)
			}
		}
		// The slug uniqueness includes deleted_at, so a soft-deleted realm
		// frees its slug; the role and user permission rows are unique per
		// realm. Losing either index changes what the endpoints can promise.
		for _, index := range []string{
			"ix_realms_slug_deleted_at",
			"ix_realm_role_permissions_realm_id_role_level",
			"ix_realm_user_permissions_realm_id_account_id",
		} {
			var count int64
			if err := database.WithContext(ctx).Raw(
				`SELECT count(*) FROM pg_indexes WHERE indexname = ?`, index).Scan(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("index %s count = %d; want 1", index, count)
			}
		}
	})

	t.Run("realm tables are additive on an existing database", func(t *testing.T) {
		database := newDatabase(t)
		if err := Run(ctx, database); err != nil {
			t.Fatal(err)
		}
		// First boot without the module: the tables do not exist.
		without, err := module.Resolve(standard.Modules(),
			[]string{"app.solian.id.auth", "app.solian.id.accounts"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := module.MigrateSchema(ctx, database, without.Factories); err != nil {
			t.Fatal(err)
		}
		if database.Migrator().HasTable("realms") {
			t.Fatal("realms should not exist before the module is enabled")
		}
		// Enabling it later reconciles the schema without touching the rest.
		with, err := module.Resolve(standard.Modules(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := module.MigrateSchema(ctx, database, with.Factories); err != nil {
			t.Fatal(err)
		}
		for _, table := range realmTables {
			if !database.Migrator().HasTable(table) {
				t.Fatalf("table %s should exist after enabling realm", table)
			}
		}
	})
	// AutoMigrate must not rewrite the column types the core DDL declared.
	// The module entities map tables the core migrations (or the legacy EF
	// schema) own; a tag without an explicit type made GORM widen uuid to
	// text and integer to bigint, which broke every COPY from the legacy
	// database into those tables.
	t.Run("module schema keeps the core column types", func(t *testing.T) {
		database := newDatabase(t)
		if err := Run(ctx, database); err != nil {
			t.Fatal(err)
		}
		resolution, err := module.Resolve(standard.Modules(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		coreTypes := columnTypes(t, database)
		if err := module.MigrateSchema(ctx, database, resolution.Factories); err != nil {
			t.Fatal(err)
		}
		for column, want := range columnTypes(t, database) {
			got, existed := coreTypes[column]
			if !existed || got == want {
				continue
			}
			t.Errorf("%s: module schema changed the type from %s to %s", column, got, want)
		}
	})
}

// columnTypes maps "<table>.<column>" to its SQL data type.
func columnTypes(t *testing.T, database *gorm.DB) map[string]string {
	t.Helper()
	var rows []struct {
		Table  string
		Column string
		Type   string
	}
	if err := database.Raw(`SELECT table_name AS "table", column_name AS "column", data_type AS "type"
		FROM information_schema.columns WHERE table_schema = 'public'`).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.Table+"."+row.Column] = row.Type
	}
	return out
}
