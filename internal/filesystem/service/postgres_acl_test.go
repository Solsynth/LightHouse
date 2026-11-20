package service

import (
	"context"
	"os"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"src.solsynth.dev/sosys/filesystem/database"

	"github.com/google/uuid"
)

// TestPostgresACLAndTree validates the PostgreSQL-only pieces of the ACL and
// materialized-path design: the text_pattern_ops index, the subtree prefix
// rewrite, and the depth arithmetic. Point LIGHTHOUSE_TEST_POSTGRES_DSN at a
// disposable database; the test drops the file tables it uses first.
func TestPostgresACLAndTree(t *testing.T) {
	dsn := os.Getenv("LIGHTHOUSE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set LIGHTHOUSE_TEST_POSTGRES_DSN to run the PostgreSQL schema check")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open() error = %v", err)
	}
	for _, model := range []any{&database.FileACLEntry{}, &database.FilePermission{}, &database.CloudFile{}, &database.FileObject{}, &database.FilePool{}} {
		if err := db.Migrator().DropTable(model); err != nil {
			t.Fatalf("DropTable(%T) error = %v", model, err)
		}
	}
	if err := db.AutoMigrate(&database.CloudFile{}, &database.FileObject{}, &database.FilePool{}, &database.FilePermission{}, &database.FileACLEntry{}); err != nil {
		t.Fatalf("AutoMigrate() error = %v", err)
	}
	if err := database.MigrateACLAndTree(context.Background(), db); err != nil {
		t.Fatalf("MigrateACLAndTree() error = %v", err)
	}

	svc := NewFileService(&database.DB{DB: db}, nil)
	accountID := uuid.New()
	folder := func(name string, parentID *string) *database.CloudFile {
		t.Helper()
		created, err := svc.CreateWorkspaceFolder(accountID, nil, name, parentID)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return created
	}
	root := folder("root", nil)
	sub := folder("sub", &root.ID)
	leaf := folder("leaf", &sub.ID)
	dest := folder("dest", nil)

	if _, err := svc.MoveBatch([]string{root.ID}, &dest.ID, nil); err != nil {
		t.Fatalf("MoveBatch() error = %v", err)
	}

	expect := func(id, wantPath string, wantDepth int) {
		t.Helper()
		file, err := svc.GetFile(id)
		if err != nil {
			t.Fatalf("GetFile(%s) error = %v", id, err)
		}
		if file.Path != wantPath || file.Depth != wantDepth {
			t.Fatalf("path/depth = %q/%d, want %q/%d", file.Path, file.Depth, wantPath, wantDepth)
		}
	}
	expect(root.ID, "/"+dest.ID+"/", 1)
	expect(sub.ID, "/"+dest.ID+"/"+root.ID+"/", 2)
	expect(leaf.ID, "/"+dest.ID+"/"+root.ID+"/"+sub.ID+"/", 3)

	if _, err := svc.UpsertFileACLEntry(context.Background(), sub, ACLEntryInput{
		SubjectType: ACLSubjectAccount,
		SubjectID:   uuid.New().String(),
		Effect:      ACLEffectAllow,
		Permissions: uint32(PermRead),
	}, &accountID); err != nil {
		t.Fatalf("UpsertFileACLEntry() error = %v", err)
	}
	var count int64
	if err := db.Model(&database.FileACLEntry{}).Where("file_id = ?", sub.ID).Count(&count).Error; err != nil {
		t.Fatalf("count acl entries: %v", err)
	}
	if count != 1 {
		t.Fatalf("acl entries = %d, want 1", count)
	}
}
