package metadata

import (
	"context"
	"fmt"
	"strings"
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

// TestMetadataEngineSplitsSearchAcrossProviders keeps a chatty upstream from
// taking every slot: with two providers and ten slots each contributes five,
// even though both ignore the limit they are handed.
func TestMetadataEngineSplitsSearchAcrossProviders(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite test db: %v", err)
	}
	_ = db.AutoMigrate(
		&model.CatalogItem{},
		&model.Creator{},
		&model.CatalogCreator{},
		&model.Scrobble{},
		&model.ActiveSession{},
	)

	engine := NewEngine(db, "https://solian.live")
	engine.RegisterProvider(&bulkProvider{name: "first", domain: model.DomainAnime, count: 20})
	engine.RegisterProvider(&bulkProvider{name: "second", domain: model.DomainAnime, count: 20})

	results, err := engine.Search(context.Background(), model.DomainAnime, "anything", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 10 {
		t.Fatalf("expected the limit to hold, got %d results", len(results))
	}

	from := map[string]int{}
	for _, result := range results {
		for _, name := range []string{"first", "second"} {
			prefix := "pulsar:anime:" + name + ":"
			if strings.HasPrefix(result.URN, prefix) {
				from[name]++
			}
		}
	}
	for _, name := range []string{"first", "second"} {
		if from[name] == 0 {
			t.Errorf("expected results from %q, got %+v", name, results)
		}
	}
}

type bulkProvider struct {
	name   string
	domain string
	count  int
}

func (b *bulkProvider) Name() string   { return b.name }
func (b *bulkProvider) Domain() string { return b.domain }
func (b *bulkProvider) FetchByProviderKey(context.Context, string) (*UnifiedMedia, error) {
	return nil, fmt.Errorf("not implemented")
}
func (b *bulkProvider) Search(context.Context, string, int) ([]UnifiedMedia, error) {
	out := make([]UnifiedMedia, 0, b.count)
	for i := 0; i < b.count; i++ {
		out = append(out, UnifiedMedia{
			URN:    fmt.Sprintf("pulsar:%s:%s:%d", b.domain, b.name, i),
			Domain: b.domain,
			Slug:   fmt.Sprintf("%s-%d", b.name, i),
			Title:  fmt.Sprintf("%s %d", b.name, i),
		})
	}
	return out, nil
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
		&model.Creator{},
		&model.CatalogCreator{},
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

// TestMetadataEngineCreators walks the creator path a resolve uses: provider
// credits become linked Creator records, resolve hands them back, and search
// finds items by a creator's name — the "everything by X" query.
func TestMetadataEngineCreators(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite test db: %v", err)
	}
	_ = db.AutoMigrate(
		&model.CatalogItem{},
		&model.Creator{},
		&model.CatalogCreator{},
	)

	engine := NewEngine(db, "https://solian.live")
	engine.RegisterProvider(&mockProvider{
		name:   "steam",
		domain: model.DomainGaming,
		media: &UnifiedMedia{
			URN:    "pulsar:gaming:steam:440",
			Domain: model.DomainGaming,
			Slug:   "steam-440",
			Title:  "Team Fortress 2",
			Creators: []model.CreatorCredit{
				{Name: "Valve", Role: model.RoleDeveloper},
				{Name: "Valve", Role: model.RolePublisher},
			},
		},
	})

	ctx := context.Background()
	resolved, err := engine.Resolve(ctx, "steam:440")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// Both roles point at one Creator record, and resolve carries them back.
	if len(resolved.Creators) != 2 {
		t.Fatalf("expected 2 credits on resolved media, got %d", len(resolved.Creators))
	}
	var creators []model.Creator
	if err := db.Find(&creators).Error; err != nil {
		t.Fatalf("list creators: %v", err)
	}
	if len(creators) != 1 || creators[0].Name != "Valve" || creators[0].Domain != model.DomainGaming {
		t.Fatalf("expected one Valve creator in gaming, got %+v", creators)
	}

	// Searching by the creator's name finds the item, like an
	// "everything by Valve" browse.
	found, err := engine.Search(ctx, model.DomainGaming, "Valve", 10)
	if err != nil {
		t.Fatalf("search by creator: %v", err)
	}
	if len(found) != 1 || found[0].Title != "Team Fortress 2" {
		t.Fatalf("expected Team Fortress 2 via creator search, got %+v", found)
	}
}
