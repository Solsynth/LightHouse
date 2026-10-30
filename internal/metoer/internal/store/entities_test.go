package store_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/metoer/internal/db"
	"src.solsynth.dev/sosys/metoer/internal/migrate"
	"src.solsynth.dev/sosys/metoer/internal/store"
)

func contractDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("METOER_TEST_DSN")
	if dsn == "" {
		t.Skip("METOER_TEST_DSN is not configured")
	}
	ctx := context.Background()
	base, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	schema := "metoer_entity_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		db.Close(base)
		t.Skipf("cannot create schema: %v", err)
	}
	db.Close(base)
	database, err := db.Connect(ctx, dsn+" search_path="+schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close(database)
		cleanup, err := db.Connect(ctx, dsn)
		if err == nil {
			cleanup.Exec("DROP SCHEMA " + schema + " CASCADE")
			db.Close(cleanup)
		}
	})
	if err := migrate.Run(ctx, database); err != nil {
		t.Fatal(err)
	}
	return database
}

func TestEntityContract(t *testing.T) {
	database := contractDatabase(t)
	entity := &store.NotificationEntity{
		ID:         uuid.New(),
		EntityBase: store.EntityBase{CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()},
		AccountID:  uuid.New(),
		Topic:      "contract",
		Meta:       datatypes.JSON(`{"ok":true,"items":[1]}`),
		Priority:   5,
	}
	if err := database.Create(entity).Error; err != nil {
		t.Fatal(err)
	}
	var loaded store.NotificationEntity
	if err := database.First(&loaded, "id = ?", entity.ID).Error; err != nil {
		t.Fatal(err)
	}
	var expected, actual map[string]any
	if err := json.Unmarshal(entity.Meta, &expected); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(loaded.Meta, &actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("json round-trip mismatch: %s", loaded.Meta)
	}
	if err := database.Delete(&loaded).Error; err != nil {
		t.Fatal(err)
	}
	if database.First(&store.NotificationEntity{}, "id = ?", entity.ID).Error == nil {
		t.Fatal("soft-deleted row remained in default scope")
	}
	if err := database.Unscoped().Delete(&loaded).Error; err != nil {
		t.Fatal(err)
	}
}
