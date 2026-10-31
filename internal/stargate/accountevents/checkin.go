package accountevents

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/model"
)

// Check-in constants mirror AccountEventService.
const (
	fortuneReportVersion = 2

	checkInWeeklyStreakDays      = 7
	checkInWeeklyStreakGold      = 1
	checkInMonthlyStreakDays     = 30
	checkInMonthlyStreakGold     = 5
	checkInStreakMaxLookbackDays = 366 * 5

	sourcePointCurrency = "points"
	goldenPointCurrency = "golds"

	captchaProbabilityPercent = 20
	captchaCacheTTL           = 24 * time.Hour

	checkInRewardPoints     = 10
	checkInRewardExperience = 100
)

var (
	// ErrCheckInNotAvailable mirrors PASSPORT_CHECK_IN_NOT_AVAILABLE.
	ErrCheckInNotAvailable = errors.New("check-in is not available for today")
	// ErrCheckInBackdatedNotAvailable mirrors
	// PASSPORT_CHECK_IN_BACKDATED_NOT_AVAILABLE.
	ErrCheckInBackdatedNotAvailable = errors.New("check-in is not available for this date")
)

type captchaDecision struct {
	required  bool
	expiresAt time.Time
}

// TodayCheckIn returns the account's newest check-in for the current UTC day.
func (s *Service) TodayCheckIn(ctx context.Context, accountID uuid.UUID) (*CheckInResult, error) {
	now := s.now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return s.deps.Store.GetCheckInBetween(ctx, accountID, startOfDay, startOfDay.Add(24*time.Hour))
}

// CheckInAvailable mirrors CheckInDailyIsAvailable: a check-in is available
// when the newest row's UTC date is before today.
func (s *Service) CheckInAvailable(ctx context.Context, accountID uuid.UUID) (bool, error) {
	latest, err := s.deps.Store.GetLatestCheckIn(ctx, accountID)
	if errors.Is(err, ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	lastDate := latest.CreatedAt.Time().UTC()
	today := s.now()
	lastDay := time.Date(lastDate.Year(), lastDate.Month(), lastDate.Day(), 0, 0, 0, 0, time.UTC)
	currentDay := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	return lastDay.Before(currentDay), nil
}

// BackdatedAvailable mirrors CheckInBackdatedIsAvailable: the day must be
// free and the month must hold fewer than four backdated check-ins.
func (s *Service) BackdatedAvailable(ctx context.Context, accountID uuid.UUID, backdated time.Time) (bool, error) {
	date := backdated.UTC()
	startOfDay := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	endOfDay := startOfDay.Add(24 * time.Hour)
	if _, err := s.deps.Store.GetCheckInBetween(ctx, accountID, startOfDay, endOfDay); err == nil {
		return false, nil
	} else if !errors.Is(err, ErrNotFound) {
		return false, err
	}

	monthStart := time.Date(date.Year(), date.Month(), 1, 0, 0, 0, 0, time.UTC)
	count, err := s.deps.Store.CountBackdatedBetween(ctx, accountID, monthStart, monthStart.AddDate(0, 1, 0))
	if err != nil {
		return false, err
	}
	return count < 4, nil
}

// NeedsCaptcha mirrors CheckInDailyDoAskCaptcha: perk subscribers never need
// one, everybody else gets a random decision cached for 24 hours.
func (s *Service) NeedsCaptcha(ctx context.Context, accountID string) bool {
	if s.hasPerkSubscription(ctx, accountID) {
		return false
	}
	if cached, ok := s.captcha.Load(accountID); ok {
		decision := cached.(captchaDecision)
		if time.Now().Before(decision.expiresAt) {
			return decision.required
		}
	}
	required := randPercent() < captchaProbabilityPercent
	s.captcha.Store(accountID, captchaDecision{required: required, expiresAt: time.Now().Add(captchaCacheTTL)})
	return required
}

// DoCheckIn persists a check-in and pays out its rewards. It is serialized
// per account so two concurrent requests cannot insert the same day twice.
func (s *Service) DoCheckIn(ctx context.Context, account *model.Account, backdated *time.Time, version int) (*CheckInResult, error) {
	accountID, err := uuid.Parse(account.Id)
	if err != nil {
		return nil, err
	}
	lock := s.accountLock(accountID)
	lock.Lock()
	defer lock.Unlock()

	now := s.now()
	isBirthday := s.isBirthday(ctx, accountID, now)

	// Re-check under the lock: two requests may both have passed the handler's
	// availability test before either acquired it.
	if backdated == nil {
		if existing, err := s.TodayCheckIn(ctx, accountID); err == nil {
			s.PrepareCheckInResult(account, existing, version)
			return existing, nil
		}
	} else {
		available, err := s.BackdatedAvailable(ctx, accountID, *backdated)
		if err != nil {
			return nil, err
		}
		if !available {
			return nil, ErrCheckInBackdatedNotAvailable
		}
	}
	tips := buildFortuneTips(account, isBirthday)

	level := rollCheckInLevel()
	if isBirthday {
		level = CheckInSpecial
	}

	createdAt := now
	if backdated != nil {
		createdAt = backdated.UTC()
	}
	experience := checkInRewardExperience
	result := &CheckInResult{
		Level:            level,
		Tips:             tips,
		AccountId:        account.Id,
		RewardExperience: &experience,
		CreatedAt:        modelTimePtrValue(createdAt),
		BackdatedFrom:    nil,
	}
	reason := fmt.Sprintf("Check-in reward on %s", now.Format("2006/01/02"))

	if backdated == nil {
		points := float64(checkInRewardPoints)
		result.RewardPoints = &points
	} else {
		instant := s.now()
		result.BackdatedFrom = modelTimePtrValue(instant)
	}

	if result.RewardPoints != nil {
		if !s.credit(ctx, account.Id, sourcePointCurrency, reason, checkInRewardPoints) {
			result.RewardPoints = nil
		}
	}

	if err := s.deps.Store.InsertCheckIn(ctx, result); err != nil {
		return nil, err
	}

	// Backdated rows skip the streak tier: they are not paid for and must not
	// re-trigger an already-paid day.
	if backdated == nil {
		s.grantStreakGold(ctx, account.Id, accountID, result.CreatedAt.Time().UTC())
	}

	if result.RewardExperience != nil {
		if s.deps.Rewards == nil {
			s.log().Warn("experience reward skipped: no recorder configured", "account", account.Id)
		} else if err := s.deps.Rewards.AddExperience(
			ctx, accountID, "check-in", reason, int64(*result.RewardExperience), s.perkMultiplier(ctx, account.Id),
		); err != nil {
			s.log().Warn("grant check-in experience", "account", account.Id, "error", err)
		}
	}

	s.PrepareCheckInResult(account, result, version)
	return result, nil
}

// PrepareCheckInResult applies the version contract: version >= 2 always
// carries a complete fortune report, older versions omit it entirely.
func (s *Service) PrepareCheckInResult(account *model.Account, result *CheckInResult, version int) {
	if version < fortuneReportVersion {
		result.FortuneReport = nil
		return
	}
	result.FortuneReport = completeFortuneReport(account, result)
}

// isBirthday compares the profile birthday's month/day in the account's
// timezone with the current local date.
func (s *Service) isBirthday(ctx context.Context, accountID uuid.UUID, now time.Time) bool {
	profile, err := s.deps.Accounts.GetOrCreateAccountProfile(ctx, accountID)
	if err != nil {
		s.log().Warn("load profile for check-in", "account", accountID, "error", err)
		return false
	}
	if profile.Birthday == nil {
		return false
	}
	location := time.UTC
	if profile.TimeZone != nil {
		if loaded, err := time.LoadLocation(*profile.TimeZone); err == nil {
			location = loaded
		}
	}
	localNow := now.In(location)
	birthday := profile.Birthday.Time().In(location)
	return birthday.Month() == localNow.Month() && birthday.Day() == localNow.Day()
}

// perkMultiplier mirrors ExperienceService.AddRecord's subscription bonus.
func (s *Service) perkMultiplier(ctx context.Context, accountID string) float64 {
	if s.deps.Perks == nil || !s.deps.Perks.Available() {
		return 1
	}
	subscription, err := s.deps.Perks.Subscription(ctx, accountID)
	if err != nil || subscription == nil {
		return 1
	}
	switch subscription.PerkLevel {
	case 1:
		return 1.5
	case 2:
		return 2
	case 3:
		return 2.5
	}
	return 1
}

func (s *Service) credit(ctx context.Context, accountID, currency, remarks string, amount int64) bool {
	if s.deps.Creditor == nil {
		return false
	}
	if err := s.deps.Creditor.Credit(ctx, accountID, currency, remarks, amount); err != nil {
		s.log().Warn("grant check-in currency", "account", accountID, "currency", currency, "error", err)
		return false
	}
	return true
}

// grantStreakGold pays the weekly/monthly streak tier. Failures are logged,
// never surfaced: the check-in itself already succeeded.
func (s *Service) grantStreakGold(ctx context.Context, accountID string, uuidAccountID uuid.UUID, checkInDate time.Time) {
	streak, err := s.resolveStreak(ctx, uuidAccountID, checkInDate)
	if err != nil {
		s.log().Warn("resolve check-in streak", "account", accountID, "error", err)
		return
	}
	reward := int64(0)
	if streak > 0 && streak%checkInWeeklyStreakDays == 0 {
		reward += checkInWeeklyStreakGold
	}
	if streak > 0 && streak%checkInMonthlyStreakDays == 0 {
		reward += checkInMonthlyStreakGold
	}
	if reward <= 0 {
		return
	}
	remarks := fmt.Sprintf("Check-in streak reward (%d days) on %s", streak, checkInDate.Format("2006/01/02"))
	if !s.credit(ctx, accountID, goldenPointCurrency, remarks, reward) {
		s.log().Warn("check-in streak gold reward failed", "account", accountID, "streak", streak)
	}
}

func (s *Service) resolveStreak(ctx context.Context, accountID uuid.UUID, checkInDate time.Time) (int, error) {
	date := time.Date(checkInDate.Year(), checkInDate.Month(), checkInDate.Day(), 0, 0, 0, 0, time.UTC)
	windowStart := date.AddDate(0, 0, -(checkInStreakMaxLookbackDays - 1))
	windowEnd := date.AddDate(0, 0, 1)
	dates, err := s.deps.Store.ListCheckInDatesBetween(ctx, accountID, windowStart, windowEnd)
	if err != nil {
		return 0, err
	}
	streak := 0
	for current := date; !current.Before(windowStart); current = current.AddDate(0, 0, -1) {
		if _, ok := dates[current.Format("2006-01-02")]; !ok {
			break
		}
		streak++
	}
	return streak, nil
}

func modelTimePtrValue(value time.Time) *model.Time {
	converted := modelTime(value)
	return &converted
}
