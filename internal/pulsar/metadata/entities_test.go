package metadata

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

// artistProvider is a MusicBrainzArtistProvider stand-in: it answers by name
// and counts how often the engine actually went upstream, which is the whole
// property under test.
type artistProvider struct {
	fetches atomic.Int32
	byName  atomic.Int32
}

func (p *artistProvider) Name() string   { return "musicbrainz-artist" }
func (p *artistProvider) Domain() string { return model.DomainMusic }
func (p *artistProvider) Kind() string   { return KindArtist }

func (p *artistProvider) ResolveByName(_ context.Context, name string) (string, error) {
	p.byName.Add(1)
	if name != "Aoi Tada" {
		return "", errors.New("unknown artist")
	}
	return "pulsar:music:musicbrainz-artist:artist-1", nil
}

func (p *artistProvider) FetchByProviderKey(_ context.Context, key string) (*UnifiedMedia, error) {
	p.fetches.Add(1)
	return &UnifiedMedia{
		URN:       "pulsar:music:musicbrainz-artist:" + key,
		Domain:    model.DomainMusic,
		Slug:      "mbid-artist-" + key,
		Title:     "Aoi Tada",
		Subtitle:  "person · Tokyo",
		Bio:       "A Japanese singer.",
		BioSource: "EN Wikipedia",
		Artist: &ArtistProfile{
			MBID:    key,
			Type:    "Person",
			Country: "JP",
			Genres:  []string{"j-pop"},
		},
		Albums: []AlbumSummary{
			{MBID: "rg-1", Title: "Sora no Oto", Year: 2010, Type: "Album"},
		},
		ExternalIDs: map[string]string{"musicbrainz": key},
	}, nil
}

func (p *artistProvider) Search(context.Context, string, int) ([]UnifiedMedia, error) {
	return nil, nil
}

// albumProvider answers one title/artist pair.
type albumProvider struct {
	fetches atomic.Int32
}

func (p *albumProvider) Name() string   { return "musicbrainz-album" }
func (p *albumProvider) Domain() string { return model.DomainMusic }
func (p *albumProvider) Kind() string   { return KindAlbum }

func (p *albumProvider) ResolveByTitle(_ context.Context, title, artist string) (string, error) {
	if title != "Sora no Oto" || artist != "Aoi Tada" {
		return "", errors.New("unknown album")
	}
	return "pulsar:music:musicbrainz-album:rg-1", nil
}

func (p *albumProvider) FetchByProviderKey(_ context.Context, key string) (*UnifiedMedia, error) {
	p.fetches.Add(1)
	return &UnifiedMedia{
		URN:         "pulsar:music:musicbrainz-album:" + key,
		Domain:      model.DomainMusic,
		Slug:        "mbid-album-" + key,
		Title:       "Sora no Oto",
		Subtitle:    "Aoi Tada",
		Description: "The soundtrack.",
		ArtworkURL:  "https://coverartarchive.org/release-group/rg-1/front-500",
		Tracks: []AlbumTrack{
			{Title: "Brave Song", Disc: 1, Position: 1, LengthMs: 185000},
		},
		ExternalIDs: map[string]string{"musicbrainz_release_group": key},
	}, nil
}

func (p *albumProvider) Search(context.Context, string, int) ([]UnifiedMedia, error) {
	return nil, nil
}

func newEntityEngine(t *testing.T, providers ...Provider) *Engine {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite test db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.CatalogItem{},
		&model.ActiveSession{},
		&model.Scrobble{},
		&model.Creator{},
		&model.CatalogCreator{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	engine := NewEngine(db, "https://solian.live")
	for _, provider := range providers {
		engine.RegisterProvider(provider)
	}
	return engine
}

// TestResolveArtistCachesUpstreamOnce is the contract the app depends on: the
// artist page is opened again and again, and MusicBrainz is asked exactly
// once per process.
func TestResolveArtistCachesUpstreamOnce(t *testing.T) {
	artist := &artistProvider{}
	engine := newEntityEngine(t, artist)
	ctx := ContextWithReaderLang(context.Background(), "en")

	first, err := engine.ResolveArtist(ctx, "Aoi Tada")
	if err != nil {
		t.Fatalf("resolve artist: %v", err)
	}
	if first.Artist == nil || first.Artist.Country != "JP" {
		t.Fatalf("expected the profile on the answer, got %+v", first.Artist)
	}
	if first.Bio != "A Japanese singer." {
		t.Fatalf("expected the biography, got %q", first.Bio)
	}
	if len(first.Albums) != 1 || first.Albums[0].Title != "Sora no Oto" {
		t.Fatalf("expected one album, got %+v", first.Albums)
	}

	// A second open, a share link resolving the id, and a third page view all
	// read the same stored row.
	for range 3 {
		again, err := engine.ResolveArtist(ctx, "Aoi Tada")
		if err != nil {
			t.Fatalf("resolve artist again: %v", err)
		}
		if again.Bio != first.Bio || len(again.Albums) != 1 {
			t.Fatalf("a cached read lost the payload: %+v", again)
		}
		if _, err := engine.Resolve(ctx, again.URN); err != nil {
			t.Fatalf("resolve by urn: %v", err)
		}
	}

	if got := artist.fetches.Load(); got != 1 {
		t.Fatalf("expected 1 upstream fetch, got %d", got)
	}
	if got := artist.byName.Load(); got != 1 {
		t.Fatalf("expected 1 name search, got %d", got)
	}
}

// TestResolveAlbumCarriesTracklist checks the album payload survives the
// round trip through the catalog row: a page draws the tracklist after the
// row was written, not from the upstream answer in memory.
func TestResolveAlbumCarriesTracklist(t *testing.T) {
	album := &albumProvider{}
	engine := newEntityEngine(t, album)
	ctx := context.Background()

	resolved, err := engine.ResolveAlbum(ctx, "Sora no Oto", "Aoi Tada")
	if err != nil {
		t.Fatalf("resolve album: %v", err)
	}
	if len(resolved.Tracks) != 1 || resolved.Tracks[0].Title != "Brave Song" {
		t.Fatalf("expected the tracklist, got %+v", resolved.Tracks)
	}
	if resolved.ArtworkURL == "" {
		t.Fatal("expected the cover to survive")
	}
	if resolved.Description != "The soundtrack." {
		t.Fatalf("expected the description, got %q", resolved.Description)
	}

	// The share link path reads the row directly.
	fromRow, err := engine.Resolve(ctx, resolved.URN)
	if err != nil {
		t.Fatalf("resolve by urn: %v", err)
	}
	if len(fromRow.Tracks) != 1 || fromRow.Tracks[0].LengthMs != 185000 {
		t.Fatalf("tracklist lost on the stored row: %+v", fromRow.Tracks)
	}
	if got := album.fetches.Load(); got != 1 {
		t.Fatalf("expected 1 upstream fetch, got %d", got)
	}
}

// TestResolveArtistMissIsAnError keeps a miss honest: a page with nothing to
// show says so, rather than drawing an empty shell as if it were an answer.
func TestResolveArtistMissIsAnError(t *testing.T) {
	engine := newEntityEngine(t, &artistProvider{})
	if _, err := engine.ResolveArtist(context.Background(), "Nobody At All"); err == nil {
		t.Fatal("expected an error for an unknown artist")
	}
}
