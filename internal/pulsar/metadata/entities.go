package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/datatypes"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

// MusicBrainz allows one request per second per client, and every client of
// the fleet shares Pulsar's budget. So an entity — an artist by name, an
// album by title — is resolved once and kept:
//
//  1. the catalog row, durable and shared: the same record a share link and a
//     scrobble resolve to, carrying the description, cover and tracklist;
//  2. an in-memory entry keyed by the question, for the window in which a
//     second identical request arrives (two rows of one artist on a page, a
//     pull-to-refresh).
//
// The memory layer is deliberately small and short-lived. It exists to
// coalesce concurrent asks, not to be a second database.

const entityCacheTTL = 6 * time.Hour

// entityKey names one question, not one answer: the artist called "Aoi Tada"
// read from a Japanese UI is a different key from the same name read in
// English, because the description comes back in the reader's language.
type entityKey struct {
	kind string
	lang string
	a    string
	b    string
}

type entityEntry struct {
	urn string
	at  time.Time
}

type entityCache struct {
	mu      sync.Mutex
	flights map[entityKey]*sync.WaitGroup
	entries map[entityKey]entityEntry
}

func newEntityCache() *entityCache {
	return &entityCache{
		flights: make(map[entityKey]*sync.WaitGroup),
		entries: make(map[entityKey]entityEntry),
	}
}

func (c *entityCache) get(key entityKey) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || time.Since(entry.at) > entityCacheTTL {
		delete(c.entries, key)
		return "", false
	}
	return entry.urn, true
}

func (c *entityCache) put(key entityKey, urn string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = entityEntry{urn: urn, at: time.Now()}
}

// enter serialises concurrent resolutions of one question: the second caller
// waits for the first instead of spending a second upstream request on the
// same answer. The returned func must always be called.
func (c *entityCache) enter(key entityKey) func() {
	c.mu.Lock()
	if flight, ok := c.flights[key]; ok {
		c.mu.Unlock()
		flight.Wait()
		return func() {}
	}
	flight := &sync.WaitGroup{}
	flight.Add(1)
	c.flights[key] = flight
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		delete(c.flights, key)
		c.mu.Unlock()
		flight.Done()
	}
}

// ResolveArtist is the artist-page door: a name in — the only handle every
// library shares, since Subsonic, Jellyfin, NetEase and a local file all
// disagree on ids — and the fleet's cached catalog row out. The reader's
// language comes off the request context.
func (e *Engine) ResolveArtist(ctx context.Context, name string) (*UnifiedMedia, error) {
	return e.resolveArtist(ctx, name, readerLang(ctx))
}

// ResolveAlbum is the album-page door: a title and its artist, the pair every
// library spells, resolved to the fleet's cached record for that release
// group.
func (e *Engine) ResolveAlbum(ctx context.Context, title, artist string) (*UnifiedMedia, error) {
	return e.resolveAlbum(ctx, title, artist, readerLang(ctx))
}

// resolveArtist turns an artist's name into the fleet's catalog row for that
// artist, through the entity cache.

func (e *Engine) resolveArtist(ctx context.Context, name, lang string) (*UnifiedMedia, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("empty artist name")
	}
	key := entityKey{kind: KindArtist, lang: lang, a: strings.ToLower(name)}
	return e.resolveEntity(ctx, key, func(ctx context.Context) (string, error) {
		resolver, ok := e.providerFor(KindArtist).(interface {
			ResolveByName(context.Context, string) (string, error)
		})
		if !ok {
			return "", fmt.Errorf("no artist metadata provider")
		}
		return resolver.ResolveByName(ctx, name)
	})
}

// resolveAlbum turns an album's title and artist into the fleet's catalog row
// for that record.
func (e *Engine) resolveAlbum(ctx context.Context, title, artist, lang string) (*UnifiedMedia, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("empty album title")
	}
	key := entityKey{
		kind: KindAlbum,
		lang: lang,
		a:    strings.ToLower(title),
		b:    strings.ToLower(strings.TrimSpace(artist)),
	}
	return e.resolveEntity(ctx, key, func(ctx context.Context) (string, error) {
		resolver, ok := e.providerFor(KindAlbum).(interface {
			ResolveByTitle(context.Context, string, string) (string, error)
		})
		if !ok {
			return "", fmt.Errorf("no album metadata provider")
		}
		return resolver.ResolveByTitle(ctx, title, artist)
	})
}

// resolveEntity answers one question through the cache and the resolve path.
func (e *Engine) resolveEntity(ctx context.Context, key entityKey, find func(context.Context) (string, error)) (*UnifiedMedia, error) {
	if urn, ok := e.entities.get(key); ok {
		media, err := e.Resolve(ctx, urn)
		// A row that was deleted between the memory entry and now falls
		// through to a fresh resolve rather than failing the page.
		if err == nil {
			return media, nil
		}
	}

	leave := e.entities.enter(key)
	defer leave()
	// Another caller may have filled the entry while this one waited.
	if urn, ok := e.entities.get(key); ok {
		if media, err := e.Resolve(ctx, urn); err == nil {
			return media, nil
		}
	}

	urn, err := find(ctx)
	if err != nil {
		return nil, err
	}
	media, err := e.Resolve(ctx, urn)
	if err != nil {
		return nil, err
	}
	e.entities.put(key, urn)
	return media, nil
}

// providerFor finds the provider registered for one entity kind.
func (e *Engine) providerFor(kind string) Provider {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, provider := range e.providers {
		if typed, ok := provider.(TypedProvider); ok && typed.Kind() == kind {
			return typed
		}
	}
	return nil
}

// refreshEntity re-asks upstream for a row that predates the entity
// providers, then writes the answer back. It is the one path that costs a
// request for an existing catalog row, and it runs at most once per row.
func (e *Engine) refreshEntity(ctx context.Context, kind string, row *model.CatalogItem) (*UnifiedMedia, error) {
	key := ""
	if ids := row.GetExternalIDs(); ids != nil {
		key = ids["musicbrainz"]
	}
	if key == "" {
		return nil, fmt.Errorf("row carries no musicbrainz id")
	}
	provider := e.providerFor(kind)
	if provider == nil {
		return nil, fmt.Errorf("no %s metadata provider", kind)
	}
	fresh, err := provider.FetchByProviderKey(ctx, key)
	if err != nil {
		return nil, err
	}
	e.enrichEntity(ctx, row, fresh)
	return e.Resolve(ctx, row.URN)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// entityKindOf reports which entity an identifier names, for the enrichment
// pass a bare row needs.
func entityKindOf(identifier string) (string, bool) {
	_, providerName, _ := parseIdentifier(identifier)
	switch providerName {
	case "musicbrainz-artist":
		return KindArtist, true
	case "musicbrainz-album":
		return KindAlbum, true
	}
	return "", false
}

// isBareEntity reports whether a catalog row lacks the rich payload its kind
// is supposed to carry.
func isBareEntity(media *UnifiedMedia, kind string) bool {
	switch kind {
	case KindArtist:
		return media.Artist == nil && len(media.Albums) == 0
	case KindAlbum:
		return len(media.Tracks) == 0 && media.Description == ""
	}
	return false
}

// enrichEntity writes a resolved entity's rich payload into its catalog row,
// so the next read — a share page, another client, this device tomorrow —
// carries the description and artwork without asking MusicBrainz again.
func (e *Engine) enrichEntity(ctx context.Context, row *model.CatalogItem, media *UnifiedMedia) {
	if row == nil || media == nil {
		return
	}
	updates := map[string]any{}
	if payload := entityPayload(media); payload != nil {
		updates["metadata"] = payload
	}
	if media.Description != "" {
		updates["description"] = nullableString(media.Description)
	} else if media.Bio != "" {
		updates["description"] = nullableString(media.Bio)
	}
	if artwork := firstNonEmptyString(media.ArtworkURL, media.ImageURL); artwork != "" {
		updates["artwork_url"] = nullableString(artwork)
	}
	if len(updates) == 0 {
		return
	}
	e.db.WithContext(ctx).
		Model(&model.CatalogItem{}).
		Where("id = ?", row.ID).
		Updates(updates)
}

// entityPayload is the rich half of a resolved media, as stored in the
// catalog row's metadata column. Null when the record has none — a track
// resolve is a name, an artist and a length, and nothing here.
func entityPayload(m *UnifiedMedia) datatypes.JSON {
	if m == nil {
		return nil
	}
	payload := map[string]any{}
	if m.Artist != nil {
		payload["artist"] = m.Artist
	}
	if len(m.Tracks) > 0 {
		payload["tracks"] = m.Tracks
	}
	if len(m.Albums) > 0 {
		payload["albums"] = m.Albums
	}
	if m.Bio != "" {
		payload["bio"] = m.Bio
		payload["bio_source"] = m.BioSource
	}
	if len(payload) == 0 {
		return nil
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return datatypes.JSON(encoded)
}

// entityMetadata reads the stored rich payload back onto a resolved media.
// It runs on every itemToUnified read, so a cached artist page costs no
// upstream call at all.
func entityMetadata(item *model.CatalogItem) (artist *ArtistProfile, tracks []AlbumTrack, albums []AlbumSummary, bio, bioSource string) {
	if item == nil || len(item.Metadata) == 0 {
		return nil, nil, nil, "", ""
	}
	var payload struct {
		Artist    *ArtistProfile `json:"artist"`
		Tracks    []AlbumTrack   `json:"tracks"`
		Albums    []AlbumSummary `json:"albums"`
		Bio       string         `json:"bio"`
		BioSource string         `json:"bio_source"`
	}
	if err := json.Unmarshal(item.Metadata, &payload); err != nil {
		return nil, nil, nil, "", ""
	}
	return payload.Artist, payload.Tracks, payload.Albums, payload.Bio, payload.BioSource
}
