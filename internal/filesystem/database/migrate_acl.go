package database

import (
	"context"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MigrateACLAndTree applies idempotent schema indexes and one-time data
// backfills for the materialized-path folder tree, derivative separation, and
// FileACLEntry table. Safe to run repeatedly on startup in both PostgreSQL and
// SQLite.
func MigrateACLAndTree(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return nil
	}
	tx := db.WithContext(ctx)

	// 1. PostgreSQL pattern-matching B-tree index for prefix scans on path.
	if tx.Dialector.Name() == "postgres" {
		if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_cloud_files_path_pattern ON cloud_files (path text_pattern_ops) WHERE deleted_at IS NULL`).Error; err != nil {
			return err
		}
	}

	// 2. Separate system derivatives out of parent_id into derived_from_id.
	if err := tx.Exec(`
		UPDATE cloud_files
		SET derived_from_id = parent_id,
		    parent_id = NULL,
		    path = '/',
		    depth = 0
		WHERE derived_from_id IS NULL
		  AND parent_id IS NOT NULL
		  AND (application_type = 'system.thumbnail' OR application_type LIKE 'system.compression%')
	`).Error; err != nil {
		return err
	}

	// 3. Ensure root rows have normalized path = '/' and depth = 0.
	if err := tx.Exec(`
		UPDATE cloud_files
		SET path = '/', depth = 0
		WHERE parent_id IS NULL AND (path = '' OR path IS NULL OR depth <> 0)
	`).Error; err != nil {
		return err
	}

	// 4. Backfill materialized path and depth for nested rows still at default '/'.
	if err := backfillMaterializedPaths(tx); err != nil {
		return err
	}

	// 5. Migrate legacy file_permissions rows into file_acl_entries (idempotent).
	if tx.Migrator().HasTable(&FilePermission{}) && tx.Migrator().HasTable(&FileACLEntry{}) {
		if err := migrateLegacyPermissions(tx); err != nil {
			return err
		}
	}

	return nil
}

func backfillMaterializedPaths(tx *gorm.DB) error {
	// Cheap validity probe before loading every row: a nested node's path must
	// end with its own id and its depth must be its parent's depth plus one.
	var pending int64
	if err := tx.Unscoped().Model(&CloudFile{}).Where(`
		(parent_id IS NULL AND (path IS NULL OR path <> '/' OR depth <> 0))
		OR (parent_id IS NOT NULL AND (
			path IS NULL OR path = '' OR path NOT LIKE '%/' || id || '/'
			OR depth <> COALESCE((SELECT p.depth + 1 FROM cloud_files p WHERE p.id = cloud_files.parent_id), depth)
		))`).Count(&pending).Error; err != nil {
		return err
	}
	if pending == 0 {
		return nil
	}

	type nodeRow struct {
		ID       string
		ParentID *string
		Path     string
		Depth    int
	}
	var all []nodeRow
	if err := tx.Unscoped().Model(&CloudFile{}).
		Select("id, parent_id, path, depth").
		Find(&all).Error; err != nil {
		return err
	}

	byID := make(map[string]*nodeRow, len(all))
	for i := range all {
		byID[all[i].ID] = &all[i]
	}

	resolved := make(map[string]struct {
		path  string
		depth int
	}, len(all))
	var resolve func(id string, visiting map[string]bool) (string, int)
	resolve = func(id string, visiting map[string]bool) (string, int) {
		if r, ok := resolved[id]; ok {
			return r.path, r.depth
		}
		node, ok := byID[id]
		if !ok || node.ParentID == nil || strings.TrimSpace(*node.ParentID) == "" {
			resolved[id] = struct {
				path  string
				depth int
			}{"/", 0}
			return "/", 0
		}
		parentID := strings.TrimSpace(*node.ParentID)
		if visiting[id] || parentID == id {
			resolved[id] = struct {
				path  string
				depth int
			}{"/", 0}
			return "/", 0
		}
		visiting[id] = true
		parentPath, parentDepth := resolve(parentID, visiting)
		delete(visiting, id)
		path := parentPath + parentID + "/"
		depth := parentDepth + 1
		resolved[id] = struct {
			path  string
			depth int
		}{path, depth}
		return path, depth
	}

	return tx.Transaction(func(inner *gorm.DB) error {
		for i := range all {
			n := &all[i]
			if n.ParentID == nil || strings.TrimSpace(*n.ParentID) == "" {
				continue
			}
			path, depth := resolve(n.ID, map[string]bool{})
			if n.Path == path && n.Depth == depth {
				continue
			}
			if err := inner.Unscoped().Model(&CloudFile{}).
				Where("id = ?", n.ID).
				Updates(map[string]any{"path": path, "depth": depth}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func migrateLegacyPermissions(tx *gorm.DB) error {
	var legacy []FilePermission
	if err := tx.Where("deleted_at IS NULL").Find(&legacy).Error; err != nil {
		return err
	}
	if len(legacy) == 0 {
		return nil
	}

	byFile := make(map[string][]FilePermission)
	for _, p := range legacy {
		if strings.TrimSpace(p.FileID) == "" {
			continue
		}
		byFile[p.FileID] = append(byFile[p.FileID], p)
	}

	return tx.Transaction(func(inner *gorm.DB) error {
		for fileID, perms := range byFile {
			var existing int64
			if err := inner.Model(&FileACLEntry{}).Where("file_id = ?", fileID).Count(&existing).Error; err != nil {
				return err
			}
			if existing > 0 {
				continue
			}

			type key struct {
				subjectType string
				subjectID   string
				effect      string
			}
			merged := make(map[key]uint32)
			hasPrivate := false
			hasPublic := false

			for _, p := range perms {
				st := strings.ToLower(strings.TrimSpace(p.SubjectType))
				sid := strings.TrimSpace(p.SubjectID)
				var mask uint32
				switch strings.ToLower(strings.TrimSpace(p.Permission)) {
				case "read":
					mask = 1
				case "write":
					mask = 1 | 2
				case "delete":
					mask = 1 | 2 | 4
				case "share":
					mask = 1 | 8
				case "manage":
					mask = 1 | 2 | 4 | 8 | 16
				default:
					mask = 1
				}
				switch st {
				case "public":
					hasPublic = true
					merged[key{"public", "", "allow"}] |= mask
				case "private":
					hasPrivate = true
				case "account", "scope":
					if sid != "" {
						merged[key{st, sid, "allow"}] |= mask
					}
				}
			}

			// A legacy "private" marker without "public" meant explicitly private
			// (even inside a shared pool), so record a public deny + break inheritance.
			if hasPrivate && !hasPublic {
				merged[key{"public", "", "deny"}] |= 1
			}

			entries := make([]FileACLEntry, 0, len(merged))
			for k, mask := range merged {
				entries = append(entries, FileACLEntry{
					ID:          NewID(),
					FileID:      fileID,
					SubjectType: k.subjectType,
					SubjectID:   k.subjectID,
					Effect:      k.effect,
					Permissions: mask,
					Inheritable: true,
				})
			}
			if len(entries) > 0 {
				if err := inner.Clauses(clause.OnConflict{DoNothing: true}).Create(&entries).Error; err != nil {
					return err
				}
			}
			updates := map[string]any{"has_acl": len(entries) > 0}
			if hasPrivate && !hasPublic {
				updates["inherit_acl"] = false
			}
			if err := inner.Model(&CloudFile{}).Where("id = ?", fileID).Updates(updates).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
