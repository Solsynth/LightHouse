package migrate_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/metoer/internal/db"
	"src.solsynth.dev/sosys/metoer/internal/migrate"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("METOER_TEST_DSN")
	if dsn == "" {
		t.Skip("METOER_TEST_DSN is not configured")
	}
	return dsn
}

func schemaDSN(dsn, schema string) string {
	if strings.Contains(dsn, "?") {
		return dsn + "&search_path=" + schema
	}
	return dsn + " search_path=" + schema
}

func withSchema(t *testing.T, dsn string, fn func(*gorm.DB, string)) {
	t.Helper()
	ctx := context.Background()
	base, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer db.Close(base)
	schema := "metoer_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Skipf("cannot create schema: %v", err)
	}
	defer base.Exec("DROP SCHEMA " + schema + " CASCADE")
	database, err := db.Connect(ctx, schemaDSN(dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(database)
	fn(database, schema)
}

func TestRunCreatesSchemaAndLedger(t *testing.T) {
	withSchema(t, testDSN(t), func(database *gorm.DB, _ string) {
		if err := migrate.Run(context.Background(), database); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"notifications", "push_subscriptions", "notification_preferences", "email_sending_plans", "email_sending_plan_recipients", "email_sending_plan_advances", "email_delivery_records", "notification_delivery_records", "notification_send_records", "schema_migrations"} {
			if !database.Migrator().HasTable(table) {
				t.Errorf("missing table %s", table)
			}
		}
	})
}

func TestRunRejectsUnledgeredNonemptySchema(t *testing.T) {
	withSchema(t, testDSN(t), func(database *gorm.DB, schema string) {
		if err := database.Exec("CREATE TABLE " + schema + ".sentinel (id integer PRIMARY KEY)").Error; err != nil {
			t.Fatal(err)
		}
		if err := migrate.Run(context.Background(), database); !errors.Is(err, migrate.ErrUnsafeDatabase) {
			t.Fatalf("expected ErrUnsafeDatabase, got %v", err)
		}
		if database.Migrator().HasTable("schema_migrations") {
			t.Fatal("safety gate created ledger")
		}
		var count int64
		if err := database.Table("sentinel").Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("unexpected sentinel rows: %d", count)
		}
	})
}
