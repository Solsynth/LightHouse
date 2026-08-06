package store

import (
	"context"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/metoer/internal/model"
	"src.solsynth.dev/sosys/go/pkg/models"
)

// RecordEmailDelivery inserts an email_delivery_records row.
func (s *Store) RecordEmailDelivery(ctx context.Context, r *model.EmailDeliveryRecord) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO email_delivery_records
		(id, source, provider, outcome, duration_milliseconds, error, created_at, updated_at, deleted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7, $8)`,
		r.Id, r.Source, r.Provider, int(r.Outcome), r.DurationMilliseconds, r.Error, r.CreatedAt, r.DeletedAt)
	return err
}

// RecordNotificationDelivery inserts a notification_delivery_records row.
func (s *Store) RecordNotificationDelivery(ctx context.Context, r *model.NotificationDeliveryRecord) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO notification_delivery_records
		(id, notification_id, subscription_id, topic, app_id, push_type, provider, outcome,
		 duration_milliseconds, error, created_at, updated_at, deleted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11, $12)`,
		r.Id, r.NotificationId, r.SubscriptionId, r.Topic, r.AppId, r.PushType, r.Provider,
		int(r.Outcome), r.DurationMilliseconds, r.Error, r.CreatedAt, r.DeletedAt)
	return err
}

// RecordNotificationSend inserts a notification_send_records row.
func (s *Store) RecordNotificationSend(ctx context.Context, r *model.NotificationSendRecord) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO notification_send_records
		(id, topic, app_id, push_type, source, created_at, updated_at, deleted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $6, $7)`,
		r.Id, r.Topic, r.AppId, r.PushType, r.Source, r.CreatedAt, r.DeletedAt)
	return err
}

// MarkSopDeliveryRead promotes Held sop delivery records to Success for the
// given subscription + notification ids (MarkSopDeliveryReadAsync).
func (s *Store) MarkSopDeliveryRead(ctx context.Context, subscriptionID uuid.UUID, notificationIDs []uuid.UUID, now time.Time) (int64, error) {
	if len(notificationIDs) == 0 {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE notification_delivery_records SET outcome = 0, updated_at = $1
		WHERE provider = 'sop' AND subscription_id = $2 AND outcome = 4 AND notification_id = ANY($3)`,
		models.Time(now.UTC()), subscriptionID, notificationIDs)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
