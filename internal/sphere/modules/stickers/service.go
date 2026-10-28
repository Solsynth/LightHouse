package stickers

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

const (
	lookupCachePrefix = "sticker:lookup:"
	lookupCacheTTL    = 15 * time.Minute
	maxStickersPerPack = 24
)

var alphanumeric = regexp.MustCompile(`^[a-zA-Z0-9]+$`)

func isAlphanumeric(value string) bool { return alphanumeric.MatchString(value) }

type service struct {
	core       *core.Services
	db         *gorm.DB
	ports      *module.PortRegistry
	popularity *popularityBuffer
}

func newService(c *core.Services, registry *module.PortRegistry) *service {
	return &service{
		core:       c,
		db:         c.DB,
		ports:      registry,
		popularity: newPopularityBuffer(),
	}
}

// ---- popularity buffer ----------------------------------------------------

// popularityBuffer keeps sticker-pack lookups in memory and flushes them as
// one UPDATE per pack every minute (job StickerPopularityFlush).
type popularityBuffer struct {
	mu     sync.Mutex
	counts map[uuid.UUID]int
}

func newPopularityBuffer() *popularityBuffer {
	return &popularityBuffer{counts: map[uuid.UUID]int{}}
}

func (b *popularityBuffer) enqueue(packID uuid.UUID) {
	if packID == uuid.Nil {
		return
	}
	b.mu.Lock()
	b.counts[packID]++
	b.mu.Unlock()
}

func (b *popularityBuffer) flush(ctx context.Context, db *gorm.DB) error {
	b.mu.Lock()
	pending := b.counts
	b.counts = map[uuid.UUID]int{}
	b.mu.Unlock()

	if len(pending) == 0 {
		return nil
	}
	var firstErr error
	for packID, count := range pending {
		if count == 0 {
			continue
		}
		err := db.WithContext(ctx).
			Exec(`UPDATE sticker_packs SET popularity = popularity + ? WHERE id = ? AND deleted_at IS NULL`, count, packID).
			Error
		if err != nil {
			firstErr = err
			b.mu.Lock()
			b.counts[packID] += count
			b.mu.Unlock()
		}
	}
	return firstErr
}

// ---- publisher membership -------------------------------------------------

func (s *service) memberRole(ctx context.Context, publisherID, accountID uuid.UUID) (int, bool) {
	var member store.PublisherMemberEntity
	err := s.db.WithContext(ctx).
		Where("publisher_id = ? AND account_id = ?", publisherID, accountID).
		First(&member).Error
	if err != nil {
		return 0, false
	}
	return member.Role, true
}

func (s *service) hasRole(ctx context.Context, publisherID, accountID uuid.UUID, required int) bool {
	role, ok := s.memberRole(ctx, publisherID, accountID)
	return ok && role >= required
}

// ---- loading --------------------------------------------------------------

func (s *service) loadPack(ctx context.Context, id uuid.UUID) (*store.StickerPackEntity, error) {
	var pack store.StickerPackEntity
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&pack).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return &pack, nil
}

func (s *service) publisherModel(ctx context.Context, id uuid.UUID) *model.Publisher {
	var entity store.PublisherEntity
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&entity).Error; err != nil {
		return nil
	}
	publisher := store.PublisherToModel(&entity)
	return &publisher
}

func (s *service) resolveFile(ctx context.Context, id string) (*model.CloudFileReferenceObject, error) {
	files, ok := module.Get[ports.Files](s.ports, ports.PortFiles)
	if !ok || files == nil {
		return nil, nil
	}
	return files.GetFile(ctx, id)
}

// stickerWire builds the SnSticker wire shape. The pack is attached when
// loaded, matching the C# Includes on each route.
func stickerWire(entity *store.StickerEntity, pack *store.StickerPackEntity, publisher *model.Publisher) model.Sticker {
	sticker := store.StickerToModel(entity)
	if pack != nil {
		packWire := store.StickerPackToModel(pack)
		packWire.Publisher = publisher
		sticker.Pack = &packWire
	}
	return sticker
}

func packWire(entity *store.StickerPackEntity, stickers []store.StickerEntity, publisher *model.Publisher) model.StickerPack {
	pack := store.StickerPackToModel(entity)
	pack.Publisher = publisher
	_ = stickers // stickers travel in packResponse.Stickers
	return pack
}

func ownershipWire(entity *store.StickerPackOwnershipEntity, pack *store.StickerPackEntity, stickers []store.StickerEntity, publisher *model.Publisher) model.StickerPackOwnership {
	ownership := model.StickerPackOwnership{
		ID:        entity.ID.String(),
		PackID:    entity.PackID.String(),
		AccountID: entity.AccountID.String(),
		Order:     entity.Order,
		CreatedAt: model.NewTime(entity.CreatedAt),
		UpdatedAt: model.NewTime(entity.UpdatedAt),
		DeletedAt: store.DeletedPtr(entity.DeletedAt),
	}
	if pack != nil {
		packModel := packWire(pack, stickers, publisher)
		ownership.Pack = &packModel
	}
	return ownership
}

func (s *service) stickersForPacks(ctx context.Context, packIDs []uuid.UUID) (map[uuid.UUID][]store.StickerEntity, error) {
	result := map[uuid.UUID][]store.StickerEntity{}
	if len(packIDs) == 0 {
		return result, nil
	}
	var stickers []store.StickerEntity
	if err := s.db.WithContext(ctx).
		Where("pack_id IN ?", packIDs).
		Order(`"order" ASC, created_at ASC`).
		Find(&stickers).Error; err != nil {
		return nil, err
	}
	for _, sticker := range stickers {
		result[sticker.PackID] = append(result[sticker.PackID], sticker)
	}
	return result, nil
}

// ---- lookup ---------------------------------------------------------------

// cachedLookup is the cache entry for one identifier: the sticker plus the pack
// snapshot the lookup query attached.
type cachedLookup struct {
	Sticker model.Sticker     `json:"sticker"`
	Pack    model.StickerPack `json:"pack"`
}

// lookup resolves an identifier exactly like StickerService:
//   - identifiers are lowercased and trimmed;
//   - "prefix+slug" matches pack prefix and sticker slug case-insensitively;
//   - a UUID matches the sticker id;
//   - anything else matches "prefix || slug" case-insensitively.
func (s *service) lookup(ctx context.Context, identifier string, trackPopularity bool) (*cachedLookup, error) {
	normalized := strings.ToLower(strings.TrimSpace(identifier))
	cacheKey := lookupCachePrefix + normalized

	var cached cachedLookup
	if s.cacheGet(ctx, cacheKey, &cached) {
		if trackPopularity {
			if packID, err := uuid.Parse(cached.Pack.ID); err == nil {
				s.popularity.enqueue(packID)
			}
		}
		return &cached, nil
	}

	query := s.db.WithContext(ctx).
		Model(&store.StickerEntity{}).
		Select("stickers.*").
		Joins("JOIN sticker_packs ON sticker_packs.id = stickers.pack_id AND sticker_packs.deleted_at IS NULL")

	parts := strings.Split(normalized, "+")
	switch {
	case len(parts) >= 2:
		query = query.Where("sticker_packs.prefix ILIKE ?", parts[0]).
			Where("stickers.slug ILIKE ?", parts[1])
	default:
		if id, err := uuid.Parse(normalized); err == nil {
			query = query.Where("stickers.id = ?", id)
		} else {
			query = query.Where("(sticker_packs.prefix || stickers.slug) ILIKE ?", normalized)
		}
	}

	var entity store.StickerEntity
	err := query.Take(&entity).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}

	var pack store.StickerPackEntity
	if err := s.db.WithContext(ctx).Where("id = ?", entity.PackID).First(&pack).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}

	result := cachedLookup{
		Sticker: store.StickerToModel(&entity),
		Pack:    store.StickerPackToModel(&pack),
	}
	result.Sticker.Pack = nil
	s.cacheSet(ctx, cacheKey, result, lookupCacheTTL)

	if trackPopularity {
		s.popularity.enqueue(entity.PackID)
	}
	return &result, nil
}

func (s *service) purgeStickers(ctx context.Context, stickers []store.StickerEntity, packs map[uuid.UUID]store.StickerPackEntity) {
	for _, sticker := range stickers {
		prefix := ""
		if pack, ok := packs[sticker.PackID]; ok {
			prefix = pack.Prefix
		}
		s.purgeSticker(ctx, sticker, prefix)
	}
}

func (s *service) purgeSticker(ctx context.Context, sticker store.StickerEntity, prefix string) {
	s.cacheRemove(ctx,
		lookupCachePrefix+sticker.ID.String(),
		lookupCachePrefix+prefix+sticker.Slug,
		lookupCachePrefix+prefix+"+"+sticker.Slug,
	)
}

// ---- cache ----------------------------------------------------------------

func (s *service) cacheGet(ctx context.Context, key string, dest any) bool {
	if s.core.Redis == nil {
		return false
	}
	found, err := s.core.Redis.Get(ctx, key, dest)
	return err == nil && found
}

func (s *service) cacheSet(ctx context.Context, key string, value any, ttl time.Duration) {
	if s.core.Redis == nil {
		return
	}
	_ = s.core.Redis.Set(ctx, key, value, ttl)
}

func (s *service) cacheRemove(ctx context.Context, keys ...string) {
	if s.core.Redis == nil {
		return
	}
	for _, key := range keys {
		_ = s.core.Redis.Remove(ctx, key)
	}
}
