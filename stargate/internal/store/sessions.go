package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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
// returns the revoked rows.
func (s *Store) RevokeSessions(ctx context.Context, ids []uuid.UUID, now time.Time) ([]RevokedSession, error) {
	rows, err := s.DB.Query(ctx, `UPDATE auth_sessions SET expired_at = $1, epoch = epoch + 1, updated_at = $1
		WHERE id = ANY($2) RETURNING id, account_id, client_id`, now, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var revoked []RevokedSession
	for rows.Next() {
		var r RevokedSession
		var clientID *uuid.UUID
		if err := rows.Scan(&r.SessionID, &r.AccountID, &clientID); err != nil {
			return nil, err
		}
		r.ClientID = uuidPtrStr(clientID)
		revoked = append(revoked, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Fill device ids from auth_clients for the event payload.
	if err := s.fillDeviceIDs(ctx, revoked); err != nil {
		return nil, err
	}
	return revoked, nil
}

// RevokeAllSessions expires every live session of an account.
func (s *Store) RevokeAllSessions(ctx context.Context, accountID string, now time.Time) ([]RevokedSession, error) {
	rows, err := s.DB.Query(ctx, `UPDATE auth_sessions SET expired_at = $1, epoch = epoch + 1, updated_at = $1
		WHERE account_id = $2 AND expired_at IS NULL RETURNING id, account_id, client_id`, now, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var revoked []RevokedSession
	for rows.Next() {
		var r RevokedSession
		var clientID *uuid.UUID
		if err := rows.Scan(&r.SessionID, &r.AccountID, &clientID); err != nil {
			return nil, err
		}
		r.ClientID = uuidPtrStr(clientID)
		revoked = append(revoked, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.fillDeviceIDs(ctx, revoked); err != nil {
		return nil, err
	}
	return revoked, nil
}

func (s *Store) fillDeviceIDs(ctx context.Context, revoked []RevokedSession) error {
	for i := range revoked {
		if revoked[i].ClientID == nil {
			continue
		}
		var deviceID string
		err := s.DB.QueryRow(ctx, `SELECT device_id FROM auth_clients WHERE id = $1`, *revoked[i].ClientID).Scan(&deviceID)
		if err == nil {
			revoked[i].DeviceID = &deviceID
		}
	}
	return nil
}

// GetEnabledFactor returns the enabled factor of the given type.
func (s *Store) GetEnabledFactor(ctx context.Context, accountID string, ftype model.AuthFactorType) (*model.AuthFactor, error) {
	var f model.AuthFactor
	var secret *string
	var config []byte
	err := s.DB.QueryRow(ctx, `SELECT id, type, secret, config, trustworthy, enabled_at, expired_at, account_id, created_at, updated_at, deleted_at
		FROM account_auth_factors
		WHERE account_id = $1 AND type = $2 AND enabled_at IS NOT NULL AND deleted_at IS NULL
		ORDER BY created_at LIMIT 1`,
		accountID, int(ftype)).Scan(&f.Id, &f.Type, &secret, &config, &f.Trustworthy, &f.EnabledAt,
		&f.ExpiredAt, &f.AccountId, &f.CreatedAt, &f.UpdatedAt, &f.DeletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if secret != nil {
		f.Secret = *secret
	}
	if len(config) > 0 {
		_ = json.Unmarshal(config, &f.Config)
	}
	return &f, nil
}

// HasEnabledFactor reports whether the account has an enabled factor of the type.
func (s *Store) HasEnabledFactor(ctx context.Context, accountID string, ftype model.AuthFactorType) (bool, error) {
	var exists bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM account_auth_factors
		WHERE account_id = $1 AND type = $2 AND enabled_at IS NOT NULL AND deleted_at IS NULL)`,
		accountID, int(ftype)).Scan(&exists)
	return exists, err
}
