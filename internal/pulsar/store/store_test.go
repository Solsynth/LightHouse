package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

func setupTestDB(t *testing.T) *Store {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite test db: %v", err)
	}

	err = db.AutoMigrate(
		&model.CatalogItem{},
		&model.ActiveSession{},
		&model.Scrobble{},
		&model.Collection{},
		&model.CollectionItem{},
		&model.ExternalSyncConnection{},
	)
	if err != nil {
		t.Fatalf("failed to migrate tables: %v", err)
	}

	return New(db)
}

func TestCatalogOperations(t *testing.T) {
	s := setupTestDB(t)
	ctx := context.Background()

	sub := "Porter Robinson"
	cap := "SMILE! :D"
	item := &model.CatalogItem{
		Domain:   model.DomainMusic,
		Title:    "Cheerleader",
		Subtitle: &sub,
		Caption:  &cap,
	}

	saved, err := s.FindOrCreateCatalogItem(ctx, item)
	if err != nil {
		t.Fatalf("FindOrCreateCatalogItem failed: %v", err)
	}
	if saved.ID == uuid.Nil {
		t.Fatalf("expected non-nil UUID for catalog item")
	}

	// Probe duplicate - should return same ID
	saved2, err := s.FindOrCreateCatalogItem(ctx, item)
	if err != nil {
		t.Fatalf("FindOrCreateCatalogItem probe failed: %v", err)
	}
	if saved2.ID != saved.ID {
		t.Fatalf("expected same catalog ID, got %s and %s", saved.ID, saved2.ID)
	}

	// List catalog
	items, total, err := s.ListCatalogItems(ctx, model.DomainMusic, "", "Cheer", 0, 10)
	if err != nil {
		t.Fatalf("ListCatalogItems failed: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected 1 item, got total=%d len=%d", total, len(items))
	}
}

func TestActiveSessionLifecycle(t *testing.T) {
	s := setupTestDB(t)
	ctx := context.Background()

	accountID := uuid.New()
	manualID := "groovybox:player"

	sess := &model.ActiveSession{
		AccountID:       accountID,
		Domain:          model.DomainMusic,
		ManualID:        manualID,
		Title:           "Russian Roulette",
		ProgressSeconds: 45,
		DurationSeconds: 210,
		LeaseMinutes:    5,
	}

	if err := s.UpsertActiveSession(ctx, sess); err != nil {
		t.Fatalf("UpsertActiveSession failed: %v", err)
	}

	active, err := s.GetActiveSession(ctx, accountID, manualID)
	if err != nil {
		t.Fatalf("GetActiveSession failed: %v", err)
	}
	if active.Title != "Russian Roulette" {
		t.Fatalf("expected title 'Russian Roulette', got %s", active.Title)
	}

	allActive, err := s.GetActiveSessionsForAccount(ctx, accountID)
	if err != nil || len(allActive) != 1 {
		t.Fatalf("expected 1 active session, got len=%d err=%v", len(allActive), err)
	}

	ended, err := s.EndActiveSession(ctx, accountID, manualID)
	if err != nil {
		t.Fatalf("EndActiveSession failed: %v", err)
	}
	if ended.EndedAt == nil {
		t.Fatalf("expected EndedAt to be set")
	}

	allActiveAfter, err := s.GetActiveSessionsForAccount(ctx, accountID)
	if err != nil || len(allActiveAfter) != 0 {
		t.Fatalf("expected 0 active sessions after end, got len=%d", len(allActiveAfter))
	}
}

func TestScrobbleAndStats(t *testing.T) {
	s := setupTestDB(t)
	ctx := context.Background()

	accountID := uuid.New()
	artist := "Porter Robinson"

	// Add 3 scrobbles
	for i := 0; i < 3; i++ {
		scrobble := &model.Scrobble{
			AccountID:       accountID,
			Domain:          model.DomainMusic,
			ClientName:      "GroovyBox",
			Title:           "Knock Yourself Out XD",
			Subtitle:        &artist,
			ListenedSeconds: 180,
			DurationSeconds: 180,
			PlayedAt:        time.Now().UTC(),
		}
		if err := s.CreateScrobble(ctx, scrobble); err != nil {
			t.Fatalf("CreateScrobble failed: %v", err)
		}
	}

	// Add 1 gaming scrobble
	dev := "Valve"
	gameScrobble := &model.Scrobble{
		AccountID:       accountID,
		Domain:          model.DomainGaming,
		ClientName:      "SteamWatcher",
		Title:           "Counter-Strike 2",
		Subtitle:        &dev,
		ListenedSeconds: 3600,
		PlayedAt:        time.Now().UTC(),
	}
	if err := s.CreateScrobble(ctx, gameScrobble); err != nil {
		t.Fatalf("CreateScrobble game failed: %v", err)
	}

	list, total, err := s.ListScrobbles(ctx, accountID, "", nil, nil, 0, 10)
	if err != nil || total != 4 || len(list) != 4 {
		t.Fatalf("expected 4 scrobbles, got total=%d len=%d err=%v", total, len(list), err)
	}

	// Check stats
	stats, err := s.GetMediaStats(ctx, accountID, "", nil, nil)
	if err != nil {
		t.Fatalf("GetMediaStats failed: %v", err)
	}
	if stats.TotalScrobbles != 4 {
		t.Fatalf("expected total 4 scrobbles, got %d", stats.TotalScrobbles)
	}
	if stats.TotalSeconds != 3*180+3600 {
		t.Fatalf("expected total %d seconds, got %d", 3*180+3600, stats.TotalSeconds)
	}
	if stats.DomainBreakdown[model.DomainMusic] != 3 || stats.DomainBreakdown[model.DomainGaming] != 1 {
		t.Fatalf("unexpected domain breakdown: %v", stats.DomainBreakdown)
	}
	if len(stats.TopCreators) == 0 {
		t.Fatalf("expected top creators to be populated")
	}
}

func TestCollections(t *testing.T) {
	s := setupTestDB(t)
	ctx := context.Background()

	accountID := uuid.New()
	coll := &model.Collection{
		AccountID: accountID,
		Domain:    model.DomainMusic,
		Kind:      model.KindPlaylist,
		Name:      "Synthpop Favorites",
		IsPublic:  true,
	}

	if err := s.CreateCollection(ctx, coll); err != nil {
		t.Fatalf("CreateCollection failed: %v", err)
	}

	item := &model.CollectionItem{
		CollectionID: coll.ID,
		Position:     1,
	}
	if err := s.AddCollectionItem(ctx, item); err != nil {
		t.Fatalf("AddCollectionItem failed: %v", err)
	}

	loaded, err := s.GetCollection(ctx, coll.ID)
	if err != nil {
		t.Fatalf("GetCollection failed: %v", err)
	}
	if loaded.Name != "Synthpop Favorites" || len(loaded.Items) != 1 {
		t.Fatalf("unexpected collection: %+v", loaded)
	}

	if err := s.RemoveCollectionItem(ctx, coll.ID, item.ID); err != nil {
		t.Fatalf("RemoveCollectionItem failed: %v", err)
	}

	loadedAfter, err := s.GetCollection(ctx, coll.ID)
	if err != nil || len(loadedAfter.Items) != 0 {
		t.Fatalf("expected 0 items after remove, got len=%d err=%v", len(loadedAfter.Items), err)
	}
}
