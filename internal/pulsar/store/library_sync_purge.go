package store

import (
	"context"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

// PurgeLibrarySyncTombstones deletes tombstones older than retention from the four entity
// tables, logging one line per table that had rows. Cursors and the two
// per-account documents are exempt: the documents have no tombstones (a cleared
// document is an empty row with a newer clock), and the cursor must stay
// monotone forever so a live pull never rewinds.
func PurgeLibrarySyncTombstones(ctx context.Context, db *gorm.DB, retention time.Duration) error {
	if retention <= 0 {
		retention = 90 * 24 * time.Hour
	}
	cutoff := time.Now().UTC().Add(-retention)
	for _, entity := range []struct {
		model any
		table string
	}{
		{&model.LibrarySyncTrack{}, "pulsar_library_tracks"},
		{&model.LibrarySyncLink{}, "pulsar_library_links"},
		{&model.LibrarySyncLyrics{}, "pulsar_library_lyrics"},
		{&model.LibrarySyncPlay{}, "pulsar_library_plays"},
	} {
		result := db.WithContext(ctx).
			Where("deleted_at IS NOT NULL AND deleted_at < ?", cutoff).
			Delete(entity.model)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			slog.Info("pulsar library sync: purged tombstones",
				"table", entity.table, "rows", result.RowsAffected, "cutoff", cutoff.Format(time.RFC3339))
		}
	}
	return nil
}
