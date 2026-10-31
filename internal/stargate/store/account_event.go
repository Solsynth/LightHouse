package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/model"
)

// Account-status and presence-activity storage, moved from Passport's
// AccountEventService. Table and column names are shared with the C# fleet
// (see internal/migrate/0014_account_statuses_and_presence.sql).
//
// Soft-deleted rows are excluded by GORM's default scope (EntityBase embeds
// gorm.DeletedAt), matching the C# ModelBase query filter. Tags live in the
// presence_activity_tags / presence_catalog_tags join tables and are decoded
// into model.PresenceTag on read, exactly as C# fills SnPresenceActivity.Tags
// from ActivityTags before serializing.

// activeStatusClause is the C# "current status" rule: cleared_at is null
// (still in force) or cleared_at lies in the future (scheduled expiry).
const activeStatusClause = "(cleared_at IS NULL OR cleared_at > now())"

// ---------------------------------------------------------------------------
// Model <-> entity mapping
// ---------------------------------------------------------------------------

func statusFromEntity(entity *AccountStatusEntity) *model.AccountStatus {
	if entity == nil {
		return nil
	}
	status := &model.AccountStatus{
		Id:            entity.ID.String(),
		Attitude:      model.StatusAttitude(entity.Attitude),
		Type:          model.StatusType(entity.Type),
		Label:         entity.Label,
		Symbol:        entity.Symbol,
		ClearedAt:     timePtr(entity.ClearedAt),
		AppIdentifier: entity.AppIdentifier,
		IsAutomated:   entity.IsAutomated,
		AccountId:     entity.AccountID.String(),
		IsCustomized:  true,
		IsInvisible:   model.StatusType(entity.Type) == model.StatusTypeInvisible,
		IsNotDisturb:  model.StatusType(entity.Type) == model.StatusTypeDoNotDisturb,
		CreatedAt:     timePtr(&entity.CreatedAt),
		UpdatedAt:     timePtr(&entity.UpdatedAt),
		DeletedAt:     deletedTime(entity.DeletedAt),
	}
	status.Icon = DecodeFileReference(entity.Icon)
	status.Background = DecodeFileReference(entity.Background)
	_ = decodeJSON(entity.Meta, &status.Meta)
	return status
}

// statusToEntity maps the persisted subset of a status; the read-time presence
// fields (IsOnline, IsIdle, ...) have no columns and are dropped.
func statusToEntity(status *model.AccountStatus) (*AccountStatusEntity, error) {
	id, err := ParseUUID(status.Id)
	if err != nil {
		return nil, err
	}
	accountID, err := ParseUUID(status.AccountId)
	if err != nil {
		return nil, err
	}
	icon, err := encodeJSONPtr(status.Icon)
	if err != nil {
		return nil, err
	}
	background, err := encodeJSONPtr(status.Background)
	if err != nil {
		return nil, err
	}
	var meta *datatypes.JSON
	if status.Meta != nil {
		encoded, err := encodeJSON(status.Meta)
		if err != nil {
			return nil, err
		}
		meta = &encoded
	}
	return &AccountStatusEntity{
		ID:            id,
		AccountID:     accountID,
		AppIdentifier: status.AppIdentifier,
		Attitude:      int(status.Attitude),
		Background:    background,
		ClearedAt:     timeValue(status.ClearedAt),
		Icon:          icon,
		IsAutomated:   status.IsAutomated,
		Label:         status.Label,
		Meta:          meta,
		Symbol:        status.Symbol,
		Type:          int(status.Type),
	}, nil
}

func activityFromEntity(entity *PresenceActivityEntity, tags []model.PresenceTag) model.PresenceActivity {
	activity := model.PresenceActivity{
		Id:             entity.ID.String(),
		Type:           entity.Type,
		Tags:           tags,
		Visibility:     model.PresenceVisibility(entity.Visibility),
		Provider:       entity.Provider,
		ReferenceId:    entity.ReferenceID,
		ManualId:       entity.ManualID,
		Title:          entity.Title,
		Subtitle:       entity.Subtitle,
		Caption:        entity.Caption,
		LargeImage:     entity.LargeImage,
		SmallImage:     entity.SmallImage,
		TitleUrl:       entity.TitleURL,
		SubtitleUrl:    entity.SubtitleURL,
		QueryableTerms: decodeJSONArray(entity.QueryableTerms),
		LeaseMinutes:   entity.LeaseMinutes,
		LeaseExpiresAt: timePtr(&entity.LeaseExpiresAt),
		StartedAt:      timePtr(entity.StartedAt),
		EndedAt:        timePtr(entity.EndedAt),
		AccountId:      entity.AccountID.String(),
		CreatedAt:      timePtr(&entity.CreatedAt),
		UpdatedAt:      timePtr(&entity.UpdatedAt),
		DeletedAt:      deletedTime(entity.DeletedAt),
	}
	if activity.Tags == nil {
		activity.Tags = []model.PresenceTag{}
	}
	if entity.CatalogID != nil {
		catalogID := entity.CatalogID.String()
		activity.CatalogId = &catalogID
	}
	_ = decodeJSON(entity.Meta, &activity.Meta)
	return activity
}

func activityEntityFromModel(activity *model.PresenceActivity) (*PresenceActivityEntity, error) {
	id, err := ParseUUID(activity.Id)
	if err != nil {
		return nil, err
	}
	accountID, err := ParseUUID(activity.AccountId)
	if err != nil {
		return nil, err
	}
	var catalogID *uuid.UUID
	if activity.CatalogId != nil && *activity.CatalogId != "" {
		parsed, err := ParseUUID(*activity.CatalogId)
		if err != nil {
			return nil, err
		}
		catalogID = &parsed
	}
	var meta *datatypes.JSON
	if activity.Meta != nil {
		encoded, err := encodeJSON(activity.Meta)
		if err != nil {
			return nil, err
		}
		meta = &encoded
	}
	terms := activity.QueryableTerms
	if terms == nil {
		terms = []string{}
	}
	queryableTerms, err := encodeJSON(terms)
	if err != nil {
		return nil, err
	}
	entity := &PresenceActivityEntity{
		ID:             id,
		AccountID:      accountID,
		Caption:        activity.Caption,
		CatalogID:      catalogID,
		EndedAt:        timeValue(activity.EndedAt),
		LargeImage:     activity.LargeImage,
		LeaseMinutes:   activity.LeaseMinutes,
		ManualID:       activity.ManualId,
		Meta:           meta,
		Provider:       activity.Provider,
		QueryableTerms: queryableTerms,
		ReferenceID:    activity.ReferenceId,
		SmallImage:     activity.SmallImage,
		StartedAt:      timeValue(activity.StartedAt),
		Subtitle:       activity.Subtitle,
		SubtitleURL:    activity.SubtitleUrl,
		Title:          activity.Title,
		TitleURL:       activity.TitleUrl,
		Type:           activity.Type,
		Visibility:     int(activity.Visibility),
	}
	if activity.LeaseExpiresAt != nil {
		entity.LeaseExpiresAt = time.Time(*activity.LeaseExpiresAt)
	}
	if activity.CreatedAt != nil {
		entity.CreatedAt = time.Time(*activity.CreatedAt)
	}
	if activity.UpdatedAt != nil {
		entity.UpdatedAt = time.Time(*activity.UpdatedAt)
	}
	return entity, nil
}

func catalogFromEntity(entity *PresenceCatalogItemEntity, tags []model.PresenceTag) model.PresenceCatalogItem {
	item := model.PresenceCatalogItem{
		Id:             entity.ID.String(),
		AccountId:      entity.AccountID.String(),
		Provider:       entity.Provider,
		CatalogKey:     entity.CatalogKey,
		ReferenceId:    entity.ReferenceID,
		Name:           entity.Name,
		Subtitle:       entity.Subtitle,
		Caption:        entity.Caption,
		Visibility:     model.PresenceVisibility(entity.Visibility),
		Tags:           tags,
		LargeImage:     entity.LargeImage,
		SmallImage:     entity.SmallImage,
		TitleUrl:       entity.TitleURL,
		SubtitleUrl:    entity.SubtitleURL,
		QueryableTerms: decodeJSONArray(entity.QueryableTerms),
		Meta:           map[string]any{},
		TotalSeconds:   entity.TotalSeconds,
		SessionCount:   entity.SessionCount,
		LastActiveAt:   timePtr(entity.LastActiveAt),
		CreatedAt:      timePtr(&entity.CreatedAt),
		UpdatedAt:      timePtr(&entity.UpdatedAt),
		DeletedAt:      deletedTime(entity.DeletedAt),
	}
	if item.Tags == nil {
		item.Tags = []model.PresenceTag{}
	}
	_ = decodeJSON(&entity.Meta, &item.Meta)
	return item
}

func catalogEntityFromModel(item *model.PresenceCatalogItem) (*PresenceCatalogItemEntity, error) {
	id, err := ParseUUID(item.Id)
	if err != nil {
		return nil, err
	}
	accountID, err := ParseUUID(item.AccountId)
	if err != nil {
		return nil, err
	}
	terms := item.QueryableTerms
	if terms == nil {
		terms = []string{}
	}
	queryableTerms, err := encodeJSON(terms)
	if err != nil {
		return nil, err
	}
	meta := item.Meta
	if meta == nil {
		meta = map[string]any{}
	}
	metaJSON, err := encodeJSON(meta)
	if err != nil {
		return nil, err
	}
	entity := &PresenceCatalogItemEntity{
		ID:             id,
		AccountID:      accountID,
		Caption:        item.Caption,
		CatalogKey:     item.CatalogKey,
		LargeImage:     item.LargeImage,
		LastActiveAt:   timeValue(item.LastActiveAt),
		Meta:           metaJSON,
		Name:           item.Name,
		Provider:       item.Provider,
		QueryableTerms: queryableTerms,
		ReferenceID:    item.ReferenceId,
		SessionCount:   item.SessionCount,
		SmallImage:     item.SmallImage,
		Subtitle:       item.Subtitle,
		SubtitleURL:    item.SubtitleUrl,
		TitleURL:       item.TitleUrl,
		TotalSeconds:   item.TotalSeconds,
		Visibility:     int(item.Visibility),
	}
	if item.CreatedAt != nil {
		entity.CreatedAt = time.Time(*item.CreatedAt)
	}
	if item.UpdatedAt != nil {
		entity.UpdatedAt = time.Time(*item.UpdatedAt)
	}
	return entity, nil
}

// ---------------------------------------------------------------------------
// Tag rows
// ---------------------------------------------------------------------------

func activityTagsFor(ctx context.Context, db *gorm.DB, activityIDs []uuid.UUID) (map[uuid.UUID][]model.PresenceTag, error) {
	grouped := make(map[uuid.UUID][]model.PresenceTag, len(activityIDs))
	if len(activityIDs) == 0 {
		return grouped, nil
	}
	var entities []PresenceActivityTagEntity
	if err := db.WithContext(ctx).Where("activity_id IN ?", activityIDs).
		Order("activity_id, slug").Find(&entities).Error; err != nil {
		return nil, err
	}
	for _, entity := range entities {
		grouped[entity.ActivityID] = append(grouped[entity.ActivityID], model.PresenceTag{Slug: entity.Slug, Name: entity.Name})
	}
	return grouped, nil
}

func catalogTagsFor(ctx context.Context, db *gorm.DB, catalogIDs []uuid.UUID) (map[uuid.UUID][]model.PresenceTag, error) {
	grouped := make(map[uuid.UUID][]model.PresenceTag, len(catalogIDs))
	if len(catalogIDs) == 0 {
		return grouped, nil
	}
	var entities []PresenceCatalogTagEntity
	if err := db.WithContext(ctx).Where("catalog_id IN ?", catalogIDs).
		Order("catalog_id, slug").Find(&entities).Error; err != nil {
		return nil, err
	}
	for _, entity := range entities {
		grouped[entity.CatalogID] = append(grouped[entity.CatalogID], model.PresenceTag{Slug: entity.Slug, Name: entity.Name})
	}
	return grouped, nil
}

// normalizeTagList trims slugs, drops blank ones and de-duplicates by slug
// keeping the first name — the C# tag normalization.
func normalizeTagList(tags []model.PresenceTag) []model.PresenceTag {
	seen := make(map[string]struct{}, len(tags))
	normalized := make([]model.PresenceTag, 0, len(tags))
	for _, tag := range tags {
		slug := strings.TrimSpace(tag.Slug)
		if slug == "" {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		normalized = append(normalized, model.PresenceTag{Slug: slug, Name: tag.Name})
	}
	return normalized
}

// replaceActivityTags reconciles the (activity_id, slug) rows of one activity:
// it inserts missing slugs, refreshes renamed ones and deletes stale ones. It
// never drops-and-reinserts wholesale, so re-running cannot trip the composite
// primary key (the C# code reconciles by slug for the same reason).
func replaceActivityTags(tx *gorm.DB, activityID uuid.UUID, tags []model.PresenceTag) error {
	var existing []PresenceActivityTagEntity
	if err := tx.Where("activity_id = ?", activityID).Find(&existing).Error; err != nil {
		return err
	}
	desired := normalizeTagList(tags)
	wanted := make(map[string]*string, len(desired))
	for _, tag := range desired {
		wanted[tag.Slug] = tag.Name
	}
	present := make(map[string]struct{}, len(existing))
	for _, entity := range existing {
		present[entity.Slug] = struct{}{}
	}
	for _, tag := range desired {
		if _, ok := present[tag.Slug]; ok {
			continue
		}
		if err := tx.Create(&PresenceActivityTagEntity{ActivityID: activityID, Slug: tag.Slug, Name: tag.Name}).Error; err != nil {
			return err
		}
	}
	for _, entity := range existing {
		wantedName, keep := wanted[entity.Slug]
		if !keep {
			if err := tx.Where("activity_id = ? AND slug = ?", activityID, entity.Slug).
				Delete(&PresenceActivityTagEntity{}).Error; err != nil {
				return err
			}
			continue
		}
		if !sameOptionalString(wantedName, entity.Name) {
			if err := tx.Model(&PresenceActivityTagEntity{}).
				Where("activity_id = ? AND slug = ?", activityID, entity.Slug).
				Update("name", wantedName).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// replaceCatalogTags reconciles the (catalog_id, slug) rows of one catalog
// item, exactly like replaceActivityTags.
func replaceCatalogTags(tx *gorm.DB, catalogID uuid.UUID, tags []model.PresenceTag) error {
	var existing []PresenceCatalogTagEntity
	if err := tx.Where("catalog_id = ?", catalogID).Find(&existing).Error; err != nil {
		return err
	}
	desired := normalizeTagList(tags)
	wanted := make(map[string]*string, len(desired))
	for _, tag := range desired {
		wanted[tag.Slug] = tag.Name
	}
	present := make(map[string]struct{}, len(existing))
	for _, entity := range existing {
		present[entity.Slug] = struct{}{}
	}
	for _, tag := range desired {
		if _, ok := present[tag.Slug]; ok {
			continue
		}
		if err := tx.Create(&PresenceCatalogTagEntity{CatalogID: catalogID, Slug: tag.Slug, Name: tag.Name}).Error; err != nil {
			return err
		}
	}
	for _, entity := range existing {
		wantedName, keep := wanted[entity.Slug]
		if !keep {
			if err := tx.Where("catalog_id = ? AND slug = ?", catalogID, entity.Slug).
				Delete(&PresenceCatalogTagEntity{}).Error; err != nil {
				return err
			}
			continue
		}
		if !sameOptionalString(wantedName, entity.Name) {
			if err := tx.Model(&PresenceCatalogTagEntity{}).
				Where("catalog_id = ? AND slug = ?", catalogID, entity.Slug).
				Update("name", wantedName).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// sameOptionalString reports whether two nullable strings are equal (nil
// equals nil, and an explicit "" differs from nil).
func sameOptionalString(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// escapeLike neutralizes LIKE wildcards in user input so searching for "100%"
// does not degenerate into a table scan.
func escapeLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

// normalizeSlugs trims and de-duplicates a tag-slug filter list.
func normalizeSlugs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		slug := strings.TrimSpace(value)
		if slug == "" {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		normalized = append(normalized, slug)
	}
	return normalized
}

// ---------------------------------------------------------------------------
// Statuses
// ---------------------------------------------------------------------------

// GetCurrentStatus returns the account's newest still-active status.
func (s *Store) GetCurrentStatus(ctx context.Context, accountID uuid.UUID) (*model.AccountStatus, error) {
	var entity AccountStatusEntity
	err := s.DB.WithContext(ctx).
		Where("account_id = ? AND "+activeStatusClause, accountID).
		Order("created_at DESC").
		First(&entity).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return statusFromEntity(&entity), nil
}

// GetCurrentStatusByApp is GetCurrentStatus restricted to automated statuses
// published by one app (C# DELETE /statuses?app=…): newest active row with
// is_automated true and app_identifier matching.
func (s *Store) GetCurrentStatusByApp(ctx context.Context, accountID uuid.UUID, app string) (*model.AccountStatus, error) {
	var entity AccountStatusEntity
	err := s.DB.WithContext(ctx).
		Where("account_id = ? AND is_automated AND app_identifier = ? AND "+activeStatusClause, accountID, app).
		Order("created_at DESC").
		First(&entity).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return statusFromEntity(&entity), nil
}

// GetCurrentStatuses batches GetCurrentStatus for many accounts in one query
// (DISTINCT ON picks the newest row per account). Accounts without an active
// status are absent from the map.
func (s *Store) GetCurrentStatuses(ctx context.Context, accountIDs []uuid.UUID) (map[uuid.UUID]*model.AccountStatus, error) {
	result := make(map[uuid.UUID]*model.AccountStatus, len(accountIDs))
	if len(accountIDs) == 0 {
		return result, nil
	}
	var entities []AccountStatusEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id IN ? AND "+activeStatusClause, accountIDs).
		Order("account_id, created_at DESC").
		Find(&entities).Error; err != nil {
		return nil, err
	}
	for i := range entities {
		entity := &entities[i]
		// Rows arrive newest-first per account: keep the first seen.
		if _, ok := result[entity.AccountID]; ok {
			continue
		}
		result[entity.AccountID] = statusFromEntity(entity)
	}
	return result, nil
}

// CreateStatus replaces the account's active status inside a transaction: it
// clears every still-active row, then inserts the new one (C# CreateStatus).
func (s *Store) CreateStatus(ctx context.Context, status *model.AccountStatus) error {
	if status == nil {
		return errors.New("create status: nil status")
	}
	if status.Id == "" {
		status.Id = uuid.New().String()
	}
	entity, err := statusToEntity(status)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if entity.CreatedAt.IsZero() {
		entity.CreatedAt = now
	}
	if entity.UpdatedAt.IsZero() {
		entity.UpdatedAt = now
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&AccountStatusEntity{}).
			Where("account_id = ? AND "+activeStatusClause, entity.AccountID).
			Updates(map[string]any{"cleared_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Create(entity).Error; err != nil {
			return err
		}
		status.CreatedAt = model.NewTime(entity.CreatedAt)
		status.UpdatedAt = model.NewTime(entity.UpdatedAt)
		return nil
	})
}

// ClearStatus stamps cleared_at on one status row of the account (C#
// ClearStatus). ErrNotFound when the row does not belong to the account.
func (s *Store) ClearStatus(ctx context.Context, accountID, statusID uuid.UUID) error {
	now := time.Now().UTC()
	result := s.DB.WithContext(ctx).Model(&AccountStatusEntity{}).
		Where("id = ? AND account_id = ?", statusID, accountID).
		Updates(map[string]any{"cleared_at": now, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateStatus rewrites the persisted columns of an existing status row.
func (s *Store) UpdateStatus(ctx context.Context, status *model.AccountStatus) error {
	if status == nil {
		return errors.New("update status: nil status")
	}
	entity, err := statusToEntity(status)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	entity.UpdatedAt = now
	result := s.DB.WithContext(ctx).Model(&AccountStatusEntity{}).
		Where("id = ? AND account_id = ?", entity.ID, entity.AccountID).
		Updates(map[string]any{
			"app_identifier": entity.AppIdentifier,
			"attitude":       entity.Attitude,
			"background":     entity.Background,
			"cleared_at":     entity.ClearedAt,
			"icon":           entity.Icon,
			"is_automated":   entity.IsAutomated,
			"label":          entity.Label,
			"meta":           entity.Meta,
			"symbol":         entity.Symbol,
			"type":           entity.Type,
			"updated_at":     now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	status.UpdatedAt = model.NewTime(now)
	return nil
}

// ---------------------------------------------------------------------------
// Activities
// ---------------------------------------------------------------------------

// ActivityFilter mirrors C# GetFilteredActivities. Nil/empty string fields mean
// "no filter"; nil IsActive/Visibility/CatalogID/From/To likewise.
type ActivityFilter struct {
	Offset      int
	Take        int
	Query       *string
	Type        *string
	Provider    *string
	ReferenceID *string
	// Term matches one entry of queryable_terms (lowercased).
	Term       *string
	IsActive   *bool
	Tags       []string
	Visibility *model.PresenceVisibility
	CatalogID  *uuid.UUID
	From       *time.Time
	To         *time.Time
}

func optStringTrimmed(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func applyActivityFilter(statement *gorm.DB, accountID uuid.UUID, filter ActivityFilter, now time.Time) *gorm.DB {
	statement = statement.Where("account_id = ?", accountID)
	if query := optStringTrimmed(filter.Query); query != "" {
		pattern := "%" + escapeLike(query) + "%"
		term, _ := json.Marshal([]string{strings.ToLower(query)})
		statement = statement.Where(
			`(title LIKE ? OR subtitle LIKE ? OR caption LIKE ? OR manual_id LIKE ?
			  OR provider LIKE ? OR reference_id LIKE ? OR queryable_terms @> ?::jsonb)`,
			pattern, pattern, pattern, pattern, pattern, pattern, string(term),
		)
	}
	if value := optStringTrimmed(filter.Type); value != "" {
		statement = statement.Where("type = ?", value)
	}
	if value := optStringTrimmed(filter.Provider); value != "" {
		statement = statement.Where("provider = ?", value)
	}
	if value := optStringTrimmed(filter.ReferenceID); value != "" {
		statement = statement.Where("reference_id = ?", value)
	}
	if value := optStringTrimmed(filter.Term); value != "" {
		term, _ := json.Marshal([]string{strings.ToLower(value)})
		statement = statement.Where("queryable_terms @> ?::jsonb", string(term))
	}
	if filter.IsActive != nil {
		if *filter.IsActive {
			statement = statement.Where("lease_expires_at > ?", now)
		} else {
			statement = statement.Where("lease_expires_at <= ?", now)
		}
	}
	for _, slug := range normalizeSlugs(filter.Tags) {
		// Every requested tag must be present (C# chains one Any() per slug).
		statement = statement.Where(
			"EXISTS (SELECT 1 FROM presence_activity_tags t WHERE t.activity_id = presence_activities.id AND t.slug = ?)",
			slug,
		)
	}
	if filter.Visibility != nil {
		statement = statement.Where("visibility = ?", int(*filter.Visibility))
	}
	if filter.CatalogID != nil {
		statement = statement.Where("catalog_id = ?", *filter.CatalogID)
	}
	if filter.From != nil {
		statement = statement.Where("started_at IS NOT NULL AND started_at >= ?", *filter.From)
	}
	if filter.To != nil {
		statement = statement.Where("started_at IS NOT NULL AND started_at < ?", *filter.To)
	}
	return statement
}

// attachActivityTags decorates entities with their tag rows in one extra
// query.
func (s *Store) attachActivityTags(ctx context.Context, entities []PresenceActivityEntity) ([]model.PresenceActivity, error) {
	ids := make([]uuid.UUID, 0, len(entities))
	for i := range entities {
		ids = append(ids, entities[i].ID)
	}
	tags, err := activityTagsFor(ctx, s.DB, ids)
	if err != nil {
		return nil, err
	}
	activities := make([]model.PresenceActivity, 0, len(entities))
	for i := range entities {
		activities = append(activities, activityFromEntity(&entities[i], tags[entities[i].ID]))
	}
	return activities, nil
}

// ListActivities pages an account's activities and returns the page plus the
// total row count for the X-Total header.
func (s *Store) ListActivities(ctx context.Context, accountID uuid.UUID, filter ActivityFilter) ([]model.PresenceActivity, int, error) {
	now := time.Now().UTC()
	var total int64
	if err := applyActivityFilter(s.DB.WithContext(ctx).Model(&PresenceActivityEntity{}), accountID, filter, now).
		Count(&total).Error; err != nil {
		return nil, 0, err
	}
	statement := applyActivityFilter(s.DB.WithContext(ctx).Model(&PresenceActivityEntity{}), accountID, filter, now).
		Order("COALESCE(started_at, created_at) DESC")
	if filter.Offset > 0 {
		statement = statement.Offset(filter.Offset)
	}
	if filter.Take > 0 {
		statement = statement.Limit(filter.Take)
	}
	var entities []PresenceActivityEntity
	if err := statement.Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	activities, err := s.attachActivityTags(ctx, entities)
	if err != nil {
		return nil, 0, err
	}
	return activities, int(total), nil
}

// GetActiveActivities returns the account's live (unexpired) activities,
// newest session first, tags included.
func (s *Store) GetActiveActivities(ctx context.Context, accountID uuid.UUID) ([]model.PresenceActivity, error) {
	var entities []PresenceActivityEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ? AND lease_expires_at > ?", accountID, time.Now().UTC()).
		Order("COALESCE(started_at, created_at) DESC").
		Find(&entities).Error; err != nil {
		return nil, err
	}
	return s.attachActivityTags(ctx, entities)
}

// GetActiveActivitiesBatch batches GetActiveActivities for many accounts.
// Accounts without live activities are absent from the map.
func (s *Store) GetActiveActivitiesBatch(ctx context.Context, accountIDs []uuid.UUID) (map[uuid.UUID][]model.PresenceActivity, error) {
	result := make(map[uuid.UUID][]model.PresenceActivity, len(accountIDs))
	if len(accountIDs) == 0 {
		return result, nil
	}
	var entities []PresenceActivityEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id IN ? AND lease_expires_at > ?", accountIDs, time.Now().UTC()).
		Order("COALESCE(started_at, created_at) DESC").
		Find(&entities).Error; err != nil {
		return nil, err
	}
	tags, err := activityTagsFor(ctx, s.DB, activityIDsOf(entities))
	if err != nil {
		return nil, err
	}
	for i := range entities {
		entity := &entities[i]
		result[entity.AccountID] = append(result[entity.AccountID], activityFromEntity(entity, tags[entity.ID]))
	}
	return result, nil
}

// ListRecentPresenceActivities returns activities of the given accounts
// created at or after `since`, newest-first by update time, optionally older
// than `cursor`. It backs DyPresenceService.ListFriendsActivities, which pages
// by updated_at rather than by an offset so a cursor stays stable across
// inserts.
func (s *Store) ListRecentPresenceActivities(
	ctx context.Context,
	accountIDs []uuid.UUID,
	since time.Time,
	cursor *time.Time,
) ([]model.PresenceActivity, error) {
	if len(accountIDs) == 0 {
		return nil, nil
	}
	statement := s.DB.WithContext(ctx).
		Where("account_id IN ? AND created_at >= ?", accountIDs, since)
	if cursor != nil {
		statement = statement.Where("updated_at < ?", *cursor)
	}
	var entities []PresenceActivityEntity
	if err := statement.Order("updated_at DESC").Find(&entities).Error; err != nil {
		return nil, err
	}
	tags, err := activityTagsFor(ctx, s.DB, activityIDsOf(entities))
	if err != nil {
		return nil, err
	}
	out := make([]model.PresenceActivity, 0, len(entities))
	for i := range entities {
		entity := &entities[i]
		out = append(out, activityFromEntity(entity, tags[entity.ID]))
	}
	return out, nil
}

// activityIDsOf collects the ids of the given activity rows.
func activityIDsOf(entities []PresenceActivityEntity) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(entities))
	for i := range entities {
		ids = append(ids, entities[i].ID)
	}
	return ids
}

// InsertActivity persists a new activity with its tag rows.
func (s *Store) InsertActivity(ctx context.Context, activity *model.PresenceActivity) error {
	if activity == nil {
		return errors.New("insert activity: nil activity")
	}
	if activity.Id == "" {
		activity.Id = uuid.New().String()
	}
	entity, err := activityEntityFromModel(activity)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if entity.CreatedAt.IsZero() {
		entity.CreatedAt = now
	}
	if entity.UpdatedAt.IsZero() {
		entity.UpdatedAt = now
	}
	if entity.LeaseExpiresAt.IsZero() {
		entity.LeaseExpiresAt = now
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(entity).Error; err != nil {
			return err
		}
		if err := replaceActivityTags(tx, entity.ID, activity.Tags); err != nil {
			return err
		}
		activity.CreatedAt = model.NewTime(entity.CreatedAt)
		activity.UpdatedAt = model.NewTime(entity.UpdatedAt)
		activity.LeaseExpiresAt = model.NewTime(entity.LeaseExpiresAt)
		return nil
	})
}

// UpdateActivity rewrites an existing activity's mutable columns and reconciles
// its tag rows by slug.
func (s *Store) UpdateActivity(ctx context.Context, activity *model.PresenceActivity) error {
	if activity == nil {
		return errors.New("update activity: nil activity")
	}
	entity, err := activityEntityFromModel(activity)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&PresenceActivityEntity{}).
			Where("id = ? AND account_id = ?", entity.ID, entity.AccountID).
			Updates(map[string]any{
				"caption":          entity.Caption,
				"catalog_id":       entity.CatalogID,
				"ended_at":         entity.EndedAt,
				"large_image":      entity.LargeImage,
				"lease_expires_at": entity.LeaseExpiresAt,
				"lease_minutes":    entity.LeaseMinutes,
				"manual_id":        entity.ManualID,
				"meta":             entity.Meta,
				"provider":         entity.Provider,
				"queryable_terms":  entity.QueryableTerms,
				"reference_id":     entity.ReferenceID,
				"small_image":      entity.SmallImage,
				"started_at":       entity.StartedAt,
				"subtitle":         entity.Subtitle,
				"subtitle_url":     entity.SubtitleURL,
				"title":            entity.Title,
				"title_url":        entity.TitleURL,
				"type":             entity.Type,
				"updated_at":       now,
				"visibility":       entity.Visibility,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		if err := replaceActivityTags(tx, entity.ID, activity.Tags); err != nil {
			return err
		}
		activity.UpdatedAt = model.NewTime(now)
		return nil
	})
}

// GetActivityByID loads one activity of the account (tags included).
func (s *Store) GetActivityByID(ctx context.Context, accountID, activityID uuid.UUID) (*model.PresenceActivity, error) {
	var entity PresenceActivityEntity
	if err := s.DB.WithContext(ctx).
		Where("id = ? AND account_id = ?", activityID, accountID).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	activities, err := s.attachActivityTags(ctx, []PresenceActivityEntity{entity})
	if err != nil {
		return nil, err
	}
	if len(activities) == 0 {
		return nil, ErrNotFound
	}
	return &activities[0], nil
}

// GetActivityByManualID loads the account's still-live activity with the given
// manual id (the C# active-session lookup).
func (s *Store) GetActivityByManualID(ctx context.Context, accountID uuid.UUID, manualID string) (*model.PresenceActivity, error) {
	var entity PresenceActivityEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ? AND manual_id = ? AND lease_expires_at > ?", accountID, manualID, time.Now().UTC()).
		Order("created_at DESC").
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	activities, err := s.attachActivityTags(ctx, []PresenceActivityEntity{entity})
	if err != nil {
		return nil, err
	}
	if len(activities) == 0 {
		return nil, ErrNotFound
	}
	return &activities[0], nil
}

// GetActiveSessionByManualID loads the account's open session with this manual
// id: lease still valid, not deleted, and not yet ended (C#
// StartActivitySession/EndActivitySession lookup).
func (s *Store) GetActiveSessionByManualID(ctx context.Context, accountID uuid.UUID, manualID string) (*model.PresenceActivity, error) {
	var entity PresenceActivityEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ? AND manual_id = ? AND lease_expires_at > ? AND ended_at IS NULL",
			accountID, manualID, time.Now().UTC()).
		Order("created_at DESC").
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	activities, err := s.attachActivityTags(ctx, []PresenceActivityEntity{entity})
	if err != nil {
		return nil, err
	}
	if len(activities) == 0 {
		return nil, ErrNotFound
	}
	return &activities[0], nil
}

// DeleteActivity ends an activity the way C# DeleteActivity does: an already
// expired row is soft-deleted, while a live row merely has its lease expired so
// the session history (and the catalog statistics built from it) survives.
func (s *Store) DeleteActivity(ctx context.Context, accountID, activityID uuid.UUID) error {
	var entity PresenceActivityEntity
	if err := s.DB.WithContext(ctx).
		Where("id = ? AND account_id = ?", activityID, accountID).
		First(&entity).Error; err != nil {
		return mapNotFound(err)
	}
	now := time.Now().UTC()
	updates := map[string]any{"lease_expires_at": now, "updated_at": now}
	if !entity.LeaseExpiresAt.After(now) {
		updates["deleted_at"] = now
	}
	result := s.DB.WithContext(ctx).Unscoped().Model(&PresenceActivityEntity{}).
		Where("id = ? AND account_id = ?", activityID, accountID).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// EndActivitySession closes the account's open session for a manual id and
// accumulates it into its catalog item, mirroring C# EndActivitySession:
// ended_at = lease_expires_at = now, then total_seconds += session length,
// session_count += 1, last_active_at = now, and the session's tags are unioned
// into the catalog's tags. ErrNotFound when there is no open session.
func (s *Store) EndActivitySession(ctx context.Context, accountID uuid.UUID, manualID string) (*model.PresenceActivity, error) {
	now := time.Now().UTC()
	var ended *model.PresenceActivity
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var entity PresenceActivityEntity
		if err := tx.Where("account_id = ? AND manual_id = ? AND lease_expires_at > ? AND ended_at IS NULL",
			accountID, manualID, now).
			Order("created_at DESC").
			First(&entity).Error; err != nil {
			return mapNotFound(err)
		}
		if err := tx.Model(&PresenceActivityEntity{}).
			Where("id = ?", entity.ID).
			Updates(map[string]any{"ended_at": now, "lease_expires_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		entity.EndedAt = &now
		entity.LeaseExpiresAt = now
		entity.UpdatedAt = now

		if entity.CatalogID != nil {
			var catalog PresenceCatalogItemEntity
			err := tx.Where("id = ?", *entity.CatalogID).First(&catalog).Error
			if err != nil && !isNotFound(err) {
				return err
			}
			if err == nil {
				var duration int64
				if entity.StartedAt != nil {
					duration = int64(now.Sub(*entity.StartedAt) / time.Second)
					if duration < 0 {
						duration = 0
					}
				}
				if err := tx.Model(&PresenceCatalogItemEntity{}).
					Where("id = ?", catalog.ID).
					Updates(map[string]any{
						"total_seconds":  gorm.Expr("total_seconds + ?", duration),
						"session_count":  gorm.Expr("session_count + 1"),
						"last_active_at": now,
						"updated_at":     now,
					}).Error; err != nil {
					return err
				}
				// Union the session's tags into the catalog item.
				var sessionTags []PresenceActivityTagEntity
				if err := tx.Where("activity_id = ?", entity.ID).Find(&sessionTags).Error; err != nil {
					return err
				}
				var existingTags []PresenceCatalogTagEntity
				if err := tx.Where("catalog_id = ?", catalog.ID).Find(&existingTags).Error; err != nil {
					return err
				}
				present := make(map[string]struct{}, len(existingTags))
				for _, tag := range existingTags {
					present[tag.Slug] = struct{}{}
				}
				for _, tag := range sessionTags {
					if _, ok := present[tag.Slug]; ok {
						continue
					}
					if err := tx.Create(&PresenceCatalogTagEntity{CatalogID: catalog.ID, Slug: tag.Slug, Name: tag.Name}).Error; err != nil {
						return err
					}
				}
			}
		}

		tags, err := activityTagsFor(ctx, tx, []uuid.UUID{entity.ID})
		if err != nil {
			return err
		}
		activity := activityFromEntity(&entity, tags[entity.ID])
		ended = &activity
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ended, nil
}

// SweepExpiredActivities soft-deletes up to limit activities whose lease
// expired at or before the cutoff — the C# 90-day presence-activity retention
// job (AppDatabaseRecyclingJob). It returns the number of rows tombstoned.
func (s *Store) SweepExpiredActivities(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	now := time.Now().UTC()
	result := s.DB.WithContext(ctx).Exec(`UPDATE presence_activities
		SET deleted_at = ?, updated_at = ?
		WHERE id IN (
			SELECT id FROM presence_activities
			WHERE deleted_at IS NULL AND lease_expires_at <= ?
			ORDER BY lease_expires_at
			LIMIT ?
		)`, now, now, before, limit)
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

// ---------------------------------------------------------------------------
// Catalog
// ---------------------------------------------------------------------------

// GetCatalogItems pages an account's catalog, most recently active first.
func (s *Store) GetCatalogItems(ctx context.Context, accountID uuid.UUID, tags []string, provider, query string, isActive *bool, offset, take int) ([]model.PresenceCatalogItem, error) {
	statement := s.DB.WithContext(ctx).Model(&PresenceCatalogItemEntity{}).Where("account_id = ?", accountID)
	for _, slug := range normalizeSlugs(tags) {
		statement = statement.Where(
			"EXISTS (SELECT 1 FROM presence_catalog_tags t WHERE t.catalog_id = presence_catalog_items.id AND t.slug = ?)",
			slug,
		)
	}
	if value := strings.TrimSpace(provider); value != "" {
		statement = statement.Where("provider = ?", value)
	}
	if value := strings.TrimSpace(query); value != "" {
		pattern := "%" + escapeLike(value) + "%"
		term, _ := json.Marshal([]string{strings.ToLower(value)})
		statement = statement.Where("(name LIKE ? OR queryable_terms @> ?::jsonb)", pattern, string(term))
	}
	if isActive != nil {
		// "Active" means used in the last 24 hours (C# GetCatalogItems).
		cutoff := time.Now().UTC().Add(-24 * time.Hour)
		if *isActive {
			statement = statement.Where("last_active_at IS NOT NULL AND last_active_at > ?", cutoff)
		} else {
			statement = statement.Where("(last_active_at IS NULL OR last_active_at <= ?)", cutoff)
		}
	}
	statement = statement.Order("COALESCE(last_active_at, updated_at) DESC")
	if offset > 0 {
		statement = statement.Offset(offset)
	}
	if take > 0 {
		statement = statement.Limit(take)
	}
	var entities []PresenceCatalogItemEntity
	if err := statement.Find(&entities).Error; err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(entities))
	for i := range entities {
		ids = append(ids, entities[i].ID)
	}
	tagRows, err := catalogTagsFor(ctx, s.DB, ids)
	if err != nil {
		return nil, err
	}
	items := make([]model.PresenceCatalogItem, 0, len(entities))
	for i := range entities {
		items = append(items, catalogFromEntity(&entities[i], tagRows[entities[i].ID]))
	}
	return items, nil
}

// GetCatalogItemByID loads one catalog item of the account.
func (s *Store) GetCatalogItemByID(ctx context.Context, accountID uuid.UUID, catalogID string) (*model.PresenceCatalogItem, error) {
	id, err := ParseUUID(catalogID)
	if err != nil {
		return nil, err
	}
	var entity PresenceCatalogItemEntity
	if err := s.DB.WithContext(ctx).
		Where("id = ? AND account_id = ?", id, accountID).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	tags, err := catalogTagsFor(ctx, s.DB, []uuid.UUID{entity.ID})
	if err != nil {
		return nil, err
	}
	item := catalogFromEntity(&entity, tags[entity.ID])
	return &item, nil
}

// GetCatalogItemByKey loads the account's catalog entry for a provider and
// catalog key — the C# StartActivitySession lookup, which only creates the
// catalog when it is missing and otherwise leaves the stored row untouched.
// ErrNotFound when no such row exists.
func (s *Store) GetCatalogItemByKey(ctx context.Context, accountID uuid.UUID, provider *string, catalogKey string) (*model.PresenceCatalogItem, error) {
	var entity PresenceCatalogItemEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ? AND provider IS NOT DISTINCT FROM ? AND catalog_key = ?", accountID, provider, catalogKey).
		Order("created_at DESC").
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	tags, err := catalogTagsFor(ctx, s.DB, []uuid.UUID{entity.ID})
	if err != nil {
		return nil, err
	}
	item := catalogFromEntity(&entity, tags[entity.ID])
	return &item, nil
}

// GetCatalogStats sums ended session durations per catalog id (seconds,
// floored at zero), optionally windowed on ended_at and filtered by the
// catalog item's tags.
func (s *Store) GetCatalogStats(ctx context.Context, accountID uuid.UUID, from, to *time.Time, tags []string) (map[string]int64, error) {
	statement := s.DB.WithContext(ctx).Table("presence_activities").
		Select(`catalog_id::text AS key,
			COALESCE(SUM(GREATEST(FLOOR(EXTRACT(EPOCH FROM (ended_at - started_at)))::bigint, 0)), 0) AS total`).
		Where("account_id = ? AND deleted_at IS NULL AND ended_at IS NOT NULL AND started_at IS NOT NULL AND catalog_id IS NOT NULL", accountID)
	if from != nil {
		statement = statement.Where("ended_at >= ?", *from)
	}
	if to != nil {
		statement = statement.Where("ended_at < ?", *to)
	}
	for _, slug := range normalizeSlugs(tags) {
		statement = statement.Where(
			"EXISTS (SELECT 1 FROM presence_catalog_tags t WHERE t.catalog_id = presence_activities.catalog_id AND t.slug = ?)",
			slug,
		)
	}
	var rows []struct {
		Key   string
		Total int64
	}
	if err := statement.Group("catalog_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	stats := make(map[string]int64, len(rows))
	for _, row := range rows {
		stats[row.Key] = row.Total
	}
	return stats, nil
}

// GetTagStats sums ended session durations per tag slug (seconds, floored at
// zero).
func (s *Store) GetTagStats(ctx context.Context, accountID uuid.UUID, from, to *time.Time) (map[string]int64, error) {
	statement := s.DB.WithContext(ctx).Table("presence_activities AS a").
		Select(`t.slug AS key,
			COALESCE(SUM(GREATEST(FLOOR(EXTRACT(EPOCH FROM (a.ended_at - a.started_at)))::bigint, 0)), 0) AS total`).
		Joins("JOIN presence_activity_tags t ON t.activity_id = a.id").
		Where("a.account_id = ? AND a.deleted_at IS NULL AND a.ended_at IS NOT NULL AND a.started_at IS NOT NULL", accountID)
	if from != nil {
		statement = statement.Where("a.ended_at >= ?", *from)
	}
	if to != nil {
		statement = statement.Where("a.ended_at < ?", *to)
	}
	var rows []struct {
		Key   string
		Total int64
	}
	if err := statement.Group("t.slug").Scan(&rows).Error; err != nil {
		return nil, err
	}
	stats := make(map[string]int64, len(rows))
	for _, row := range rows {
		stats[row.Key] = row.Total
	}
	return stats, nil
}

// UpsertCatalogItem returns the account's catalog entry for (account_id,
// provider, catalog_key), creating it from item when absent. An existing row is
// returned unchanged — C# StartActivitySession only inserts a missing catalog
// item and then merely attaches its id, leaving accumulated statistics and
// tags untouched. catalog_key is required; item.Id/CreatedAt/UpdatedAt are
// filled in on insert.
func (s *Store) UpsertCatalogItem(ctx context.Context, item *model.PresenceCatalogItem) (*model.PresenceCatalogItem, error) {
	if item == nil {
		return nil, errors.New("upsert catalog item: nil item")
	}
	if item.CatalogKey == nil || strings.TrimSpace(*item.CatalogKey) == "" {
		return nil, errors.New("upsert catalog item: catalog_key is required")
	}
	accountID, err := ParseUUID(item.AccountId)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	created := false
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing PresenceCatalogItemEntity
		err := tx.Where("account_id = ? AND provider IS NOT DISTINCT FROM ? AND catalog_key = ?",
			accountID, item.Provider, *item.CatalogKey).
			Order("created_at DESC").
			First(&existing).Error
		if err != nil && !isNotFound(err) {
			return err
		}
		if err == nil {
			item.Id = existing.ID.String()
			item.CreatedAt = timePtr(&existing.CreatedAt)
			item.UpdatedAt = timePtr(&existing.UpdatedAt)
			item.TotalSeconds = existing.TotalSeconds
			item.SessionCount = existing.SessionCount
			item.LastActiveAt = timePtr(existing.LastActiveAt)
			return nil
		}

		if item.Id == "" {
			item.Id = uuid.New().String()
		}
		entity, err := catalogEntityFromModel(item)
		if err != nil {
			return err
		}
		entity.CreatedAt = now
		entity.UpdatedAt = now
		if err := tx.Create(entity).Error; err != nil {
			return err
		}
		if err := replaceCatalogTags(tx, entity.ID, item.Tags); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if created {
		item.CreatedAt = model.NewTime(now)
		item.UpdatedAt = model.NewTime(now)
	}
	if item.Tags == nil {
		item.Tags = []model.PresenceTag{}
	}
	if item.Meta == nil {
		item.Meta = map[string]any{}
	}
	if item.QueryableTerms == nil {
		item.QueryableTerms = []string{}
	}
	return item, nil
}
