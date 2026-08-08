package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// sessionWithAccountColumns mirrors the SELECT list used by
// GetSessionWithAccount (session columns + account columns joined).
var sessionWithAccountColumns = `s.id, s.type, s.last_granted_at, s.expired_at, s.audiences, s.scopes, s.ip_address,
	s.user_agent, s.location, s.account_id, s.client_id, s.parent_session_id, s.challenge_id, s.app_id, s.epoch,
	s.created_at, s.updated_at, s.deleted_at,
	` + accountColsPrefixed("a")

// FindValidOauthSession loads the most recent non-expired OAuth-typed session
// for an account + app pair, mirroring OidcProviderService.FindValidSessionAsync
// (s.Type == SessionType.OAuth, app_id == client id, not expired, newest first).
func (s *Store) FindValidOauthSession(ctx context.Context, accountID, appID string) (*model.AuthSession, error) {
	row := s.queryRow(ctx, `SELECT `+sessionWithAccountColumns+`
		FROM auth_sessions s
		JOIN accounts a ON a.id = s.account_id
		WHERE s.account_id = $1 AND s.app_id = $2
		  AND (s.expired_at IS NULL OR s.expired_at > $3)
		  AND s.type = $4
		ORDER BY s.created_at DESC LIMIT 1`,
		accountID, appID, time.Now().UTC(), int(model.SessionTypeOAuth))
	return scanSessionWithAccount(row)
}

// GetEmailContact returns the first email contact of an account (primary
// first), mirroring the AccountContacts email lookups in the OIDC provider.
func (s *Store) GetEmailContact(ctx context.Context, accountID string) (*model.Contact, error) {
	row := s.queryRow(ctx, `SELECT id, type, verified_at, is_primary, is_public, content, account_id,
		created_at, updated_at, deleted_at
		FROM account_contacts
		WHERE account_id = $1 AND type = $2 AND deleted_at IS NULL
		ORDER BY is_primary DESC, created_at LIMIT 1`,
		accountID, int(model.ContactTypeEmail))
	contact := &model.Contact{}
	err := row.Scan(&contact.Id, &contact.Type, &contact.VerifiedAt, &contact.IsPrimary, &contact.IsPublic,
		&contact.Content, &contact.AccountId, &contact.CreatedAt, &contact.UpdatedAt, &contact.DeletedAt)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return contact, nil
}

// GetAuthorizedAppScopes loads the most recent authorized-app scope list for
// an account + app pair (type Oidc, not deleted), mirroring the refresh-token
// fallback in OidcProviderService.HandleRefreshTokenFlowAsync.
func (s *Store) GetAuthorizedAppScopes(ctx context.Context, accountID, appID string) ([]string, error) {
	var scopes []string
	err := s.queryRow(ctx, `SELECT scopes FROM authorized_apps
		WHERE account_id = $1 AND app_id = $2 AND type = $3 AND deleted_at IS NULL
		ORDER BY last_authorized_at DESC LIMIT 1`,
		accountID, appID, int(model.AuthorizedAppTypeOidc)).Scan(&scopes)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return scopes, nil
}

// UpdateSessionScopes persists a session's scope list, mirroring the UPDATE
// in OidcProviderService.SetSessionScopesAsync.
func (s *Store) UpdateSessionScopes(ctx context.Context, sessionID string, scopes []string) error {
	_, err := s.exec(ctx, `UPDATE auth_sessions SET scopes = $1, updated_at = $2 WHERE id = $3`,
		scopes, time.Now().UTC(), sessionID)
	return err
}

// UpdateSessionRefresh bumps a session's grant time, expiry and epoch on an
// OIDC refresh-token rotation (mirrors HandleRefreshTokenFlowAsync).
func (s *Store) UpdateSessionRefresh(ctx context.Context, sessionID string, lastGrantedAt, expiredAt time.Time) error {
	_, err := s.exec(ctx, `UPDATE auth_sessions
		SET last_granted_at = $1, expired_at = $2, epoch = epoch + 1, updated_at = $1 WHERE id = $3`,
		lastGrantedAt, expiredAt, sessionID)
	return err
}

func scanSessionWithAccount(row rowScanner) (*model.AuthSession, error) {
	session := &model.AuthSession{}
	var (
		audiences, scopes                             []string
		location                                      []byte
		clientID, parentSessionID, challengeID, appID *uuid.UUID
		epoch                                         int
	)
	account := &model.Account{}
	var automatedID *uuid.UUID
	err := row.Scan(
		&session.Id, &session.Type, &session.LastGrantedAt, &session.ExpiredAt, &audiences, &scopes,
		&session.IpAddress, &session.UserAgent, &location, &session.AccountId,
		&clientID, &parentSessionID, &challengeID, &appID, &epoch,
		&session.CreatedAt, &session.UpdatedAt, &session.DeletedAt,
		&account.Id, &account.Name, &account.Nick, &account.Language, &account.Region, &account.ActivatedAt,
		&account.IsSuperuser, &automatedID, &account.CreatedAt, &account.UpdatedAt, &account.DeletedAt,
	)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	session.Audiences = audiences
	session.Scopes = scopes
	if len(location) > 0 && string(location) != "null" {
		var gp model.GeoPoint
		if err := json.Unmarshal(location, &gp); err == nil {
			session.Location = &gp
		}
	}
	session.ClientId = uuidPtrStr(clientID)
	session.ParentSessionId = uuidPtrStr(parentSessionID)
	session.ChallengeId = uuidPtrStr(challengeID)
	session.AppId = uuidPtrStr(appID)
	session.Epoch = epoch
	if account.Id != "" {
		account.AutomatedId = uuidPtrStr(automatedID)
		session.Account = account
	}
	return session, nil
}
