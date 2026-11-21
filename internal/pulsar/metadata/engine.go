package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/store"
)

// Engine orchestrates metadata resolution, caching, and upstream querying.
type Engine struct {
	db        *gorm.DB
	siteURL   string
	providers map[string]Provider
	mu        sync.RWMutex
	// entities coalesces and remembers name-keyed resolves: an artist by
	// name is one upstream pass per process, not one per page open.
	entities *entityCache
	// trackProviders is the subset of providers the generic search asks: the
	// ones that answer with tracks.
	trackProviders []Provider
}

// NewEngine constructs a metadata resolution engine.
func NewEngine(db *gorm.DB, siteURL string) *Engine {
	return &Engine{
		db:        db,
		siteURL:   strings.TrimRight(siteURL, "/"),
		providers: make(map[string]Provider),
		entities:  newEntityCache(),
	}
}

// RegisterProvider attaches an upstream metadata provider.
func (e *Engine) RegisterProvider(p Provider) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.providers[strings.ToLower(p.Name())] = p
	// An artist or album provider answers for one entity, not a track query:
	// the generic search is a track search, and asking these as well would
	// triple every search's upstream cost to return rows of the wrong kind.
	// The entity endpoints reach them by kind instead.
	if typed, ok := p.(TypedProvider); !ok || typed.Kind() == KindTrack {
		e.trackProviders = append(e.trackProviders, p)
	}
}

// GetProvider retrieves an upstream provider by name.
func (e *Engine) GetProvider(name string) (Provider, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	p, ok := e.providers[strings.ToLower(name)]
	return p, ok
}

// Resolve identifies and returns canonical metadata for an identifier, querying upstream if absent.
func (e *Engine) Resolve(ctx context.Context, identifier string) (*UnifiedMedia, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return nil, errors.New("empty identifier")
	}

	// 1. A catalog row is the answer, and an entity page reads it: that is
	// what keeps the second open of an artist off MusicBrainz. A row written
	// before the entity providers existed carries a name and nothing else, so
	// it is enriched once instead of staying blank for as long as it lives.
	if item, err := e.lookupLocal(ctx, identifier); err == nil && item != nil {
		media := e.itemToUnified(ctx, item)
		if kind, ok := entityKindOf(identifier); ok && isBareEntity(media, kind) {
			if enriched, enrichErr := e.refreshEntity(ctx, kind, item); enrichErr == nil {
				media = enriched
			}
		}
		return media, nil
	}

	// 2. Parse provider and key from identifier
	domain, providerName, key := parseIdentifier(identifier)
	if providerName == "" || key == "" {
		return nil, fmt.Errorf("unable to resolve metadata for: %s", identifier)
	}

	// 3. Query upstream provider
	provider, ok := e.GetProvider(providerName)
	if !ok {
		return nil, fmt.Errorf("provider %q not supported", providerName)
	}

	upstream, err := provider.FetchByProviderKey(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("upstream provider %s error: %w", providerName, err)
	}
	if domain != "" && upstream.Domain == "" {
		upstream.Domain = domain
	}

	// 4. Upsert into database
	saved, err := e.persistUpstream(ctx, providerName, key, upstream)
	if err != nil {
		// Even if saving failed, return the upstream unified data
		upstream.ShareURL = e.FormatShareURL(upstream.Slug)
		return upstream, nil
	}

	// A resolve that knows the answer is durable: the description, cover and
	// tracklist go into the catalog row so the next read — a share page,
	// another client, this device tomorrow — costs nothing upstream.
	e.enrichEntity(ctx, saved, upstream)
	return e.itemToUnified(ctx, saved), nil
}
func (e *Engine) lookupLocal(ctx context.Context, probe string) (*model.CatalogItem, error) {
	db := e.db.WithContext(ctx)

	// Probe UUID
	if id, err := uuid.Parse(probe); err == nil {
		var item model.CatalogItem
		if err := db.Where("id = ?", id).First(&item).Error; err == nil {
			return &item, nil
		}
	}

	// Probe URN or Slug
	var item model.CatalogItem
	if err := db.Where("urn = ? OR slug = ?", probe, probe).First(&item).Error; err == nil {
		return &item, nil
	}

	// Probe provider:key (e.g. steam:730 or mbid:xxx)
	if parts := strings.SplitN(probe, ":", 2); len(parts) == 2 {
		prov := strings.ToLower(parts[0])
		key := parts[1]
		if err := db.Where("provider = ? AND provider_key = ?", prov, key).First(&item).Error; err == nil {
			return &item, nil
		}
	}

	return nil, gorm.ErrRecordNotFound
}

func (e *Engine) persistUpstream(ctx context.Context, providerName, key string, m *UnifiedMedia) (*model.CatalogItem, error) {
	slug := m.Slug
	if slug == "" {
		slug = fmt.Sprintf("%s-%s", providerName, key)
	}
	urn := m.URN
	if urn == "" {
		urn = fmt.Sprintf("pulsar:%s:%s:%s", m.Domain, providerName, key)
	}

	extIDsBytes, _ := json.Marshal(m.ExternalIDs)
	extLinksBytes, _ := json.Marshal(m.ExternalLinks)
	genresBytes, _ := json.Marshal(m.Genres)

	item := &model.CatalogItem{
		ID:              uuid.New(),
		Domain:          m.Domain,
		Slug:            slug,
		URN:             urn,
		Provider:        providerName,
		ProviderKey:     key,
		Title:           m.Title,
		Subtitle:        nullableString(m.Subtitle),
		Caption:         nullableString(m.Caption),
		Description:     nullableString(m.Description),
		ArtworkURL:      nullableString(m.ArtworkURL),
		BannerURL:       nullableString(m.BannerURL),
		DurationSeconds: m.DurationSeconds,
		ReleaseDate:     nullableString(m.ReleaseDate),
		Genres:          datatypes.JSON(genresBytes),
		// The entity payload travels with the row from the first write, so a
		// cached artist or album page costs exactly one upstream pass ever.
		Metadata:      entityPayload(m),
		ExternalIDs:   datatypes.JSON(extIDsBytes),
		ExternalLinks: datatypes.JSON(extLinksBytes),
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}

	err := e.db.WithContext(ctx).Create(item).Error
	if err != nil {
		return nil, err
	}
	e.attachCreators(ctx, item.ID, item.Domain, m.Creators)
	return item, nil
}

// attachCreators resolves the unified media's creator credits into linked
// Creator records. Credits organize the catalog but never fail a resolve.
func (e *Engine) attachCreators(ctx context.Context, catalogID uuid.UUID, domain string, credits []model.CreatorCredit) {
	st := store.New(e.db)
	if err := st.AttachCatalogCreators(ctx, catalogID, domain, credits); err != nil {
		// Credits organize the catalog but never fail a resolve.
		return
	}
}

func (e *Engine) itemToUnified(ctx context.Context, item *model.CatalogItem) *UnifiedMedia {
	slug := item.Slug
	if slug == "" {
		slug = item.ID.String()
	}
	urn := item.URN
	if urn == "" {
		prov := item.Provider
		if prov == "" {
			prov = "native"
		}
		key := item.ProviderKey
		if key == "" {
			key = item.ID.String()
		}
		urn = fmt.Sprintf("pulsar:%s:%s:%s", item.Domain, prov, key)
	}

	var extLinks []ExternalLink
	if len(item.ExternalLinks) > 0 {
		_ = json.Unmarshal(item.ExternalLinks, &extLinks)
	}

	// Compute scrobble metrics
	var scrobbleCount int64
	e.db.WithContext(ctx).Model(&model.Scrobble{}).
		Where("catalog_id = ?", item.ID).
		Count(&scrobbleCount)

	// Compute active now-playing sessions
	var activeCount int64
	e.db.WithContext(ctx).Model(&model.ActiveSession{}).
		Where("catalog_id = ? AND lease_expires_at > ?", item.ID, time.Now().UTC()).
		Count(&activeCount)
	// The rich payload the entity endpoints stored at resolve time: a cached
	// artist or album page is answered from this row, with no upstream call.
	artist, tracks, albums, bio, bioSource := entityMetadata(item)

	return &UnifiedMedia{
		ID:                  item.ID,
		URN:                 urn,
		Domain:              item.Domain,
		Slug:                slug,
		Title:               item.Title,
		Subtitle:            deref(item.Subtitle),
		Caption:             deref(item.Caption),
		Description:         deref(item.Description),
		ArtworkURL:          deref(item.ArtworkURL),
		BannerURL:           deref(item.BannerURL),
		DurationSeconds:     item.DurationSeconds,
		ReleaseDate:         deref(item.ReleaseDate),
		Genres:              item.GetGenres(),
		ExternalIDs:         item.GetExternalIDs(),
		ExternalLinks:       extLinks,
		Creators:            e.catalogCreators(ctx, item.ID),
		Artist:              artist,
		Tracks:              tracks,
		Albums:              albums,
		ReleaseGroupCount:   len(albums),
		Bio:                 bio,
		BioSource:           bioSource,
		ShareURL:            e.FormatShareURL(slug),
		ScrobbleCount:       scrobbleCount,
		ActiveListenerCount: activeCount,
	}
}

// catalogCreators reads one catalog item's creator credits, in credit order.
func (e *Engine) catalogCreators(ctx context.Context, catalogID uuid.UUID) []model.CreatorCredit {
	var credits []model.CreatorCredit
	e.db.WithContext(ctx).
		Raw(`SELECT c.name, cc.role FROM catalog_creators cc JOIN creators c ON c.id = cc.creator_id WHERE cc.catalog_id = ? ORDER BY cc.position, cc.role`, catalogID).
		Scan(&credits)
	return credits
}

// Search searches local items and upstream providers for matching media.
func (e *Engine) Search(ctx context.Context, domain, query string, limit int) ([]UnifiedMedia, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	var localItems []model.CatalogItem
	dbQuery := e.db.WithContext(ctx).Model(&model.CatalogItem{})
	if domain != "" {
		dbQuery = dbQuery.Where("domain = ?", domain)
	}
	term := "%" + strings.ToLower(strings.TrimSpace(query)) + "%"
	dbQuery = dbQuery.Where(
		"LOWER(title) LIKE ? OR LOWER(subtitle) LIKE ? OR id IN (SELECT cc.catalog_id FROM catalog_creators cc JOIN creators c ON c.id = cc.creator_id WHERE LOWER(c.name) LIKE ?)",
		term, term, term,
	)
	_ = dbQuery.Limit(limit).Find(&localItems).Error

	results := make([]UnifiedMedia, 0, limit)
	seen := make(map[string]bool)

	for _, it := range localItems {
		m := e.itemToUnified(ctx, &it)
		results = append(results, *m)
		seen[m.URN] = true
	}

	// If fewer than limit, search upstream providers for domain. The slots are
	// divided between the providers that can answer: a first upstream must not
	// fill every slot before the second is asked, or adding a source would
	// change nothing whenever the older one is chatty.
	if len(results) < limit {
		e.mu.RLock()
		var providers []Provider
		for _, prov := range e.trackProviders {
			if domain != "" && prov.Domain() != domain {
				continue
			}
			providers = append(providers, prov)
		}
		for idx, prov := range providers {
			rem := limit - len(results)
			if rem <= 0 {
				break
			}
			// Split what is left across the providers still to be asked, so a
			// quiet provider hands its unused share to the next one.
			share := (rem + len(providers) - idx - 1) / (len(providers) - idx)
			target := len(results) + share
			if target > limit {
				target = limit
			}
			upstreamResults, err := prov.Search(ctx, query, share)
			if err == nil {
				for _, up := range upstreamResults {
					if len(results) >= target {
						break
					}
					if seen[up.URN] {
						continue
					}
					up.ShareURL = e.FormatShareURL(up.Slug)
					results = append(results, up)
					seen[up.URN] = true
				}
			}
		}
		e.mu.RUnlock()
	}

	return results, nil
}

// FormatShareURL generates the canonical public web share link for an item.
func (e *Engine) FormatShareURL(slugOrID string) string {
	base := e.siteURL
	if base == "" {
		base = "https://solsynth.dev"
	}
	return fmt.Sprintf("%s/share/m/%s", base, slugOrID)
}

// parseIdentifier breaks down various URN, slug, or prefixed forms into (domain, provider, key).
func parseIdentifier(probe string) (domain, provider, key string) {
	// 1. pulsar:<domain>:<provider>:<key>
	if strings.HasPrefix(probe, "pulsar:") {
		parts := strings.Split(probe, ":")
		if len(parts) >= 4 {
			return parts[1], parts[2], strings.Join(parts[3:], ":")
		}
	}

	// 2. provider:key (e.g. steam:730, mbid:xxx, anilist:123, imdb:tt123)
	if parts := strings.SplitN(probe, ":", 2); len(parts) == 2 {
		prov := strings.ToLower(parts[0])
		k := parts[1]
		switch prov {
		case "steam":
			return model.DomainGaming, "steam", k
		case "mbid", "musicbrainz":
			return model.DomainMusic, "musicbrainz", k
		case "anilist":
			return model.DomainAnime, "anilist", k
		case "bangumi", "bgm":
			return model.DomainAnime, "bangumi", k
		case "bangumi-book", "bgm-book":
			return model.DomainBook, "bangumi-book", k
		case "bangumi-game", "bgm-game":
			return model.DomainGaming, "bangumi-game", k
		case "bangumi-video", "bgm-video":
			return model.DomainVideo, "bangumi-video", k
		case "imdb", "tmdb":
			return model.DomainVideo, "tmdb", k
		default:
			return "", prov, k
		}
	}

	// 3. slug like steam-730, mbid-xxx, anilist-123, imdb-tt123,
	// bangumi-book-123. Longest prefixes are matched first: the generic
	// first-dash split below would read "bangumi-book" as a slug named
	// "bangumi" with the key "book-123".
	lowerProbe := strings.ToLower(probe)
	for _, prefix := range []struct {
		slug     string
		domain   string
		provider string
	}{
		{"bangumi-book-", model.DomainBook, "bangumi-book"},
		{"bangumi-game-", model.DomainGaming, "bangumi-game"},
		{"bangumi-video-", model.DomainVideo, "bangumi-video"},
	} {
		if strings.HasPrefix(lowerProbe, prefix.slug) {
			return prefix.domain, prefix.provider, probe[len(prefix.slug):]
		}
	}
	if idx := strings.Index(probe, "-"); idx > 0 {
		prefix := strings.ToLower(probe[:idx])
		k := probe[idx+1:]
		switch prefix {
		case "steam":
			return model.DomainGaming, "steam", k
		case "mbid":
			return model.DomainMusic, "musicbrainz", k
		case "anilist":
			return model.DomainAnime, "anilist", k
		case "bangumi", "bgm":
			return model.DomainAnime, "bangumi", k
		case "imdb":
			return model.DomainVideo, "tmdb", k
		}
	}

	return "", "", ""
}

func nullableString(s string) *string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
