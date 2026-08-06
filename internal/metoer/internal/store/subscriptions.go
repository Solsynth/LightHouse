package store

import (
	"context"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/metoer/internal/model"
	"src.solsynth.dev/sosys/go/pkg/models"
)

// DeactivateSubscriptions sets is_activated=false on matching rows
// (SubscribeDevice's pre-insert deactivation; the global soft-delete filter
// applies).
func (s *Store) DeactivateSubscriptions(ctx context.Context, accountID uuid.UUID, deviceID string, provider model.PushProvider, now time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE push_subscriptions SET is_activated = false, updated_at = $1
		 WHERE account_id = $2 AND device_id = $3 AND deleted_at IS NULL AND is_activated AND provider = $4`,
		models.Time(now.UTC()), accountID, deviceID, int(provider))
	return err
}

// GetExistingSubscription loads the reusable row for an (account, device,
// provider) triple (SubscribeDevice's existing-subscription lookup).
func (s *Store) GetExistingSubscription(ctx context.Context, accountID uuid.UUID, deviceID string, provider model.PushProvider) (*model.PushSubscription, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+subscriptionColumns+` FROM push_subscriptions
		 WHERE account_id = $1 AND device_id = $2 AND provider = $3 AND deleted_at IS NULL
		 ORDER BY created_at LIMIT 1`,
		accountID, deviceID, int(provider))
	sub, err := scanSubscription(row)
	if err != nil {
		return nil, err
	}
	return sub, nil
}

// InsertSubscription inserts a new push subscription row.
func (s *Store) InsertSubscription(ctx context.Context, sub *model.PushSubscription) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO push_subscriptions (id, account_id, app_id, count_delivered, created_at, deleted_at, device_id, device_name, device_token, is_activated, last_used_at, provider, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		sub.Id, sub.AccountId, sub.AppId, sub.CountDelivered, sub.CreatedAt, sub.DeletedAt,
		sub.DeviceId, sub.DeviceName, sub.DeviceToken, sub.IsActivated, sub.LastUsedAt, int(sub.Provider), sub.UpdatedAt)
	return err
}

// UpdateSubscription writes the reusable row's mutable fields (the C#
// existingSubscription branch of SubscribeDevice).
func (s *Store) UpdateSubscription(ctx context.Context, sub *model.PushSubscription) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE push_subscriptions SET device_id = $1, device_token = $2, provider = $3, is_activated = $4, last_used_at = $5, updated_at = $6, device_name = $7, app_id = $8
		 WHERE id = $9`,
		sub.DeviceId, sub.DeviceToken, int(sub.Provider), sub.IsActivated, sub.LastUsedAt, sub.UpdatedAt, sub.DeviceName, sub.AppId, sub.Id)
	return err
}

// CountSubscriptions counts non-deleted subscriptions of an account
// (SubscribeDevice's existingCount check).
func (s *Store) CountSubscriptions(ctx context.Context, accountID uuid.UUID) (int, error) {
	var count int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM push_subscriptions WHERE account_id = $1 AND deleted_at IS NULL`, accountID).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// DeleteSubscriptionById hard-deletes one row by id and account
// (UnsubscribeFromPushNotification).
func (s *Store) DeleteSubscriptionById(ctx context.Context, accountID, id uuid.UUID) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM push_subscriptions WHERE account_id = $1 AND id = $2 AND deleted_at IS NULL`, accountID, id)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// DeleteSubscriptionsByDevice hard-deletes every row with the device id
// (UnsubscribeDevice).
func (s *Store) DeleteSubscriptionsByDevice(ctx context.Context, deviceID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM push_subscriptions WHERE device_id = $1`, deviceID)
	return err
}

// ListSubscriptions lists an account's non-deleted subscriptions ordered by
// updated_at DESC with the app filter applied.
func (s *Store) ListSubscriptions(ctx context.Context, accountID uuid.UUID, resolvedAppID, defaultAppID *string) ([]*model.PushSubscription, error) {
	clause, args := appFilterClause("app_id", resolvedAppID, defaultAppID, 2)
	query := `SELECT ` + subscriptionColumns + ` FROM push_subscriptions WHERE account_id = $1 AND deleted_at IS NULL`
	if clause != "" {
		query += " AND " + clause
	}
	query += ` ORDER BY updated_at DESC`
	rows, err := s.pool.Query(ctx, query, append([]any{accountID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []*model.PushSubscription
	for rows.Next() {
		sub, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, sub)
	}
	return items, rows.Err()
}

// GetSopSubscriptionByToken loads an activated Sop subscription by token
// (GetSopSubscriptionByToken).
func (s *Store) GetSopSubscriptionByToken(ctx context.Context, token string) (*model.PushSubscription, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+subscriptionColumns+` FROM push_subscriptions
		 WHERE provider = 2 AND device_token = $1 AND is_activated AND deleted_at IS NULL
		 ORDER BY created_at LIMIT 1`, token)
	return scanSubscription(row)
}

// GetCurrentDeviceSubscriptions loads rows for deviceId or deviceId+":sop"
// ordered by updated_at DESC (GetCurrentDeviceSubscriptions).
func (s *Store) GetCurrentDeviceSubscriptions(ctx context.Context, accountID uuid.UUID, deviceID string) ([]*model.PushSubscription, error) {
	sopDeviceID := deviceID + ":sop"
	rows, err := s.pool.Query(ctx,
		`SELECT `+subscriptionColumns+` FROM push_subscriptions
		 WHERE account_id = $1 AND (device_id = $2 OR device_id = $3) AND deleted_at IS NULL
		 ORDER BY updated_at DESC`,
		accountID, deviceID, sopDeviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []*model.PushSubscription
	for rows.Next() {
		sub, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, sub)
	}
	return items, rows.Err()
}

// ListActivatedSubscriptions loads every activated, non-deleted subscription
// of an account (DeliverPushNotification / SendNotificationBatch).
func (s *Store) ListActivatedSubscriptions(ctx context.Context, accountID uuid.UUID) ([]*model.PushSubscription, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+subscriptionColumns+` FROM push_subscriptions
		 WHERE account_id = $1 AND is_activated AND deleted_at IS NULL`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []*model.PushSubscription
	for rows.Next() {
		sub, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, sub)
	}
	return items, rows.Err()
}

// DeleteSubscriptionsByIds hard-deletes the given subscription ids
// (PushSubFlushHandler).
func (s *Store) DeleteSubscriptionsByIds(ctx context.Context, ids []uuid.UUID) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM push_subscriptions WHERE id = ANY($1)`, ids)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
