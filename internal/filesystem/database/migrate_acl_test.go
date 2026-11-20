package database

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openMigrateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open() error = %v", err)
	}
	if err := db.AutoMigrate(&CloudFile{}, &FilePermission{}, &FileACLEntry{}); err != nil {
		t.Fatalf("AutoMigrate() error = %v", err)
	}
	return db
}

func TestMigrateACLAndTreeBackfillsLegacyTree(t *testing.T) {
	db := openMigrateTestDB(t)

	root := CloudFile{ID: NewID(), Name: "root", Indexed: true, IsFolder: true}
	child := CloudFile{ID: NewID(), Name: "child", Indexed: true, IsFolder: true}
	grandchild := CloudFile{ID: NewID(), Name: "file", Indexed: true}
	thumbKind := "system.thumbnail"
	derived := CloudFile{ID: NewID(), Name: "file", Indexed: false, ApplicationType: &thumbKind}
	for _, file := range []*CloudFile{&root, &child, &grandchild, &derived} {
		if err := db.Create(file).Error; err != nil {
			t.Fatalf("create %s: %v", file.Name, err)
		}
	}
	// Simulate the legacy adjacency tree: parent_id links written after insert,
	// with every path still at the default '/'.
	links := []struct{ id, parent string }{
		{child.ID, root.ID},
		{grandchild.ID, child.ID},
		{derived.ID, child.ID},
	}
	for _, link := range links {
		if err := db.Model(&CloudFile{}).Where("id = ?", link.id).Update("parent_id", link.parent).Error; err != nil {
			t.Fatalf("link %s: %v", link.id, err)
		}
	}

	perms := []FilePermission{
		{ID: NewID(), FileID: root.ID, SubjectType: "private", Permission: "read"},
		{ID: NewID(), FileID: child.ID, SubjectType: "public", Permission: "read"},
		{ID: NewID(), FileID: grandchild.ID, SubjectType: "account", SubjectID: "00000000-0000-0000-0000-000000000009", Permission: "write"},
	}
	for i := range perms {
		if err := db.Create(&perms[i]).Error; err != nil {
			t.Fatalf("create legacy permission: %v", err)
		}
	}

	ctx := context.Background()
	if err := MigrateACLAndTree(ctx, db); err != nil {
		t.Fatalf("MigrateACLAndTree() error = %v", err)
	}

	reload := func(id string) CloudFile {
		t.Helper()
		var file CloudFile
		if err := db.First(&file, "id = ?", id).Error; err != nil {
			t.Fatalf("reload %s: %v", id, err)
		}
		return file
	}

	if got := reload(root.ID); got.Path != "/" || got.Depth != 0 {
		t.Fatalf("root path/depth = %q/%d, want ///0", got.Path, got.Depth)
	}
	if got := reload(child.ID); got.Path != "/"+root.ID+"/" || got.Depth != 1 {
		t.Fatalf("child path/depth = %q/%d, want %q/1", got.Path, got.Depth, "/"+root.ID+"/")
	}
	if got := reload(grandchild.ID); got.Path != "/"+root.ID+"/"+child.ID+"/" || got.Depth != 2 {
		t.Fatalf("grandchild path/depth = %q/%d, want nested path at depth 2", got.Path, got.Depth)
	}
	moved := reload(derived.ID)
	if moved.ParentID != nil || moved.DerivedFromID == nil || *moved.DerivedFromID != child.ID {
		t.Fatalf("derivative = %+v, want derived_from_id %s and no parent", moved, child.ID)
	}
	if moved.Path != "/" || moved.Depth != 0 {
		t.Fatalf("derivative path/depth = %q/%d, want root placement", moved.Path, moved.Depth)
	}

	entriesFor := func(fileID string) []FileACLEntry {
		t.Helper()
		var entries []FileACLEntry
		if err := db.Where("file_id = ?", fileID).Find(&entries).Error; err != nil {
			t.Fatalf("load acl entries: %v", err)
		}
		return entries
	}

	rootEntries := entriesFor(root.ID)
	if len(rootEntries) != 1 || rootEntries[0].SubjectType != "public" || rootEntries[0].Effect != "deny" {
		t.Fatalf("root acl entries = %+v, want a single public deny", rootEntries)
	}
	if reload(root.ID).InheritACL {
		t.Fatal("root inherit_acl = true, want false for a legacy private marker")
	}
	childEntries := entriesFor(child.ID)
	if len(childEntries) != 1 || childEntries[0].SubjectType != "public" || childEntries[0].Effect != "allow" {
		t.Fatalf("child acl entries = %+v, want a single public allow", childEntries)
	}
	if !reload(child.ID).HasACL || !reload(grandchild.ID).HasACL || !reload(root.ID).HasACL {
		t.Fatal("has_acl not set on migrated files")
	}
	grandchildEntries := entriesFor(grandchild.ID)
	if len(grandchildEntries) != 1 || grandchildEntries[0].Permissions&2 == 0 {
		t.Fatalf("grandchild acl entries = %+v, want an account allow carrying write", grandchildEntries)
	}

	// A second pass must not duplicate rows or change anything.
	if err := MigrateACLAndTree(ctx, db); err != nil {
		t.Fatalf("second MigrateACLAndTree() error = %v", err)
	}
	var total int64
	if err := db.Model(&FileACLEntry{}).Count(&total).Error; err != nil {
		t.Fatalf("count acl entries: %v", err)
	}
	if total != 3 {
		t.Fatalf("acl entries after second pass = %d, want 3", total)
	}
}
