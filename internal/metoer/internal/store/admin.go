package store

import (
	"context"
	"time"

	"src.solsynth.dev/sosys/metoer/internal/model"
	"src.solsynth.dev/sosys/go/pkg/models"
)

// OutcomeCount is one GROUP BY outcome row.
type OutcomeCount struct {
	Outcome model.DeliveryOutcome
	Count   int64
}

// KeyOutcomeCount is one GROUP BY key + outcome row.
type KeyOutcomeCount struct {
	Key     string
	Outcome model.DeliveryOutcome
	Count   int64
}

// KeyCount is one GROUP BY key row.
type KeyCount struct {
	Key   string
	Count int64
}

// NotificationStats holds the /api/admin/stats counters.
type NotificationStats struct {
	CalculatedAt            time.Time
	TotalNotifications      int64
	UnreadNotifications     int64
	NotificationsLastDay    int64
	NotificationsLastWeek   int64
	NotificationsLastMonth  int64
	TotalPushSubscriptions  int64
	ActivePushSubscriptions int64
	TotalSendRequests       int64
	TotalDeliveryAttempts   int64
}

// GetNotificationStats computes every admin stats counter. All counts honor
// the global soft-delete filter, mirroring the C# LongCountAsync queries.
func (s *Store) GetNotificationStats(ctx context.Context, now time.Time) (*NotificationStats, error) {
	stats := &NotificationStats{CalculatedAt: now}
	day := now.Add(-24 * time.Hour)
	week := now.Add(-7 * 24 * time.Hour)
	month := now.Add(-30 * 24 * time.Hour)
	counts := []struct {
		dst *int64
		sql string
		arg any
	}{
		{&stats.TotalNotifications, `SELECT count(*) FROM notifications WHERE deleted_at IS NULL`, nil},
		{&stats.UnreadNotifications, `SELECT count(*) FROM notifications WHERE viewed_at IS NULL AND deleted_at IS NULL`, nil},
		{&stats.NotificationsLastDay, `SELECT count(*) FROM notifications WHERE created_at >= $1 AND deleted_at IS NULL`, models.Time(day.UTC())},
		{&stats.NotificationsLastWeek, `SELECT count(*) FROM notifications WHERE created_at >= $1 AND deleted_at IS NULL`, models.Time(week.UTC())},
		{&stats.NotificationsLastMonth, `SELECT count(*) FROM notifications WHERE created_at >= $1 AND deleted_at IS NULL`, models.Time(month.UTC())},
		{&stats.TotalPushSubscriptions, `SELECT count(*) FROM push_subscriptions WHERE deleted_at IS NULL`, nil},
		{&stats.ActivePushSubscriptions, `SELECT count(*) FROM push_subscriptions WHERE is_activated AND deleted_at IS NULL`, nil},
		{&stats.TotalSendRequests, `SELECT count(*) FROM notification_send_records WHERE deleted_at IS NULL`, nil},
		{&stats.TotalDeliveryAttempts, `SELECT count(*) FROM notification_delivery_records WHERE deleted_at IS NULL`, nil},
	}
	for _, c := range counts {
		var value int64
		if c.arg == nil {
			if err := s.pool.QueryRow(ctx, c.sql).Scan(&value); err != nil {
				return nil, err
			}
		} else {
			if err := s.pool.QueryRow(ctx, c.sql, c.arg).Scan(&value); err != nil {
				return nil, err
			}
		}
		*c.dst = value
	}
	return stats, nil
}

// CountDeliveryOutcomes groups delivery records by outcome within [from, to]
// (BuildSummaryAsync).
func (s *Store) CountDeliveryOutcomes(ctx context.Context, table string, from, to time.Time) ([]OutcomeCount, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT outcome, count(*) FROM `+table+` WHERE created_at >= $1 AND created_at <= $2 AND deleted_at IS NULL GROUP BY outcome`,
		models.Time(from.UTC()), models.Time(to.UTC()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []OutcomeCount
	for rows.Next() {
		var outcome, count int
		if err := rows.Scan(&outcome, &count); err != nil {
			return nil, err
		}
		items = append(items, OutcomeCount{Outcome: model.DeliveryOutcome(outcome), Count: int64(count)})
	}
	return items, rows.Err()
}

// CountDeliveryByKeyOutcome groups delivery records by key + outcome within
// the range (BuildBreakdownAsync).
func (s *Store) CountDeliveryByKeyOutcome(ctx context.Context, table, keyColumn string, from, to time.Time) ([]KeyOutcomeCount, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+keyColumn+`, outcome, count(*) FROM `+table+` WHERE created_at >= $1 AND created_at <= $2 AND deleted_at IS NULL GROUP BY `+keyColumn+`, outcome`,
		models.Time(from.UTC()), models.Time(to.UTC()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []KeyOutcomeCount
	for rows.Next() {
		var key string
		var outcome, count int
		if err := rows.Scan(&key, &outcome, &count); err != nil {
			return nil, err
		}
		items = append(items, KeyOutcomeCount{Key: key, Outcome: model.DeliveryOutcome(outcome), Count: int64(count)})
	}
	return items, rows.Err()
}

// CountSendByTopic groups send records by topic within the range
// (BuildSendBreakdownAsync).
func (s *Store) CountSendByTopic(ctx context.Context, from, to time.Time) ([]KeyCount, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT topic, count(*) FROM notification_send_records WHERE created_at >= $1 AND created_at <= $2 AND deleted_at IS NULL GROUP BY topic`,
		models.Time(from.UTC()), models.Time(to.UTC()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []KeyCount
	for rows.Next() {
		var key string
		var count int
		if err := rows.Scan(&key, &count); err != nil {
			return nil, err
		}
		items = append(items, KeyCount{Key: key, Count: int64(count)})
	}
	return items, rows.Err()
}

// CountSendRecords counts send records within the range.
func (s *Store) CountSendRecords(ctx context.Context, from, to time.Time) (int64, error) {
	var count int64
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM notification_send_records WHERE created_at >= $1 AND created_at <= $2 AND deleted_at IS NULL`,
		models.Time(from.UTC()), models.Time(to.UTC())).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}
