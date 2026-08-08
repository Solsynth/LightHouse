package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"src.solsynth.dev/sosys/metoer/internal/model"
)

type OutcomeCount struct {
	Outcome model.DeliveryOutcome
	Count   int64
}
type KeyOutcomeCount struct {
	Key     string
	Outcome model.DeliveryOutcome
	Count   int64
}
type KeyCount struct {
	Key   string
	Count int64
}

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

func countEntity(db *gorm.DB, entity any, conditions ...any) (int64, error) {
	var count int64
	query := db.Model(entity)
	if len(conditions) > 0 {
		query = query.Where(conditions[0], conditions[1:]...)
	}
	return count, query.Count(&count).Error
}

func (s *Store) GetNotificationStats(ctx context.Context, now time.Time) (*NotificationStats, error) {
	stats := &NotificationStats{CalculatedAt: now}
	var err error
	if stats.TotalNotifications, err = countEntity(s.db(ctx), &NotificationEntity{}); err != nil {
		return nil, err
	}
	if stats.UnreadNotifications, err = countEntity(s.db(ctx), &NotificationEntity{}, "viewed_at IS NULL"); err != nil {
		return nil, err
	}
	if stats.NotificationsLastDay, err = countEntity(s.db(ctx), &NotificationEntity{}, "created_at >= ?", now.Add(-24*time.Hour).UTC()); err != nil {
		return nil, err
	}
	if stats.NotificationsLastWeek, err = countEntity(s.db(ctx), &NotificationEntity{}, "created_at >= ?", now.Add(-7*24*time.Hour).UTC()); err != nil {
		return nil, err
	}
	if stats.NotificationsLastMonth, err = countEntity(s.db(ctx), &NotificationEntity{}, "created_at >= ?", now.Add(-30*24*time.Hour).UTC()); err != nil {
		return nil, err
	}
	if stats.TotalPushSubscriptions, err = countEntity(s.db(ctx), &PushSubscriptionEntity{}); err != nil {
		return nil, err
	}
	if stats.ActivePushSubscriptions, err = countEntity(s.db(ctx), &PushSubscriptionEntity{}, "is_activated"); err != nil {
		return nil, err
	}
	if stats.TotalSendRequests, err = countEntity(s.db(ctx), &NotificationSendRecordEntity{}); err != nil {
		return nil, err
	}
	if stats.TotalDeliveryAttempts, err = countEntity(s.db(ctx), &NotificationDeliveryRecordEntity{}); err != nil {
		return nil, err
	}
	return stats, nil
}

func deliveryEntity(table string) (any, error) {
	switch table {
	case "email_delivery_records":
		return &EmailDeliveryRecordEntity{}, nil
	case "notification_delivery_records":
		return &NotificationDeliveryRecordEntity{}, nil
	default:
		return nil, fmt.Errorf("unsupported delivery table %q", table)
	}
}

func (s *Store) CountDeliveryOutcomes(ctx context.Context, table string, from, to time.Time) ([]OutcomeCount, error) {
	entity, err := deliveryEntity(table)
	if err != nil {
		return nil, err
	}
	type row struct {
		Outcome int
		Count   int64
	}
	var rows []row
	err = s.db(ctx).Model(entity).Select("outcome, count(*) AS count").Where("created_at >= ? AND created_at <= ?", from.UTC(), to.UTC()).Group("outcome").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	items := make([]OutcomeCount, 0, len(rows))
	for _, item := range rows {
		items = append(items, OutcomeCount{Outcome: model.DeliveryOutcome(item.Outcome), Count: item.Count})
	}
	return items, nil
}

func allowedDeliveryKey(table, key string) bool {
	if table == "notification_delivery_records" {
		return key == "topic" || key == "provider" || key == "push_type" || key == "app_id"
	}
	return table == "email_delivery_records" && key == "provider"
}

func (s *Store) CountDeliveryByKeyOutcome(ctx context.Context, table, keyColumn string, from, to time.Time) ([]KeyOutcomeCount, error) {
	entity, err := deliveryEntity(table)
	if err != nil {
		return nil, err
	}
	if !allowedDeliveryKey(table, keyColumn) {
		return nil, fmt.Errorf("unsupported delivery key %q", keyColumn)
	}
	type row struct {
		Key     string
		Outcome int
		Count   int64
	}
	var rows []row
	err = s.db(ctx).Model(entity).Select(keyColumn+" AS key, outcome, count(*) AS count").Where("created_at >= ? AND created_at <= ?", from.UTC(), to.UTC()).Group(keyColumn + ", outcome").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	items := make([]KeyOutcomeCount, 0, len(rows))
	for _, item := range rows {
		items = append(items, KeyOutcomeCount{Key: item.Key, Outcome: model.DeliveryOutcome(item.Outcome), Count: item.Count})
	}
	return items, nil
}

func (s *Store) CountSendByTopic(ctx context.Context, from, to time.Time) ([]KeyCount, error) {
	type row struct {
		Key   string
		Count int64
	}
	var rows []row
	err := s.db(ctx).Model(&NotificationSendRecordEntity{}).Select("topic AS key, count(*) AS count").Where("created_at >= ? AND created_at <= ?", from.UTC(), to.UTC()).Group("topic").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	items := make([]KeyCount, 0, len(rows))
	for _, item := range rows {
		items = append(items, KeyCount{Key: item.Key, Count: item.Count})
	}
	return items, nil
}

func (s *Store) CountSendRecords(ctx context.Context, from, to time.Time) (int64, error) {
	return countEntity(s.db(ctx), &NotificationSendRecordEntity{}, "created_at >= ? AND created_at <= ?", from.UTC(), to.UTC())
}
