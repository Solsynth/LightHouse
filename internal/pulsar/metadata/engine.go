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
)

// Engine orchestrates metadata resolution, caching, and upstream querying.
type Engine struct {
	db        *gorm.DB
	siteURL   string
	providers map[string]Provider
	mu        sync.RWMutex
}

// NewEngine constructs a metadata resolution engine.
func NewEngine(db *gorm.DB, siteURL string) *Engine {
	return &Engine{
		db:        db,
		siteURL:   strings.TrimRight(siteURL, "/"),
		providers: make(map[string]Provider),
	}
}

// RegisterProvider attaches an upstream metadata provider.
func (e *Engine) RegisterProvider(p Provider) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.providers[strings.ToLower(p.Name())] = p
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

	// 1. Try local database lookup first
	item, err := e.lookupLocal(ctx, identifier)
	if err == nil && item != nil {
		return e.itemToUnified(ctx, item), nil
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
		ExternalIDs:     datatypes.JSON(extIDsBytes),
		ExternalLinks:   datatypes.JSON(extLinksBytes),
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}

	err := e.db.WithContext(ctx).Create(item).Error
	if err != nil {
		return nil, err
	}
	return item, nil
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
		ShareURL:            e.FormatShareURL(slug),
		ScrobbleCount:       scrobbleCount,
		ActiveListenerCount: activeCount,
	}
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
	dbQuery = dbQuery.Where("LOWER(title) LIKE ? OR LOWER(subtitle) LIKE ?", term, term)
	_ = dbQuery.Limit(limit).Find(&localItems).Error

	results := make([]UnifiedMedia, 0, limit)
	seen := make(map[string]bool)

	for _, it := range localItems {
		m := e.itemToUnified(ctx, &it)
		results = append(results, *m)
		seen[m.URN] = true
	}

	// If fewer than limit, search upstream providers for domain
	if len(results) < limit {
		e.mu.RLock()
		for _, prov := range e.providers {
			if domain != "" && prov.Domain() != domain {
				continue
			}
			rem := limit - len(results)
			if rem <= 0 {
				break
			}
			upstreamResults, err := prov.Search(ctx, query, rem)
			if err == nil {
				for _, up := range upstreamResults {
					if !seen[up.URN] {
						up.ShareURL = e.FormatShareURL(up.Slug)
						results = append(results, up)
						seen[up.URN] = true
					}
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
		case "imdb", "tmdb":
			return model.DomainVideo, "tmdb", k
		default:
			return "", prov, k
		}
	}

	// 3. slug like steam-730, mbid-xxx, anilist-123, imdb-tt123
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
