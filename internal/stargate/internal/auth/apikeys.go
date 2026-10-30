package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// --- API keys (port of AuthService ApiKey methods) ---

// GetApiKey loads an API key with its session; scoped to an account when
// accountID is non-nil.
func (s *AuthService) GetApiKey(ctx context.Context, id uuid.UUID, accountID *uuid.UUID) (*model.ApiKey, error) {
	// Unscoped: the legacy lookup returned soft-deleted keys with their
	// deleted_at populated.
	query := s.store.DB.WithContext(ctx).Unscoped().Model(&store.APIKeyEntity{}).Where("id = ?", id)
	if accountID != nil {
		query = query.Where("account_id = ?", *accountID)
	}
	var entity store.APIKeyEntity
	if err := query.First(&entity).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return apiKeyModel(&entity), nil
}

// CreateApiKey creates an API key with its backing ApiKey-typed session.
func (s *AuthService) CreateApiKey(ctx context.Context, accountID string, label string, expiredAt *time.Time, parentSession *model.AuthSession) (*model.ApiKey, error) {
	normalized := strings.TrimSpace(label)
	if normalized == "" {
		return nil, &ErrInvalid{Message: "Label is required."}
	}
	now := time.Now().UTC()
	if expiredAt != nil && !expiredAt.After(now) {
		return nil, &ErrInvalid{Message: "ExpiredAt must be in the future."}
	}
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return nil, err
	}
	var appID *uuid.UUID
	var parentID *uuid.UUID
	if parentSession != nil {
		appID = uuidPtr(parentSession.AppId)
		if parentSession.Id != "" {
			parsedParentID, err := uuid.Parse(parentSession.Id)
			if err != nil {
				return nil, err
			}
			parentID = &parsedParentID
		}
	}

	sessionID, keyID, err := s.store.CreateApiKeyWithSession(ctx, accountUUID, normalized, expiredAt, appID, parentID)
	if err != nil {
		return nil, err
	}
	key := &model.ApiKey{
		Id:        keyID.String(),
		Label:     normalized,
		AccountId: accountID,
		AppId:     uuidPtrToStr(appID),
		SessionId: sessionID.String(),
		CreatedAt: model.NewTime(now),
		UpdatedAt: model.NewTime(now),
	}
	if expiredAt != nil {
		key.ExpiredAt = model.NewTime(*expiredAt)
	}
	return key, nil
}

// IssueApiKeyToken issues a Bot token for the key's session.
func (s *AuthService) IssueApiKeyToken(ctx context.Context, key *model.ApiKey) (string, error) {
	sessionID, err := uuid.Parse(key.SessionId)
	if err != nil {
		return "", errors.New("API key session is not available.")
	}
	now := time.Now().UTC()
	res := s.store.DB.WithContext(ctx).Unscoped().Model(&store.AuthSessionEntity{}).
		Where("id = ? AND (expired_at IS NULL OR expired_at > ?)", sessionID, now).
		Updates(map[string]any{"last_granted_at": now, "updated_at": now})
	if res.Error != nil {
		return "", res.Error
	}
	if res.RowsAffected == 0 {
		return "", errors.New("API key session has expired or does not exist.")
	}
	session, err := s.store.GetSessionWithAccount(ctx, sessionID)
	if err != nil {
		return "", err
	}
	return s.jwt.CreateBotToken(key, session)
}

// RevokeApiKeyToken soft-deletes the key and revokes its session. The
// soft-delete is rolled back when session revocation fails, matching the
// legacy transaction.
func (s *AuthService) RevokeApiKeyToken(ctx context.Context, key *model.ApiKey) error {
	now := time.Now().UTC()
	sessionID, err := uuid.Parse(key.SessionId)
	if err != nil {
		return err
	}
	tx := s.store.DB.WithContext(ctx).Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.Unscoped().Model(&store.APIKeyEntity{}).
		Where("id = ?", key.Id).
		Updates(map[string]any{"deleted_at": now, "updated_at": now}).Error; err != nil {
		return err
	}
	if _, err := s.RevokeSession(ctx, sessionID); err != nil {
		return err
	}
	return tx.Commit().Error
}

// RotateApiKeyToken rotates the key to a fresh session (old tokens die via
// epoch bump).
func (s *AuthService) RotateApiKeyToken(ctx context.Context, key *model.ApiKey) (*model.ApiKey, error) {
	now := time.Now().UTC()
	sessionID, err := uuid.Parse(key.SessionId)
	if err != nil {
		return nil, err
	}
	var (
		newSessionID uuid.UUID
		newAppID     *uuid.UUID
	)
	err = s.store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var old store.AuthSessionEntity
		if err := tx.Unscoped().
			Where("id = ? AND account_id = ?", sessionID, key.AccountId).
			First(&old).Error; err != nil {
			return errors.New("API key session was not found.")
		}
		// Expire old session + bump epoch.
		if err := tx.Unscoped().Model(&store.AuthSessionEntity{}).
			Where("id = ?", sessionID).
			Updates(map[string]any{
				"expired_at": now, "last_granted_at": now,
				"epoch": gorm.Expr("epoch + 1"), "updated_at": now,
			}).Error; err != nil {
			return err
		}
		// Create the replacement session mirroring the old one.
		replacement := store.AuthSessionEntity{
			ID:              uuid.New(),
			CreatedAt:       now,
			UpdatedAt:       now,
			AccountID:       old.AccountID,
			Type:            old.Type,
			AppID:           old.AppID,
			ClientID:        old.ClientID,
			ParentSessionID: old.ParentSessionID,
			Audiences:       old.Audiences,
			Scopes:          old.Scopes,
			IPAddress:       old.IPAddress,
			UserAgent:       old.UserAgent,
			Location:        old.Location,
			LastGrantedAt:   &now,
			ExpiredAt:       old.ExpiredAt,
			Epoch:           0,
		}
		if err := tx.Create(&replacement).Error; err != nil {
			return err
		}
		newSessionID = replacement.ID
		newAppID = old.AppID
		// Re-point the key at the new session.
		return tx.Unscoped().Model(&store.APIKeyEntity{}).
			Where("id = ?", key.Id).
			Updates(map[string]any{"session_id": newSessionID, "app_id": old.AppID, "updated_at": now}).Error
	})
	if err != nil {
		return nil, err
	}
	if s.redis != nil && s.redis.Available() {
		_ = s.redis.Cache.Remove(ctx, "auth:session:"+sessionID.String())
		_ = s.redis.Raw.Del(ctx, "auth:session_tokens:"+sessionID.String()).Err()
	}

	key.SessionId = newSessionID.String()
	key.AppId = uuidPtrToStr(newAppID)
	return key, nil
}

// --- Authorized apps ---

func mergeAuthorizedAppScopes(existing, requested []string) []string {
	if len(existing) == 0 && len(requested) == 0 {
		return []string{}
	}

	merged := make([]string, 0, len(existing)+len(requested))
	seen := make(map[string]struct{}, len(existing)+len(requested))
	for _, scope := range existing {
		merged = append(merged, scope)
		seen[scope] = struct{}{}
	}
	for _, scope := range requested {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if _, dup := seen[scope]; dup {
			continue
		}
		seen[scope] = struct{}{}
		merged = append(merged, scope)
	}
	return merged
}

// UpsertAuthorizedAppAsync creates or updates an authorized-app record.
func (s *AuthService) UpsertAuthorizedAppAsync(ctx context.Context, accountID, appID string, appType model.AuthorizedAppType, appSlug, appName *string, scopes []string) (*model.AuthorizedApp, error) {
	now := time.Now().UTC()
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return nil, err
	}
	appUUID, err := uuid.Parse(appID)
	if err != nil {
		return nil, err
	}
	var normalized []string
	seen := map[string]struct{}{}
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if _, dup := seen[scope]; !dup {
			seen[scope] = struct{}{}
			normalized = append(normalized, scope)
		}
	}
	var entity store.AuthorizedAppEntity
	err = s.store.DB.WithContext(ctx).
		Where("account_id = ? AND app_id = ? AND type = ?", accountUUID, appUUID, int(appType)).
		First(&entity).Error
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		created := store.AuthorizedAppEntity{
			ID:               uuid.New(),
			EntityBase:       store.EntityBase{CreatedAt: now, UpdatedAt: now},
			Type:             int(appType),
			AccountID:        accountUUID,
			AppID:            appUUID,
			AppSlug:          appSlug,
			AppName:          appName,
			Scopes:           datatypes.JSON(mustJSON(mergeAuthorizedAppScopes(nil, normalized))),
			LastAuthorizedAt: now,
			LastUsedAt:       &now,
		}
		if err := s.store.DB.WithContext(ctx).Create(&created).Error; err != nil {
			return nil, err
		}
		return authorizedAppModel(&created), nil
	}

	existing := authorizedAppModel(&entity)
	if appSlug != nil && *appSlug != "" {
		existing.AppSlug = appSlug
	}
	if appName != nil && *appName != "" {
		existing.AppName = appName
	}
	existing.Scopes = mergeAuthorizedAppScopes(existing.Scopes, normalized)
	existing.LastAuthorizedAt = model.NewTime(now)
	existing.LastUsedAt = model.NewTime(now)
	existing.UpdatedAt = model.NewTime(now)
	if err := s.store.DB.WithContext(ctx).Model(&store.AuthorizedAppEntity{}).
		Where("id = ?", entity.ID).
		Updates(map[string]any{
			"last_authorized_at": now, "last_used_at": now, "updated_at": now,
			"app_slug": existing.AppSlug, "app_name": existing.AppName,
			"scopes": datatypes.JSON(mustJSON(existing.Scopes)),
		}).Error; err != nil {
		return nil, err
	}
	return existing, nil
}

// RevokeAuthorizedAppAccessByIdAsync revokes access by authorized-app record id.
func (s *AuthService) RevokeAuthorizedAppAccessByIdAsync(ctx context.Context, accountID, recordID string, appType *model.AuthorizedAppType) (int, error) {
	query := s.store.DB.WithContext(ctx).Model(&store.AuthorizedAppEntity{}).
		Where("account_id = ? AND id = ?", accountID, recordID)
	if appType != nil {
		query = query.Where("type = ?", int(*appType))
	}
	var entity store.AuthorizedAppEntity
	if err := query.First(&entity).Error; err != nil {
		return 0, nil
	}
	return s.RevokeAuthorizedAppAccessAsync(ctx, accountID, entity.AppID.String(), appType)
}

// RevokeAuthorizedAppAccessAsync soft-deletes authorized apps and revokes
// their sessions and API keys.
func (s *AuthService) RevokeAuthorizedAppAccessAsync(ctx context.Context, accountID, appID string, appType *model.AuthorizedAppType) (int, error) {
	now := time.Now().UTC()
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return 0, err
	}
	appUUID, err := uuid.Parse(appID)
	if err != nil {
		return 0, err
	}
	// Unscoped with an explicit deleted_at IS NULL: the statement sets
	// deleted_at itself, so GORM's soft-delete filter must not be relied on.
	update := s.store.DB.WithContext(ctx).Unscoped().Model(&store.AuthorizedAppEntity{}).
		Where("account_id = ? AND app_id = ? AND deleted_at IS NULL", accountUUID, appUUID)
	if appType != nil {
		update = update.Where("type = ?", int(*appType))
	}
	res := update.Updates(map[string]any{"deleted_at": now, "last_used_at": now, "updated_at": now})
	if res.Error != nil {
		return 0, res.Error
	}
	count := int(res.RowsAffected)
	if count == 0 {
		return 0, nil
	}

	// Revoke the app's sessions.
	var sessionIDs []uuid.UUID
	if err := s.store.DB.WithContext(ctx).Model(&store.AuthSessionEntity{}).Select("id").
		Where("account_id = ? AND app_id = ? AND (expired_at IS NULL OR expired_at > ?)", accountUUID, appUUID, now).
		Find(&sessionIDs).Error; err != nil {
		return count, err
	}
	for _, id := range sessionIDs {
		_, _ = s.RevokeSession(ctx, id)
	}

	// Revoke the app's API keys.
	var keyEntities []store.APIKeyEntity
	if err := s.store.DB.WithContext(ctx).
		Where("account_id = ? AND app_id = ?", accountUUID, appUUID).
		Find(&keyEntities).Error; err != nil {
		return count, err
	}
	for _, entity := range keyEntities {
		key := apiKeyModel(&entity)
		key.AccountId = accountID
		_ = s.RevokeApiKeyToken(ctx, key)
	}

	if s.logs != nil {
		typeText := ""
		if appType != nil {
			typeText = model.AuthorizedAppType(*appType).String()
		}
		_ = s.logs.Create(ctx, accountID, model.ActionLogAuthorizedAppDeauthorize, map[string]any{
			"app_id": appID,
			"count":  count,
			"type":   typeText,
		}, "", "", nil, nil)
	}
	return count, nil
}

func uuidPtr(s *string) *uuid.UUID {
	if s == nil {
		return nil
	}
	id, err := uuid.Parse(*s)
	if err != nil {
		return nil
	}
	return &id
}

func uuidPtrToStr(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	v := id.String()
	return &v
}
