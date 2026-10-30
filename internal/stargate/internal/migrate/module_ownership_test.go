package migrate

import (
	"context"
	"os"
	"testing"
	"time"

	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/internal/db"
	"src.solsynth.dev/sosys/stargate/internal/dbtest"
	"src.solsynth.dev/sosys/stargate/internal/module"
	"src.solsynth.dev/sosys/stargate/internal/modules/standard"
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
	oauthTables := []string{"oauth_apps", "oauth_app_secrets"}

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

	t.Run("oauthapps module disabled skips tables", func(t *testing.T) {
		database := newDatabase(t)
		if err := Run(ctx, database); err != nil {
			t.Fatal(err)
		}
		resolution, err := module.Resolve(standard.Modules(), []string{"app.solian.id.auth"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if resolution.Enabled["app.solian.id.oauthapps"] {
			t.Fatal("oauthapps should be disabled by the allowlist")
		}
		if err := module.MigrateSchema(ctx, database, resolution.Factories); err != nil {
			t.Fatal(err)
		}
		for _, table := range oauthTables {
			if database.Migrator().HasTable(table) {
				t.Fatalf("table %s should not exist when oauthapps is disabled", table)
			}
		}
	})

	t.Run("oauthapps module enabled creates tables and constraints", func(t *testing.T) {
		database := newDatabase(t)
		if err := Run(ctx, database); err != nil {
			t.Fatal(err)
		}
		resolution, err := module.Resolve(standard.Modules(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !resolution.Enabled["app.solian.id.oauthapps"] {
			t.Fatal("oauthapps should be enabled by default")
		}
		if err := module.MigrateSchema(ctx, database, resolution.Factories); err != nil {
			t.Fatal(err)
		}
		for _, table := range oauthTables {
			if !database.Migrator().HasTable(table) {
				t.Fatalf("table %s should exist when oauthapps is enabled", table)
			}
		}
		// The owner constraint is what keeps a registration meaningful.
		var count int64
		if err := database.WithContext(ctx).Raw(
			`SELECT count(*) FROM pg_constraint WHERE conname = 'ck_oauth_apps_owner'`).Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("ck_oauth_apps_owner constraints = %d; want 1", count)
		}
	})
}
