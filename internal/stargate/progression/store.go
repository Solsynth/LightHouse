package progression

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/stargate/model"
)

// liveRows is the predicate of every uniqueness index in this module: a
// soft-deleted row does not participate, so a conflict target must restate it
// or Postgres cannot infer the partial index.
var liveRows = clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "deleted_at IS NULL"}}}

// Store is the progression persistence layer over the six module tables.
type Store struct {
	DB *gorm.DB
}

// NewStore builds the persistence layer.
func NewStore(db *gorm.DB) *Store { return &Store{DB: db} }

func mapNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

// ─────────────────────────── definitions ───────────────────────────

// ListAchievements returns every achievement definition ordered the way the
// listings expect (sort order, then title). includeDisabled matches the admin
// listing; the account listing only sees enabled definitions.
func (s *Store) ListAchievements(ctx context.Context, includeDisabled bool) ([]AchievementDefinitionEntity, error) {
	query := s.DB.WithContext(ctx).Model(&AchievementDefinitionEntity{})
	if !includeDisabled {
		query = query.Where("is_enabled = ?", true)
	}
	var entities []AchievementDefinitionEntity
	if err := query.Order("sort_order").Order("title").Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// GetAchievement resolves a definition by identifier.
func (s *Store) GetAchievement(ctx context.Context, identifier string) (*AchievementDefinitionEntity, error) {
	var entity AchievementDefinitionEntity
	if err := s.DB.WithContext(ctx).Where("identifier = ?", identifier).First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &entity, nil
}

// SaveAchievement inserts or updates an achievement definition.
func (s *Store) SaveAchievement(ctx context.Context, entity *AchievementDefinitionEntity) error {
	return s.DB.WithContext(ctx).Save(entity).Error
}

// ListQuests returns every quest definition ordered the way the listings
// expect.
func (s *Store) ListQuests(ctx context.Context, includeDisabled bool) ([]QuestDefinitionEntity, error) {
	query := s.DB.WithContext(ctx).Model(&QuestDefinitionEntity{})
	if !includeDisabled {
		query = query.Where("is_enabled = ?", true)
	}
	var entities []QuestDefinitionEntity
	if err := query.Order("sort_order").Order("title").Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// GetQuest resolves a quest definition by identifier.
func (s *Store) GetQuest(ctx context.Context, identifier string) (*QuestDefinitionEntity, error) {
	var entity QuestDefinitionEntity
	if err := s.DB.WithContext(ctx).Where("identifier = ?", identifier).First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &entity, nil
}

// SaveQuest inserts or updates a quest definition.
func (s *Store) SaveQuest(ctx context.Context, entity *QuestDefinitionEntity) error {
	return s.DB.WithContext(ctx).Save(entity).Error
}

// ToggleAchievement flips the enabled flag and reports whether the definition
// existed.
func (s *Store) ToggleAchievement(ctx context.Context, identifier string, enabled bool) (*AchievementDefinitionEntity, error) {
	entity, err := s.GetAchievement(ctx, identifier)
	if err != nil {
		return nil, err
	}
	entity.IsEnabled = enabled
	if err := s.SaveAchievement(ctx, entity); err != nil {
		return nil, err
	}
	return entity, nil
}

// ToggleQuest flips the enabled flag and reports whether the definition
// existed.
func (s *Store) ToggleQuest(ctx context.Context, identifier string, enabled bool) (*QuestDefinitionEntity, error) {
	entity, err := s.GetQuest(ctx, identifier)
	if err != nil {
		return nil, err
	}
	entity.IsEnabled = enabled
	if err := s.SaveQuest(ctx, entity); err != nil {
		return nil, err
	}
	return entity, nil
}

// ─────────────────────────── progress ───────────────────────────

// AchievementProgress loads the account's progress rows keyed by definition id.
func (s *Store) AchievementProgress(ctx context.Context, accountID uuid.UUID) (map[uuid.UUID]*AccountAchievementEntity, error) {
	var entities []AccountAchievementEntity
	if err := s.DB.WithContext(ctx).Where("account_id = ?", accountID).Find(&entities).Error; err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]*AccountAchievementEntity, len(entities))
	for i := range entities {
		out[entities[i].AchievementDefinitionID] = &entities[i]
	}
	return out, nil
}

// GetAchievementProgress resolves one progress row, or nil when absent.
func (s *Store) GetAchievementProgress(ctx context.Context, accountID, definitionID uuid.UUID) (*AccountAchievementEntity, error) {
	var entity AccountAchievementEntity
	err := s.DB.WithContext(ctx).
		Where("account_id = ? AND achievement_definition_id = ?", accountID, definitionID).
		First(&entity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &entity, nil
}

// SaveAchievementProgress upserts the progress row. The uniqueness is a
// partial index (deleted_at IS NULL), so the conflict target carries the same
// predicate Postgres needs to infer it.
func (s *Store) SaveAchievementProgress(ctx context.Context, entity *AccountAchievementEntity) error {
	return s.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:     []clause.Column{{Name: "account_id"}, {Name: "achievement_definition_id"}},
		TargetWhere: liveRows,
		DoUpdates:   clause.AssignmentColumns([]string{"progress_count", "current_streak", "best_streak", "last_progress_at", "completed_at", "claimed_at", "last_reward_token", "updated_at"}),
	}).Create(entity).Error
}

// QuestProgress loads the account's progress rows for one period, keyed by
// definition id.
func (s *Store) QuestProgress(ctx context.Context, accountID uuid.UUID, periodKeys map[uuid.UUID]string) (map[uuid.UUID]*AccountQuestProgressEntity, error) {
	out := map[uuid.UUID]*AccountQuestProgressEntity{}
	if len(periodKeys) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, 0, len(periodKeys))
	for id := range periodKeys {
		ids = append(ids, id)
	}
	var entities []AccountQuestProgressEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ? AND quest_definition_id IN ?", accountID, ids).
		Find(&entities).Error; err != nil {
		return nil, err
	}
	for i := range entities {
		if periodKeys[entities[i].QuestDefinitionID] != entities[i].PeriodKey {
			continue
		}
		out[entities[i].QuestDefinitionID] = &entities[i]
	}
	return out, nil
}

// GetQuestProgress resolves one progress row for (account, definition, period).
func (s *Store) GetQuestProgress(ctx context.Context, accountID, definitionID uuid.UUID, periodKey string) (*AccountQuestProgressEntity, error) {
	var entity AccountQuestProgressEntity
	err := s.DB.WithContext(ctx).
		Where("account_id = ? AND quest_definition_id = ? AND period_key = ?", accountID, definitionID, periodKey).
		First(&entity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &entity, nil
}

// SaveQuestProgress upserts the progress row.
func (s *Store) SaveQuestProgress(ctx context.Context, entity *AccountQuestProgressEntity) error {
	return s.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:     []clause.Column{{Name: "account_id"}, {Name: "quest_definition_id"}, {Name: "period_key"}},
		TargetWhere: liveRows,
		DoUpdates:   clause.AssignmentColumns([]string{"progress_count", "repeat_iteration_count", "completed_at", "claimed_at", "last_reward_token", "updated_at"}),
	}).Create(entity).Error
}

// ─────────────────────────── grants and receipts ───────────────────────────

// FindGrant resolves a grant by its reward token.
func (s *Store) FindGrant(ctx context.Context, token string) (*RewardGrantEntity, error) {
	var entity RewardGrantEntity
	err := s.DB.WithContext(ctx).Where("reward_token = ?", token).First(&entity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &entity, nil
}

// SaveGrant upserts a grant.
func (s *Store) SaveGrant(ctx context.Context, entity *RewardGrantEntity) error {
	return s.DB.WithContext(ctx).Save(entity).Error
}

// CreateGrant inserts a grant; a duplicate reward token is a no-op.
func (s *Store) CreateGrant(ctx context.Context, entity *RewardGrantEntity) error {
	return s.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "reward_token"}}, TargetWhere: liveRows, DoNothing: true,
	}).Create(entity).Error
}

// ListGrants returns the account's newest grants.
func (s *Store) ListGrants(ctx context.Context, accountID uuid.UUID, take int) ([]RewardGrantEntity, error) {
	var entities []RewardGrantEntity
	if err := s.DB.WithContext(ctx).Where("account_id = ?", accountID).
		Order("created_at DESC").Limit(clampTake(take, 50)).Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// ReceiptExists reports whether the event was already applied to that
// definition and period.
func (s *Store) ReceiptExists(ctx context.Context, eventID uuid.UUID, definitionType, identifier, periodKey string) (bool, error) {
	var count int64
	if err := s.DB.WithContext(ctx).Model(&EventReceiptEntity{}).
		Where("event_id = ? AND definition_type = ? AND definition_identifier = ? AND period_key = ?",
			eventID, definitionType, identifier, periodKey).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// RecordReceipt stores the receipt, tolerating a concurrent duplicate.
func (s *Store) RecordReceipt(ctx context.Context, entity *EventReceiptEntity) error {
	return s.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "event_id"}, {Name: "definition_type"},
			{Name: "definition_identifier"}, {Name: "period_key"},
		},
		TargetWhere: liveRows, DoNothing: true,
	}).Create(entity).Error
}

// ─────────────────────────── helpers ───────────────────────────

func clampTake(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	if value > 100 {
		return 100
	}
	return value
}

func encodeJSON(value any) ([]byte, error) { return json.Marshal(value) }

func decodeTrigger(raw []byte) (ProgressTrigger, error) {
	trigger := ProgressTrigger{}
	if len(raw) == 0 {
		return trigger, nil
	}
	if err := json.Unmarshal(raw, &trigger); err != nil {
		return trigger, err
	}
	return trigger, nil
}

func decodeSchedule(raw []byte) (QuestSchedule, error) {
	schedule := QuestSchedule{}
	if len(raw) == 0 {
		return schedule, nil
	}
	if err := json.Unmarshal(raw, &schedule); err != nil {
		return schedule, err
	}
	return schedule, nil
}

func decodeReward(raw []byte) (ProgressReward, error) {
	reward := ProgressReward{}
	if len(raw) == 0 {
		return reward, nil
	}
	if err := json.Unmarshal(raw, &reward); err != nil {
		return reward, err
	}
	return reward, nil
}

// timePtr converts a nullable column time.
func timePtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

// accountTimeZone reads the account's profile time zone, the zone the C#
// service resolved before computing quest periods.
func (s *Store) accountTimeZone(ctx context.Context, accountID uuid.UUID) (string, error) {
	var zone *string
	err := s.DB.WithContext(ctx).Model(&struct {
		TimeZone *string `gorm:"column:time_zone"`
	}{}).Table("account_profiles").Select("time_zone").
		Where("account_id = ?", accountID).Limit(1).Scan(&zone).Error
	if err != nil {
		return "", err
	}
	if zone == nil {
		return "", nil
	}
	return *zone, nil
}

var _ = model.Time{}

// mustTriggerJSON encodes a trigger for the Normalize defaults.
func mustTriggerJSON(trigger ProgressTrigger) datatypesJSON {
	raw, err := json.Marshal(trigger)
	if err != nil {
		return nil
	}
	return datatypesJSON(raw)
}

// mustScheduleJSON encodes a schedule for the Normalize defaults.
func mustScheduleJSON(schedule QuestSchedule) datatypesJSON {
	raw, err := json.Marshal(schedule)
	if err != nil {
		return nil
	}
	return datatypesJSON(raw)
}

// mustRewardJSON encodes a reward for the Normalize defaults.
func mustRewardJSON(reward ProgressReward) datatypesJSON {
	raw, err := json.Marshal(reward)
	if err != nil {
		return nil
	}
	return datatypesJSON(raw)
}
