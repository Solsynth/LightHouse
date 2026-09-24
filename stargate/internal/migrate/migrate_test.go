package migrate

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/internal/db"
	"src.solsynth.dev/sosys/stargate/internal/dbtest"
)

var migrationApplicationTables = []string{
	"accounts", "permission_groups", "auth_clients", "auth_sessions", "api_keys", "account_auth_factors", "account_contacts", "account_connections", "account_passkeys", "punishments", "authorized_apps", "action_logs", "auth_challenges", "e2ee_devices", "e2ee_key_bundles", "e2ee_one_time_pre_keys", "e2ee_sessions", "e2ee_envelopes", "mls_key_packages", "mls_group_states", "mls_device_memberships", "permission_group_members", "permission_nodes", "account_profiles", "account_relationships", "magic_spells", "affiliation_spells", "affiliation_results",
}

func TestMigrationSafetyGate(t *testing.T) {
	baseDSN := os.Getenv("STARGATE_TEST_DSN")
	if baseDSN == "" {
		t.Skip("STARGATE_TEST_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Each subtest runs migrations in its own fresh dedicated database: the
	// migrations begin with unqualified DROP TABLE IF EXISTS (which fall
	// through the search_path), and CREATE EXTENSION IF NOT EXISTS pg_trgm
	// installs into the first schema on the path. A dedicated empty database
	// keeps both harmless and isolates subtests from each other.
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

	t.Run("empty database receives complete ledger and tables", func(t *testing.T) {
		database := newDatabase(t)
		if err := Run(ctx, database); err != nil {
			t.Fatalf("Run: %v", err)
		}
		for _, table := range append(migrationApplicationTables, "schema_migrations") {
			if !database.Migrator().HasTable(table) {
				t.Errorf("missing migrated table %q", table)
			}
		}
	})

	t.Run("unledgered nonempty database is rejected without changes", func(t *testing.T) {
		database := newDatabase(t)
		if err := database.Exec("CREATE TABLE sentinel (id integer PRIMARY KEY)").Error; err != nil {
			t.Fatal(err)
		}
		err := Run(ctx, database)
		if !errors.Is(err, ErrUnsafeDatabase) {
			t.Fatalf("Run error = %v, want ErrUnsafeDatabase", err)
		}
		var count int64
		if err := database.Raw(`SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'`).Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("table count = %d, want sentinel only", count)
		}
		if database.Migrator().HasTable("schema_migrations") {
			t.Fatal("safety gate created schema_migrations")
		}
	})
}
