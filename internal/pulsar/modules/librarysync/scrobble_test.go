package librarysync

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

func playChange(fingerprint, startedAt string, listened int64, completed bool) Change {
	return Change{
		Kind:   ChangeUpsert,
		Entity: EntityPlay,
		Play: &PlayRecord{
			TrackFingerprint: fingerprint,
			SourceID:         "subsonic",
			StartedAt:        startedAt,
			Clock:            startedAt,
			ListenedMs:       listened,
			Completed:        completed,
		},
	}
}

// A play past the scrobble thresholds lands in the scrobble history with the
// track's metadata and a catalog link.
func TestPlayMaterializesScrobble(t *testing.T) {
	db := testDB(t)
	account := uuid.New()

	pushBatch(t, db, account, "dev-a", []Change{trackChange("coldplay|yellow", "Yellow", "2026-01-01T00:00:00Z")})
	pushBatch(t, db, account, "dev-a", []Change{
		playChange("coldplay|yellow", "2026-01-02T10:00:00Z", 200000, false),
	})

	scrobbles := []model.Scrobble{}
	db.Where("account_id = ?", account).Find(&scrobbles)
	if len(scrobbles) != 1 {
		t.Fatalf("scrobbles=%d, want 1", len(scrobbles))
	}
	row := scrobbles[0]
	if row.Title != "Yellow" || row.ClientName != groovyBoxClientName || row.Domain != model.DomainMusic {
		t.Fatalf("scrobble=%+v", row)
	}
	if row.ListenedSeconds != 200 || row.DurationSeconds != 266 {
		t.Fatalf("seconds=%d/%d, want 200/266", row.ListenedSeconds, row.DurationSeconds)
	}
	if row.CatalogID == nil {
		t.Fatalf("scrobble has no catalog link")
	}
	var catalog model.CatalogItem
	if err := db.Where("id = ?", *row.CatalogID).Take(&catalog).Error; err != nil {
		t.Fatalf("catalog lookup: %v", err)
	}
	if catalog.Provider != "groovybox" || catalog.ProviderKey != "coldplay|yellow" {
		t.Fatalf("catalog=%+v", catalog)
	}
	started, _ := time.Parse(time.RFC3339, "2026-01-02T10:00:00Z")
	if !row.PlayedAt.Equal(started.UTC()) {
		t.Fatalf("played_at=%v, want %v", row.PlayedAt, started.UTC())
	}
}

// A re-pushed play folds into the same scrobble instead of duplicating it.
func TestPlayFoldUpdatesOneScrobble(t *testing.T) {
	db := testDB(t)
	account := uuid.New()

	pushBatch(t, db, account, "dev-a", []Change{
		playChange("coldplay|yellow", "2026-01-02T10:00:00Z", 200000, false),
	})
	pushBatch(t, db, account, "dev-a", []Change{
		playChange("coldplay|yellow", "2026-01-02T10:00:00Z", 250000, false),
	})

	scrobbles := []model.Scrobble{}
	db.Where("account_id = ?", account).Find(&scrobbles)
	if len(scrobbles) != 1 {
		t.Fatalf("scrobbles=%d, want 1", len(scrobbles))
	}
	if scrobbles[0].ListenedSeconds != 250 {
		t.Fatalf("listened=%d, want 250", scrobbles[0].ListenedSeconds)
	}
}

// A too-short play produces no scrobble; a later fold that crosses the
// threshold materializes one.
func TestPartialPlayStaysUnscrobbledUntilThreshold(t *testing.T) {
	db := testDB(t)
	account := uuid.New()

	pushBatch(t, db, account, "dev-a", []Change{trackChange("coldplay|yellow", "Yellow", "2026-01-01T00:00:00Z")})
	pushBatch(t, db, account, "dev-a", []Change{
		playChange("coldplay|yellow", "2026-01-02T10:00:00Z", 5000, false),
	})

	var count int64
	db.Model(&model.Scrobble{}).Where("account_id = ?", account).Count(&count)
	if count != 0 {
		t.Fatalf("scrobbles=%d, want 0 for a partial play", count)
	}

	pushBatch(t, db, account, "dev-a", []Change{
		playChange("coldplay|yellow", "2026-01-02T10:00:00Z", 200000, false),
	})
	db.Model(&model.Scrobble{}).Where("account_id = ?", account).Count(&count)
	if count != 1 {
		t.Fatalf("scrobbles=%d, want 1 after the fold crossed the threshold", count)
	}
}
