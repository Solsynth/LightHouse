package store

import (
	"context"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/metoer/internal/model"
	"src.solsynth.dev/sosys/go/pkg/models"
)

// ListPreferences lists an account's preferences ordered by topic
// (NotificationPreferenceService.GetPreferencesAsync).
func (s *Store) ListPreferences(ctx context.Context, accountID uuid.UUID) ([]*model.NotificationPreference, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+preferenceColumns+` FROM notification_preferences
		 WHERE account_id = $1 AND deleted_at IS NULL ORDER BY topic`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []*model.NotificationPreference
	for rows.Next() {
		p, err := scanPreference(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	return items, rows.Err()
}

// GetPreference loads one preference (nil when missing; the C# default is
// Normal).
func (s *Store) GetPreference(ctx context.Context, accountID uuid.UUID, topic string) (*model.NotificationPreference, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+preferenceColumns+` FROM notification_preferences
		 WHERE account_id = $1 AND topic = $2 AND deleted_at IS NULL
		 ORDER BY created_at LIMIT 1`, accountID, topic)
	p, err := scanPreference(row)
	if err != nil {
		if err == ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

// SetPreference inserts or updates a preference (SetPreferenceAsync).
func (s *Store) SetPreference(ctx context.Context, accountID uuid.UUID, topic string, level model.NotificationPreferenceLevel, now time.Time) error {
	existing, err := s.GetPreference(ctx, accountID, topic)
	if err != nil {
		return err
	}
	if existing != nil {
		_, err = s.pool.Exec(ctx,
			`UPDATE notification_preferences SET preference = $1, updated_at = $2 WHERE id = $3`,
			int(level), models.Time(now.UTC()), existing.Id)
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO notification_preferences (id, account_id, topic, preference, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $5)`,
		uuid.New(), accountID, topic, int(level), models.Time(now.UTC()))
	return err
}

// DeletePreference soft-deletes the preference (DeletePreferenceAsync).
func (s *Store) DeletePreference(ctx context.Context, accountID uuid.UUID, topic string, now time.Time) error {
	existing, err := s.GetPreference(ctx, accountID, topic)
	if err != nil || existing == nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`UPDATE notification_preferences SET deleted_at = $1, updated_at = $1 WHERE id = $2`,
		models.Time(now.UTC()), existing.Id)
	return err
}

// GetPreferencesByTopics loads preferences for a set of accounts and one
// topic (SendNotificationBatch's preference dictionary).
func (s *Store) GetPreferencesByTopics(ctx context.Context, accounts []uuid.UUID, topic string) (map[uuid.UUID]model.NotificationPreferenceLevel, error) {
	if len(accounts) == 0 {
		return map[uuid.UUID]model.NotificationPreferenceLevel{}, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT account_id, preference FROM notification_preferences
		 WHERE account_id = ANY($1) AND topic = $2 AND deleted_at IS NULL`, accounts, topic)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[uuid.UUID]model.NotificationPreferenceLevel{}
	for rows.Next() {
		var account uuid.UUID
		var level int
		if err := rows.Scan(&account, &level); err != nil {
			return nil, err
		}
		result[account] = model.NotificationPreferenceLevel(level)
	}
	return result, rows.Err()
}
