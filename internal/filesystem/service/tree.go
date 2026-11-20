package service

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"src.solsynth.dev/sosys/filesystem/database"
)

// NormalizePath canonicalizes a materialized ancestor path so it always starts
// and ends with "/" ("/" for root nodes, "/<rootID>/<subID>/" for nested nodes).
func NormalizePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || trimmed == "/" {
		return "/"
	}
	if !strings.HasPrefix(trimmed, "/") {
		trimmed = "/" + trimmed
	}
	if !strings.HasSuffix(trimmed, "/") {
		trimmed += "/"
	}
	return trimmed
}

// AncestorIDsFromPath extracts the ordered root-to-parent ancestor IDs from a
// materialized path in O(depth) with zero database queries.
func AncestorIDsFromPath(path string) []string {
	trimmed := strings.Trim(NormalizePath(path), "/")
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if id := strings.TrimSpace(part); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// SubtreePrefix returns the materialized path prefix shared by all descendants
// of node (e.g. "/rootID/nodeID/").
func SubtreePrefix(node *database.CloudFile) string {
	if node == nil || strings.TrimSpace(node.ID) == "" {
		return "/"
	}
	return NormalizePath(node.Path) + strings.TrimSpace(node.ID) + "/"
}

// IsDerivedFile reports whether file is a system derivative (thumbnail or
// compression) stored outside the folder tree.
func IsDerivedFile(file *database.CloudFile) bool {
	if file == nil {
		return false
	}
	if file.DerivedFromID != nil && strings.TrimSpace(*file.DerivedFromID) != "" {
		return true
	}
	if file.ApplicationType == nil {
		return false
	}
	kind := strings.TrimSpace(*file.ApplicationType)
	return kind == "system.thumbnail" || strings.HasPrefix(kind, "system.compression")
}

// resolveParentLocation validates parentID (if present) and computes the
// materialized path and depth for a child placed under it.
func (s *FileService) resolveParentLocation(tx *gorm.DB, parentID *string, workspaceID *string) (string, int, error) {
	if parentID == nil || strings.TrimSpace(*parentID) == "" {
		return "/", 0, nil
	}
	if tx == nil {
		tx = s.db.DB
	}
	var parent database.CloudFile
	if err := tx.Select("id", "path", "depth", "workspace_id", "is_folder", "derived_from_id").
		First(&parent, "id = ? AND deleted_at IS NULL", strings.TrimSpace(*parentID)).Error; err != nil {
		return "", 0, fmt.Errorf("load parent folder: %w", err)
	}
	if !parent.IsFolder {
		return "", 0, errors.New("parent must be a folder")
	}
	if parent.DerivedFromID != nil && strings.TrimSpace(*parent.DerivedFromID) != "" {
		return "", 0, errors.New("parent cannot be a derived file")
	}
	if firstNonEmptyString(parent.WorkspaceID) != firstNonEmptyString(workspaceID) {
		return "", 0, errors.New("parent and file must belong to the same workspace")
	}
	return SubtreePrefix(&parent), parent.Depth + 1, nil
}

// loadSubtreeIDs returns fileIDs plus every descendant under any folder in
// fileIDs and any derivatives attached to those files, using materialized-path
// prefix scans (zero recursive CTEs).
func (s *FileService) loadSubtreeIDs(fileIDs []string, includeDeleted bool) ([]string, error) {
	fileIDs = normalizeFileIDs(fileIDs)
	if len(fileIDs) == 0 {
		return []string{}, nil
	}

	baseQuery := s.db.DB
	if includeDeleted {
		baseQuery = baseQuery.Unscoped()
	}

	var roots []database.CloudFile
	if err := baseQuery.Select("id", "path", "is_folder").
		Where("id IN ?", fileIDs).
		Find(&roots).Error; err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(fileIDs))
	out := make([]string, 0, len(fileIDs))
	addID := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}

	for _, id := range fileIDs {
		addID(id)
	}

	// Collect descendants of any folder roots via materialized path prefix scan.
	folderPrefixes := make([]string, 0, len(roots))
	for i := range roots {
		if roots[i].IsFolder {
			folderPrefixes = append(folderPrefixes, SubtreePrefix(&roots[i])+"%")
		}
	}
	if len(folderPrefixes) > 0 {
		q := s.db.DB.Model(&database.CloudFile{})
		if includeDeleted {
			q = q.Unscoped()
		}
		var conds []string
		var args []any
		for _, prefix := range folderPrefixes {
			conds = append(conds, "path LIKE ?")
			args = append(args, prefix)
		}
		var descIDs []string
		if err := q.Where(strings.Join(conds, " OR "), args...).Pluck("id", &descIDs).Error; err != nil {
			return nil, err
		}
		for _, id := range descIDs {
			addID(id)
		}
	}

	// Include derivatives linked via derived_from_id (or legacy parent_id).
	if len(out) > 0 {
		q := s.db.DB.Model(&database.CloudFile{})
		if includeDeleted {
			q = q.Unscoped()
		}
		var derivIDs []string
		if err := q.Where("derived_from_id IN ?", out).Pluck("id", &derivIDs).Error; err != nil {
			return nil, err
		}
		for _, id := range derivIDs {
			addID(id)
		}
	}

	return out, nil
}

// ListChildrenPage returns one SQL-paginated page of direct children inside
// parentID within the selected workspace (nil means personal namespace).
func (s *FileService) ListChildrenPage(parentID string, workspaceID *string, opts FileListOptions) ([]database.CloudFile, int64, error) {
	query := s.db.Model(&database.CloudFile{}).
		Where("cloud_files.parent_id = ? AND cloud_files.derived_from_id IS NULL", strings.TrimSpace(parentID))
	if workspaceID == nil || strings.TrimSpace(*workspaceID) == "" {
		query = query.Where("cloud_files.workspace_id IS NULL")
	} else {
		query = query.Where("cloud_files.workspace_id = ?", strings.TrimSpace(*workspaceID))
	}
	return s.listFilesPage(query, opts)
}
