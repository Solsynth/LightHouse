package store

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

// AttachCatalogCreators resolves creator credits into Creator records and
// links them to one catalog item. Names match per domain, so the same name in
// two domains stays two creators. Re-attaching the same credits is a no-op:
// the unique index on (catalog, creator, role) dedupes.
func (s *Store) AttachCatalogCreators(ctx context.Context, catalogID uuid.UUID, domain string, credits []model.CreatorCredit) error {
	db := s.DB.WithContext(ctx)
	for i, credit := range credits {
		name := strings.TrimSpace(credit.Name)
		role := strings.TrimSpace(credit.Role)
		if name == "" || role == "" {
			continue
		}

		var creator model.Creator
		if err := db.Where("domain = ? AND name = ?", domain, name).First(&creator).Error; err != nil {
			creator = model.Creator{ID: uuid.New(), Domain: domain, Name: name}
			if err := db.Create(&creator).Error; err != nil {
				return err
			}
		}

		link := model.CatalogCreator{CatalogID: catalogID, CreatorID: creator.ID, Role: role, Position: i}
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&link).Error; err != nil {
			return err
		}
	}
	return nil
}

// CreatorWithCount is a creator with how many catalog items carry them.
type CreatorWithCount struct {
	model.Creator
	ItemCount int64 `json:"item_count"`
}

// ListCreators lists creators, optionally within one domain and by name
// search, alphabetically.
func (s *Store) ListCreators(ctx context.Context, domain, q string, offset, take int) ([]CreatorWithCount, int64, error) {
	query := s.DB.WithContext(ctx).Model(&model.Creator{})
	if domain != "" {
		query = query.Where("domain = ?", domain)
	}
	if term := strings.TrimSpace(q); term != "" {
		query = query.Where("LOWER(name) LIKE ?", "%"+strings.ToLower(term)+"%")
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var creators []model.Creator
	if err := query.Order("name ASC").Offset(offset).Limit(take).Find(&creators).Error; err != nil {
		return nil, 0, err
	}

	counts := make(map[uuid.UUID]int64, len(creators))
	if len(creators) > 0 {
		ids := make([]uuid.UUID, 0, len(creators))
		for _, creator := range creators {
			ids = append(ids, creator.ID)
		}
		var rows []struct {
			CreatorID uuid.UUID
			Count     int64
		}
		if err := s.DB.WithContext(ctx).Model(&model.CatalogCreator{}).
			Select("creator_id, COUNT(*) AS count").
			Where("creator_id IN ?", ids).
			Group("creator_id").
			Scan(&rows).Error; err != nil {
			return nil, 0, err
		}
		for _, row := range rows {
			counts[row.CreatorID] = row.Count
		}
	}

	out := make([]CreatorWithCount, 0, len(creators))
	for _, creator := range creators {
		out = append(out, CreatorWithCount{Creator: creator, ItemCount: counts[creator.ID]})
	}
	return out, total, nil
}

// GetCreatorWithCount returns one creator and its catalog item count.
func (s *Store) GetCreatorWithCount(ctx context.Context, id uuid.UUID) (*CreatorWithCount, error) {
	var creator model.Creator
	if err := s.DB.WithContext(ctx).First(&creator, "id = ?", id).Error; err != nil {
		return nil, err
	}
	var count int64
	s.DB.WithContext(ctx).Model(&model.CatalogCreator{}).Where("creator_id = ?", id).Count(&count)
	return &CreatorWithCount{Creator: creator, ItemCount: count}, nil
}

// ListCatalogItemsByCreator lists the catalog items a creator worked on,
// newest first, optionally within one domain.
func (s *Store) ListCatalogItemsByCreator(ctx context.Context, creatorID uuid.UUID, domain string, offset, take int) ([]model.CatalogItem, int64, error) {
	countQuery := s.DB.WithContext(ctx).Model(&model.CatalogItem{}).
		Joins("JOIN catalog_creators cc ON cc.catalog_id = catalog_items.id").
		Where("cc.creator_id = ?", creatorID)
	if domain != "" {
		countQuery = countQuery.Where("catalog_items.domain = ?", domain)
	}
	var total int64
	if err := countQuery.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	listQuery := s.DB.WithContext(ctx).
		Joins("JOIN catalog_creators cc ON cc.catalog_id = catalog_items.id").
		Where("cc.creator_id = ?", creatorID)
	if domain != "" {
		listQuery = listQuery.Where("catalog_items.domain = ?", domain)
	}
	var items []model.CatalogItem
	if err := listQuery.Order("catalog_items.created_at DESC").Offset(offset).Limit(take).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}
