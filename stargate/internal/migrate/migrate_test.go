package migrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/internal/db"
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
	admin, err := db.Connect(ctx, baseDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer db.Close(admin)

	newSchema := func(t *testing.T, sentinel bool) (string, *gorm.DB) {
		t.Helper()
		schema := "stargate_migrate_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error })
		if sentinel {
			if err := admin.Exec("CREATE TABLE " + schema + ".sentinel (id integer PRIMARY KEY)").Error; err != nil {
				t.Fatal(err)
			}
		}
		schemaDB, err := db.Connect(ctx, withSearchPath(baseDSN, schema))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close(schemaDB) })
		return schema, schemaDB
	}

	t.Run("empty schema receives complete ledger and tables", func(t *testing.T) {
		_, database := newSchema(t, false)
		if err := Run(ctx, database); err != nil {
			t.Fatalf("Run: %v", err)
		}
		for _, table := range append(migrationApplicationTables, "schema_migrations") {
			if !database.Migrator().HasTable(table) {
				t.Errorf("missing migrated table %q", table)
			}
		}
	})

	t.Run("unledgered nonempty schema is rejected without changes", func(t *testing.T) {
		schema, database := newSchema(t, true)
		err := Run(ctx, database)
		if !errors.Is(err, ErrUnsafeDatabase) {
			t.Fatalf("Run error = %v, want ErrUnsafeDatabase", err)
		}
		var count int64
		if err := admin.Raw(fmt.Sprintf("SELECT count(*) FROM information_schema.tables WHERE table_schema = '%s'", schema)).Scan(&count).Error; err != nil {
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

func withSearchPath(dsn, schema string) string {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		separator := "?"
		if strings.Contains(dsn, "?") {
			separator = "&"
		}
		return dsn + separator + "options=-csearch_path%3D" + schema
	}
	return dsn + " search_path=" + schema
}
