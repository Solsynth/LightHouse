package store

import (
	"context"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/model"
)

// Read-only status and presence-activity windows used by the accountevents
// module (calendar aggregation and the public account timeline). These are
// plain additive reads over the core tables; no module writes them.

// ListStatusesBetween returns every status the account created inside
// [from, to), oldest first. Unlike GetCurrentStatus it does not filter on
// cleared_at: the C# calendar selects the status history of the month.
func (s *Store) ListStatusesBetween(ctx context.Context, accountID uuid.UUID, from, to time.Time) ([]model.AccountStatus, error) {
	var entities []AccountStatusEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ? AND created_at >= ? AND created_at < ?", accountID, from, to).
		Order("created_at").Find(&entities).Error; err != nil {
		return nil, err
	}
	statuses := make([]model.AccountStatus, 0, len(entities))
	for i := range entities {
		statuses = append(statuses, *statusFromEntity(&entities[i]))
	}
	return statuses, nil
}

// ListStatusesForTimeline returns every status of the account, newest first.
func (s *Store) ListStatusesForTimeline(ctx context.Context, accountID uuid.UUID) ([]model.AccountStatus, error) {
	var entities []AccountStatusEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("created_at DESC").Find(&entities).Error; err != nil {
		return nil, err
	}
	statuses := make([]model.AccountStatus, 0, len(entities))
	for i := range entities {
		statuses = append(statuses, *statusFromEntity(&entities[i]))
	}
	return statuses, nil
}

// ListActivitiesForTimeline returns every activity of the account with its
// tags, newest first.
func (s *Store) ListActivitiesForTimeline(ctx context.Context, accountID uuid.UUID) ([]model.PresenceActivity, error) {
	var entities []PresenceActivityEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("created_at DESC").Find(&entities).Error; err != nil {
		return nil, err
	}
	return s.attachActivityTags(ctx, entities)
}
