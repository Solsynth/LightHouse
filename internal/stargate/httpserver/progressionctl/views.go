package progressionctl

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/datatypes"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/progression"
)

// Wire views. The admin endpoints return the definition entity the way the C#
// controller returned its EF model: column names for scalars, decoded objects
// for the jsonb trigger/schedule/reward.

type triggerView struct {
	Actions    []string          `json:"actions"`
	MetaEquals map[string]string `json:"meta_equals,omitempty"`
	Mode       string            `json:"mode"`
}

type scheduleView struct {
	Repeatability    string `json:"repeatability"`
	ActiveDaysOfWeek []int  `json:"active_days_of_week,omitempty"`
}

type badgeRewardView struct {
	Type    string         `json:"type"`
	Label   *string        `json:"label,omitempty"`
	Caption *string        `json:"caption,omitempty"`
	Meta    map[string]any `json:"meta,omitempty"`
}

type rewardView struct {
	Experience           int64            `json:"experience"`
	SourcePoints         int64            `json:"source_points"`
	SourcePointsCurrency string           `json:"source_points_currency,omitempty"`
	Badge                *badgeRewardView `json:"badge,omitempty"`
}

// definitionView is one achievement or quest definition row.
type definitionView struct {
	Id                uuid.UUID     `json:"id"`
	Identifier        string        `json:"identifier"`
	Title             string        `json:"title"`
	Summary           string        `json:"summary"`
	Icon              *string       `json:"icon,omitempty"`
	SeriesIdentifier  *string       `json:"series_identifier,omitempty"`
	SeriesTitle       *string       `json:"series_title,omitempty"`
	SeriesOrder       *int          `json:"series_order,omitempty"`
	SortOrder         int           `json:"sort_order"`
	Hidden            bool          `json:"hidden"`
	IsEnabled         bool          `json:"is_enabled"`
	IsSeedManaged     bool          `json:"is_seed_managed"`
	IsProgressEnabled bool          `json:"is_progress_enabled"`
	AvailableFrom     *TimeJSON     `json:"available_from,omitempty"`
	AvailableUntil    *TimeJSON     `json:"available_until,omitempty"`
	TargetCount       int           `json:"target_count"`
	Trigger           triggerView   `json:"trigger"`
	Schedule          *scheduleView `json:"schedule,omitempty"`
	Reward            rewardView    `json:"reward"`
	CreatedAt         *TimeJSON     `json:"created_at,omitempty"`
	UpdatedAt         *TimeJSON     `json:"updated_at,omitempty"`
	DeletedAt         *TimeJSON     `json:"deleted_at,omitempty"`
}

// TimeJSON is a nullable RFC3339 instant.
type TimeJSON = progression.TimeValue

func achievementView(entity *progression.AchievementDefinitionEntity) definitionView {
	if entity == nil {
		return definitionView{}
	}
	view := definitionView{
		Id: entity.ID, Identifier: entity.Identifier, Title: entity.Title, Summary: entity.Summary,
		Icon: entity.Icon, SeriesIdentifier: entity.SeriesIdentifier, SeriesTitle: entity.SeriesTitle,
		SeriesOrder: entity.SeriesOrder, SortOrder: entity.SortOrder, Hidden: entity.Hidden,
		IsEnabled: entity.IsEnabled, IsSeedManaged: entity.IsSeedManaged,
		IsProgressEnabled: entity.IsProgressEnabled,
		AvailableFrom:     progression.TimeOf(entity.AvailableFrom),
		AvailableUntil:    progression.TimeOf(entity.AvailableUntil),
		TargetCount:       entity.TargetCount,
		Trigger:           decodeTriggerView(entity.Trigger),
		Reward:            decodeRewardView(entity.Reward),
		CreatedAt:         progression.TimeOf(&entity.CreatedAt),
		UpdatedAt:         progression.TimeOf(&entity.UpdatedAt),
	}
	if entity.DeletedAt.Valid {
		deleted := entity.DeletedAt.Time
		view.DeletedAt = progression.TimeOf(&deleted)
	}
	return view
}

func questView(entity *progression.QuestDefinitionEntity) definitionView {
	if entity == nil {
		return definitionView{}
	}
	schedule := decodeScheduleView(entity.Schedule)
	view := definitionView{
		Id: entity.ID, Identifier: entity.Identifier, Title: entity.Title, Summary: entity.Summary,
		Icon: entity.Icon, SeriesIdentifier: entity.SeriesIdentifier, SeriesTitle: entity.SeriesTitle,
		SeriesOrder: entity.SeriesOrder, SortOrder: entity.SortOrder, Hidden: entity.Hidden,
		IsEnabled: entity.IsEnabled, IsSeedManaged: entity.IsSeedManaged,
		IsProgressEnabled: entity.IsProgressEnabled,
		AvailableFrom:     progression.TimeOf(entity.AvailableFrom),
		AvailableUntil:    progression.TimeOf(entity.AvailableUntil),
		TargetCount:       entity.TargetCount,
		Trigger:           decodeTriggerView(entity.Trigger),
		Schedule:          &schedule,
		Reward:            decodeRewardView(entity.Reward),
		CreatedAt:         progression.TimeOf(&entity.CreatedAt),
		UpdatedAt:         progression.TimeOf(&entity.UpdatedAt),
	}
	if entity.DeletedAt.Valid {
		deleted := entity.DeletedAt.Time
		view.DeletedAt = progression.TimeOf(&deleted)
	}
	return view
}

// grantView is one reward grant, with the decoded reward object.
type grantView struct {
	Id                    uuid.UUID  `json:"id"`
	AccountId             uuid.UUID  `json:"account_id"`
	DefinitionType        string     `json:"definition_type"`
	DefinitionIdentifier  string     `json:"definition_identifier"`
	DefinitionTitle       string     `json:"definition_title"`
	RewardToken           string     `json:"reward_token"`
	SourceEventId         uuid.UUID  `json:"source_event_id"`
	Reward                rewardView `json:"reward"`
	PeriodKey             *string    `json:"period_key,omitempty"`
	BadgeGrantedAt        *TimeJSON  `json:"badge_granted_at,omitempty"`
	ExperienceGrantedAt   *TimeJSON  `json:"experience_granted_at,omitempty"`
	SourcePointsGrantedAt *TimeJSON  `json:"source_points_granted_at,omitempty"`
	NotificationSentAt    *TimeJSON  `json:"notification_sent_at,omitempty"`
	CreatedAt             *TimeJSON  `json:"created_at,omitempty"`
	UpdatedAt             *TimeJSON  `json:"updated_at,omitempty"`
}

func grantFromEntity(entity *progression.RewardGrantEntity) grantView {
	view := grantView{
		Id: entity.ID, AccountId: entity.AccountID, DefinitionType: entity.DefinitionType,
		DefinitionIdentifier: entity.DefinitionIdentifier, DefinitionTitle: entity.DefinitionTitle,
		RewardToken: entity.RewardToken, SourceEventId: entity.SourceEventID,
		Reward:                decodeRewardView(entity.Reward),
		PeriodKey:             entity.PeriodKey,
		BadgeGrantedAt:        progression.TimeOf(entity.BadgeGrantedAt),
		ExperienceGrantedAt:   progression.TimeOf(entity.ExperienceGrantedAt),
		SourcePointsGrantedAt: progression.TimeOf(entity.SourcePointsGrantedAt),
		NotificationSentAt:    progression.TimeOf(entity.NotificationSentAt),
		CreatedAt:             progression.TimeOf(&entity.CreatedAt),
		UpdatedAt:             progression.TimeOf(&entity.UpdatedAt),
	}
	return view
}

func decodeTriggerView(raw datatypes.JSON) triggerView {
	view := triggerView{Actions: []string{}, Mode: progression.ModeCount}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &view)
	}
	if view.Actions == nil {
		view.Actions = []string{}
	}
	if view.Mode == "" {
		view.Mode = progression.ModeCount
	}
	return view
}

func decodeScheduleView(raw datatypes.JSON) scheduleView {
	view := scheduleView{Repeatability: progression.RepeatNone}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &view)
	}
	if view.Repeatability == "" {
		view.Repeatability = progression.RepeatNone
	}
	return view
}

func decodeRewardView(raw datatypes.JSON) rewardView {
	view := rewardView{SourcePointsCurrency: progression.SourcePointCurrency}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &view)
	}
	if view.SourcePointsCurrency == "" {
		view.SourcePointsCurrency = progression.SourcePointCurrency
	}
	if view.Badge != nil {
		view.Badge = &badgeRewardView{
			Type: view.Badge.Type, Label: view.Badge.Label,
			Caption: view.Badge.Caption, Meta: view.Badge.Meta,
		}
	}
	return view
}

// bindJSON decodes the request body, writing the ASP.NET validation shape on
// failure. An empty body binds the zero value.
func bindJSON(c *gin.Context, target any) bool {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Invalid request body."}}))
		return false
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return true
	}
	if err := json.Unmarshal(body, target); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Invalid request body."}}))
		return false
	}
	return true
}

func validation(c *gin.Context, field string) {
	c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{field: {"The field is invalid."}}))
}
