package rewind

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"src.solsynth.dev/sosys/stargate/model"
)

// window returns Passport's rewind window: [Dec 26 year-1, Dec 26 year) in
// UTC. The day is deliberately not January 1 — the "year in review" covers
// the 365 days up to late December.
func window(year int) (start, end time.Time) {
	return time.Date(year-1, time.December, 26, 0, 0, 0, 0, time.UTC),
		time.Date(year, time.December, 26, 0, 0, 0, 0, time.UTC)
}

// passportStats is PassRewindService.CreateRewindEvent without the gRPC
// envelope: check-in streak/completeness and the activity rhythm over the
// rewind window, in the account's timezone where Passport used it.
//
// Deviation from the C# original: PassRewindService threw away the action
// clock and converted midnight to the account timezone, so latest_active_time
// was the UTC offset rather than a real activity time. This port keeps the
// timestamps so the value means what the key says.
func (s *Service) passportStats(ctx context.Context, account *model.Account, accountID uuid.UUID, year int) (map[string]any, error) {
	start, end := window(year)

	checkInDates, err := s.checkInDates(ctx, accountID, start, end)
	if err != nil {
		return nil, err
	}
	actionTimes, err := s.actionTimes(ctx, accountID, start, end)
	if err != nil {
		return nil, err
	}

	maxStreak := maxCheckInStreak(checkInDates)
	completeness := float64(len(checkInDates)) / 365.0
	mostActiveDay, mostActiveWeekday := mostActive(actionTimes)
	latestActive := latestActiveTime(actionTimes, accountLocation(account))

	return map[string]any{
		"max_check_in_streak":   maxStreak,
		"check_in_completeness": completeness,
		"most_active_day":       mostActiveDay,
		"most_active_weekday":   mostActiveWeekday,
		"latest_active_time":    latestActive,
	}, nil
}

// checkInDates loads the distinct UTC days with a successful check-in. The
// table belongs to the account-events module; when that module is disabled the
// table is absent and the passport counters degrade to zero instead of
// failing the whole rewind.
func (s *Service) checkInDates(ctx context.Context, accountID uuid.UUID, start, end time.Time) ([]time.Time, error) {
	var rows []struct {
		Day time.Time
	}
	err := s.DB.WithContext(ctx).Raw(`
		SELECT DISTINCT (created_at AT TIME ZONE 'UTC')::date AS day
		FROM account_check_in_results
		WHERE account_id = ? AND created_at >= ? AND created_at < ? AND deleted_at IS NULL
		ORDER BY day`, accountID, start, end).Scan(&rows).Error
	if err != nil {
		if isUndefinedTable(err) {
			s.missingCheckInTableOnce.Do(func() {
				s.log().Warn("account_check_in_results is missing; rewind check-in stats are zero",
					"module", ModuleID)
			})
			return nil, nil
		}
		return nil, err
	}
	dates := make([]time.Time, 0, len(rows))
	for _, row := range rows {
		dates = append(dates, dateOnly(row.Day))
	}
	return dates, nil
}

// actionTimes loads every action-log instant in the window. Action logs live
// in Stargate's core table; Passport fetched the same rows through the remote
// action-log service (500-row pages).
func (s *Service) actionTimes(ctx context.Context, accountID uuid.UUID, start, end time.Time) ([]time.Time, error) {
	var rows []struct {
		CreatedAt time.Time
	}
	err := s.DB.WithContext(ctx).
		Table("action_logs").
		Select("created_at").
		Where("account_id = ? AND created_at >= ? AND created_at < ? AND deleted_at IS NULL", accountID, start, end).
		Order("created_at ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	times := make([]time.Time, 0, len(rows))
	for _, row := range rows {
		times = append(times, row.CreatedAt.UTC())
	}
	return times, nil
}

// maxCheckInStreak counts the longest run of consecutive days (the C#
// date-minus-index grouping).
func maxCheckInStreak(dates []time.Time) int {
	if len(dates) == 0 {
		return 0
	}
	groups := make(map[time.Time]int, len(dates))
	longest := 0
	for index, day := range dates {
		key := day.AddDate(0, 0, -index)
		groups[key]++
		if groups[key] > longest {
			longest = groups[key]
		}
	}
	return longest
}

func mostActive(times []time.Time) (day *string, weekday *string) {
	if len(times) == 0 {
		return nil, nil
	}
	dayCounts := make(map[time.Time]int, len(times))
	weekdayCounts := make(map[time.Weekday]int, 7)
	for _, instant := range times {
		dayCounts[dateOnly(instant)]++
		weekdayCounts[instant.Weekday()]++
	}

	// Iterate in chronological order and take the first strict maximum, which
	// mirrors LINQ's stable OrderByDescending over the grouped sequence.
	bestDayCount := 0
	var bestDay time.Time
	for _, instant := range times {
		instantDay := dateOnly(instant)
		if dayCounts[instantDay] > bestDayCount {
			bestDayCount = dayCounts[instantDay]
			bestDay = instantDay
		}
	}
	bestWeekdayCount := 0
	var bestWeekday time.Weekday
	for _, instant := range times {
		if weekdayCounts[instant.Weekday()] > bestWeekdayCount {
			bestWeekdayCount = weekdayCounts[instant.Weekday()]
			bestWeekday = instant.Weekday()
		}
	}
	dayValue := bestDay.Format("2006-01-02")
	weekdayValue := bestWeekday.String()
	return &dayValue, &weekdayValue
}

// latestActiveTime prefers the latest action before 06:00 local time, like
// PassRewindService, and otherwise reports the latest time of day.
func latestActiveTime(times []time.Time, location *time.Location) *string {
	if len(times) == 0 {
		return nil
	}
	if location == nil {
		location = time.UTC
	}
	latest := -1
	latestBeforeSix := -1
	for _, instant := range times {
		local := instant.In(location)
		minute := local.Hour()*60 + local.Minute()
		if minute > latest {
			latest = minute
		}
		if minute < 6*60 && minute > latestBeforeSix {
			latestBeforeSix = minute
		}
	}
	if latestBeforeSix >= 0 {
		latest = latestBeforeSix
	}
	value := fmt.Sprintf("%02d:%02d", latest/60, latest%60)
	return &value
}

func accountLocation(account *model.Account) *time.Location {
	if account == nil || account.Profile == nil || account.Profile.TimeZone == nil {
		return time.UTC
	}
	name := strings.TrimSpace(*account.Profile.TimeZone)
	if name == "" {
		return time.UTC
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return location
}

func dateOnly(instant time.Time) time.Time {
	utc := instant.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
}

// isUndefinedTable reports a missing table/column. The account-events module
// may be disabled (no table), and an older account_check_in_results may not
// carry every column this query expects; both degrade the check-in counters
// instead of failing the rewind.
func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "42P01" || pgErr.Code == "42703"
}
