package store

import (
	"context"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/metoer/internal/model"
)

func (s *Store) DeactivateSubscriptions(ctx context.Context, accountID uuid.UUID, deviceID string, provider model.PushProvider, now time.Time) error {
	return s.db(ctx).Model(&PushSubscriptionEntity{}).Where("account_id = ? AND device_id = ? AND is_activated", accountID, deviceID).Where("provider = ?", int(provider)).Updates(map[string]any{"is_activated": false, "updated_at": now.UTC()}).Error
}

func (s *Store) GetExistingSubscription(ctx context.Context, accountID uuid.UUID, deviceID string, provider model.PushProvider) (*model.PushSubscription, error) {
	var entity PushSubscriptionEntity
	err := s.db(ctx).Where("account_id = ? AND device_id = ? AND provider = ?", accountID, deviceID, int(provider)).Order("created_at").First(&entity).Error
	if err != nil {
		return nil, notFound(err)
	}
	return subscriptionFromEntity(&entity), nil
}

func (s *Store) InsertSubscription(ctx context.Context, sub *model.PushSubscription) error {
	entity := subscriptionEntityFromModel(sub)
	return s.db(ctx).Create(&entity).Error
}

func (s *Store) UpdateSubscription(ctx context.Context, sub *model.PushSubscription) error {
	return s.db(ctx).Model(&PushSubscriptionEntity{}).Where("id = ?", sub.Id).Updates(map[string]any{
		"device_id": sub.DeviceId, "device_token": sub.DeviceToken, "provider": int(sub.Provider),
		"is_activated": sub.IsActivated, "last_used_at": timePtr(sub.LastUsedAt), "updated_at": time.Time(sub.UpdatedAt),
		"device_name": sub.DeviceName, "app_id": sub.AppId,
	}).Error
}

func (s *Store) CountSubscriptions(ctx context.Context, accountID uuid.UUID) (int, error) {
	var count int64
	err := s.db(ctx).Model(&PushSubscriptionEntity{}).Where("account_id = ?", accountID).Count(&count).Error
	return int(count), err
}

func (s *Store) DeleteSubscriptionById(ctx context.Context, accountID, id uuid.UUID) (int64, error) {
	result := s.db(ctx).Unscoped().Where("account_id = ? AND id = ?", accountID, id).Delete(&PushSubscriptionEntity{})
	return result.RowsAffected, result.Error
}

func (s *Store) DeleteSubscriptionsByDevice(ctx context.Context, deviceID string) error {
	return s.db(ctx).Unscoped().Where("device_id = ?", deviceID).Delete(&PushSubscriptionEntity{}).Error
}

func (s *Store) ListSubscriptions(ctx context.Context, accountID uuid.UUID, resolvedAppID, defaultAppID *string) ([]*model.PushSubscription, error) {
	query := applyAppFilter(s.db(ctx).Model(&PushSubscriptionEntity{}).Where("account_id = ?", accountID), "app_id", resolvedAppID, defaultAppID)
	var entities []PushSubscriptionEntity
	if err := query.Order("updated_at DESC").Find(&entities).Error; err != nil {
		return nil, err
	}
	items := make([]*model.PushSubscription, 0, len(entities))
	for i := range entities {
		items = append(items, subscriptionFromEntity(&entities[i]))
	}
	return items, nil
}

func (s *Store) GetSopSubscriptionByToken(ctx context.Context, token string) (*model.PushSubscription, error) {
	var entity PushSubscriptionEntity
	err := s.db(ctx).Where("provider = ? AND device_token = ? AND is_activated", int(model.PushProviderSop), token).Order("created_at").First(&entity).Error
	if err != nil {
		return nil, notFound(err)
	}
	return subscriptionFromEntity(&entity), nil
}

func (s *Store) GetCurrentDeviceSubscriptions(ctx context.Context, accountID uuid.UUID, deviceID string) ([]*model.PushSubscription, error) {
	var entities []PushSubscriptionEntity
	if err := s.db(ctx).Where("account_id = ? AND device_id IN ?", accountID, []string{deviceID, deviceID + ":sop"}).Order("updated_at DESC").Find(&entities).Error; err != nil {
		return nil, err
	}
	items := make([]*model.PushSubscription, 0, len(entities))
	for i := range entities {
		items = append(items, subscriptionFromEntity(&entities[i]))
	}
	return items, nil
}

// ListActivatedSubscriptions returns the account's activated subscriptions
// scoped to the resolved app (same semantics as ListSubscriptions: legacy
// NULL/empty app_id rows belong to the default app; an empty resolved app id
// disables the filter).
func (s *Store) ListActivatedSubscriptions(ctx context.Context, accountID uuid.UUID, resolvedAppID, defaultAppID *string) ([]*model.PushSubscription, error) {
	query := applyAppFilter(s.db(ctx).Model(&PushSubscriptionEntity{}).Where("account_id = ? AND is_activated", accountID), "app_id", resolvedAppID, defaultAppID)
	var entities []PushSubscriptionEntity
	if err := query.Find(&entities).Error; err != nil {
		return nil, err
	}
	items := make([]*model.PushSubscription, 0, len(entities))
	for i := range entities {
		items = append(items, subscriptionFromEntity(&entities[i]))
	}
	return items, nil
}

func (s *Store) DeleteSubscriptionsByIds(ctx context.Context, ids []uuid.UUID) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	result := s.db(ctx).Unscoped().Where("id IN ?", ids).Delete(&PushSubscriptionEntity{})
	return result.RowsAffected, result.Error
}
