package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/internal/db"
	"src.solsynth.dev/sosys/stargate/internal/dbtest"
	"src.solsynth.dev/sosys/stargate/internal/migrate"
)

func TestEntitySchemaAndRoundTrip(t *testing.T) {
	baseDSN := os.Getenv("STARGATE_TEST_DSN")
	if baseDSN == "" {
		baseDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	database, err := db.Connect(ctx, baseDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	if os.Getenv("STARGATE_TEST_DSN") != "" {
		// The migrations begin with unqualified `DROP TABLE IF EXISTS`
		// statements, which fall through the search_path: with public on the
		// path they drop the live database's tables before the CREATEs land
		// in the test schema. Run them in a fresh dedicated database so the
		// DROPs can only ever hit an empty public. public stays on the
		// search_path solely so pg_trgm's gin_trgm_ops resolves — the
		// extension is not installed in the fresh database, so
		// `CREATE EXTENSION IF NOT EXISTS pg_trgm` installs it into the test
		// schema, the first schema on the path.
		dedicatedDSN, cleanupDB, dbErr := dbtest.NewDatabase(ctx, baseDSN)
		if dbErr != nil {
			t.Skipf("cannot create dedicated database: %v", dbErr)
		}
		t.Cleanup(cleanupDB)
		// Close the schema connection before cleanupDB drops the database
		// (t.Cleanup runs LIFO).
		t.Cleanup(func() { _ = db.Close(database) })
		schema := "stargate_entities_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		admin, adminErr := db.Connect(ctx, dedicatedDSN)
		if adminErr != nil {
			t.Skipf("postgres unavailable: %v", adminErr)
		}
		if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
			_ = db.Close(admin)
			t.Fatal(err)
		}
		_ = db.Close(admin)
		_ = db.Close(database)
		schemaDSN := dedicatedDSN + " search_path='" + schema + ", public'"
		if strings.HasPrefix(dedicatedDSN, "postgres://") || strings.HasPrefix(dedicatedDSN, "postgresql://") {
			separator := "?"
			if strings.Contains(dedicatedDSN, "?") {
				separator = "&"
			}
			schemaDSN = dedicatedDSN + separator + "options=-csearch_path%3D" + schema + "%2C%20public"
		}
		database, err = db.Connect(ctx, schemaDSN)
		if err != nil {
			t.Fatal(err)
		}
		if err := migrate.Run(ctx, database); err != nil {
			_ = db.Close(database)
			t.Fatal(err)
		}
	} else {
		defer db.Close(database)
	}
	for _, table := range append(allEntityTables, "schema_migrations") {
		if !database.Migrator().HasTable(table) {
			t.Fatalf("missing entity table %q", table)
		}
	}

	rollback := errors.New("rollback fixture")
	err = database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		accountID := uuid.New()
		profileID := uuid.New()
		if err := tx.Create(&AccountEntity{ID: accountID, EntityBase: EntityBase{CreatedAt: now, UpdatedAt: now}, Language: "en", Name: "gorm-contract", Nick: "gorm-contract", Region: "US"}).Error; err != nil {
			return err
		}
		activeBadge := datatypes.JSON(`{"id":"badge","meta":{}}`)
		if err := tx.Create(&ProfileEntity{ID: profileID, EntityBase: EntityBase{CreatedAt: now, UpdatedAt: now}, AccountID: accountID, ActiveBadge: &activeBadge, Experience: 0, SocialCredits: 1}).Error; err != nil {
			return err
		}
		var loaded ProfileEntity
		if err := tx.Where("account_id = ?", accountID).First(&loaded).Error; err != nil {
			return err
		}
		var got, want map[string]any
		if loaded.ActiveBadge == nil || json.Unmarshal(*loaded.ActiveBadge, &got) != nil || json.Unmarshal(activeBadge, &want) != nil || !reflect.DeepEqual(got, want) {
			return errors.New("json round trip changed active badge")
		}
		if err := tx.Delete(&ProfileEntity{}, "id = ?", profileID).Error; err != nil {
			return err
		}
		var visible ProfileEntity
		if err := tx.Where("id = ?", profileID).First(&visible).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("soft-deleted profile remained visible")
		}
		if err := tx.Unscoped().Delete(&ProfileEntity{}, "id = ?", profileID).Error; err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("fixture transaction error = %v", err)
	}
}
