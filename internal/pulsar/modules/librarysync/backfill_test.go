package librarysync

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/config"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

func TestBackfillProjectsHistoricalPlays(t *testing.T) {
	db := testDB(t)
	account := uuid.New()

	pushBatch(t, db, account, "dev-a", []Change{trackChange("coldplay|yellow", "Yellow", "2026-01-01T00:00:00Z")})

	started, _ := time.Parse(time.RFC3339, "2026-01-01T10:00:00Z")
	plays := []model.LibrarySyncPlay{
		{
			AccountID:        account,
			TrackFingerprint: "coldplay|yellow",
			StartedAt:        started,
			SourceID:         "subsonic",
			ListenedMs:       266000,
			Completed:        true,
			Rev:              1,
			DeviceID:         "dev-a",
			LibrarySyncBase:  model.LibrarySyncBase{CreatedAt: started, UpdatedAt: started},
		},
		{
			AccountID:        account,
			TrackFingerprint: "coldplay|yellow",
			StartedAt:        started.Add(time.Hour),
			SourceID:         "subsonic",
			ListenedMs:       5000,
			Completed:        false,
			Rev:              2,
			DeviceID:         "dev-a",
			LibrarySyncBase:  model.LibrarySyncBase{CreatedAt: started.Add(time.Hour), UpdatedAt: started.Add(time.Hour)},
		},
	}
	for i := range plays {
		if err := db.Create(&plays[i]).Error; err != nil {
			t.Fatalf("insert play: %v", err)
		}
	}

	created, err := BackfillPlayScrobbles(context.Background(), db, config.Scrobbler{})
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if created != 1 {
		t.Fatalf("created=%d, want 1 (the completed play only)", created)
	}

	scrobbles := []model.Scrobble{}
	db.Where("account_id = ?", account).Find(&scrobbles)
	if len(scrobbles) != 1 {
		t.Fatalf("scrobbles=%d, want 1", len(scrobbles))
	}
	if scrobbles[0].Title != "Yellow" || scrobbles[0].ListenedSeconds != 266 || !scrobbles[0].PlayedAt.Equal(started) {
		t.Fatalf("scrobble=%+v", scrobbles[0])
	}

	// A second run is a no-op thanks to the per-account marker.
	created, err = BackfillPlayScrobbles(context.Background(), db, config.Scrobbler{})
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if created != 0 {
		t.Fatalf("created=%d on rerun, want 0", created)
	}
}

func TestBackfillDoesNotResurrectDeletedScrobbles(t *testing.T) {
	db := testDB(t)
	account := uuid.New()

	pushBatch(t, db, account, "dev-a", []Change{trackChange("coldplay|yellow", "Yellow", "2026-01-01T00:00:00Z")})
	started, _ := time.Parse(time.RFC3339, "2026-01-01T10:00:00Z")
	play := model.LibrarySyncPlay{
		AccountID:        account,
		TrackFingerprint: "coldplay|yellow",
		StartedAt:        started,
		SourceID:         "subsonic",
		ListenedMs:       266000,
		Completed:        true,
		Rev:              1,
		DeviceID:         "dev-a",
		LibrarySyncBase:  model.LibrarySyncBase{CreatedAt: started, UpdatedAt: started},
	}
	if err := db.Create(&play).Error; err != nil {
		t.Fatalf("insert play: %v", err)
	}

	if _, err := BackfillPlayScrobbles(context.Background(), db, config.Scrobbler{}); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if err := db.Where("account_id = ?", account).Delete(&model.Scrobble{}).Error; err != nil {
		t.Fatalf("delete scrobble: %v", err)
	}

	if _, err := BackfillPlayScrobbles(context.Background(), db, config.Scrobbler{}); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	var count int64
	db.Model(&model.Scrobble{}).Where("account_id = ?", account).Count(&count)
	if count != 0 {
		t.Fatalf("scrobbles=%d after rerun, want 0 (deletion stays deleted)", count)
	}
}
