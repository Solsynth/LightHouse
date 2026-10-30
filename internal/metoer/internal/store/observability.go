package store

import (
	"context"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/metoer/internal/model"
)

func (s *Store) RecordEmailDelivery(ctx context.Context, record *model.EmailDeliveryRecord) error {
	entity := EmailDeliveryRecordEntity{ID: record.Id, EntityBase: EntityBase{CreatedAt: time.Time(record.CreatedAt), UpdatedAt: time.Time(record.CreatedAt), DeletedAt: deletedAt(record.DeletedAt)}, DurationMilliseconds: record.DurationMilliseconds, Error: record.Error, Outcome: int(record.Outcome), Provider: record.Provider, Source: record.Source}
	return s.db(ctx).Create(&entity).Error
}

func (s *Store) RecordNotificationDelivery(ctx context.Context, record *model.NotificationDeliveryRecord) error {
	entity := NotificationDeliveryRecordEntity{ID: record.Id, EntityBase: EntityBase{CreatedAt: time.Time(record.CreatedAt), UpdatedAt: time.Time(record.CreatedAt), DeletedAt: deletedAt(record.DeletedAt)}, AppID: record.AppId, DurationMilliseconds: record.DurationMilliseconds, Error: record.Error, NotificationID: record.NotificationId, Outcome: int(record.Outcome), Provider: record.Provider, PushType: record.PushType, SubscriptionID: record.SubscriptionId, Topic: record.Topic}
	return s.db(ctx).Create(&entity).Error
}

func (s *Store) RecordNotificationSend(ctx context.Context, record *model.NotificationSendRecord) error {
	entity := NotificationSendRecordEntity{ID: record.Id, EntityBase: EntityBase{CreatedAt: time.Time(record.CreatedAt), UpdatedAt: time.Time(record.CreatedAt), DeletedAt: deletedAt(record.DeletedAt)}, AppID: record.AppId, PushType: record.PushType, Source: record.Source, Topic: record.Topic}
	return s.db(ctx).Create(&entity).Error
}

func (s *Store) MarkSopDeliveryRead(ctx context.Context, subscriptionID uuid.UUID, notificationIDs []uuid.UUID, now time.Time) (int64, error) {
	if len(notificationIDs) == 0 {
		return 0, nil
	}
	result := s.db(ctx).Model(&NotificationDeliveryRecordEntity{}).Where("provider = ? AND subscription_id = ? AND outcome = ? AND notification_id IN ?", "sop", subscriptionID, int(model.DeliveryOutcomeHeld), notificationIDs).Updates(map[string]any{"outcome": int(model.DeliveryOutcomeSuccess), "updated_at": now.UTC()})
	return result.RowsAffected, result.Error
}
