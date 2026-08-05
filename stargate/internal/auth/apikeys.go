package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// --- API keys (port of AuthService ApiKey methods) ---

// GetApiKey loads an API key with its session; scoped to an account when
// accountID is non-nil.
func (s *AuthService) GetApiKey(ctx context.Context, id uuid.UUID, accountID *uuid.UUID) (*model.ApiKey, error) {
	var key model.ApiKey
	var sessionID uuid.UUID
	var appID *uuid.UUID
	var deletedAt *time.Time
	var expiredAt *time.Time
	query := `SELECT id, label, account_id, app_id, session_id, created_at, updated_at, deleted_at
		FROM api_keys WHERE id = $1`
	args := []any{id}
	if accountID != nil {
		query += ` AND account_id = $2`
		args = append(args, *accountID)
	}
	err := s.store.DB.QueryRow(ctx, query, args...).Scan(
		&key.Id, &key.Label, &key.AccountId, &appID, &sessionID, &key.CreatedAt, &key.UpdatedAt, &deletedAt)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) || err.Error() == "no rows in result set" {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	key.SessionId = sessionID.String()
	key.AppId = uuidPtrToStr(appID)
	if deletedAt != nil {
		key.DeletedAt = model.NewTime(*deletedAt)
	}
	_ = expiredAt
	return &key, nil
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
	var appID *uuid.UUID
	var parentID *string
	if parentSession != nil {
		appID = uuidPtr(parentSession.AppId)
		parentID = &parentSession.Id
	}

	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var sessionID uuid.UUID
	err = tx.QueryRow(ctx, `INSERT INTO auth_sessions
		(id, type, created_at, last_granted_at, expired_at, account_id, app_id, parent_session_id, epoch, updated_at)
		VALUES (gen_random_uuid(),$1,$2,$2,$3,$4,$5,$6,0,$2) RETURNING id`,
		int(model.SessionTypeApiKey), now, expiredAt, accountID, appID, parentID).Scan(&sessionID)
	if err != nil {
		return nil, err
	}
	var keyID uuid.UUID
	err = tx.QueryRow(ctx, `INSERT INTO api_keys (id, label, account_id, app_id, session_id, created_at, updated_at)
		VALUES (gen_random_uuid(),$1,$2,$3,$4,$5,$5) RETURNING id`,
		normalized, accountID, appID, sessionID).Scan(&keyID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
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
	tag, err := s.store.DB.Exec(ctx, `UPDATE auth_sessions SET last_granted_at = $1, updated_at = $1
		WHERE id = $2 AND (expired_at IS NULL OR expired_at > $1)`, now, sessionID)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", errors.New("API key session has expired or does not exist.")
	}
	session, err := s.store.GetSessionWithAccount(ctx, sessionID)
	if err != nil {
		return "", err
	}
	version, err := s.token.GetAccountVersion(ctx, key.AccountId)
	if err != nil {
		return "", err
	}
	return s.jwt.CreateBotToken(key, session, version)
}

// RevokeApiKeyToken soft-deletes the key and revokes its session.
func (s *AuthService) RevokeApiKeyToken(ctx context.Context, key *model.ApiKey) error {
	now := time.Now().UTC()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE api_keys SET deleted_at = $1, updated_at = $1 WHERE id = $2`, now, key.Id); err != nil {
		return err
	}
	if _, err := s.token.BumpAccountVersion(ctx, key.AccountId); err != nil {
		return err
	}
	sessionID, err := uuid.Parse(key.SessionId)
	if err != nil {
		return err
	}
	if _, err := s.RevokeSession(ctx, sessionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RotateApiKeyToken rotates the key to a fresh session (old tokens die via
// epoch bump).
func (s *AuthService) RotateApiKeyToken(ctx context.Context, key *model.ApiKey) (*model.ApiKey, error) {
	now := time.Now().UTC()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	sessionID, err := uuid.Parse(key.SessionId)
	if err != nil {
		return nil, err
	}
	var (
		oldType      int
		oldAppID     *uuid.UUID
		oldClientID  *uuid.UUID
		oldParentID  *uuid.UUID
		oldExpiry    *time.Time
		oldAudiences []string
		oldScopes    []string
		oldIP, oldUA *string
		oldLocation  []byte
	)
	err = tx.QueryRow(ctx, `SELECT type, app_id, client_id, parent_session_id, expired_at, audiences, scopes, ip_address, user_agent, location
		FROM auth_sessions WHERE id = $1 AND account_id = $2`, sessionID, key.AccountId).Scan(
		&oldType, &oldAppID, &oldClientID, &oldParentID, &oldExpiry, &oldAudiences, &oldScopes, &oldIP, &oldUA, &oldLocation)
	if err != nil {
		return nil, errors.New("API key session was not found.")
	}
	// Expire old session + bump epoch.
	if _, err := tx.Exec(ctx, `UPDATE auth_sessions SET expired_at = $1, last_granted_at = $1, epoch = epoch + 1, updated_at = $1 WHERE id = $2`, now, sessionID); err != nil {
		return nil, err
	}
	// Create the replacement session.
	var newSessionID uuid.UUID
	err = tx.QueryRow(ctx, `INSERT INTO auth_sessions
		(id, type, created_at, last_granted_at, expired_at, account_id, app_id, client_id, parent_session_id,
		 audiences, scopes, ip_address, user_agent, location, epoch, updated_at)
		VALUES (gen_random_uuid(),$1,$2,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,0,$2) RETURNING id`,
		oldType, now, oldExpiry, key.AccountId, oldAppID, oldClientID, oldParentID,
		oldAudiences, oldScopes, oldIP, oldUA, oldLocation).Scan(&newSessionID)
	if err != nil {
		return nil, err
	}
	// Re-point the key at the new session.
	if _, err := tx.Exec(ctx, `UPDATE api_keys SET session_id = $1, app_id = $2, updated_at = $3 WHERE id = $4`,
		newSessionID, oldAppID, now, key.Id); err != nil {
		return nil, err
	}
	if _, err := s.token.BumpAccountVersion(ctx, key.AccountId); err != nil {
		return nil, err
	}
	_ = s.redis.Cache.Remove(ctx, "auth:session:"+sessionID.String())
	_ = s.redis.Raw.Del(ctx, "auth:session_tokens:"+sessionID.String()).Err()
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	key.SessionId = newSessionID.String()
	key.AppId = uuidPtrToStr(oldAppID)
	return key, nil
}

// --- Authorized apps ---

// UpsertAuthorizedAppAsync creates or updates an authorized-app record.
func (s *AuthService) UpsertAuthorizedAppAsync(ctx context.Context, accountID, appID string, appType model.AuthorizedAppType, appSlug, appName *string, scopes []string) (*model.AuthorizedApp, error) {
	now := time.Now().UTC()
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
	var existing model.AuthorizedApp
	var slug, name *string
	err := s.store.DB.QueryRow(ctx, `SELECT id, app_slug, app_name, scopes FROM authorized_apps
		WHERE account_id = $1 AND app_id = $2 AND type = $3 AND deleted_at IS NULL`,
		accountID, appID, int(appType)).Scan(&existing.Id, &slug, &name, &existing.Scopes)
	if err != nil {
		// Create.
		existing = model.AuthorizedApp{
			Type:             appType,
			AccountId:        accountID,
			AppId:            appID,
			AppSlug:          appSlug,
			AppName:          appName,
			Scopes:           normalized,
			LastAuthorizedAt: model.NewTime(now),
			LastUsedAt:       model.NewTime(now),
		}
		var id string
		if err := s.store.DB.QueryRow(ctx, `INSERT INTO authorized_apps
			(type, account_id, app_id, app_slug, app_name, scopes, last_authorized_at, last_used_at, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$7,$8,$8) RETURNING id`,
			int(appType), accountID, appID, appSlug, appName, normalized, now, now).Scan(&id); err != nil {
			return nil, err
		}
		existing.Id = id
		existing.CreatedAt = model.NewTime(now)
		existing.UpdatedAt = model.NewTime(now)
		return &existing, nil
	}
	// Update.
	existing.AppSlug = slug
	existing.AppName = name
	if appSlug != nil && *appSlug != "" {
		existing.AppSlug = appSlug
	}
	if appName != nil && *appName != "" {
		existing.AppName = appName
	}
	if normalized != nil {
		existing.Scopes = normalized
	}
	existing.LastAuthorizedAt = model.NewTime(now)
	existing.LastUsedAt = model.NewTime(now)
	existing.UpdatedAt = model.NewTime(now)
	_, err = s.store.DB.Exec(ctx, `UPDATE authorized_apps SET last_authorized_at = $1, last_used_at = $1, updated_at = $1,
		app_slug = $2, app_name = $3, scopes = $4 WHERE id = $5`,
		now, existing.AppSlug, existing.AppName, existing.Scopes, existing.Id)
	if err != nil {
		return nil, err
	}
	return &existing, nil
}

// RevokeAuthorizedAppAccessByIdAsync revokes access by authorized-app record id.
func (s *AuthService) RevokeAuthorizedAppAccessByIdAsync(ctx context.Context, accountID, recordID string, appType *model.AuthorizedAppType) (int, error) {
	query := `SELECT app_id FROM authorized_apps WHERE account_id = $1 AND id = $2 AND deleted_at IS NULL`
	args := []any{accountID, recordID}
	if appType != nil {
		query += ` AND type = $3`
		args = append(args, int(*appType))
	}
	var appID string
	err := s.store.DB.QueryRow(ctx, query, args...).Scan(&appID)
	if err != nil {
		return 0, nil
	}
	return s.RevokeAuthorizedAppAccessAsync(ctx, accountID, appID, appType)
}

// RevokeAuthorizedAppAccessAsync soft-deletes authorized apps and revokes
// their sessions and API keys.
func (s *AuthService) RevokeAuthorizedAppAccessAsync(ctx context.Context, accountID, appID string, appType *model.AuthorizedAppType) (int, error) {
	now := time.Now().UTC()
	query := `UPDATE authorized_apps SET deleted_at = $1, last_used_at = $1, updated_at = $1
		WHERE account_id = $2 AND app_id = $3 AND deleted_at IS NULL`
	args := []any{now, accountID, appID}
	if appType != nil {
		query += ` AND type = $4`
		args = append(args, int(*appType))
	}
	tag, err := s.store.DB.Exec(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	count := int(tag.RowsAffected())
	if count == 0 {
		return 0, nil
	}

	// Revoke the app's sessions.
	sessionRows, err := s.store.DB.Query(ctx, `SELECT id FROM auth_sessions
		WHERE account_id = $1 AND app_id = $2 AND (expired_at IS NULL OR expired_at > $3)`, accountID, appID, now)
	if err != nil {
		return count, err
	}
	var sessionIDs []uuid.UUID
	for sessionRows.Next() {
		var id uuid.UUID
		if err := sessionRows.Scan(&id); err != nil {
			sessionRows.Close()
			return count, err
		}
		sessionIDs = append(sessionIDs, id)
	}
	sessionRows.Close()
	for _, id := range sessionIDs {
		_, _ = s.RevokeSession(ctx, id)
	}

	// Revoke the app's API keys.
	keyRows, err := s.store.DB.Query(ctx, `SELECT id, session_id FROM api_keys
		WHERE account_id = $1 AND app_id = $2 AND deleted_at IS NULL`, accountID, appID)
	if err != nil {
		return count, err
	}
	var keys []*model.ApiKey
	for keyRows.Next() {
		var id, sessionID uuid.UUID
		if err := keyRows.Scan(&id, &sessionID); err != nil {
			keyRows.Close()
			return count, err
		}
		keys = append(keys, &model.ApiKey{Id: id.String(), SessionId: sessionID.String(), AccountId: accountID})
	}
	keyRows.Close()
	for _, key := range keys {
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
