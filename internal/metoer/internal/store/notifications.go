package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/metoer/internal/model"
)

func notificationQuery(ctx context.Context, s *Store, accountID uuid.UUID, resolvedAppID, defaultAppID *string) *gorm.DB {
	query := s.db(ctx).Where("account_id = ?", accountID)
	return applyAppFilter(query, "app_id", resolvedAppID, defaultAppID)
}

// CountUnreadNotifications counts unread, non-deleted notifications.
func (s *Store) CountUnreadNotifications(ctx context.Context, accountID uuid.UUID, resolvedAppID, defaultAppID *string) (int, error) {
	var count int64
	err := notificationQuery(ctx, s, accountID, resolvedAppID, defaultAppID).Where("viewed_at IS NULL").Model(&NotificationEntity{}).Count(&count).Error
	return int(count), err
}

// ListNotifications returns one page and its total count.
func (s *Store) ListNotifications(ctx context.Context, accountID uuid.UUID, resolvedAppID, defaultAppID *string, offset, take int) ([]*model.Notification, int, error) {
	query := notificationQuery(ctx, s, accountID, resolvedAppID, defaultAppID).Model(&NotificationEntity{})
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var entities []NotificationEntity
	if err := query.Order("created_at DESC").Offset(offset).Limit(take).Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	items := make([]*model.Notification, 0, len(entities))
	for i := range entities {
		item, err := notificationFromEntity(&entities[i])
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, int(total), nil
}

// ListNotificationsForSop returns the capped database page and duplicate count.
func (s *Store) ListNotificationsForSop(ctx context.Context, accountID uuid.UUID, resolvedAppID, defaultAppID *string, offset, take, replayCount int, replayIDs []uuid.UUID) ([]*model.Notification, int, int, error) {
	query := notificationQuery(ctx, s, accountID, resolvedAppID, defaultAppID).Model(&NotificationEntity{})
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, 0, err
	}
	var duplicate int64
	if len(replayIDs) > 0 {
		dupQuery := notificationQuery(ctx, s, accountID, resolvedAppID, defaultAppID).Where("id IN ?", replayIDs).Model(&NotificationEntity{})
		if err := dupQuery.Count(&duplicate).Error; err != nil {
			return nil, 0, 0, err
		}
	}
	var entities []NotificationEntity
	if err := query.Order("created_at DESC").Limit(offset + take + replayCount).Find(&entities).Error; err != nil {
		return nil, 0, 0, err
	}
	items := make([]*model.Notification, 0, len(entities))
	for i := range entities {
		item, err := notificationFromEntity(&entities[i])
		if err != nil {
			return nil, 0, 0, err
		}
		items = append(items, item)
	}
	return items, int(total), int(duplicate), nil
}

// MarkNotificationsViewed marks currently unread notifications as viewed.
func (s *Store) MarkNotificationsViewed(ctx context.Context, ids []uuid.UUID, now time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	return s.db(ctx).Model(&NotificationEntity{}).Where("id IN ? AND viewed_at IS NULL", ids).Updates(map[string]any{"viewed_at": now.UTC(), "updated_at": now.UTC()}).Error
}

// MarkAllNotificationsViewed marks every unread notification under the app filter.
func (s *Store) MarkAllNotificationsViewed(ctx context.Context, accountID uuid.UUID, resolvedAppID, defaultAppID *string, now time.Time) error {
	return notificationQuery(ctx, s, accountID, resolvedAppID, defaultAppID).Model(&NotificationEntity{}).Where("viewed_at IS NULL").Updates(map[string]any{"viewed_at": now.UTC(), "updated_at": now.UTC()}).Error
}

// BatchInsertNotifications inserts notifications in one GORM batch.
func (s *Store) BatchInsertNotifications(ctx context.Context, notifications []*model.Notification) error {
	if len(notifications) == 0 {
		return nil
	}
	entities := make([]NotificationEntity, 0, len(notifications))
	for _, item := range notifications {
		entity, err := notificationEntityFromModel(item)
		if err != nil {
			return err
		}
		entities = append(entities, entity)
	}
	return s.db(ctx).CreateInBatches(&entities, len(entities)).Error
}

func (s *Store) SaveNotification(ctx context.Context, n *model.Notification) error {
	return s.BatchInsertNotifications(ctx, []*model.Notification{n})
}

// DeleteExcessNotifications keeps the newest 100 rows per account/app partition.
func (s *Store) DeleteExcessNotifications(ctx context.Context) (int64, error) {
	ranked := s.db(ctx).Model(&NotificationEntity{}).Select("id, ROW_NUMBER() OVER (PARTITION BY account_id, COALESCE(app_id, '') ORDER BY created_at DESC) AS rn")
	ids := s.db(ctx).Table("(?) AS ranked", ranked).Select("id").Where("rn > ?", 100)
	result := s.db(ctx).Unscoped().Where("id IN (?)", ids).Delete(&NotificationEntity{})
	return result.RowsAffected, result.Error
}

// RecycleSoftDeleted hard-deletes rows older than threshold.
func (s *Store) RecycleSoftDeleted(ctx context.Context, threshold time.Time) error {
	tables := []struct {
		name   string
		entity any
	}{
		{"notifications", &NotificationEntity{}},
		{"push_subscriptions", &PushSubscriptionEntity{}},
		{"notification_preferences", &NotificationPreferenceEntity{}},
		{"email_sending_plans", &EmailSendingPlanEntity{}},
		{"email_sending_plan_recipients", &EmailSendingPlanRecipientEntity{}},
		{"email_sending_plan_advances", &EmailSendingPlanAdvanceEntity{}},
		{"email_delivery_records", &EmailDeliveryRecordEntity{}},
		{"notification_delivery_records", &NotificationDeliveryRecordEntity{}},
		{"notification_send_records", &NotificationSendRecordEntity{}},
	}
	for _, table := range tables {
		if err := s.db(ctx).Unscoped().Where("deleted_at IS NOT NULL AND deleted_at < ?", threshold.UTC()).Delete(table.entity).Error; err != nil {
			return fmt.Errorf("recycle %s: %w", table.name, err)
		}
	}
	return nil
}
