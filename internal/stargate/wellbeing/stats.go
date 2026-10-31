package wellbeing

import (
	"context"
	"errors"
	"time"

	"src.solsynth.dev/sosys/stargate/model"
)

// Stats is the /api/admin/stats payload, mirroring
// AccountStatsAdminController.AccountStatsResponse.
type Stats struct {
	CalculatedAt             model.Time `json:"calculated_at"`
	TotalProfiledAccounts    int64      `json:"total_profiled_accounts"`
	ActiveUsersLastDay       int64      `json:"active_users_last_day"`
	ActiveUsersLastWeek      int64      `json:"active_users_last_week"`
	ActiveUsersLastMonth     int64      `json:"active_users_last_month"`
	RegisteredUsersLastDay   int64      `json:"registered_users_last_day"`
	RegisteredUsersLastWeek  int64      `json:"registered_users_last_week"`
	RegisteredUsersLastMonth int64      `json:"registered_users_last_month"`
}

// Activity is the /api/admin/accounts/metrics/activity payload, mirroring
// AccountAdminController.AdminAccountActivityMetricsResponse.
type Activity struct {
	CalculatedAt               model.Time `json:"calculated_at"`
	CurrentDayStartedAt        model.Time `json:"current_day_started_at"`
	DailyActiveUsers           int64      `json:"daily_active_users"`
	WeeklyActiveUsers          int64      `json:"weekly_active_users"`
	MonthlyActiveUsers         int64      `json:"monthly_active_users"`
	PreviousDailyActiveUsers   int64      `json:"previous_daily_active_users"`
	PreviousWeeklyActiveUsers  int64      `json:"previous_weekly_active_users"`
	PreviousMonthlyActiveUsers int64      `json:"previous_monthly_active_users"`
	NewAccountsToday           int64      `json:"new_accounts_today"`
	NewAccountsThisWeek        int64      `json:"new_accounts_this_week"`
	NewAccountsThisMonth       int64      `json:"new_accounts_this_month"`
	TotalProfiledAccounts      int64      `json:"total_profiled_accounts"`
}

type statsRow struct {
	TotalProfiledAccounts    int64 `gorm:"column:total_profiled_accounts"`
	ActiveUsersLastDay       int64 `gorm:"column:active_users_last_day"`
	ActiveUsersLastWeek      int64 `gorm:"column:active_users_last_week"`
	ActiveUsersLastMonth     int64 `gorm:"column:active_users_last_month"`
	RegisteredUsersLastDay   int64 `gorm:"column:registered_users_last_day"`
	RegisteredUsersLastWeek  int64 `gorm:"column:registered_users_last_week"`
	RegisteredUsersLastMonth int64 `gorm:"column:registered_users_last_month"`
}

type activityRow struct {
	DailyActiveUsers           int64 `gorm:"column:daily_active_users"`
	WeeklyActiveUsers          int64 `gorm:"column:weekly_active_users"`
	MonthlyActiveUsers         int64 `gorm:"column:monthly_active_users"`
	PreviousDailyActiveUsers   int64 `gorm:"column:previous_daily_active_users"`
	PreviousWeeklyActiveUsers  int64 `gorm:"column:previous_weekly_active_users"`
	PreviousMonthlyActiveUsers int64 `gorm:"column:previous_monthly_active_users"`
	NewAccountsToday           int64 `gorm:"column:new_accounts_today"`
	NewAccountsThisWeek        int64 `gorm:"column:new_accounts_this_week"`
	NewAccountsThisMonth       int64 `gorm:"column:new_accounts_this_month"`
	TotalProfiledAccounts      int64 `gorm:"column:total_profiled_accounts"`
}

// accountStatsSQL aggregates the rolling 1/7/30-day active and registered
// counts in one pass. A missing profile row is a NULL last_seen_at (LEFT
// JOIN), so the account still counts toward total_profiled_accounts.
//
// Note: the C# implementation iterated the hydrated account list, which
// carries exactly one profile per account, so its TotalAccounts equals the
// non-deleted account count. The Go query counts accounts directly.
const accountStatsSQL = `
SELECT
	COUNT(*) AS total_profiled_accounts,
	COUNT(*) FILTER (WHERE p.last_seen_at >= ?) AS active_users_last_day,
	COUNT(*) FILTER (WHERE p.last_seen_at >= ?) AS active_users_last_week,
	COUNT(*) FILTER (WHERE p.last_seen_at >= ?) AS active_users_last_month,
	COUNT(*) FILTER (WHERE a.created_at >= ?) AS registered_users_last_day,
	COUNT(*) FILTER (WHERE a.created_at >= ?) AS registered_users_last_week,
	COUNT(*) FILTER (WHERE a.created_at >= ?) AS registered_users_last_month
FROM accounts a
LEFT JOIN account_profiles p ON p.account_id = a.id AND p.deleted_at IS NULL
WHERE a.deleted_at IS NULL`

// activitySQL aggregates the current and previous UTC calendar windows in
// one pass, mirroring AccountService.GetAccountActivityStatsAsync.
const activitySQL = `
SELECT
	COUNT(*) FILTER (WHERE p.last_seen_at >= ?) AS daily_active_users,
	COUNT(*) FILTER (WHERE p.last_seen_at >= ?) AS weekly_active_users,
	COUNT(*) FILTER (WHERE p.last_seen_at >= ?) AS monthly_active_users,
	COUNT(*) FILTER (WHERE p.last_seen_at >= ? AND p.last_seen_at < ?) AS previous_daily_active_users,
	COUNT(*) FILTER (WHERE p.last_seen_at >= ? AND p.last_seen_at < ?) AS previous_weekly_active_users,
	COUNT(*) FILTER (WHERE p.last_seen_at >= ? AND p.last_seen_at < ?) AS previous_monthly_active_users,
	COUNT(*) FILTER (WHERE a.created_at >= ?) AS new_accounts_today,
	COUNT(*) FILTER (WHERE a.created_at >= ?) AS new_accounts_this_week,
	COUNT(*) FILTER (WHERE a.created_at >= ?) AS new_accounts_this_month,
	COUNT(*) AS total_profiled_accounts
FROM accounts a
LEFT JOIN account_profiles p ON p.account_id = a.id AND p.deleted_at IS NULL
WHERE a.deleted_at IS NULL`

// Stats computes the rolling 1/7/30-day activity and registration totals from
// the supplied instant.
func (s *Service) Stats(ctx context.Context, now time.Time) (Stats, error) {
	if s == nil || s.DB == nil {
		return Stats{}, errors.New("wellbeing: missing database")
	}
	now = now.UTC()
	day := now.Add(-24 * time.Hour)
	week := now.Add(-7 * 24 * time.Hour)
	month := now.Add(-30 * 24 * time.Hour)

	var row statsRow
	err := s.DB.WithContext(ctx).Raw(accountStatsSQL,
		day, week, month,
		day, week, month,
	).Scan(&row).Error
	if err != nil {
		return Stats{}, err
	}
	return Stats{
		CalculatedAt:             model.Time(now),
		TotalProfiledAccounts:    row.TotalProfiledAccounts,
		ActiveUsersLastDay:       row.ActiveUsersLastDay,
		ActiveUsersLastWeek:      row.ActiveUsersLastWeek,
		ActiveUsersLastMonth:     row.ActiveUsersLastMonth,
		RegisteredUsersLastDay:   row.RegisteredUsersLastDay,
		RegisteredUsersLastWeek:  row.RegisteredUsersLastWeek,
		RegisteredUsersLastMonth: row.RegisteredUsersLastMonth,
	}, nil
}

// Activity computes the current/previous UTC window metrics from the supplied
// instant.
func (s *Service) Activity(ctx context.Context, now time.Time) (Activity, error) {
	if s == nil || s.DB == nil {
		return Activity{}, errors.New("wellbeing: missing database")
	}
	now = now.UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	weekStart := dayStart.AddDate(0, 0, -6)
	monthStart := dayStart.AddDate(0, 0, -29)
	prevDayStart := dayStart.AddDate(0, 0, -1)
	prevWeekStart := weekStart.AddDate(0, 0, -7)
	prevMonthStart := monthStart.AddDate(0, 0, -30)

	var row activityRow
	err := s.DB.WithContext(ctx).Raw(activitySQL,
		dayStart, weekStart, monthStart,
		prevDayStart, dayStart,
		prevWeekStart, weekStart,
		prevMonthStart, monthStart,
		dayStart, weekStart, monthStart,
	).Scan(&row).Error
	if err != nil {
		return Activity{}, err
	}
	return Activity{
		CalculatedAt:               model.Time(now),
		CurrentDayStartedAt:        model.Time(dayStart),
		DailyActiveUsers:           row.DailyActiveUsers,
		WeeklyActiveUsers:          row.WeeklyActiveUsers,
		MonthlyActiveUsers:         row.MonthlyActiveUsers,
		PreviousDailyActiveUsers:   row.PreviousDailyActiveUsers,
		PreviousWeeklyActiveUsers:  row.PreviousWeeklyActiveUsers,
		PreviousMonthlyActiveUsers: row.PreviousMonthlyActiveUsers,
		NewAccountsToday:           row.NewAccountsToday,
		NewAccountsThisWeek:        row.NewAccountsThisWeek,
		NewAccountsThisMonth:       row.NewAccountsThisMonth,
		TotalProfiledAccounts:      row.TotalProfiledAccounts,
	}, nil
}
