package accountevents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/filejson"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/store"
)

// ErrNotFound marks a missing notable day, calendar event, subscription or
// check-in row.
var ErrNotFound = errors.New("not found")

// Store is the persistence layer over the four account-event tables.
type Store struct {
	DB *gorm.DB
}

// NewStore wraps a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{DB: db} }

func mapNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

func jsonBytes(value any) datatypes.JSON {
	raw, err := json.Marshal(value)
	if err != nil {
		return datatypes.JSON("null")
	}
	return datatypes.JSON(raw)
}

func optionalJSON(raw datatypes.JSON) datatypes.JSON {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return raw
}

// ───────────────────────────── check-ins ─────────────────────────────

func checkInFromEntity(entity *CheckInResultEntity) *CheckInResult {
	result := &CheckInResult{
		Id:               entity.ID.String(),
		Level:            CheckInLevel(entity.Level),
		RewardPoints:     entity.RewardPoints,
		RewardExperience: entity.RewardExperience,
		Tips:             []FortuneTip{},
		AccountId:        entity.AccountID.String(),
		BackdatedFrom:    modelTimePtr(entity.BackdatedFrom),
		CreatedAt:        model.NewTime(entity.CreatedAt),
		UpdatedAt:        model.NewTime(entity.UpdatedAt),
	}
	filejson.Decode(entity.Tips, &result.Tips)
	if result.Tips == nil {
		result.Tips = []FortuneTip{}
	}
	if len(entity.FortuneReport) > 0 && string(entity.FortuneReport) != "null" {
		report := &FortuneReport{}
		filejson.Decode(entity.FortuneReport, report)
		result.FortuneReport = report
	}
	if entity.DeletedAt.Valid {
		result.DeletedAt = model.NewTime(entity.DeletedAt.Time)
	}
	return result
}

func checkInToEntity(result *CheckInResult) (*CheckInResultEntity, error) {
	accountID, err := uuid.Parse(result.AccountId)
	if err != nil {
		return nil, err
	}
	entity := &CheckInResultEntity{
		ID:               uuid.New(),
		Level:            int(result.Level),
		RewardPoints:     result.RewardPoints,
		RewardExperience: result.RewardExperience,
		Tips:             jsonBytes(result.Tips),
		AccountID:        accountID,
	}
	if result.Id != "" {
		if parsed, err := uuid.Parse(result.Id); err == nil {
			entity.ID = parsed
		}
	}
	if result.FortuneReport != nil {
		entity.FortuneReport = jsonBytes(result.FortuneReport)
	}
	if result.BackdatedFrom != nil {
		entity.BackdatedFrom = timePtr(result.BackdatedFrom)
	}
	if result.CreatedAt != nil {
		entity.CreatedAt = result.CreatedAt.Time()
	}
	if result.UpdatedAt != nil {
		entity.UpdatedAt = result.UpdatedAt.Time()
	}
	return entity, nil
}

func timePtr(value *model.Time) *time.Time {
	if value == nil {
		return nil
	}
	t := value.Time()
	return &t
}

// modelTimePtr converts an optional time.Time into an optional model.Time.
func modelTimePtr(value *time.Time) *model.Time {
	if value == nil {
		return nil
	}
	return model.NewTime(*value)
}

// InsertCheckIn persists a new check-in row. CreatedAt is the backdated
// instant when the caller supplied one, matching C# CheckInDaily.
func (s *Store) InsertCheckIn(ctx context.Context, result *CheckInResult) error {
	now := time.Now().UTC()
	entity, err := checkInToEntity(result)
	if err != nil {
		return err
	}

	if entity.CreatedAt.IsZero() {
		entity.CreatedAt = now
	}
	entity.UpdatedAt = now
	if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
		return err
	}
	*result = *checkInFromEntity(entity)
	return nil
}

// UpdateCheckIn rewrites the fortune columns of an existing row.
func (s *Store) UpdateCheckIn(ctx context.Context, result *CheckInResult) error {
	id, err := uuid.Parse(result.Id)
	if err != nil {
		return err
	}
	updates := map[string]any{
		"tips":       jsonBytes(result.Tips),
		"updated_at": time.Now().UTC(),
	}
	if result.FortuneReport != nil {
		updates["fortune_report"] = jsonBytes(result.FortuneReport)
	}
	return s.DB.WithContext(ctx).Model(&CheckInResultEntity{}).
		Where("id = ?", id).Updates(updates).Error
}

// GetCheckInBetween returns the newest check-in of the account created inside
// the [from, to) window.
func (s *Store) GetCheckInBetween(ctx context.Context, accountID uuid.UUID, from, to time.Time) (*CheckInResult, error) {
	var entity CheckInResultEntity
	err := s.DB.WithContext(ctx).
		Where("account_id = ? AND created_at >= ? AND created_at < ?", accountID, from, to).
		Order("created_at DESC").First(&entity).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return checkInFromEntity(&entity), nil
}

// GetLatestCheckIn returns the account's most recent check-in.
func (s *Store) GetLatestCheckIn(ctx context.Context, accountID uuid.UUID) (*CheckInResult, error) {
	var entity CheckInResultEntity
	err := s.DB.WithContext(ctx).Where("account_id = ?", accountID).
		Order("created_at DESC").First(&entity).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return checkInFromEntity(&entity), nil
}

// ListCheckInsBetween returns every check-in created inside [from, to),
// oldest first (the calendar groups them by UTC date).
func (s *Store) ListCheckInsBetween(ctx context.Context, accountID uuid.UUID, from, to time.Time) ([]CheckInResult, error) {
	var entities []CheckInResultEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ? AND created_at >= ? AND created_at < ?", accountID, from, to).
		Order("created_at").Find(&entities).Error; err != nil {
		return nil, err
	}
	results := make([]CheckInResult, 0, len(entities))
	for i := range entities {
		results = append(results, *checkInFromEntity(&entities[i]))
	}
	return results, nil
}

// CountBackdatedBetween counts backdated check-ins created inside [from, to).
func (s *Store) CountBackdatedBetween(ctx context.Context, accountID uuid.UUID, from, to time.Time) (int, error) {
	var count int64
	err := s.DB.WithContext(ctx).Model(&CheckInResultEntity{}).
		Where("account_id = ? AND created_at >= ? AND created_at < ? AND backdated_from IS NOT NULL",
			accountID, from, to).
		Count(&count).Error
	return int(count), err
}

// ListCheckInDatesBetween returns the distinct UTC dates of the account's
// check-ins inside [from, to).
func (s *Store) ListCheckInDatesBetween(ctx context.Context, accountID uuid.UUID, from, to time.Time) (map[string]struct{}, error) {
	var createdAt []time.Time
	if err := s.DB.WithContext(ctx).Model(&CheckInResultEntity{}).
		Where("account_id = ? AND created_at >= ? AND created_at < ?", accountID, from, to).
		Pluck("created_at", &createdAt).Error; err != nil {
		return nil, err
	}
	dates := make(map[string]struct{}, len(createdAt))
	for _, instant := range createdAt {
		dates[instant.UTC().Format("2006-01-02")] = struct{}{}
	}
	return dates, nil
}

// ───────────────────────────── calendar ─────────────────────────────

func userEventFromEntity(entity *UserCalendarEventEntity) *UserCalendarEvent {
	event := &UserCalendarEvent{
		Id:          entity.ID.String(),
		Title:       entity.Title,
		Description: entity.Description,
		Location:    entity.Location,
		StartTime:   model.Time(entity.StartTime.UTC()),
		EndTime:     model.Time(entity.EndTime.UTC()),
		IsAllDay:    entity.IsAllDay,
		Visibility:  EventVisibility(entity.Visibility),
		Tags:        []string{},
		AccountId:   entity.AccountID.String(),
		CreatedAt:   model.NewTime(entity.CreatedAt),
		UpdatedAt:   model.NewTime(entity.UpdatedAt),
	}
	event.Recurrence = decodeRecurrence(entity.Recurrence)
	filejson.Decode(entity.Tags, &event.Tags)
	if event.Tags == nil {
		event.Tags = []string{}
	}
	if len(entity.Meta) > 0 {
		filejson.Decode(entity.Meta, &event.Meta)
	}
	event.Icon = store.DecodeFileReference(&entity.Icon)
	event.Background = store.DecodeFileReference(&entity.Background)
	if entity.DeletedAt.Valid {
		event.DeletedAt = model.NewTime(entity.DeletedAt.Time)
	}
	return event
}

func decodeRecurrence(raw datatypes.JSON) *RecurrencePattern {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	pattern := &RecurrencePattern{}
	if err := json.Unmarshal(raw, pattern); err != nil {
		return nil
	}
	if pattern.Interval <= 0 {
		pattern.Interval = 1
	}
	return pattern
}

func userEventToEntity(event *UserCalendarEvent) (*UserCalendarEventEntity, error) {
	accountID, err := uuid.Parse(event.AccountId)
	if err != nil {
		return nil, err
	}
	entity := &UserCalendarEventEntity{
		ID:          uuid.New(),
		Title:       event.Title,
		Description: event.Description,
		Location:    event.Location,
		StartTime:   event.StartTime.Time(),
		EndTime:     event.EndTime.Time(),
		IsAllDay:    event.IsAllDay,
		Visibility:  int(event.Visibility),
		Tags:        jsonBytes(event.Tags),
		AccountID:   accountID,
	}
	if event.Id != "" {
		if parsed, err := uuid.Parse(event.Id); err == nil {
			entity.ID = parsed
		}
	}
	if event.Recurrence != nil {
		entity.Recurrence = jsonBytes(event.Recurrence)
	}
	if event.Meta != nil {
		entity.Meta = jsonBytes(event.Meta)
	}
	if event.Icon != nil {
		entity.Icon = jsonBytes(event.Icon)
	}
	if event.Background != nil {
		entity.Background = jsonBytes(event.Background)
	}
	return entity, nil
}

// CalendarEventFilter is the coarse SQL filter applied before recurrence
// expansion (mirrors BuildAccessibleCalendarEventsQueryAsync).
type CalendarEventFilter struct {
	AccountID *uuid.UUID
	Query     string
	Tags      []string
	StartTime *time.Time
	EndTime   *time.Time
}

// ListAccessibleEvents returns the events the viewer may see: their own, their
// friends' Public/Friends events and their subscribed accounts' Public events.
func (s *Store) ListAccessibleEvents(ctx context.Context, viewerID uuid.UUID, friendIDs, subscribedIDs []uuid.UUID, filter CalendarEventFilter) ([]UserCalendarEvent, error) {
	query := s.DB.WithContext(ctx).Model(&UserCalendarEventEntity{}).
		Where("deleted_at IS NULL")
	visible := "account_id = ?"
	args := []any{viewerID}
	if len(friendIDs) > 0 {
		visible += " OR (account_id IN ? AND visibility IN ?)"
		args = append(args, friendIDs, []int{int(VisibilityPublic), int(VisibilityFriends)})
	}
	if len(subscribedIDs) > 0 {
		visible += " OR (account_id IN ? AND visibility = ?)"
		args = append(args, subscribedIDs, int(VisibilityPublic))
	}
	query = query.Where("("+visible+")", args...)
	return s.listEvents(ctx, query, filter)
}

// ListAccountEvents returns one account's events, applying the visibility rule
// for an anonymous viewer, a friend viewer or the owner (GetUserCalendarEvents).
func (s *Store) ListAccountEvents(ctx context.Context, accountID uuid.UUID, viewerID *uuid.UUID, filter CalendarEventFilter) ([]UserCalendarEvent, error) {
	query := s.DB.WithContext(ctx).Model(&UserCalendarEventEntity{}).
		Where("deleted_at IS NULL AND account_id = ?", accountID)
	isOwner := viewerID != nil && *viewerID == accountID
	if !isOwner {
		if viewerID != nil {
			query = query.Where("visibility IN ?", []int{int(VisibilityPublic), int(VisibilityFriends)})
		} else {
			query = query.Where("visibility = ?", int(VisibilityPublic))
		}
	}
	return s.listEvents(ctx, query, filter)
}

func (s *Store) listEvents(ctx context.Context, query *gorm.DB, filter CalendarEventFilter) ([]UserCalendarEvent, error) {
	if filter.AccountID != nil {
		query = query.Where("account_id = ?", *filter.AccountID)
	}
	if trimmed := strings.TrimSpace(filter.Query); trimmed != "" {
		like := "%" + escapeLike(trimmed) + "%"
		query = query.Where(
			"(title LIKE ? OR description LIKE ? OR location LIKE ? OR tags @> ?::jsonb)",
			like, like, like, string(jsonBytes([]string{strings.ToLower(trimmed)})),
		)
	}
	for _, tag := range filter.Tags {
		query = query.Where("tags @> ?::jsonb", string(jsonBytes([]string{tag})))
	}
	if filter.StartTime != nil {
		query = query.Where("(recurrence IS NOT NULL OR end_time >= ?)", *filter.StartTime)
	}
	if filter.EndTime != nil {
		query = query.Where("(recurrence IS NOT NULL OR start_time <= ?)", *filter.EndTime)
	}
	var entities []UserCalendarEventEntity
	if err := query.Order("start_time").Find(&entities).Error; err != nil {
		return nil, err
	}
	events := make([]UserCalendarEvent, 0, len(entities))
	for i := range entities {
		events = append(events, *userEventFromEntity(&entities[i]))
	}
	return events, nil
}

// ListEventsForAccounts returns the events of several accounts limited to the
// given visibilities (the owner calendar merges friends and subscriptions).
func (s *Store) ListEventsForAccounts(ctx context.Context, accountIDs []uuid.UUID, visibilities []EventVisibility, filter CalendarEventFilter) ([]UserCalendarEvent, error) {
	if len(accountIDs) == 0 {
		return []UserCalendarEvent{}, nil
	}
	values := make([]int, 0, len(visibilities))
	for _, visibility := range visibilities {
		values = append(values, int(visibility))
	}
	query := s.DB.WithContext(ctx).Model(&UserCalendarEventEntity{}).
		Where("deleted_at IS NULL AND account_id IN ? AND visibility IN ?", accountIDs, values)
	return s.listEvents(ctx, query, filter)
}

// GetCalendarEvent loads one non-deleted event.
func (s *Store) GetCalendarEvent(ctx context.Context, eventID uuid.UUID) (*UserCalendarEvent, error) {
	var entity UserCalendarEventEntity
	if err := s.DB.WithContext(ctx).Where("id = ? AND deleted_at IS NULL", eventID).First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return userEventFromEntity(&entity), nil
}

// CreateCalendarEvent inserts an event.
func (s *Store) CreateCalendarEvent(ctx context.Context, event *UserCalendarEvent) error {
	entity, err := userEventToEntity(event)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	entity.CreatedAt = now
	entity.UpdatedAt = now
	if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
		return err
	}
	*event = *userEventFromEntity(entity)
	return nil
}

// UpdateCalendarEvent rewrites the mutable columns of an event.
func (s *Store) UpdateCalendarEvent(ctx context.Context, event *UserCalendarEvent) error {
	entity, err := userEventToEntity(event)
	if err != nil {
		return err
	}
	updates := map[string]any{
		"title":       entity.Title,
		"description": entity.Description,
		"location":    entity.Location,
		"start_time":  entity.StartTime,
		"end_time":    entity.EndTime,
		"is_all_day":  entity.IsAllDay,
		"visibility":  entity.Visibility,
		"recurrence":  optionalJSON(entity.Recurrence),
		"tags":        entity.Tags,
		"meta":        optionalJSON(entity.Meta),
		"icon":        optionalJSON(entity.Icon),
		"background":  optionalJSON(entity.Background),
		"updated_at":  time.Now().UTC(),
	}
	result := s.DB.WithContext(ctx).Model(&UserCalendarEventEntity{}).
		Where("id = ? AND deleted_at IS NULL", entity.ID).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteCalendarEvent soft-deletes an event.
func (s *Store) DeleteCalendarEvent(ctx context.Context, eventID uuid.UUID) error {
	result := s.DB.WithContext(ctx).Where("id = ? AND deleted_at IS NULL", eventID).
		Delete(&UserCalendarEventEntity{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// CalendarTags returns the de-duplicated, lower-cased, sorted union of the
// account's event tags.
func (s *Store) CalendarTags(ctx context.Context, accountID uuid.UUID) ([]string, error) {
	var rows []datatypes.JSON
	if err := s.DB.WithContext(ctx).Model(&UserCalendarEventEntity{}).
		Where("account_id = ? AND deleted_at IS NULL", accountID).
		Pluck("tags", &rows).Error; err != nil {
		return nil, err
	}
	all := make([]string, 0)
	for _, row := range rows {
		var tags []string
		filejson.Decode(row, &tags)
		all = append(all, tags...)
	}
	return normalizeCalendarTags(all), nil
}

// ───────────────────────── subscriptions ─────────────────────────

func subscriptionFromEntity(entity *CalendarEventSubscriptionEntity) *CalendarEventSubscription {
	subscription := &CalendarEventSubscription{
		Id:              entity.ID.String(),
		SubscriberId:    entity.SubscriberID.String(),
		TargetAccountId: entity.TargetAccountID.String(),
		Notify:          entity.Notify,
		CreatedAt:       model.NewTime(entity.CreatedAt),
		UpdatedAt:       model.NewTime(entity.UpdatedAt),
	}
	if entity.DeletedAt.Valid {
		subscription.DeletedAt = model.NewTime(entity.DeletedAt.Time)
	}
	return subscription
}

// GetSubscription returns the live subscription row for a subscriber/target.
func (s *Store) GetSubscription(ctx context.Context, subscriberID, targetID uuid.UUID) (*CalendarEventSubscription, error) {
	var entity CalendarEventSubscriptionEntity
	err := s.DB.WithContext(ctx).
		Where("subscriber_id = ? AND target_account_id = ? AND deleted_at IS NULL", subscriberID, targetID).
		First(&entity).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return subscriptionFromEntity(&entity), nil
}

// CreateSubscription inserts a subscription.
func (s *Store) CreateSubscription(ctx context.Context, subscriberID, targetID uuid.UUID) (*CalendarEventSubscription, error) {
	now := time.Now().UTC()
	entity := &CalendarEventSubscriptionEntity{
		ID:              uuid.New(),
		SubscriberID:    subscriberID,
		TargetAccountID: targetID,
		Notify:          true,
		EntityBase:      EntityBase{CreatedAt: now, UpdatedAt: now},
	}
	if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
		return nil, err
	}
	return subscriptionFromEntity(entity), nil
}

// DeleteSubscription soft-deletes a subscription row.
func (s *Store) DeleteSubscription(ctx context.Context, subscriberID, targetID uuid.UUID) error {
	result := s.DB.WithContext(ctx).
		Where("subscriber_id = ? AND target_account_id = ? AND deleted_at IS NULL", subscriberID, targetID).
		Delete(&CalendarEventSubscriptionEntity{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ListSubscriptions returns the accounts a subscriber follows.
func (s *Store) ListSubscriptions(ctx context.Context, subscriberID uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := s.DB.WithContext(ctx).Model(&CalendarEventSubscriptionEntity{}).
		Where("subscriber_id = ? AND deleted_at IS NULL", subscriberID).
		Pluck("target_account_id", &ids).Error
	return ids, err
}

// ListSubscribers returns the accounts subscribed to a target account.
func (s *Store) ListSubscribers(ctx context.Context, targetID uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := s.DB.WithContext(ctx).Model(&CalendarEventSubscriptionEntity{}).
		Where("target_account_id = ? AND deleted_at IS NULL", targetID).
		Pluck("subscriber_id", &ids).Error
	return ids, err
}

// ───────────────────────── notable days ─────────────────────────

func storedNotableDayFromEntity(entity *NotableDayEntity) *StoredNotableDay {
	day := &StoredNotableDay{
		Id:                entity.ID.String(),
		Name:              entity.Name,
		Description:       entity.Description,
		LocalName:         entity.LocalName,
		LocalizableKey:    entity.LocalizableKey,
		StartDate:         model.Time(entity.StartDate.UTC()),
		EndDate:           model.Time(entity.EndDate.UTC()),
		IsAllDay:          entity.IsAllDay,
		Region:            entity.Region,
		Tags:              []NotableDayTag{},
		IsRecurring:       entity.IsRecurring,
		RecurrencePattern: entity.RecurrencePattern,
		IsPeriod:          entity.IsPeriod,
		HolidayDays:       []string{},
		DisplayOrder:      entity.DisplayOrder,
		CreatedAt:         model.NewTime(entity.CreatedAt),
		UpdatedAt:         model.NewTime(entity.UpdatedAt),
	}
	filejson.Decode(entity.Tags, &day.Tags)
	if day.Tags == nil {
		day.Tags = []NotableDayTag{}
	}
	if len(entity.Meta) > 0 {
		filejson.Decode(entity.Meta, &day.Meta)
	}
	filejson.Decode(entity.HolidayDays, &day.HolidayDays)
	if day.HolidayDays == nil {
		day.HolidayDays = []string{}
	}
	if entity.DeletedAt.Valid {
		day.DeletedAt = model.NewTime(entity.DeletedAt.Time)
	}
	return day
}

// StoredNotableDayFromEntity maps a raw row to its wire shape.
func StoredNotableDayFromEntity(entity *NotableDayEntity) *StoredNotableDay {
	return storedNotableDayFromEntity(entity)
}

func notableDayEntityFromStored(day *StoredNotableDay) (*NotableDayEntity, error) {
	id := uuid.Nil
	if strings.TrimSpace(day.Id) != "" {
		parsed, err := uuid.Parse(day.Id)
		if err != nil {
			return nil, err
		}
		id = parsed
	}
	entity := &NotableDayEntity{
		ID:                id,
		Name:              day.Name,
		Description:       day.Description,
		LocalName:         day.LocalName,
		LocalizableKey:    day.LocalizableKey,
		StartDate:         day.StartDate.Time(),
		EndDate:           day.EndDate.Time(),
		IsAllDay:          day.IsAllDay,
		Region:            day.Region,
		Tags:              jsonBytes(day.Tags),
		IsRecurring:       day.IsRecurring,
		RecurrencePattern: day.RecurrencePattern,
		IsPeriod:          day.IsPeriod,
		HolidayDays:       jsonBytes(day.HolidayDays),
		DisplayOrder:      day.DisplayOrder,
	}
	if day.Meta != nil {
		entity.Meta = jsonBytes(day.Meta)
	}
	return entity, nil
}

// ListNotableDayEntities is the raw `notable_days` listing used by
// /api/notable-days and by the seed service.
func (s *Store) ListNotableDayEntities(ctx context.Context, region string, recurringOnly bool) ([]NotableDayEntity, error) {
	query := s.DB.WithContext(ctx).Model(&NotableDayEntity{}).
		Where("deleted_at IS NULL AND lower(region) = ?", strings.ToLower(region))
	if recurringOnly {
		query = query.Where("is_recurring")
	}
	var entities []NotableDayEntity
	if err := query.Order("display_order NULLS LAST").Order("start_date").Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// ListNotableDaysForYear is the generated-calendar query: recurring rows keep
// their anchor dates, only non-recurring rows are constrained to the year.
func (s *Store) ListNotableDaysForYear(ctx context.Context, year int, region string, tag *NotableDayTag) ([]NotableDayEntity, error) {
	startOfYear := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	endOfYear := time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC)
	query := s.DB.WithContext(ctx).Model(&NotableDayEntity{}).
		Where("deleted_at IS NULL AND lower(region) = ?", strings.ToLower(region)).
		Where("(is_recurring OR (start_date < ? AND end_date >= ?))", endOfYear, startOfYear)
	if tag != nil {
		query = query.Where("tags @> ?::jsonb", string(jsonBytes([]int{int(*tag)})))
	}
	var entities []NotableDayEntity
	if err := query.Order("display_order NULLS LAST").Order("start_date").Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// GetNotableDayEntity loads one notable day row.
func (s *Store) GetNotableDayEntity(ctx context.Context, id uuid.UUID) (*NotableDayEntity, error) {
	var entity NotableDayEntity
	if err := s.DB.WithContext(ctx).Where("id = ? AND deleted_at IS NULL", id).First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &entity, nil
}

// CreateNotableDay inserts a notable day row.
func (s *Store) CreateNotableDay(ctx context.Context, day *StoredNotableDay) error {
	entity, err := notableDayEntityFromStored(day)
	if err != nil {
		return err
	}
	if entity.ID == uuid.Nil {
		entity.ID = uuid.New()
	}
	now := time.Now().UTC()
	entity.CreatedAt = now
	entity.UpdatedAt = now
	if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
		return err
	}
	*day = *storedNotableDayFromEntity(entity)
	return nil
}

// UpdateNotableDay rewrites an existing notable day row.
func (s *Store) UpdateNotableDay(ctx context.Context, day *StoredNotableDay) error {
	entity, err := notableDayEntityFromStored(day)
	if err != nil {
		return err
	}
	result := s.DB.WithContext(ctx).Model(&NotableDayEntity{}).
		Where("id = ? AND deleted_at IS NULL", entity.ID).
		Updates(map[string]any{
			"name":               entity.Name,
			"description":        entity.Description,
			"local_name":         entity.LocalName,
			"localizable_key":    entity.LocalizableKey,
			"start_date":         entity.StartDate,
			"end_date":           entity.EndDate,
			"is_all_day":         entity.IsAllDay,
			"region":             entity.Region,
			"tags":               entity.Tags,
			"meta":               optionalJSON(entity.Meta),
			"is_recurring":       entity.IsRecurring,
			"recurrence_pattern": entity.RecurrencePattern,
			"is_period":          entity.IsPeriod,
			"holiday_days":       optionalJSON(entity.HolidayDays),
			"display_order":      entity.DisplayOrder,
			"updated_at":         time.Now().UTC(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteNotableDay soft-deletes a notable day row.
func (s *Store) DeleteNotableDay(ctx context.Context, id uuid.UUID) error {
	result := s.DB.WithContext(ctx).Where("id = ? AND deleted_at IS NULL", id).
		Delete(&NotableDayEntity{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
