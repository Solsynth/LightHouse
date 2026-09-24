package store

import (
	"context"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// FindValidOauthSession loads the most recent non-expired OAuth-typed session
// for an account + app pair, mirroring OidcProviderService.FindValidSessionAsync
// (s.Type == SessionType.OAuth, app_id == client id, not expired, newest first).
func (s *Store) FindValidOauthSession(ctx context.Context, accountID, appID string) (*model.AuthSession, error) {
	var entity AuthSessionEntity
	err := s.DB.WithContext(ctx).
		Where("account_id = ? AND app_id = ? AND (expired_at IS NULL OR expired_at > ?) AND type = ?",
			accountID, appID, time.Now().UTC(), int(model.SessionTypeOAuth)).
		Order("created_at DESC").
		First(&entity).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	session := sessionFromEntity(&entity)
	account, err := s.GetAccountByID(ctx, entity.AccountID)
	if err != nil {
		return nil, err
	}
	session.Account = account
	return session, nil
}

// GetEmailContact returns the first email contact of an account (primary
// first), mirroring the AccountContacts email lookups in the OIDC provider.
func (s *Store) GetEmailContact(ctx context.Context, accountID string) (*model.Contact, error) {
	var entity ContactEntity
	err := s.DB.WithContext(ctx).
		Where("account_id = ? AND type = ?", accountID, int(model.ContactTypeEmail)).
		Order("is_primary DESC, created_at").
		First(&entity).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	contact := contactFromEntity(&entity)
	return &contact, nil
}

// GetAuthorizedAppScopes loads the most recent authorized-app scope list for
// an account + app pair (type Oidc, not deleted), mirroring the refresh-token
// fallback in OidcProviderService.HandleRefreshTokenFlowAsync.
func (s *Store) GetAuthorizedAppScopes(ctx context.Context, accountID, appID string) ([]string, error) {
	var entity AuthorizedAppEntity
	err := s.DB.WithContext(ctx).
		Where("account_id = ? AND app_id = ? AND type = ?", accountID, appID, int(model.AuthorizedAppTypeOidc)).
		Order("last_authorized_at DESC").
		First(&entity).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	var scopes []string
	_ = decodeJSONValue(entity.Scopes, &scopes)
	return scopes, nil
}

// UpdateSessionScopes persists a session's scope list, mirroring the UPDATE
// in OidcProviderService.SetSessionScopesAsync. Unscoped for parity with the
// legacy UPDATE (no deleted_at filter).
func (s *Store) UpdateSessionScopes(ctx context.Context, sessionID string, scopes []string) error {
	now := time.Now().UTC()
	return s.DB.WithContext(ctx).Unscoped().Model(&AuthSessionEntity{}).
		Where("id = ?", sessionID).
		Updates(map[string]any{"scopes": datatypes.JSON(jsonbOrEmpty(scopes)), "updated_at": now}).Error
}

// UpdateSessionRefresh atomically rotates an OIDC refresh token. The update
// only succeeds when [expectedEpoch] matches the epoch embedded in the
// presented token, so concurrent refreshes cannot both issue a replacement.
func (s *Store) UpdateSessionRefresh(ctx context.Context, sessionID string, expectedEpoch int, lastGrantedAt, expiredAt time.Time) (bool, error) {
	res := s.DB.WithContext(ctx).Unscoped().Model(&AuthSessionEntity{}).
		Where("id = ? AND epoch = ?", sessionID, expectedEpoch).
		Updates(map[string]any{
			"last_granted_at": lastGrantedAt, "expired_at": expiredAt,
			"epoch": gorm.Expr("epoch + 1"), "updated_at": lastGrantedAt,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}
