package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

// Store encapsulates database operations for Pulsar.
type Store struct {
	DB *gorm.DB
}

// New constructs a Store with the given DB connection.
func New(db *gorm.DB) *Store {
	return &Store{DB: db}
}

// ─────────────────────────── Catalog ───────────────────────────

// FindOrCreateCatalogItem looks up a catalog item by provider & provider_key, or domain + title + subtitle,
// or creates it if not found.
func (s *Store) FindOrCreateCatalogItem(ctx context.Context, item *model.CatalogItem) (*model.CatalogItem, error) {
	if item == nil {
		return nil, errors.New("nil catalog item")
	}

	db := s.DB.WithContext(ctx)

	// If provider & provider_key are supplied, probe on that unique tuple first.
	if item.Provider != "" && item.ProviderKey != "" {
		var existing model.CatalogItem
		err := db.Where("domain = ? AND provider = ? AND provider_key = ?", item.Domain, item.Provider, item.ProviderKey).
			First(&existing).Error
		if err == nil {
			return &existing, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}

	// Probe on domain + title + subtitle
	var match model.CatalogItem
	query := db.Where("domain = ? AND LOWER(title) = LOWER(?)", item.Domain, strings.TrimSpace(item.Title))
	if item.Subtitle != nil && strings.TrimSpace(*item.Subtitle) != "" {
		query = query.Where("LOWER(subtitle) = LOWER(?)", strings.TrimSpace(*item.Subtitle))
	} else {
		query = query.Where("subtitle IS NULL OR subtitle = ''")
	}

	err := query.First(&match).Error
	if err == nil {
		return &match, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// Create new entry
	if item.ID == uuid.Nil {
		item.ID = uuid.New()
	}
	now := time.Now().UTC()
	item.CreatedAt = now
	item.UpdatedAt = now

	if err := db.Create(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

// GetCatalogItem fetches a catalog item by its ID.
func (s *Store) GetCatalogItem(ctx context.Context, id uuid.UUID) (*model.CatalogItem, error) {
	var item model.CatalogItem
	if err := s.DB.WithContext(ctx).Where("id = ?", id).First(&item).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

// ListCatalogItems queries catalog entries with optional domain, provider, and search filters.
func (s *Store) ListCatalogItems(ctx context.Context, domain, provider, search string, offset, take int) ([]model.CatalogItem, int64, error) {
	db := s.DB.WithContext(ctx).Model(&model.CatalogItem{})

	if domain != "" {
		db = db.Where("domain = ?", domain)
	}
	if provider != "" {
		db = db.Where("provider = ?", provider)
	}
	if strings.TrimSpace(search) != "" {
		term := "%" + strings.ToLower(strings.TrimSpace(search)) + "%"
		db = db.Where("LOWER(title) LIKE ? OR LOWER(subtitle) LIKE ? OR LOWER(caption) LIKE ?", term, term, term)
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var items []model.CatalogItem
	if err := db.Order("updated_at DESC").Offset(offset).Limit(take).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// ─────────────────────────── Sessions (Now Playing) ───────────────────────────

// UpsertActiveSession saves or updates an active playback session.
func (s *Store) UpsertActiveSession(ctx context.Context, session *model.ActiveSession) error {
	if session == nil {
		return errors.New("nil session")
	}
	if session.ID == uuid.Nil {
		session.ID = uuid.New()
	}
	now := time.Now().UTC()
	if session.StartedAt.IsZero() {
		session.StartedAt = now
	}
	session.UpdatedAt = now
	if session.LeaseMinutes <= 0 {
		session.LeaseMinutes = 5
	}
	session.LeaseExpiresAt = now.Add(time.Duration(session.LeaseMinutes) * time.Minute)

	return s.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "account_id"}, {Name: "manual_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"catalog_id", "domain", "title", "subtitle", "caption",
			"artwork_url", "large_image", "small_image",
			"progress_seconds", "duration_seconds", "is_paused",
			"lease_minutes", "lease_expires_at", "meta", "updated_at",
		}),
	}).Create(session).Error
}

// GetActiveSession retrieves a session by account and manual_id.
func (s *Store) GetActiveSession(ctx context.Context, accountID uuid.UUID, manualID string) (*model.ActiveSession, error) {
	var session model.ActiveSession
	err := s.DB.WithContext(ctx).
		Preload("CatalogItem").
		Where("account_id = ? AND manual_id = ? AND lease_expires_at > ?", accountID, manualID, time.Now().UTC()).
		First(&session).Error
	if err != nil {
		return nil, err
	}
	return &session, nil
}

// GetActiveSessionsForAccount lists all non-expired playback sessions for an account.
func (s *Store) GetActiveSessionsForAccount(ctx context.Context, accountID uuid.UUID) ([]model.ActiveSession, error) {
	var sessions []model.ActiveSession
	err := s.DB.WithContext(ctx).
		Preload("CatalogItem").
		Where("account_id = ? AND lease_expires_at > ?", accountID, time.Now().UTC()).
		Order("updated_at DESC").
		Find(&sessions).Error
	return sessions, err
}

// EndActiveSession marks a session as ended and removes it from active list.
func (s *Store) EndActiveSession(ctx context.Context, accountID uuid.UUID, manualID string) (*model.ActiveSession, error) {
	var session model.ActiveSession
	err := s.DB.WithContext(ctx).
		Preload("CatalogItem").
		Where("account_id = ? AND manual_id = ?", accountID, manualID).
		First(&session).Error
	if err != nil {
		return nil, err
	}

	// Delete from active sessions table upon end
	if err := s.DB.WithContext(ctx).Delete(&session).Error; err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	session.EndedAt = &now
	return &session, nil
}

// ─────────────────────────── Scrobbles (History) ───────────────────────────

// CreateScrobble writes an immutable playback history record.
func (s *Store) CreateScrobble(ctx context.Context, scrobble *model.Scrobble) error {
	if scrobble == nil {
		return errors.New("nil scrobble")
	}
	if scrobble.ID == uuid.Nil {
		scrobble.ID = uuid.New()
	}
	if scrobble.PlayedAt.IsZero() {
		scrobble.PlayedAt = time.Now().UTC()
	}
	scrobble.CreatedAt = time.Now().UTC()

	return s.DB.WithContext(ctx).Create(scrobble).Error
}

// ListScrobbles returns paginated scrobble history for an account.
func (s *Store) ListScrobbles(ctx context.Context, accountID uuid.UUID, domain string, from, to *time.Time, offset, take int) ([]model.Scrobble, int64, error) {
	db := s.DB.WithContext(ctx).Model(&model.Scrobble{}).Where("account_id = ?", accountID)

	if domain != "" {
		db = db.Where("domain = ?", domain)
	}
	if from != nil && !from.IsZero() {
		db = db.Where("played_at >= ?", *from)
	}
	if to != nil && !to.IsZero() {
		db = db.Where("played_at <= ?", *to)
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var scrobbles []model.Scrobble
	if err := db.Preload("CatalogItem").Order("played_at DESC").Offset(offset).Limit(take).Find(&scrobbles).Error; err != nil {
		return nil, 0, err
	}
	return scrobbles, total, nil
}

// DeleteScrobble removes a scrobble record owned by the account.
func (s *Store) DeleteScrobble(ctx context.Context, accountID, scrobbleID uuid.UUID) error {
	res := s.DB.WithContext(ctx).Where("account_id = ? AND id = ?", accountID, scrobbleID).Delete(&model.Scrobble{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ─────────────────────────── Collections & Playlists ───────────────────────────

// CreateCollection creates a new collection.
func (s *Store) CreateCollection(ctx context.Context, coll *model.Collection) error {
	if coll == nil {
		return errors.New("nil collection")
	}
	if coll.ID == uuid.Nil {
		coll.ID = uuid.New()
	}
	now := time.Now().UTC()
	coll.CreatedAt = now
	coll.UpdatedAt = now

	return s.DB.WithContext(ctx).Create(coll).Error
}

// GetCollection returns a collection with its items.
func (s *Store) GetCollection(ctx context.Context, id uuid.UUID) (*model.Collection, error) {
	var coll model.Collection
	err := s.DB.WithContext(ctx).
		Preload("Items", func(db *gorm.DB) *gorm.DB {
			return db.Order("position ASC")
		}).
		Preload("Items.CatalogItem").
		Where("id = ?", id).
		First(&coll).Error
	if err != nil {
		return nil, err
	}
	return &coll, nil
}

// ListCollections returns collections owned by or visible to the user.
func (s *Store) ListCollections(ctx context.Context, accountID uuid.UUID, domain, kind string, offset, take int) ([]model.Collection, int64, error) {
	db := s.DB.WithContext(ctx).Model(&model.Collection{}).Where("account_id = ?", accountID)

	if domain != "" {
		db = db.Where("domain = ?", domain)
	}
	if kind != "" {
		db = db.Where("kind = ?", kind)
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var collections []model.Collection
	if err := db.Order("updated_at DESC").Offset(offset).Limit(take).Find(&collections).Error; err != nil {
		return nil, 0, err
	}
	return collections, total, nil
}

// UpdateCollection updates collection details.
func (s *Store) UpdateCollection(ctx context.Context, coll *model.Collection) error {
	coll.UpdatedAt = time.Now().UTC()
	return s.DB.WithContext(ctx).Save(coll).Error
}

// DeleteCollection deletes a collection.
func (s *Store) DeleteCollection(ctx context.Context, accountID, id uuid.UUID) error {
	res := s.DB.WithContext(ctx).Where("account_id = ? AND id = ?", accountID, id).Delete(&model.Collection{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// AddCollectionItem appends or inserts an item into a collection.
func (s *Store) AddCollectionItem(ctx context.Context, item *model.CollectionItem) error {
	if item == nil {
		return errors.New("nil collection item")
	}
	if item.ID == uuid.Nil {
		item.ID = uuid.New()
	}
	now := time.Now().UTC()
	item.CreatedAt = now
	item.UpdatedAt = now

	// If position is 0, assign next position at the end
	if item.Position <= 0 {
		var maxPos *int
		s.DB.WithContext(ctx).Model(&model.CollectionItem{}).
			Where("collection_id = ?", item.CollectionID).
			Select("MAX(position)").Row().Scan(&maxPos)
		if maxPos != nil {
			item.Position = *maxPos + 1
		} else {
			item.Position = 1
		}
	}

	return s.DB.WithContext(ctx).Create(item).Error
}

// RemoveCollectionItem removes an item from a collection.
func (s *Store) RemoveCollectionItem(ctx context.Context, collectionID, itemID uuid.UUID) error {
	res := s.DB.WithContext(ctx).Where("collection_id = ? AND id = ?", collectionID, itemID).Delete(&model.CollectionItem{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ─────────────────────────── Statistics & Rewind ───────────────────────────

// TopEntity represents an aggregated count / duration leader.
type TopEntity struct {
	Name     string `json:"name"`
	Subtitle string `json:"subtitle,omitempty"`
	Count    int64  `json:"count"`
	Seconds  int64  `json:"seconds"`
}

// MediaStats aggregates listening, gaming, or watching statistics.
type MediaStats struct {
	AccountID       uuid.UUID            `json:"account_id"`
	Domain          string               `json:"domain,omitempty"`
	TotalScrobbles  int64                `json:"total_scrobbles"`
	TotalSeconds    int64                `json:"total_seconds"`
	TopItems        []TopEntity          `json:"top_items"`
	TopCreators     []TopEntity          `json:"top_creators"`
	DomainBreakdown map[string]int64     `json:"domain_breakdown"`
	DaysActive      int64                `json:"days_active"`
	MostProductive  *TopEntity           `json:"most_active_day,omitempty"`
}

// GetMediaStats aggregates playback statistics for an account over an interval.
func (s *Store) GetMediaStats(ctx context.Context, accountID uuid.UUID, domain string, from, to *time.Time) (*MediaStats, error) {
	baseQuery := s.DB.WithContext(ctx).Model(&model.Scrobble{}).Where("account_id = ?", accountID)
	if domain != "" {
		baseQuery = baseQuery.Where("domain = ?", domain)
	}
	if from != nil && !from.IsZero() {
		baseQuery = baseQuery.Where("played_at >= ?", *from)
	}
	if to != nil && !to.IsZero() {
		baseQuery = baseQuery.Where("played_at <= ?", *to)
	}

	stats := &MediaStats{
		AccountID:       accountID,
		Domain:          domain,
		DomainBreakdown: make(map[string]int64),
	}

	// 1. Total counts & duration
	if err := baseQuery.Session(&gorm.Session{}).
		Select("count(*), coalesce(sum(listened_seconds), 0)").Row().
		Scan(&stats.TotalScrobbles, &stats.TotalSeconds); err != nil {
		return nil, err
	}

	if stats.TotalScrobbles == 0 {
		return stats, nil
	}

	// 2. Domain breakdown
	type domainRow struct {
		Domain string `gorm:"column:domain"`
		Count  int64  `gorm:"column:count"`
	}
	var dRows []domainRow
	if err := baseQuery.Session(&gorm.Session{}).
		Select("domain, count(*) as count").
		Group("domain").
		Scan(&dRows).Error; err == nil {
		for _, r := range dRows {
			stats.DomainBreakdown[r.Domain] = r.Count
		}
	}

	// 3. Top items
	type itemRow struct {
		Title    string `gorm:"column:title"`
		Subtitle string `gorm:"column:subtitle"`
		Count    int64  `gorm:"column:count"`
		Seconds  int64  `gorm:"column:seconds"`
	}
	var iRows []itemRow
	if err := baseQuery.Session(&gorm.Session{}).
		Select("title, subtitle, count(*) as count, coalesce(sum(listened_seconds), 0) as seconds").
		Group("title, subtitle").
		Order("count DESC").
		Limit(10).
		Scan(&iRows).Error; err == nil {
		for _, r := range iRows {
			stats.TopItems = append(stats.TopItems, TopEntity{
				Name:     r.Title,
				Subtitle: r.Subtitle,
				Count:    r.Count,
				Seconds:  r.Seconds,
			})
		}
	}

	// 4. Top creators (artists / developers / studios from subtitle)
	var cRows []itemRow
	if err := baseQuery.Session(&gorm.Session{}).
		Where("subtitle IS NOT NULL AND subtitle != ''").
		Select("subtitle, '' as title, count(*) as count, coalesce(sum(listened_seconds), 0) as seconds").
		Group("subtitle").
		Order("count DESC").
		Limit(10).
		Scan(&cRows).Error; err == nil {
		for _, r := range cRows {
			stats.TopCreators = append(stats.TopCreators, TopEntity{
				Name:    r.Subtitle,
				Count:   r.Count,
				Seconds: r.Seconds,
			})
		}
	}

	// 5. Active days & most active day
	dayExpr := "date_trunc('day', played_at)"
	if s.DB.Dialector != nil && s.DB.Dialector.Name() == "sqlite" {
		dayExpr = "date(played_at)"
	}

	var dCount int64
	baseQuery.Session(&gorm.Session{}).
		Select("count(distinct " + dayExpr + ")").Row().Scan(&dCount)
	stats.DaysActive = dCount

	type dayRow struct {
		Day   string `gorm:"column:day"`
		Count int64  `gorm:"column:count"`
	}
	var bestDay dayRow
	if err := baseQuery.Session(&gorm.Session{}).
		Select(dayExpr + " as day, count(*) as count").
		Group("day").
		Order("count DESC, day ASC").
		Limit(1).
		Scan(&bestDay).Error; err == nil && bestDay.Day != "" {
		stats.MostProductive = &TopEntity{
			Name:  bestDay.Day,
			Count: bestDay.Count,
		}
	}

	return stats, nil
}
