package progression

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// SeedService upserts the catalog on boot and from POST /api/admin/progression/sync.
// It mirrors ProgressionSeedService: a missing definition is inserted, and an
// existing one is overwritten only when it is seed-managed, so an operator's
// hand-edited definition is never clobbered.
type SeedService struct {
	Store *Store
	Log   *slog.Logger
}

// NewSeedService builds the seed service.
func NewSeedService(store *Store, log *slog.Logger) *SeedService {
	if log == nil {
		log = slog.Default()
	}
	return &SeedService{Store: store, Log: log}
}

// EnsureSeeded upserts both catalogs.
func (s *SeedService) EnsureSeeded(ctx context.Context) error {
	if err := s.syncAchievements(ctx, Achievements); err != nil {
		return err
	}
	return s.syncQuests(ctx, Quests)
}

func (s *SeedService) syncAchievements(ctx context.Context, definitions []SeedDefinition) error {
	changed := 0
	for _, raw := range definitions {
		definition := raw.Normalized()
		existing, err := s.Store.GetAchievement(ctx, definition.Identifier)
		if err != nil && err != ErrNotFound {
			return err
		}
		if existing == nil {
			entity, err := achievementFromSeed(definition)
			if err != nil {
				return err
			}
			if err := s.Store.SaveAchievement(ctx, entity); err != nil {
				return err
			}
			changed++
			continue
		}
		if !existing.IsSeedManaged {
			continue
		}
		applyAchievementSeed(existing, definition)
		if err := s.Store.SaveAchievement(ctx, existing); err != nil {
			return err
		}
		changed++
	}
	if changed > 0 {
		s.Log.Info("synchronized achievement definitions from the catalog", "count", changed)
	}
	return nil
}

func (s *SeedService) syncQuests(ctx context.Context, definitions []SeedDefinition) error {
	changed := 0
	for _, raw := range definitions {
		definition := raw.Normalized()
		existing, err := s.Store.GetQuest(ctx, definition.Identifier)
		if err != nil && err != ErrNotFound {
			return err
		}
		if existing == nil {
			entity, err := questFromSeed(definition)
			if err != nil {
				return err
			}
			if err := s.Store.SaveQuest(ctx, entity); err != nil {
				return err
			}
			changed++
			continue
		}
		if !existing.IsSeedManaged {
			continue
		}
		applyQuestSeed(existing, definition)
		if err := s.Store.SaveQuest(ctx, existing); err != nil {
			return err
		}
		changed++
	}
	if changed > 0 {
		s.Log.Info("synchronized quest definitions from the catalog", "count", changed)
	}
	return nil
}

// ─────────────────────────── seed <-> entity ───────────────────────────

func achievementFromSeed(definition SeedDefinition) (*AchievementDefinitionEntity, error) {
	entity := &AchievementDefinitionEntity{ID: uuid.New()}
	if err := applyAchievementSeed(entity, definition); err != nil {
		return nil, err
	}
	return entity, nil
}

func applyAchievementSeed(entity *AchievementDefinitionEntity, definition SeedDefinition) error {
	trigger, err := json.Marshal(definition.Trigger)
	if err != nil {
		return err
	}
	reward, err := json.Marshal(definition.Reward)
	if err != nil {
		return err
	}
	entity.Identifier = definition.Identifier
	entity.Title = definition.Title
	entity.Summary = definition.Summary
	entity.Icon = stringPtrOrNil(definition.Icon)
	entity.SeriesIdentifier = stringPtrOrNil(definition.SeriesIdentifier)
	entity.SeriesTitle = stringPtrOrNil(definition.SeriesTitle)
	entity.SeriesOrder = definition.SeriesOrder
	entity.SortOrder = definition.SortOrder
	entity.Hidden = definition.Hidden
	entity.IsEnabled = definition.IsEnabled
	entity.IsSeedManaged = definition.IsSeedManaged
	entity.IsProgressEnabled = definition.IsProgressEnabled
	entity.AvailableFrom = definition.AvailableFrom
	entity.AvailableUntil = definition.AvailableUntil
	entity.TargetCount = definition.TargetCount
	entity.Trigger = datatypes.JSON(trigger)
	entity.Reward = datatypes.JSON(reward)
	return nil
}

func questFromSeed(definition SeedDefinition) (*QuestDefinitionEntity, error) {
	entity := &QuestDefinitionEntity{ID: uuid.New()}
	if err := applyQuestSeed(entity, definition); err != nil {
		return nil, err
	}
	return entity, nil
}

func applyQuestSeed(entity *QuestDefinitionEntity, definition SeedDefinition) error {
	trigger, err := json.Marshal(definition.Trigger)
	if err != nil {
		return err
	}
	schedule := definition.Schedule
	if schedule.Repeatability == "" {
		schedule.Repeatability = RepeatNone
	}
	scheduleJSON, err := json.Marshal(schedule)
	if err != nil {
		return err
	}
	reward, err := json.Marshal(definition.Reward)
	if err != nil {
		return err
	}
	entity.Identifier = definition.Identifier
	entity.Title = definition.Title
	entity.Summary = definition.Summary
	entity.Icon = stringPtrOrNil(definition.Icon)
	entity.SeriesIdentifier = stringPtrOrNil(definition.SeriesIdentifier)
	entity.SeriesTitle = stringPtrOrNil(definition.SeriesTitle)
	entity.SeriesOrder = definition.SeriesOrder
	entity.SortOrder = definition.SortOrder
	entity.Hidden = definition.Hidden
	entity.IsEnabled = definition.IsEnabled
	entity.IsSeedManaged = definition.IsSeedManaged
	entity.IsProgressEnabled = definition.IsProgressEnabled
	entity.AvailableFrom = definition.AvailableFrom
	entity.AvailableUntil = definition.AvailableUntil
	entity.TargetCount = definition.TargetCount
	entity.Trigger = datatypes.JSON(trigger)
	entity.Schedule = datatypes.JSON(scheduleJSON)
	entity.Reward = datatypes.JSON(reward)
	return nil
}

// UpsertRequest is ProgressionDefinitionUpsertRequest / QuestDefinitionUpsertRequest.
type UpsertRequest struct {
	Identifier        string          `json:"identifier"`
	Title             string          `json:"title"`
	Summary           string          `json:"summary"`
	Icon              *string         `json:"icon"`
	SeriesIdentifier  *string         `json:"series_identifier"`
	SeriesTitle       *string         `json:"series_title"`
	SeriesOrder       *int            `json:"series_order"`
	SortOrder         int             `json:"sort_order"`
	Hidden            bool            `json:"hidden"`
	IsEnabled         bool            `json:"is_enabled"`
	IsSeedManaged     bool            `json:"is_seed_managed"`
	IsProgressEnabled bool            `json:"is_progress_enabled"`
	AvailableFrom     *time.Time      `json:"available_from"`
	AvailableUntil    *time.Time      `json:"available_until"`
	TargetCount       int             `json:"target_count"`
	Trigger           ProgressTrigger `json:"trigger"`
	Schedule          QuestSchedule   `json:"schedule"`
	Reward            ProgressReward  `json:"reward"`
}

// ApplyToAchievement writes the request onto an achievement definition, the way
// ProgressionAdminController.ApplyAchievement did.
func (r UpsertRequest) ApplyToAchievement(entity *AchievementDefinitionEntity) error {
	trigger, err := json.Marshal(r.Trigger)
	if err != nil {
		return err
	}
	reward, err := json.Marshal(r.Reward)
	if err != nil {
		return err
	}
	entity.Identifier = r.Identifier
	entity.Title = r.Title
	entity.Summary = r.Summary
	entity.Icon = r.Icon
	entity.SeriesIdentifier = r.SeriesIdentifier
	entity.SeriesTitle = r.SeriesTitle
	entity.SeriesOrder = r.SeriesOrder
	entity.SortOrder = r.SortOrder
	entity.Hidden = r.Hidden
	entity.IsEnabled = r.IsEnabled
	entity.IsSeedManaged = r.IsSeedManaged
	entity.IsProgressEnabled = r.IsProgressEnabled
	entity.AvailableFrom = r.AvailableFrom
	entity.AvailableUntil = r.AvailableUntil
	entity.TargetCount = r.TargetCount
	entity.Trigger = datatypes.JSON(trigger)
	entity.Reward = datatypes.JSON(reward)
	return nil
}

// ApplyToQuest writes the request onto a quest definition.
func (r UpsertRequest) ApplyToQuest(entity *QuestDefinitionEntity) error {
	trigger, err := json.Marshal(r.Trigger)
	if err != nil {
		return err
	}
	schedule, err := json.Marshal(r.Schedule)
	if err != nil {
		return err
	}
	reward, err := json.Marshal(r.Reward)
	if err != nil {
		return err
	}
	entity.Identifier = r.Identifier
	entity.Title = r.Title
	entity.Summary = r.Summary
	entity.Icon = r.Icon
	entity.SeriesIdentifier = r.SeriesIdentifier
	entity.SeriesTitle = r.SeriesTitle
	entity.SeriesOrder = r.SeriesOrder
	entity.SortOrder = r.SortOrder
	entity.Hidden = r.Hidden
	entity.IsEnabled = r.IsEnabled
	entity.IsSeedManaged = r.IsSeedManaged
	entity.IsProgressEnabled = r.IsProgressEnabled
	entity.AvailableFrom = r.AvailableFrom
	entity.AvailableUntil = r.AvailableUntil
	entity.TargetCount = r.TargetCount
	entity.Trigger = datatypes.JSON(trigger)
	entity.Schedule = datatypes.JSON(schedule)
	entity.Reward = datatypes.JSON(reward)
	return nil
}

func stringPtrOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
