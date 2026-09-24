package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// RevokedSession carries the fields needed for cache invalidation and the
// auth.session.revoked event.
type RevokedSession struct {
	SessionID string
	AccountID string
	ClientID  *string
	DeviceID  *string
}

// RevokeSessions marks the given sessions expired, bumps their epoch, and
// returns the revoked rows. Unscoped: the legacy UPDATE had no deleted_at
// filter, so revoked sessions are reported even when soft-deleted.
func (s *Store) RevokeSessions(ctx context.Context, ids []uuid.UUID, now time.Time) ([]RevokedSession, error) {
	var entities []AuthSessionEntity
	if err := s.DB.WithContext(ctx).Unscoped().Where("id IN ?", ids).Find(&entities).Error; err != nil {
		return nil, err
	}
	revoked := make([]RevokedSession, 0, len(entities))
	for _, e := range entities {
		revoked = append(revoked, RevokedSession{
			SessionID: e.ID.String(),
			AccountID: e.AccountID.String(),
			ClientID:  uuidPtrStr(e.ClientID),
		})
	}
	if len(revoked) == 0 {
		return revoked, nil
	}
	if err := s.DB.WithContext(ctx).Unscoped().Model(&AuthSessionEntity{}).
		Where("id IN ?", ids).
		Updates(map[string]any{"expired_at": now, "epoch": gorm.Expr("epoch + 1"), "updated_at": now}).Error; err != nil {
		return nil, err
	}
	// Fill device ids from auth_clients for the event payload.
	if err := s.fillDeviceIDs(ctx, revoked); err != nil {
		return nil, err
	}
	return revoked, nil
}

// RevokeAllSessions expires every live session of an account and rotates its
// epoch. Live sessions may have no expiry or a future expiry. Unscoped for
// parity with the legacy UPDATE (no deleted_at filter).
func (s *Store) RevokeAllSessions(ctx context.Context, accountID string, now time.Time) ([]RevokedSession, error) {
	where := "account_id = ? AND (expired_at IS NULL OR expired_at > ?)"
	var entities []AuthSessionEntity
	if err := s.DB.WithContext(ctx).Unscoped().Where(where, accountID, now).Find(&entities).Error; err != nil {
		return nil, err
	}
	revoked := make([]RevokedSession, 0, len(entities))
	for _, e := range entities {
		revoked = append(revoked, RevokedSession{
			SessionID: e.ID.String(),
			AccountID: e.AccountID.String(),
			ClientID:  uuidPtrStr(e.ClientID),
		})
	}
	if len(revoked) == 0 {
		return revoked, nil
	}
	if err := s.DB.WithContext(ctx).Unscoped().Model(&AuthSessionEntity{}).
		Where(where, accountID, now).
		Updates(map[string]any{"expired_at": now, "epoch": gorm.Expr("epoch + 1"), "updated_at": now}).Error; err != nil {
		return nil, err
	}
	if err := s.fillDeviceIDs(ctx, revoked); err != nil {
		return nil, err
	}
	return revoked, nil
}

func (s *Store) fillDeviceIDs(ctx context.Context, revoked []RevokedSession) error {
	ids := make([]uuid.UUID, 0, len(revoked))
	for _, r := range revoked {
		if r.ClientID != nil {
			if id, err := uuid.Parse(*r.ClientID); err == nil {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	// Unscoped: the legacy lookup had no deleted_at filter on auth_clients.
	var clients []AuthClientEntity
	if err := s.DB.WithContext(ctx).Unscoped().Select("id", "device_id").Where("id IN ?", ids).Find(&clients).Error; err != nil {
		return nil
	}
	deviceByClient := make(map[string]string, len(clients))
	for _, c := range clients {
		deviceByClient[c.ID.String()] = c.DeviceID
	}
	for i := range revoked {
		if revoked[i].ClientID == nil {
			continue
		}
		if deviceID, ok := deviceByClient[*revoked[i].ClientID]; ok {
			revoked[i].DeviceID = &deviceID
		}
	}
	return nil
}

// GetEnabledFactor returns the enabled factor of the given type.
func (s *Store) GetEnabledFactor(ctx context.Context, accountID string, ftype model.AuthFactorType) (*model.AuthFactor, error) {
	var entity AuthFactorEntity
	err := s.DB.WithContext(ctx).
		Where("account_id = ? AND type = ? AND enabled_at IS NOT NULL", accountID, int(ftype)).
		Order("created_at").
		First(&entity).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	factor := factorFromEntity(&entity)
	return &factor, nil
}

// HasEnabledFactor reports whether the account has an enabled factor of the type.
func (s *Store) HasEnabledFactor(ctx context.Context, accountID string, ftype model.AuthFactorType) (bool, error) {
	var count int64
	err := s.DB.WithContext(ctx).Model(&AuthFactorEntity{}).
		Where("account_id = ? AND type = ? AND enabled_at IS NOT NULL", accountID, int(ftype)).
		Count(&count).Error
	return count > 0, err
}
