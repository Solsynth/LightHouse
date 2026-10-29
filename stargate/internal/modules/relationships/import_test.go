package relationships

import (
	"context"
	"os"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"src.solsynth.dev/sosys/stargate/internal/dbtest"
	"src.solsynth.dev/sosys/stargate/internal/module"
)

// TestRelationshipImportRoundTrip verifies the legacy Passport importer
// against real postgres instances. STARGATE_IMPORT_SOURCE_DSN must point at a database
// holding the legacy account_relationships table.
func TestRelationshipImportRoundTrip(t *testing.T) {
	sourceDSN := os.Getenv("STARGATE_IMPORT_SOURCE_DSN")
	if sourceDSN == "" {
		t.Skip("STARGATE_IMPORT_SOURCE_DSN not set")
	}
	ctx := context.Background()

	// Target is always a fresh dedicated database so the test is repeatable.
	baseDSN := mustEnv(t, "STARGATE_TEST_DSN")
	targetDSN, cleanup, err := dbtest.NewDatabase(ctx, baseDSN)
	if err != nil {
		t.Skipf("cannot create dedicated database: %v", err)
	}
	t.Cleanup(cleanup)

	source, err := connect(ctx, sourceDSN)
	if err != nil {
		t.Fatal(err)
	}
	target, err := connect(ctx, targetDSN)
	if err != nil {
		t.Fatal(err)
	}

	// Prepare the module's new schema (AutoMigrate + index hook) as
	// cmd/stargate-module-import does.
	factory := Factory()
	if err := module.MigrateSchema(ctx, target, []module.Factory{factory}); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}

	// Dry run first: nothing written, but the read count is validated.
	dry := &importer{}
	stats, err := dry.Import(ctx, source, target, module.ImportOptions{DryRun: true, BatchSize: 100})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if stats.Read != 6 {
		t.Fatalf("dry run read = %d, want 6", stats.Read)
	}
	if stats.Written != 0 {
		t.Fatalf("dry run wrote = %d, want 0", stats.Written)
	}
	var count int64
	if err := target.Table("account_relationships").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("dry run left %d rows (err %v)", count, err)
	}

	// Real import.
	stats, err = dry.Import(ctx, source, target, module.ImportOptions{BatchSize: 100})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if stats.Read != 6 || stats.Written != 6 {
		t.Fatalf("import read=%d written=%d, want 6/6", stats.Read, stats.Written)
	}
	if err := target.Table("account_relationships").Count(&count).Error; err != nil || count != 6 {
		t.Fatalf("target rows = %d, want 6", count)
	}

	// Idempotency: a second run must not duplicate or error.
	if _, err := dry.Import(ctx, source, target, module.ImportOptions{BatchSize: 100}); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if err := target.Table("account_relationships").Count(&count).Error; err != nil || count != 6 {
		t.Fatalf("rows after re-import = %d, want 6", count)
	}

	// Wire shape: soft-deleted rows keep deleted_at (history preserved), and
	// the degraded block keeps its expiry. degrade_to_status is optional in
	// Passport (it is written when a status changes on expiry), so this test
	// validates the structural mapping; the degrade-column mapping is
	// exercised separately by the promote path when the status changes.
	var softDeleted, timedMute int64
	if err := target.Table("account_relationships").
		Where("account_id = ? AND related_id = ? AND deleted_at IS NOT NULL",
			"11111111-1111-1111-1111-111111111111", "55555555-5555-5555-5555-555555555555").
		Count(&softDeleted).Error; err != nil || softDeleted != 1 {
		t.Fatalf("soft-deleted rows = %d, want 1", softDeleted)
	}
	// The degraded block carries only its expiry (status=-100, no
	// degrade_to_status in this fixture — see comment above).
	if err := target.Table("account_relationships").
		Where("status = ? AND expired_at IS NOT NULL", -100).
		Count(&timedMute).Error; err != nil || timedMute != 1 {
		t.Fatalf("degraded-block rows = %d, want 1", timedMute)
	}
}

func mustEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("%s not set", key)
	}
	return v
}

func connect(ctx context.Context, dsn string) (*gorm.DB, error) {
	return gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
}
