package store

import "context"

// fileReferenceColumns mirrors the EF metadata registry. Metoer's schema has
// no registered JSON file-reference properties.
var fileReferenceColumns = map[string][]string{}

func (s *Store) ApplyFileReferenceUpdates(ctx context.Context, fileID string) (int, error) {
	return 0, nil
}
