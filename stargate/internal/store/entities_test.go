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
		schema := "stargate_entities_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		admin, adminErr := db.Connect(ctx, baseDSN)
		if adminErr != nil {
			t.Skipf("postgres unavailable: %v", adminErr)
		}
		if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
			_ = db.Close(admin)
			t.Fatal(err)
		}
		_ = db.Close(database)
		schemaDSN := baseDSN + " search_path=" + schema
		if strings.HasPrefix(baseDSN, "postgres://") || strings.HasPrefix(baseDSN, "postgresql://") {
			separator := "?"
			if strings.Contains(baseDSN, "?") {
				separator = "&"
			}
			schemaDSN = baseDSN + separator + "options=-csearch_path%3D" + schema
		}
		database, err = db.Connect(ctx, schemaDSN)
		if err != nil {
			_ = admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE")
			_ = db.Close(admin)
			t.Fatal(err)
		}
		if err := migrate.Run(ctx, database); err != nil {
			_ = db.Close(database)
			_ = admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE")
			_ = db.Close(admin)
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = db.Close(database)
			_ = admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
			_ = db.Close(admin)
		})
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
