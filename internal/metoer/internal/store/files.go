package store

import (
	"context"
	"fmt"
)

// fileReferenceColumns lists the jsonb columns typed as
// SnCloudFileReferenceObject / collections of it per table. This mirrors
// FileMetadataReferenceUpdater's EF metadata enumeration
// (IsReferenceProperty). Ring's schema has none — notifications.meta is a
// Dictionary<string, object?> and is excluded — so the list is empty; keep
// it in sync with schema evolution the same way the C# updater stays in
// sync with entity properties.
var fileReferenceColumns = map[string][]string{}

// ApplyFileReferenceUpdates mirrors
// FileMetadataReferenceUpdater<TDbContext>.ApplyAsync: it rewrites the file
// reference object's fields (id, created_at, updated_at, mime, size, etc.)
// in every registered jsonb column. For Ring's schema the registry is empty,
// so it always updates 0 rows.
func (s *Store) ApplyFileReferenceUpdates(ctx context.Context, fileID string) (int, error) {
	total := 0
	for table, columns := range fileReferenceColumns {
		for _, column := range columns {
			tag, err := s.pool.Exec(ctx, fmt.Sprintf(
				`UPDATE %s SET %s = %s || jsonb_build_object('updated_at', now()) WHERE %s @> $1`,
				table, column, column, column), `{"id":"`+fileID+`"}`)
			if err != nil {
				return total, err
			}
			total += int(tag.RowsAffected())
		}
	}
	return total, nil
}
