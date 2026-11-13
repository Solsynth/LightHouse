package librarysync

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/store"
)

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("sqlite open: %v", err)
	}
	if err := db.AutoMigrate(model.LibrarySyncModels()...); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

// pushBatch applies one push in a transaction, mirroring the handler.
func pushBatch(t *testing.T, db *gorm.DB, accountID uuid.UUID, deviceID string, changes []Change) (int64, []int) {
	t.Helper()
	rejected := []int{}
	rev := int64(0)
	now := time.Now().UTC()
	err := db.Transaction(func(tx *gorm.DB) error {
		base, err := store.NextLibrarySyncRevs(tx, accountID, len(changes))
		if err != nil {
			return err
		}
		a := &applier{tx: tx, accountID: accountID, deviceID: deviceID, now: now}
		for i, change := range changes {
			ok, err := a.applyOne(change, base+int64(i))
			if err != nil {
				return err
			}
			if !ok {
				rejected = append(rejected, i)
			}
		}
		rev, err = store.CurrentLibrarySyncRev(tx, accountID)
		return err
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	return rev, rejected
}

func trackChange(fingerprint, title, updatedAt string) Change {
	return Change{
		Kind:   ChangeUpsert,
		Entity: EntityTrack,
		Track: &TrackRecord{
			Fingerprint: fingerprint,
			Title:       title,
			Artist:      new("Coldplay"),
			DurationMs:  266000,
			CreatedAt:   "2026-01-01T00:00:00Z",
			UpdatedAt:   updatedAt,
		},
	}
}

// A stale clock loses: the stored row and its revision are untouched.
func TestTrackPushRejectsOlderClock(t *testing.T) {
	db := testDB(t)
	account := uuid.New()

	rev, rejected := pushBatch(t, db, account, "dev-a", []Change{
		trackChange("coldplay|yellow", "Yellow", "2026-01-02T00:00:00Z"),
	})
	if len(rejected) != 0 || rev != 1 {
		t.Fatalf("first push: rev=%d rejected=%v", rev, rejected)
	}

	rev, rejected = pushBatch(t, db, account, "dev-a", []Change{
		trackChange("coldplay|yellow", "Yellow (stale)", "2026-01-01T00:00:00Z"),
	})
	if len(rejected) != 1 || rejected[0] != 0 {
		t.Fatalf("stale push rejected=%v, want [0]", rejected)
	}
	if rev != 2 {
		t.Fatalf("cursor rev=%d, want 2 (a revision is reserved per change)", rev)
	}

	row := model.LibrarySyncTrack{}
	if err := db.Where("account_id = ? AND fingerprint = ?", account, "coldplay|yellow").Take(&row).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if row.Title != "Yellow" {
		t.Fatalf("title=%q, want the first push's value", row.Title)
	}
	if row.Rev != 1 {
		t.Fatalf("row rev=%d, want 1", row.Rev)
	}
}

// A newer clock overwrites every mirror column and claims the new revision.
func TestTrackPushAcceptsNewerClock(t *testing.T) {
	db := testDB(t)
	account := uuid.New()

	pushBatch(t, db, account, "dev-a", []Change{trackChange("coldplay|yellow", "Yellow", "2026-01-02T00:00:00Z")})
	rev, rejected := pushBatch(t, db, account, "dev-b", []Change{
		trackChange("coldplay|yellow", "Yellow (remaster)", "2026-02-02T00:00:00Z"),
	})
	if len(rejected) != 0 {
		t.Fatalf("newer push rejected=%v, want none", rejected)
	}
	if rev != 2 {
		t.Fatalf("cursor rev=%d, want 2", rev)
	}

	row := model.LibrarySyncTrack{}
	if err := db.Where("account_id = ? AND fingerprint = ?", account, "coldplay|yellow").Take(&row).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if row.Title != "Yellow (remaster)" {
		t.Fatalf("title=%q, want the newer value", row.Title)
	}
	if row.Rev != 2 {
		t.Fatalf("row rev=%d, want 2", row.Rev)
	}
	if row.DeviceID != "dev-b" {
		t.Fatalf("device=%q, want dev-b", row.DeviceID)
	}
}

// Local-file links carry one device's paths; they never reach the mirror.
func TestLocalLinkIsRejected(t *testing.T) {
	db := testDB(t)
	account := uuid.New()

	_, rejected := pushBatch(t, db, account, "dev-a", []Change{{
		Kind:   ChangeUpsert,
		Entity: EntityLink,
		Link: &LinkRecord{
			SourceID:         localSourceID,
			ExternalID:       "/Users/me/Music/yellow.mp3",
			TrackFingerprint: "coldplay|yellow",
			LinkedAt:         "2026-01-01T00:00:00Z",
			LastSeenAt:       "2026-01-01T00:00:00Z",
		},
	}})
	if len(rejected) != 1 || rejected[0] != 0 {
		t.Fatalf("rejected=%v, want [0]", rejected)
	}
	var count int64
	db.Model(&model.LibrarySyncLink{}).Count(&count)
	if count != 0 {
		t.Fatalf("link rows=%d, want 0", count)
	}
}

// A play re-sent with more progress folds into one row instead of duplicating.
func TestPlayMergesProgress(t *testing.T) {
	db := testDB(t)
	account := uuid.New()

	play := func(listened int64, completed bool) Change {
		return Change{
			Kind:   ChangeUpsert,
			Entity: EntityPlay,
			Play: &PlayRecord{
				TrackFingerprint: "coldplay|yellow",
				SourceID:         "subsonic",
				StartedAt:        "2026-01-01T10:00:00Z",
				Clock:            "2026-01-01T10:00:00Z",
				ListenedMs:       listened,
				Completed:        completed,
			},
		}
	}

	rev, _ := pushBatch(t, db, account, "dev-a", []Change{play(12000, false)})
	if rev != 1 {
		t.Fatalf("rev=%d, want 1", rev)
	}
	rev, rejected := pushBatch(t, db, account, "dev-a", []Change{play(266000, true)})
	if len(rejected) != 0 || rev != 2 {
		t.Fatalf("rev=%d rejected=%v", rev, rejected)
	}

	rows := []model.LibrarySyncPlay{}
	db.Where("account_id = ?", account).Find(&rows)
	if len(rows) != 1 {
		t.Fatalf("play rows=%d, want 1", len(rows))
	}
	if rows[0].ListenedMs != 266000 || !rows[0].Completed {
		t.Fatalf("stored play=%+v, want max listened and completed", rows[0])
	}
	if rows[0].Rev != 2 {
		t.Fatalf("play rev=%d, want 2", rows[0].Rev)
	}
}

// A deleted track keeps a visible tombstone so the next pull ships it.
func TestTrackDeleteLeavesTombstone(t *testing.T) {
	db := testDB(t)
	account := uuid.New()

	pushBatch(t, db, account, "dev-a", []Change{trackChange("coldplay|yellow", "Yellow", "2026-01-02T00:00:00Z")})
	_, rejected := pushBatch(t, db, account, "dev-a", []Change{{
		Kind:      ChangeDelete,
		Entity:    EntityTrack,
		DeletedAt: new("2026-01-03T00:00:00Z"),
		Track:     &TrackRecord{Fingerprint: "coldplay|yellow"},
	}})
	if len(rejected) != 0 {
		t.Fatalf("delete rejected=%v, want none", rejected)
	}

	row := model.LibrarySyncTrack{}
	if err := db.Where("account_id = ? AND fingerprint = ?", account, "coldplay|yellow").Take(&row).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if row.DeletedAt == nil {
		t.Fatal("deleted_at is nil, want the tombstone clock")
	}
	if row.Rev != 2 {
		t.Fatalf("row rev=%d, want 2", row.Rev)
	}

	removals := []string{}
	for _, decoded := range []map[string]any{
		{"fingerprint": row.Fingerprint, "deleted_at": formatTime(*row.DeletedAt)},
	} {
		encoded, err := json.Marshal(decoded)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		removals = append(removals, string(encoded))
	}
	if removals[0] != `{"deleted_at":"2026-01-03T00:00:00Z","fingerprint":"coldplay|yellow"}` {
		t.Fatalf("removal=%q", removals[0])
	}
}

// Session documents are per-account last-write-wins; a stale push is rejected.
func TestSessionDocumentRejectsStaleClock(t *testing.T) {
	db := testDB(t)
	account := uuid.New()

	session := func(position int64, updatedAt string) Change {
		return Change{
			Kind:   ChangeUpsert,
			Entity: EntitySession,
			Session: &SessionRecord{
				QueueJSON:  `["coldplay|yellow"]`,
				PositionMs: position,
				DurationMs: 266000,
				RepeatMode: "off",
				UpdatedAt:  updatedAt,
			},
		}
	}

	pushBatch(t, db, account, "dev-a", []Change{session(1000, "2026-01-02T00:00:00Z")})
	if _, rejected := pushBatch(t, db, account, "dev-b", []Change{session(2000, "2026-01-01T00:00:00Z")}); len(rejected) != 1 {
		t.Fatalf("stale session rejected=%v, want [0]", rejected)
	}

	rows := []model.LibrarySyncSession{}
	db.Where("account_id = ?", account).Find(&rows)
	if len(rows) != 1 {
		t.Fatalf("session rows=%d, want 1", len(rows))
	}
	stored := map[string]any{}
	if err := json.Unmarshal(rows[0].Data, &stored); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if stored["position_ms"] == float64(2000) {
		t.Fatalf("stored session adopted the stale push: %+v", stored)
	}
}

// Settings documents key their clock on _updated_at and store the map without
// that key.
func TestSettingsDocumentClock(t *testing.T) {
	db := testDB(t)
	account := uuid.New()

	pushBatch(t, db, account, "dev-a", []Change{{
		Kind:     ChangeUpsert,
		Entity:   EntitySettings,
		Settings: map[string]any{"themeMode": "dark", settingsClockKey: "2026-01-02T00:00:00Z"},
	}})

	rows := []model.LibrarySyncSettings{}
	db.Where("account_id = ?", account).Find(&rows)
	if len(rows) != 1 {
		t.Fatalf("settings rows=%d, want 1", len(rows))
	}
	if !rows[0].Clock.Equal(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("clock=%s, want the pushed _updated_at", rows[0].Clock)
	}
	stored := map[string]any{}
	if err := json.Unmarshal(rows[0].Data, &stored); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if _, ok := stored[settingsClockKey]; ok {
		t.Fatalf("_updated_at leaked into the stored document: %+v", stored)
	}
	if stored["themeMode"] != "dark" {
		t.Fatalf("stored settings=%+v, want themeMode dark", stored)
	}

	if _, rejected := pushBatch(t, db, account, "dev-b", []Change{{
		Kind:     ChangeUpsert,
		Entity:   EntitySettings,
		Settings: map[string]any{"themeMode": "light", settingsClockKey: "2026-01-01T00:00:00Z"},
	}}); len(rejected) != 1 {
		t.Fatalf("stale settings rejected=%v, want [0]", rejected)
	}
}

func TestPageBoundaryCutsAtOverflow(t *testing.T) {
	boundary, more := pageBoundary(0, 2,
		snapshot{1, 2, 3}, snapshot{1, 2}, snapshot{1})
	if !more || boundary != 2 {
		t.Fatalf("boundary=%d hasMore=%v, want 2/true", boundary, more)
	}
	boundary, more = pageBoundary(0, 3, snapshot{1, 2}, snapshot{1})
	if more || boundary != 2 {
		t.Fatalf("boundary=%d hasMore=%v, want 2/false", boundary, more)
	}
}
