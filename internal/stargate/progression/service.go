package progression

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Collaborators. Each is optional: a missing one degrades that reward type
// rather than failing the grant, matching the C# service's try/catch around
// the payment and badge grants.

// ExperienceGranter grants account XP. It is the leveling service.
type ExperienceGranter interface {
	AddRecord(ctx context.Context, reasonType, reason string, delta int64, accountID uuid.UUID) error
}

// BadgeGranter grants and lists badges. It is the badges port.
type BadgeGranter interface {
	Available() bool
	// Has reports whether the account already holds a badge of that type.
	Has(ctx context.Context, accountID, badgeType string) (bool, error)
	Grant(ctx context.Context, accountID, badgeType string, label, caption *string, meta map[string]any) error
}

// WalletCreditor credits source points. It is the payments/points collaborator.
type WalletCreditor interface {
	Available() bool
	// Credit adds amount of currency to the account's wallet-backed balance.
	Credit(ctx context.Context, accountID, currency, remarks string, amount int64) error
}

// PacketSender pushes the completion websocket packet.
type PacketSender interface {
	SendPacket(ctx context.Context, accountID, event string, payload any) error
}

// Deps is everything the progression service shares.
type Deps struct {
	Store      *Store
	Experience ExperienceGranter
	Badges     BadgeGranter
	Wallet     WalletCreditor
	Packets    PacketSender
	Log        *slog.Logger
}

// Service is the progression domain logic.
type Service struct {
	d Deps
}

// NewService builds the progression service.
func NewService(d Deps) *Service {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Service{d: d}
}

// Store exposes the persistence layer for the seed service and handlers.
func (s *Service) Store() *Store { return s.d.Store }

// ─────────────────────────── action-log consumer ───────────────────────────

// ActionLogEvent is one action log the consumer applies, mirroring the C#
// ActionLogTriggeredEvent the event bus delivered.
type ActionLogEvent struct {
	EventID    uuid.UUID
	AccountID  uuid.UUID
	Action     string
	Meta       map[string]any
	OccurredAt time.Time
}

// ApplyActionLog advances every matching achievement and quest, delivering the
// rewards of anything it completed. Redelivered events are no-ops: the receipt
// ledger and the reward token both guard them.
func (s *Service) ApplyActionLog(ctx context.Context, event ActionLogEvent) error {
	if event.EventID == uuid.Nil || event.AccountID == uuid.Nil {
		return errors.New("progression: event and account id are required")
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	loc := s.accountZone(ctx, event.AccountID)

	achievements, err := s.d.Store.ListAchievements(ctx, false)
	if err != nil {
		return err
	}
	for i := range achievements {
		definition := &achievements[i]
		if !definition.IsProgressEnabled {
			continue
		}
		trigger, err := decodeTrigger(definition.Trigger)
		if err != nil {
			s.d.Log.Warn("achievement trigger is malformed", "identifier", definition.Identifier, "error", err)
			continue
		}
		if !availableAt(definition.AvailableFrom, definition.AvailableUntil, event.OccurredAt) || !matches(event, trigger) {
			continue
		}
		grant, err := s.applyAchievement(ctx, event, definition, trigger, loc)
		if err != nil {
			return err
		}
		if grant != nil {
			if err := s.deliver(ctx, grant); err != nil {
				return err
			}
		}
	}

	quests, err := s.d.Store.ListQuests(ctx, false)
	if err != nil {
		return err
	}
	for i := range quests {
		definition := &quests[i]
		if !definition.IsProgressEnabled {
			continue
		}
		trigger, err := decodeTrigger(definition.Trigger)
		if err != nil {
			s.d.Log.Warn("quest trigger is malformed", "identifier", definition.Identifier, "error", err)
			continue
		}
		if !availableAt(definition.AvailableFrom, definition.AvailableUntil, event.OccurredAt) || !matches(event, trigger) {
			continue
		}
		schedule, err := decodeSchedule(definition.Schedule)
		if err != nil {
			s.d.Log.Warn("quest schedule is malformed", "identifier", definition.Identifier, "error", err)
			continue
		}
		grant, err := s.applyQuest(ctx, event, definition, schedule, loc)
		if err != nil {
			return err
		}
		if grant != nil {
			if err := s.deliver(ctx, grant); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyAchievement advances one achievement and returns a grant when the event
// completed it. It is a no-op for an already-completed achievement.
func (s *Service) applyAchievement(ctx context.Context, event ActionLogEvent, definition *AchievementDefinitionEntity, trigger ProgressTrigger, loc *time.Location) (*RewardGrantEntity, error) {
	token := RewardToken(TypeAchievement, definition.Identifier, event.AccountID, "")
	existing, err := s.d.Store.FindGrant(ctx, token)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		// The grant is the completion marker, so a crash between writing it
		// and completing the progress row is healed here instead of leaving a
		// rewarded-but-incomplete definition behind.
		if progress, err := s.d.Store.GetAchievementProgress(ctx, event.AccountID, definition.ID); err == nil && progress != nil && progress.CompletedAt == nil {
			completedAt := existing.CreatedAt
			if completedAt.IsZero() {
				completedAt = time.Now().UTC()
			}
			progress.CompletedAt = &completedAt
			progress.LastRewardToken = &token
			if progress.ProgressCount < maxInt(definition.TargetCount, 1) {
				progress.ProgressCount = maxInt(definition.TargetCount, 1)
			}
			if err := s.d.Store.SaveAchievementProgress(ctx, progress); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	seen, err := s.d.Store.ReceiptExists(ctx, event.EventID, TypeAchievement, definition.Identifier, "")
	if err != nil {
		return nil, err
	}
	if seen {
		return nil, nil
	}
	progress, err := s.d.Store.GetAchievementProgress(ctx, event.AccountID, definition.ID)
	if err != nil {
		return nil, err
	}
	if progress == nil {
		progress = &AccountAchievementEntity{
			ID: uuid.New(), AccountID: event.AccountID, AchievementDefinitionID: definition.ID,
		}
	}
	if progress.CompletedAt != nil {
		return nil, nil
	}

	applyAchievementProgress(progress, trigger, definition.TargetCount, event.OccurredAt, loc)
	if progress.ProgressCount < maxInt(definition.TargetCount, 1) {
		if err := s.d.Store.SaveAchievementProgress(ctx, progress); err != nil {
			return nil, err
		}
		return nil, s.d.Store.RecordReceipt(ctx, &EventReceiptEntity{
			ID: uuid.New(), EventID: event.EventID, AccountID: event.AccountID,
			DefinitionType: TypeAchievement, DefinitionIdentifier: definition.Identifier,
		})
	}

	// The grant row is the completion marker: it is written first, and only
	// then does the progress row carry completed_at. A crash between the two
	// leaves an incomplete progress row whose next event finds the grant and
	// skips, so a reward can never be granted twice.
	grant := &RewardGrantEntity{
		ID: uuid.New(), AccountID: event.AccountID,
		DefinitionType: TypeAchievement, DefinitionIdentifier: definition.Identifier,
		DefinitionTitle: definition.Title, RewardToken: token,
		SourceEventID: event.EventID, Reward: definition.Reward,
	}
	if err := s.d.Store.CreateGrant(ctx, grant); err != nil {
		return nil, err
	}
	completedAt := time.Now().UTC()
	progress.CompletedAt = &completedAt
	progress.LastRewardToken = &token
	if err := s.d.Store.SaveAchievementProgress(ctx, progress); err != nil {
		return nil, err
	}
	if err := s.d.Store.RecordReceipt(ctx, &EventReceiptEntity{
		ID: uuid.New(), EventID: event.EventID, AccountID: event.AccountID,
		DefinitionType: TypeAchievement, DefinitionIdentifier: definition.Identifier,
	}); err != nil {
		return nil, err
	}
	return grant, nil
}

// applyQuest advances one quest period and returns a grant when the event
// completed it.
func (s *Service) applyQuest(ctx context.Context, event ActionLogEvent, definition *QuestDefinitionEntity, schedule QuestSchedule, loc *time.Location) (*RewardGrantEntity, error) {
	periodKey, _ := QuestPeriod(schedule, event.OccurredAt, loc)
	token := RewardToken(TypeQuest, definition.Identifier, event.AccountID, periodKey)
	existing, err := s.d.Store.FindGrant(ctx, token)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if progress, err := s.d.Store.GetQuestProgress(ctx, event.AccountID, definition.ID, periodKey); err == nil && progress != nil && progress.CompletedAt == nil {
			completedAt := existing.CreatedAt
			if completedAt.IsZero() {
				completedAt = time.Now().UTC()
			}
			progress.CompletedAt = &completedAt
			progress.LastRewardToken = &token
			if progress.ProgressCount < maxInt(definition.TargetCount, 1) {
				progress.ProgressCount = maxInt(definition.TargetCount, 1)
			}
			if err := s.d.Store.SaveQuestProgress(ctx, progress); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	seen, err := s.d.Store.ReceiptExists(ctx, event.EventID, TypeQuest, definition.Identifier, periodKey)
	if err != nil {
		return nil, err
	}
	if seen {
		return nil, nil
	}
	progress, err := s.d.Store.GetQuestProgress(ctx, event.AccountID, definition.ID, periodKey)
	if err != nil {
		return nil, err
	}
	if progress == nil {
		progress = &AccountQuestProgressEntity{
			ID: uuid.New(), AccountID: event.AccountID,
			QuestDefinitionID: definition.ID, PeriodKey: periodKey,
		}
	}
	if progress.CompletedAt != nil {
		return nil, nil
	}

	target := maxInt(definition.TargetCount, 1)
	progress.ProgressCount = minInt(target, progress.ProgressCount+1)
	if progress.ProgressCount < target {
		if err := s.d.Store.SaveQuestProgress(ctx, progress); err != nil {
			return nil, err
		}
		return nil, s.d.Store.RecordReceipt(ctx, &EventReceiptEntity{
			ID: uuid.New(), EventID: event.EventID, AccountID: event.AccountID,
			DefinitionType: TypeQuest, DefinitionIdentifier: definition.Identifier, PeriodKey: periodKey,
		})
	}

	// Same ordering as achievements: the grant is the completion marker.
	grant := &RewardGrantEntity{
		ID: uuid.New(), AccountID: event.AccountID,
		DefinitionType: TypeQuest, DefinitionIdentifier: definition.Identifier,
		DefinitionTitle: definition.Title, RewardToken: token,
		SourceEventID: event.EventID, Reward: definition.Reward, PeriodKey: &periodKey,
	}
	if err := s.d.Store.CreateGrant(ctx, grant); err != nil {
		return nil, err
	}
	completedAt := time.Now().UTC()
	progress.CompletedAt = &completedAt
	progress.LastRewardToken = &token
	progress.RepeatIterationCount++
	if err := s.d.Store.SaveQuestProgress(ctx, progress); err != nil {
		return nil, err
	}
	if err := s.d.Store.RecordReceipt(ctx, &EventReceiptEntity{
		ID: uuid.New(), EventID: event.EventID, AccountID: event.AccountID,
		DefinitionType: TypeQuest, DefinitionIdentifier: definition.Identifier, PeriodKey: periodKey,
	}); err != nil {
		return nil, err
	}
	return grant, nil
}

// deliver applies each reward type once. The per-type timestamps on the grant
// make a retry resume at the step that failed.
func (s *Service) deliver(ctx context.Context, grant *RewardGrantEntity) error {
	reward, err := decodeReward(grant.Reward)
	if err != nil {
		return err
	}
	now := time.Now().UTC()

	if reward.Badge != nil && grant.BadgeGrantedAt == nil && s.d.Badges != nil && s.d.Badges.Available() {
		has, err := s.d.Badges.Has(ctx, grant.AccountID.String(), reward.Badge.Type)
		if err != nil {
			return err
		}
		if !has {
			if err := s.d.Badges.Grant(ctx, grant.AccountID.String(), reward.Badge.Type,
				reward.Badge.Label, reward.Badge.Caption, reward.Badge.Meta); err != nil {
				return err
			}
		}
		grant.BadgeGrantedAt = &now
	}

	if reward.Experience > 0 && grant.ExperienceGrantedAt == nil && s.d.Experience != nil {
		if err := s.d.Experience.AddRecord(ctx, "progression", grant.RewardToken, reward.Experience, grant.AccountID); err != nil {
			return err
		}
		grant.ExperienceGrantedAt = &now
	}

	if reward.SourcePoints > 0 && grant.SourcePointsGrantedAt == nil && s.d.Wallet != nil && s.d.Wallet.Available() {
		remarks := "Progression reward: " + grant.DefinitionTitle
		if err := s.d.Wallet.Credit(ctx, grant.AccountID.String(), reward.CurrencyOr(), remarks, reward.SourcePoints); err != nil {
			return err
		}
		grant.SourcePointsGrantedAt = &now
	}

	if grant.NotificationSentAt == nil && s.d.Packets != nil {
		payload := CompletionPacket{
			Kind: grant.DefinitionType, Identifier: grant.DefinitionIdentifier,
			Title: grant.DefinitionTitle, PeriodKey: grant.PeriodKey, Reward: reward,
		}
		if err := s.d.Packets.SendPacket(ctx, grant.AccountID.String(), Settings.CompletionPacketType, payload); err != nil {
			return err
		}
		grant.NotificationSentAt = &now
	}

	return s.d.Store.SaveGrant(ctx, grant)
}

// CompletionPacket is ProgressionCompletionPacket.
type CompletionPacket struct {
	Kind       string         `json:"kind"`
	Identifier string         `json:"identifier"`
	Title      string         `json:"title"`
	PeriodKey  *string        `json:"period_key,omitempty"`
	Reward     ProgressReward `json:"reward"`
}

// SendTestPacket pushes a synthetic completion packet (the admin test route).
func (s *Service) SendTestPacket(ctx context.Context, accountID uuid.UUID, kind, identifier, title string) error {
	if s.d.Packets == nil {
		return ErrUnavailable
	}
	return s.d.Packets.SendPacket(ctx, accountID.String(), Settings.CompletionPacketType, CompletionPacket{
		Kind: kind, Identifier: identifier, Title: title, Reward: ProgressReward{},
	})
}

// ─────────────────────────── state listings ───────────────────────────

// AchievementState is ProgressionAchievementState.
type AchievementState struct {
	Identifier           string         `json:"identifier"`
	Title                string         `json:"title"`
	Summary              string         `json:"summary"`
	Icon                 *string        `json:"icon,omitempty"`
	SeriesIdentifier     *string        `json:"series_identifier,omitempty"`
	SeriesTitle          *string        `json:"series_title,omitempty"`
	SeriesOrder          *int           `json:"series_order,omitempty"`
	SortOrder            int            `json:"sort_order"`
	Hidden               bool           `json:"hidden"`
	IsEnabled            bool           `json:"is_enabled"`
	IsProgressEnabled    bool           `json:"is_progress_enabled"`
	IsCurrentlyAvailable bool           `json:"is_currently_available"`
	AvailableFrom        *TimeValue     `json:"available_from,omitempty"`
	AvailableUntil       *TimeValue     `json:"available_until,omitempty"`
	TargetCount          int            `json:"target_count"`
	ProgressCount        int            `json:"progress_count"`
	CurrentStreak        int            `json:"current_streak"`
	BestStreak           int            `json:"best_streak"`
	SeriesTotalSteps     int            `json:"series_total_steps"`
	SeriesCompletedSteps int            `json:"series_completed_steps"`
	SeriesStages         []SeriesStage  `json:"series_stages"`
	IsCompleted          bool           `json:"is_completed"`
	CompletedAt          *TimeValue     `json:"completed_at,omitempty"`
	Reward               ProgressReward `json:"reward"`
}

// QuestState is ProgressionQuestState.
type QuestState struct {
	Identifier           string         `json:"identifier"`
	Title                string         `json:"title"`
	Summary              string         `json:"summary"`
	Icon                 *string        `json:"icon,omitempty"`
	SeriesIdentifier     *string        `json:"series_identifier,omitempty"`
	SeriesTitle          *string        `json:"series_title,omitempty"`
	SeriesOrder          *int           `json:"series_order,omitempty"`
	SortOrder            int            `json:"sort_order"`
	Hidden               bool           `json:"hidden"`
	IsEnabled            bool           `json:"is_enabled"`
	IsProgressEnabled    bool           `json:"is_progress_enabled"`
	IsCurrentlyAvailable bool           `json:"is_currently_available"`
	AvailableFrom        *TimeValue     `json:"available_from,omitempty"`
	AvailableUntil       *TimeValue     `json:"available_until,omitempty"`
	TargetCount          int            `json:"target_count"`
	ProgressCount        int            `json:"progress_count"`
	SeriesTotalSteps     int            `json:"series_total_steps"`
	SeriesCompletedSteps int            `json:"series_completed_steps"`
	SeriesStages         []SeriesStage  `json:"series_stages"`
	IsCompleted          bool           `json:"is_completed"`
	CompletedAt          *TimeValue     `json:"completed_at,omitempty"`
	PeriodKey            string         `json:"period_key"`
	NextResetAt          *TimeValue     `json:"next_reset_at,omitempty"`
	Schedule             QuestSchedule  `json:"schedule"`
	Reward               ProgressReward `json:"reward"`
}

// AchievementStats is ProgressionAchievementStats.
type AchievementStats struct {
	TotalCount           int     `json:"total_count"`
	CompletedCount       int     `json:"completed_count"`
	HiddenTotalCount     int     `json:"hidden_total_count"`
	HiddenCompletedCount int     `json:"hidden_completed_count"`
	CompletionPercentage float64 `json:"completion_percentage"`
}

// ListAchievements returns the account's achievement states, series merged and
// ordered exactly like ProgressionService.ListAchievementStatesAsync.
func (s *Service) ListAchievements(ctx context.Context, accountID uuid.UUID) ([]AchievementState, error) {
	definitions, err := s.d.Store.ListAchievements(ctx, false)
	if err != nil {
		return nil, err
	}
	progress, err := s.d.Store.AchievementProgress(ctx, accountID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	states := make([]AchievementState, 0, len(definitions))
	for i := range definitions {
		definition := &definitions[i]
		// The trigger only drives accumulation, which the listing does not
		// show; a malformed value must not hide the definition.
		reachable := definition.IsProgressEnabled && availableAt(definition.AvailableFrom, definition.AvailableUntil, now)
		row := progress[definition.ID]
		state := AchievementState{
			Identifier: definition.Identifier, Title: definition.Title, Summary: definition.Summary,
			Icon: definition.Icon, SeriesIdentifier: definition.SeriesIdentifier,
			SeriesTitle: definition.SeriesTitle, SeriesOrder: definition.SeriesOrder,
			SortOrder: definition.SortOrder, Hidden: definition.Hidden,
			IsEnabled: definition.IsEnabled, IsProgressEnabled: definition.IsProgressEnabled,
			IsCurrentlyAvailable: reachable,
			AvailableFrom:        TimeOf(definition.AvailableFrom),
			AvailableUntil:       TimeOf(definition.AvailableUntil),
			TargetCount:          maxInt(definition.TargetCount, 1),
			SeriesTotalSteps:     1,
			SeriesStages:         []SeriesStage{},
		}
		if reward, err := decodeReward(definition.Reward); err == nil {
			state.Reward = reward
		}
		if row != nil {
			state.ProgressCount = row.ProgressCount
			state.CurrentStreak = row.CurrentStreak
			state.BestStreak = row.BestStreak
			state.CompletedAt = TimeOf(row.CompletedAt)
			state.IsCompleted = row.CompletedAt != nil
			if state.IsCompleted {
				state.SeriesCompletedSteps = 1
			}
		}
		states = append(states, state)
	}
	return orderAchievements(mergeAchievementSeries(states)), nil
}

// SearchAchievements filters the achievement listing by the fields the C#
// search matched on.
func (s *Service) SearchAchievements(ctx context.Context, accountID uuid.UUID, query string) ([]AchievementState, error) {
	if strings.TrimSpace(query) == "" {
		return s.ListAchievements(ctx, accountID)
	}
	states, err := s.ListAchievements(ctx, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]AchievementState, 0, len(states))
	for _, state := range states {
		if containsFold(state.Identifier, query) || containsFold(state.Title, query) ||
			containsFold(state.Summary, query) ||
			containsFold(derefOr(state.SeriesIdentifier, ""), query) ||
			containsFold(derefOr(state.SeriesTitle, ""), query) {
			out = append(out, state)
		}
	}
	return out, nil
}

// AchievementStatistics computes the completion summary.
func (s *Service) AchievementStatistics(ctx context.Context, accountID uuid.UUID) (AchievementStats, error) {
	definitions, err := s.d.Store.ListAchievements(ctx, false)
	if err != nil {
		return AchievementStats{}, err
	}
	progress, err := s.d.Store.AchievementProgress(ctx, accountID)
	if err != nil {
		return AchievementStats{}, err
	}
	stats := AchievementStats{TotalCount: len(definitions)}
	for i := range definitions {
		definition := &definitions[i]
		row := progress[definition.ID]
		completed := row != nil && row.CompletedAt != nil
		if completed {
			stats.CompletedCount++
		}
		if definition.Hidden {
			stats.HiddenTotalCount++
			if completed {
				stats.HiddenCompletedCount++
			}
		}
	}
	if stats.TotalCount > 0 {
		stats.CompletionPercentage = round2(float64(stats.CompletedCount) / float64(stats.TotalCount) * 100)
	}
	return stats, nil
}

// ListQuests returns the account's quest states for the current period.
func (s *Service) ListQuests(ctx context.Context, accountID uuid.UUID) ([]QuestState, error) {
	definitions, err := s.d.Store.ListQuests(ctx, false)
	if err != nil {
		return nil, err
	}
	loc := s.accountZone(ctx, accountID)
	now := time.Now().UTC()

	periods := make(map[uuid.UUID]string, len(definitions))
	schedules := make(map[uuid.UUID]QuestSchedule, len(definitions))
	resets := make(map[uuid.UUID]time.Time, len(definitions))
	for i := range definitions {
		definition := &definitions[i]
		schedule, decodeErr := decodeSchedule(definition.Schedule)
		if decodeErr != nil {
			schedule = QuestSchedule{}
		}
		key, next := QuestPeriod(schedule, now, loc)
		periods[definition.ID] = key
		schedules[definition.ID] = schedule
		resets[definition.ID] = next
	}
	progress, err := s.d.Store.QuestProgress(ctx, accountID, periods)
	if err != nil {
		return nil, err
	}

	states := make([]QuestState, 0, len(definitions))
	for i := range definitions {
		definition := &definitions[i]
		row := progress[definition.ID]
		next := resets[definition.ID]
		state := QuestState{
			Identifier: definition.Identifier, Title: definition.Title, Summary: definition.Summary,
			Icon: definition.Icon, SeriesIdentifier: definition.SeriesIdentifier,
			SeriesTitle: definition.SeriesTitle, SeriesOrder: definition.SeriesOrder,
			SortOrder: definition.SortOrder, Hidden: definition.Hidden,
			IsEnabled: definition.IsEnabled, IsProgressEnabled: definition.IsProgressEnabled,
			IsCurrentlyAvailable: definition.IsProgressEnabled && availableAt(definition.AvailableFrom, definition.AvailableUntil, now),
			AvailableFrom:        TimeOf(definition.AvailableFrom),
			AvailableUntil:       TimeOf(definition.AvailableUntil),
			TargetCount:          maxInt(definition.TargetCount, 1),
			SeriesTotalSteps:     1,
			SeriesStages:         []SeriesStage{},
			PeriodKey:            periods[definition.ID],
			NextResetAt:          TimeOf(&next),
			Schedule:             schedules[definition.ID],
		}
		if reward, err := decodeReward(definition.Reward); err == nil {
			state.Reward = reward
		}
		if row != nil {
			state.ProgressCount = row.ProgressCount
			state.CompletedAt = TimeOf(row.CompletedAt)
			state.IsCompleted = row.CompletedAt != nil
			if state.IsCompleted {
				state.SeriesCompletedSteps = 1
			}
		}
		states = append(states, state)
	}
	return orderQuests(mergeQuestSeries(states)), nil
}

// ListGrants returns the account's reward grants.
func (s *Service) ListGrants(ctx context.Context, accountID uuid.UUID, take int) ([]RewardGrantEntity, error) {
	return s.d.Store.ListGrants(ctx, accountID, take)
}

// ─────────────────────────── progress math ───────────────────────────

// matches is ProgressionService.Matches: the event's action must be listed
// (when the trigger lists any) and every MetaEquals pair must hold.
func matches(event ActionLogEvent, trigger ProgressTrigger) bool {
	if len(trigger.Actions) > 0 {
		found := false
		for _, action := range trigger.Actions {
			if strings.EqualFold(action, event.Action) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for key, expected := range trigger.MetaEquals {
		raw, ok := lookupMeta(event.Meta, key)
		if !ok {
			return false
		}
		if !strings.EqualFold(metaString(raw), expected) {
			return false
		}
	}
	return true
}

func applyAchievementProgress(progress *AccountAchievementEntity, trigger ProgressTrigger, target int, occurredAt time.Time, loc *time.Location) {
	switch strings.ToLower(trigger.ModeOrDefault()) {
	case ModeStreak:
		applyStreakProgress(progress, target, occurredAt, loc)
	case ModeDailyCount:
		applyDailyCountProgress(progress, target, occurredAt, loc)
	default:
		progress.ProgressCount = minInt(maxInt(target, 1), progress.ProgressCount+1)
		progress.LastProgressAt = &occurredAt
	}
}

// applyStreakProgress mirrors ProgressionService.ApplyStreakProgress: a
// same-day repeat restores the current streak, a consecutive day extends it,
// and a gap restarts at one.
func applyStreakProgress(progress *AccountAchievementEntity, target int, occurredAt time.Time, loc *time.Location) {
	if progress.LastProgressAt != nil {
		days := DaysBetween(*progress.LastProgressAt, occurredAt, loc)
		if days <= 0 {
			progress.ProgressCount = minInt(maxInt(target, 1), progress.CurrentStreak)
			progress.BestStreak = maxInt(progress.BestStreak, progress.CurrentStreak)
			return
		}
		if days == 1 {
			progress.CurrentStreak++
		} else {
			progress.CurrentStreak = 1
		}
	} else {
		progress.CurrentStreak = 1
	}
	progress.BestStreak = maxInt(progress.BestStreak, progress.CurrentStreak)
	progress.ProgressCount = minInt(maxInt(target, 1), progress.CurrentStreak)
	progress.LastProgressAt = &occurredAt
}

// applyDailyCountProgress mirrors ApplyDailyCountProgress: the counter resets
// when the local day changed.
func applyDailyCountProgress(progress *AccountAchievementEntity, target int, occurredAt time.Time, loc *time.Location) {
	if progress.LastProgressAt != nil && !SameDay(*progress.LastProgressAt, occurredAt, loc) {
		progress.ProgressCount = 0
	}
	progress.ProgressCount = minInt(maxInt(target, 1), progress.ProgressCount+1)
	progress.LastProgressAt = &occurredAt
}

// availableAt is ProgressionService.IsAvailableAt.
func availableAt(from, until *time.Time, instant time.Time) bool {
	if from != nil && instant.Before(*from) {
		return false
	}
	if until != nil && instant.After(*until) {
		return false
	}
	return true
}

// ─────────────────────────── series merging ───────────────────────────

// mergeAchievementSeries collapses each series into one representative: the
// first incomplete stage, or the last stage when the series is finished.
func mergeAchievementSeries(states []AchievementState) []AchievementState {
	groups := map[string][]AchievementState{}
	order := make([]string, 0, len(states))
	for _, state := range states {
		key := state.Identifier
		if state.SeriesIdentifier != nil && strings.TrimSpace(*state.SeriesIdentifier) != "" {
			key = *state.SeriesIdentifier
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], state)
	}
	out := make([]AchievementState, 0, len(order))
	for _, key := range order {
		group := groups[key]
		sort.SliceStable(group, func(i, j int) bool {
			return stageLess(group[i].SeriesOrder, group[i].SortOrder, group[i].Title,
				group[j].SeriesOrder, group[j].SortOrder, group[j].Title)
		})
		representative := group[len(group)-1]
		for _, state := range group {
			if !state.IsCompleted {
				representative = state
				break
			}
		}
		representative.SeriesTotalSteps = len(group)
		completed := 0
		stages := make([]SeriesStage, 0, len(group))
		for _, state := range group {
			if state.IsCompleted {
				completed++
			}
			stages = append(stages, SeriesStage{
				Identifier: state.Identifier, Title: state.Title,
				SeriesOrder: state.SeriesOrder, TargetCount: state.TargetCount,
				IsCompleted: state.IsCompleted, CompletedAt: state.CompletedAt,
			})
		}
		representative.SeriesCompletedSteps = completed
		if representative.SeriesTitle == nil {
			title := representative.Title
			representative.SeriesTitle = &title
		}
		if representative.SeriesIdentifier == nil {
			identifier := representative.Identifier
			representative.SeriesIdentifier = &identifier
		}
		representative.SeriesStages = stages
		out = append(out, representative)
	}
	return out
}

func mergeQuestSeries(states []QuestState) []QuestState {
	groups := map[string][]QuestState{}
	order := make([]string, 0, len(states))
	for _, state := range states {
		key := state.Identifier
		if state.SeriesIdentifier != nil && strings.TrimSpace(*state.SeriesIdentifier) != "" {
			key = *state.SeriesIdentifier
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], state)
	}
	out := make([]QuestState, 0, len(order))
	for _, key := range order {
		group := groups[key]
		sort.SliceStable(group, func(i, j int) bool {
			return stageLess(group[i].SeriesOrder, group[i].SortOrder, group[i].Title,
				group[j].SeriesOrder, group[j].SortOrder, group[j].Title)
		})
		representative := group[len(group)-1]
		for _, state := range group {
			if !state.IsCompleted {
				representative = state
				break
			}
		}
		representative.SeriesTotalSteps = len(group)
		completed := 0
		stages := make([]SeriesStage, 0, len(group))
		for _, state := range group {
			if state.IsCompleted {
				completed++
			}
			stages = append(stages, SeriesStage{
				Identifier: state.Identifier, Title: state.Title,
				SeriesOrder: state.SeriesOrder, TargetCount: state.TargetCount,
				IsCompleted: state.IsCompleted, CompletedAt: state.CompletedAt,
			})
		}
		representative.SeriesCompletedSteps = completed
		if representative.SeriesTitle == nil {
			title := representative.Title
			representative.SeriesTitle = &title
		}
		if representative.SeriesIdentifier == nil {
			identifier := representative.Identifier
			representative.SeriesIdentifier = &identifier
		}
		representative.SeriesStages = stages
		out = append(out, representative)
	}
	return out
}

func stageLess(orderA *int, sortA int, titleA string, orderB *int, sortB int, titleB string) bool {
	a, b := int(1<<31-1), int(1<<31-1)
	if orderA != nil {
		a = *orderA
	}
	if orderB != nil {
		b = *orderB
	}
	if a != b {
		return a < b
	}
	if sortA != sortB {
		return sortA < sortB
	}
	return titleA < titleB
}

// orderAchievements is the C# listing order: incomplete first, then sort
// order, series order, title.
func orderAchievements(states []AchievementState) []AchievementState {
	sort.SliceStable(states, func(i, j int) bool {
		a, b := states[i], states[j]
		if a.IsCompleted != b.IsCompleted {
			return !a.IsCompleted
		}
		if a.SortOrder != b.SortOrder {
			return a.SortOrder < b.SortOrder
		}
		return stageLess(a.SeriesOrder, a.SortOrder, a.Title, b.SeriesOrder, b.SortOrder, b.Title)
	})
	return states
}

func orderQuests(states []QuestState) []QuestState {
	sort.SliceStable(states, func(i, j int) bool {
		a, b := states[i], states[j]
		if a.IsCompleted != b.IsCompleted {
			return !a.IsCompleted
		}
		if a.SortOrder != b.SortOrder {
			return a.SortOrder < b.SortOrder
		}
		return stageLess(a.SeriesOrder, a.SortOrder, a.Title, b.SeriesOrder, b.SortOrder, b.Title)
	})
	return states
}

// ─────────────────────────── small helpers ───────────────────────────

func (s *Service) accountZone(ctx context.Context, accountID uuid.UUID) *time.Location {
	zone, err := s.d.Store.accountTimeZone(ctx, accountID)
	if err != nil {
		s.d.Log.Warn("progression time zone lookup failed", "account", accountID, "error", err)
		return time.UTC
	}
	if strings.TrimSpace(zone) == "" {
		zone = Settings.DefaultTimeZone
	}
	return ResolveZone(zone)
}

func containsFold(value, query string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(query))
}

func derefOr(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func round2(value float64) float64 {
	return float64(int64(value*100+0.5)) / 100
}

// lookupMeta reads a meta key, matching the C# dictionary's
// case-insensitive comparer.
func lookupMeta(meta map[string]any, key string) (any, bool) {
	if meta == nil {
		return nil, false
	}
	if value, ok := meta[key]; ok {
		return value, true
	}
	for candidate, value := range meta {
		if strings.EqualFold(candidate, key) {
			return value, true
		}
	}
	return nil, false
}

// metaString is ProgressionService.ConvertMetaValue: strings are used as-is
// and everything else is rendered as JSON.
func metaString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return trimFloat(typed)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(raw)
	}
}

// trimFloat renders a float the way System.Text.Json writes it (no trailing
// .0 for integral values).
func trimFloat(value float64) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}
