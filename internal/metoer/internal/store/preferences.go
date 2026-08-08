package store

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"time"

	"src.solsynth.dev/sosys/metoer/internal/model"
)

func (s *Store) ListPreferences(ctx context.Context, accountID uuid.UUID) ([]*model.NotificationPreference, error) {
	var entities []NotificationPreferenceEntity
	if err := s.db(ctx).Where("account_id = ?", accountID).Order("topic").Find(&entities).Error; err != nil {
		return nil, err
	}
	items := make([]*model.NotificationPreference, 0, len(entities))
	for i := range entities {
		items = append(items, preferenceFromEntity(&entities[i]))
	}
	return items, nil
}

func (s *Store) GetPreference(ctx context.Context, accountID uuid.UUID, topic string) (*model.NotificationPreference, error) {
	var entity NotificationPreferenceEntity
	err := s.db(ctx).Where("account_id = ? AND topic = ?", accountID, topic).Order("created_at").First(&entity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return preferenceFromEntity(&entity), nil
}

func (s *Store) SetPreference(ctx context.Context, accountID uuid.UUID, topic string, level model.NotificationPreferenceLevel, now time.Time) error {
	existing, err := s.GetPreference(ctx, accountID, topic)
	if err != nil {
		return err
	}
	if existing != nil {
		return s.db(ctx).Model(&NotificationPreferenceEntity{}).Where("id = ?", existing.Id).Updates(map[string]any{"preference": int(level), "updated_at": now.UTC()}).Error
	}
	entity := NotificationPreferenceEntity{ID: uuid.New(), EntityBase: EntityBase{CreatedAt: now.UTC(), UpdatedAt: now.UTC()}, AccountID: accountID, Topic: topic, Preference: int(level)}
	return s.db(ctx).Create(&entity).Error
}

func (s *Store) DeletePreference(ctx context.Context, accountID uuid.UUID, topic string, now time.Time) error {
	existing, err := s.GetPreference(ctx, accountID, topic)
	if err != nil || existing == nil {
		return err
	}
	return s.db(ctx).Model(&NotificationPreferenceEntity{}).Where("id = ?", existing.Id).Updates(map[string]any{"deleted_at": now.UTC(), "updated_at": now.UTC()}).Error
}

func (s *Store) GetPreferencesByTopics(ctx context.Context, accounts []uuid.UUID, topic string) (map[uuid.UUID]model.NotificationPreferenceLevel, error) {
	result := map[uuid.UUID]model.NotificationPreferenceLevel{}
	if len(accounts) == 0 {
		return result, nil
	}
	var entities []NotificationPreferenceEntity
	if err := s.db(ctx).Where("account_id IN ? AND topic = ?", accounts, topic).Find(&entities).Error; err != nil {
		return nil, err
	}
	for _, entity := range entities {
		result[entity.AccountID] = model.NotificationPreferenceLevel(entity.Preference)
	}
	return result, nil
}
