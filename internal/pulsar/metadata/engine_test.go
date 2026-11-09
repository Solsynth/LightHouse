package metadata

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

type mockProvider struct {
	name   string
	domain string
	media  *UnifiedMedia
}

func (m *mockProvider) Name() string   { return m.name }
func (m *mockProvider) Domain() string { return m.domain }
func (m *mockProvider) FetchByProviderKey(_ context.Context, key string) (*UnifiedMedia, error) {
	clone := *m.media
	clone.ExternalIDs = map[string]string{m.name: key}
	return &clone, nil
}
func (m *mockProvider) Search(_ context.Context, _ string, _ int) ([]UnifiedMedia, error) {
	return []UnifiedMedia{*m.media}, nil
}

func TestMetadataEngine(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite test db: %v", err)
	}
	_ = db.AutoMigrate(
		&model.CatalogItem{},
		&model.ActiveSession{},
		&model.Scrobble{},
	)

	engine := NewEngine(db, "https://solian.live")

	// Register mock Steam provider
	mockSteam := &mockProvider{
		name:   "steam",
		domain: model.DomainGaming,
		media: &UnifiedMedia{
			URN:         "pulsar:gaming:steam:730",
			Domain:      model.DomainGaming,
			Slug:        "steam-730",
			Title:       "Counter-Strike 2",
			Subtitle:    "Valve",
			Caption:     "PC / Steam",
			Description: "Tactical shooter",
		},
	}
	engine.RegisterProvider(mockSteam)

	ctx := context.Background()

	// 1. Resolve via provider:key
	resolved, err := engine.Resolve(ctx, "steam:730")
	if err != nil {
		t.Fatalf("failed to resolve steam:730: %v", err)
	}
	if resolved.Title != "Counter-Strike 2" || resolved.ID == uuid.Nil {
		t.Fatalf("unexpected resolved media: %+v", resolved)
	}
	if resolved.ShareURL != "https://solian.live/share/m/steam-730" {
		t.Fatalf("unexpected share URL: %s", resolved.ShareURL)
	}

	// 2. Resolve again by UUID directly from database
	fromDB, err := engine.Resolve(ctx, resolved.ID.String())
	if err != nil {
		t.Fatalf("failed to resolve from DB by UUID: %v", err)
	}
	if fromDB.Title != "Counter-Strike 2" {
		t.Fatalf("expected title Counter-Strike 2, got %s", fromDB.Title)
	}

	// 3. Resolve by Slug
	fromSlug, err := engine.Resolve(ctx, "steam-730")
	if err != nil {
		t.Fatalf("failed to resolve by slug: %v", err)
	}
	if fromSlug.Title != "Counter-Strike 2" {
		t.Fatalf("expected title Counter-Strike 2, got %s", fromSlug.Title)
	}

	// 4. Record a scrobble linked to this item and check ScrobbleCount in resolved metadata
	scrobble := &model.Scrobble{
		ID:         uuid.New(),
		AccountID:  uuid.New(),
		CatalogID:  &resolved.ID,
		Domain:     model.DomainGaming,
		ClientName: "TestClient",
		Title:      "Counter-Strike 2",
	}
	if err := db.Create(scrobble).Error; err != nil {
		t.Fatalf("failed to create scrobble: %v", err)
	}

	withStats, err := engine.Resolve(ctx, "steam:730")
	if err != nil {
		t.Fatalf("failed to resolve with stats: %v", err)
	}
	if withStats.ScrobbleCount != 1 {
		t.Fatalf("expected ScrobbleCount=1, got %d", withStats.ScrobbleCount)
	}
}
