package store

// Security-domain store helpers for the AccountSecurityController /
// ApiKeyController / ConnectionController port (internal/httpserver/securityctl).
// Query shapes mirror the EF Core LINQ in the C# controllers exactly,
// including which soft-delete filters are (or are not) applied.

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// --- Sessions ---

// ListSessions lists the account's sessions with pagination. When
// includeChildren is false only root sessions (no parent) are returned,
// mirroring GetSessions: by default includeChildren=false shows roots only.
func (s *Store) ListSessions(ctx context.Context, accountID string, typ *model.SessionType, clientID *uuid.UUID, includeChildren bool, take, offset int) ([]model.AuthSession, int, error) {
	// The legacy query carried no deleted_at filter, so the scoped statement
	// stays Unscoped.
	base := func() *gorm.DB {
		query := s.DB.WithContext(ctx).Unscoped().Model(&AuthSessionEntity{}).
			Where("account_id = ?", accountID)
		if !includeChildren {
			query = query.Where("parent_session_id IS NULL")
		}
		if typ != nil {
			query = query.Where("type = ?", int(*typ))
		}
		if clientID != nil {
			query = query.Where("client_id = ?", *clientID)
		}
		return query
	}

	var total int64
	if err := base().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var entities []AuthSessionEntity
	if err := base().
		Order("last_granted_at DESC NULLS LAST").
		Limit(take).Offset(offset).
		Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	var sessions []model.AuthSession
	for i := range entities {
		sessions = append(sessions, *sessionFromEntity(&entities[i]))
	}
	if err := s.fillChildrenCounts(ctx, sessions); err != nil {
		return nil, 0, err
	}
	return sessions, int(total), nil
}

// ListSessionChildren lists the direct children of a parent session with
// pagination (GetSessionChildren).
func (s *Store) ListSessionChildren(ctx context.Context, accountID string, parentID uuid.UUID, take, offset int) ([]model.AuthSession, int, error) {
	where := func(query *gorm.DB) *gorm.DB {
		return query.Where("parent_session_id = ? AND account_id = ?", parentID, accountID)
	}

	var total int64
	if err := where(s.DB.WithContext(ctx).Unscoped().Model(&AuthSessionEntity{})).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var entities []AuthSessionEntity
	if err := where(s.DB.WithContext(ctx).Unscoped().Model(&AuthSessionEntity{})).
		Order("created_at DESC").
		Limit(take).Offset(offset).
		Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	var children []model.AuthSession
	for i := range entities {
		children = append(children, *sessionFromEntity(&entities[i]))
	}
	if err := s.fillChildrenCounts(ctx, children); err != nil {
		return nil, 0, err
	}
	return children, int(total), nil
}

// GetOwnedSession loads a session scoped to an account (DeleteSession /
// GetSessionChildren parent check).
func (s *Store) GetOwnedSession(ctx context.Context, accountID string, id uuid.UUID) (*model.AuthSession, error) {
	var entity AuthSessionEntity
	if err := s.DB.WithContext(ctx).Unscoped().
		Where("id = ? AND account_id = ?", id, accountID).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return sessionFromEntity(&entity), nil
}

func (s *Store) fillChildrenCounts(ctx context.Context, sessions []model.AuthSession) error {
	if len(sessions) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(sessions))
	for _, session := range sessions {
		id, err := uuid.Parse(session.Id)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	// The legacy query filtered on parent_session_id only (no deleted_at
	// filter), so it counts soft-deleted children too.
	var rows []struct {
		ParentSessionID uuid.UUID `gorm:"column:parent_session_id"`
		Children        int       `gorm:"column:children"`
	}
	if err := s.DB.WithContext(ctx).Unscoped().Model(&AuthSessionEntity{}).
		Select("parent_session_id, COUNT(*) AS children").
		Where("parent_session_id IN ?", ids).
		Group("parent_session_id").
		Scan(&rows).Error; err != nil {
		return err
	}
	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		counts[row.ParentSessionID.String()] = row.Children
	}
	for i := range sessions {
		if count, ok := counts[sessions[i].Id]; ok {
			sessions[i].ChildrenCount = &count
		}
	}
	return nil
}

// --- Devices ---

// ListDevices lists the account's devices with pagination (GetDevices).
func (s *Store) ListDevices(ctx context.Context, accountID string, take, offset int) ([]model.AuthClient, int, error) {
	var total int64
	if err := s.DB.WithContext(ctx).Model(&AuthClientEntity{}).
		Where("account_id = ?", accountID).
		Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var entities []AuthClientEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("created_at DESC").
		Limit(take).Offset(offset).
		Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	var devices []model.AuthClient
	for i := range entities {
		devices = append(devices, authClientFromEntity(&entities[i]))
	}
	return devices, int(total), nil
}

// ListSessionsByClientIDs loads sessions grouped by client id for the given
// devices (the sessions attached to SnAuthClientWithSessions).
func (s *Store) ListSessionsByClientIDs(ctx context.Context, clientIDs []uuid.UUID) (map[string][]model.AuthSession, error) {
	if len(clientIDs) == 0 {
		return map[string][]model.AuthSession{}, nil
	}
	var entities []AuthSessionEntity
	if err := s.DB.WithContext(ctx).Unscoped().
		Where("client_id IN ?", clientIDs).
		Find(&entities).Error; err != nil {
		return nil, err
	}
	grouped := map[string][]model.AuthSession{}
	for i := range entities {
		session := sessionFromEntity(&entities[i])
		if session.ClientId == nil {
			continue
		}
		grouped[*session.ClientId] = append(grouped[*session.ClientId], *session)
	}
	return grouped, nil
}

// ListClientsByAccountIDs loads every non-deleted auth client for the given
// accounts, grouped by account id (device presence).
func (s *Store) ListClientsByAccountIDs(ctx context.Context, accountIDs []uuid.UUID) (map[string][]model.AuthClient, error) {
	if len(accountIDs) == 0 {
		return map[string][]model.AuthClient{}, nil
	}
	var entities []AuthClientEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id IN ?", accountIDs).
		Order("created_at DESC").
		Find(&entities).Error; err != nil {
		return nil, err
	}
	grouped := map[string][]model.AuthClient{}
	for i := range entities {
		client := authClientFromEntity(&entities[i])
		grouped[client.AccountId] = append(grouped[client.AccountId], client)
	}
	return grouped, nil
}

// GetClientByDeviceID loads a device by (account_id, device_id).
func (s *Store) GetClientByDeviceID(ctx context.Context, accountID, deviceID string) (*model.AuthClient, error) {
	var entity AuthClientEntity
	if err := s.DB.WithContext(ctx).Unscoped().
		Where("account_id = ? AND device_id = ?", accountID, deviceID).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	client := authClientFromEntity(&entity)
	return &client, nil
}

// GetClientByID loads a device by id (UpdateCurrentDeviceLabel).
func (s *Store) GetClientByID(ctx context.Context, id uuid.UUID) (*model.AuthClient, error) {
	var entity AuthClientEntity
	if err := s.DB.WithContext(ctx).Unscoped().
		Where("id = ?", id).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	client := authClientFromEntity(&entity)
	return &client, nil
}

// DeleteDevice expires every session bound to the device and soft-deletes
// the device (AccountService.DeleteDevice).
func (s *Store) DeleteDevice(ctx context.Context, accountID, deviceID string, now time.Time) error {
	var client AuthClientEntity
	if err := s.DB.WithContext(ctx).Unscoped().
		Select("id").
		Where("account_id = ? AND device_id = ?", accountID, deviceID).
		First(&client).Error; err != nil {
		return mapNotFound(err)
	}
	// Both legacy updates ran without a deleted_at filter.
	if err := s.DB.WithContext(ctx).Unscoped().Model(&AuthSessionEntity{}).
		Where("client_id = ?", client.ID).
		Updates(map[string]any{"expired_at": now, "updated_at": now}).Error; err != nil {
		return err
	}
	return s.DB.WithContext(ctx).Unscoped().Model(&AuthClientEntity{}).
		Where("id = ?", client.ID).
		Updates(map[string]any{"deleted_at": now, "updated_at": now}).Error
}

// UpdateDeviceName renames the device's display name (UpdateDeviceName).
func (s *Store) UpdateDeviceName(ctx context.Context, accountID, deviceID, label string) error {
	res := s.DB.WithContext(ctx).Unscoped().Model(&AuthClientEntity{}).
		Where("account_id = ? AND device_id = ?", accountID, deviceID).
		Updates(map[string]any{"device_name": label, "updated_at": time.Now().UTC()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Auth factors ---

// ListAllFactors lists every auth factor of the account (GetAuthFactors; no
// soft-delete filter, matching the C# query).
func (s *Store) ListAllFactors(ctx context.Context, accountID uuid.UUID) ([]model.AuthFactor, error) {
	var entities []AuthFactorEntity
	if err := s.DB.WithContext(ctx).Unscoped().
		Where("account_id = ?", accountID).
		Order("created_at").
		Find(&entities).Error; err != nil {
		return nil, err
	}
	var factors []model.AuthFactor
	for i := range entities {
		factors = append(factors, factorFromEntity(&entities[i]))
	}
	return factors, nil
}

// GetAuthFactorByID loads one of the account's factors.
func (s *Store) GetAuthFactorByID(ctx context.Context, accountID string, id uuid.UUID) (*model.AuthFactor, error) {
	var entity AuthFactorEntity
	if err := s.DB.WithContext(ctx).Unscoped().
		Where("account_id = ? AND id = ?", accountID, id).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	factor := factorFromEntity(&entity)
	return &factor, nil
}

// CheckAuthFactorExists reports whether the account already has a factor of
// the given type (CheckAuthFactorExists).
func (s *Store) CheckAuthFactorExists(ctx context.Context, accountID string, ftype model.AuthFactorType) (bool, error) {
	var count int64
	if err := s.DB.WithContext(ctx).Unscoped().Model(&AuthFactorEntity{}).
		Where("account_id = ? AND type = ?", accountID, int(ftype)).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// InsertAuthFactor persists a new factor row.
func (s *Store) InsertAuthFactor(ctx context.Context, f *model.AuthFactor) (*model.AuthFactor, error) {
	now := time.Now().UTC()
	accountID, err := uuid.Parse(f.AccountId)
	if err != nil {
		return nil, err
	}
	var config *datatypes.JSON
	if len(f.Config) > 0 {
		if encoded, err := encodeJSON(f.Config); err == nil {
			config = &encoded
		}
	}
	var secret *string
	if f.Secret != "" {
		secret = &f.Secret
	}
	entity := &AuthFactorEntity{
		ID: uuid.New(),
		EntityBase: EntityBase{
			CreatedAt: now,
			UpdatedAt: now,
		},
		AccountID:   accountID,
		Config:      config,
		EnabledAt:   timeValue(f.EnabledAt),
		ExpiredAt:   timeValue(f.ExpiredAt),
		Secret:      secret,
		Trustworthy: f.Trustworthy,
		Type:        int(f.Type),
	}
	if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
		return nil, err
	}
	f.Id = entity.ID.String()
	f.CreatedAt = model.NewTime(now)
	f.UpdatedAt = model.NewTime(now)
	return f, nil
}

// UpdateAuthFactor persists the mutable factor columns.
func (s *Store) UpdateAuthFactor(ctx context.Context, f *model.AuthFactor) error {
	var config *datatypes.JSON
	if len(f.Config) > 0 {
		if encoded, err := encodeJSON(f.Config); err == nil {
			config = &encoded
		}
	}
	var secret *string
	if f.Secret != "" {
		secret = &f.Secret
	}
	return s.DB.WithContext(ctx).Unscoped().Model(&AuthFactorEntity{}).
		Where("id = ?", f.Id).
		Updates(map[string]any{
			"secret":      secret,
			"config":      config,
			"trustworthy": f.Trustworthy,
			"enabled_at":  timeValue(f.EnabledAt),
			"expired_at":  timeValue(f.ExpiredAt),
			"updated_at":  time.Now().UTC(),
		}).Error
}

// DeleteAuthFactorRow hard-deletes a factor row (DeleteAuthFactor).
func (s *Store) DeleteAuthFactorRow(ctx context.Context, id uuid.UUID) error {
	return s.DB.WithContext(ctx).Unscoped().
		Where("id = ?", id).
		Delete(&AuthFactorEntity{}).Error
}

// DeletePasskeysByAccount deletes every passkey of the account (used when the
// Passkey factor itself is deleted).
func (s *Store) DeletePasskeysByAccount(ctx context.Context, accountID string) error {
	return s.DB.WithContext(ctx).Unscoped().
		Where("account_id = ?", accountID).
		Delete(&PasskeyEntity{}).Error
}

// --- Passkeys ---

// ListPasskeys lists the account's passkeys ordered by creation time.
func (s *Store) ListPasskeys(ctx context.Context, accountID string) ([]model.Passkey, error) {
	var entities []PasskeyEntity
	if err := s.DB.WithContext(ctx).Unscoped().
		Where("account_id = ?", accountID).
		Order("created_at").
		Find(&entities).Error; err != nil {
		return nil, err
	}
	var passkeys []model.Passkey
	for i := range entities {
		passkeys = append(passkeys, passkeyFromEntity(&entities[i]))
	}
	return passkeys, nil
}

// GetPasskeyByID loads one of the account's passkeys.
func (s *Store) GetPasskeyByID(ctx context.Context, accountID string, id uuid.UUID) (*model.Passkey, error) {
	var entity PasskeyEntity
	if err := s.DB.WithContext(ctx).Unscoped().
		Where("id = ? AND account_id = ?", id, accountID).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	passkey := passkeyFromEntity(&entity)
	return &passkey, nil
}

// PasskeyCredentialIDExists checks the partial-unique credential id index.
func (s *Store) PasskeyCredentialIDExists(ctx context.Context, credentialID string) (bool, error) {
	var count int64
	if err := s.DB.WithContext(ctx).Model(&PasskeyEntity{}).
		Where("credential_id = ?", credentialID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// InsertPasskey persists a passkey row.
func (s *Store) InsertPasskey(ctx context.Context, p *model.Passkey) (*model.Passkey, error) {
	now := time.Now().UTC()
	accountID, err := uuid.Parse(p.AccountId)
	if err != nil {
		return nil, err
	}
	entity := &PasskeyEntity{
		ID: uuid.New(),
		EntityBase: EntityBase{
			CreatedAt: now,
			UpdatedAt: now,
		},
		AccountID:    accountID,
		Credential:   datatypes.JSON(p.Credential),
		CredentialID: p.CredentialId,
		Label:        p.Label,
	}
	if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
		return nil, err
	}
	p.Id = entity.ID.String()
	p.CreatedAt = model.NewTime(now)
	p.UpdatedAt = model.NewTime(now)
	return p, nil
}

// UpdatePasskeyLabel renames the passkey.
func (s *Store) UpdatePasskeyLabel(ctx context.Context, id uuid.UUID, label string) (*model.Passkey, error) {
	res := s.DB.WithContext(ctx).Unscoped().Model(&PasskeyEntity{}).
		Where("id = ?", id).
		Updates(map[string]any{"label": label, "updated_at": time.Now().UTC()})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	var entity PasskeyEntity
	if err := s.DB.WithContext(ctx).Unscoped().Where("id = ?", id).First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	passkey := passkeyFromEntity(&entity)
	return &passkey, nil
}

// DeletePasskeyRow hard-deletes a passkey row.
func (s *Store) DeletePasskeyRow(ctx context.Context, id uuid.UUID) error {
	return s.DB.WithContext(ctx).Unscoped().
		Where("id = ?", id).
		Delete(&PasskeyEntity{}).Error
}

// --- Contacts ---

// ListContacts lists the account's contact methods.
func (s *Store) ListContacts(ctx context.Context, accountID string) ([]model.Contact, error) {
	var entities []ContactEntity
	if err := s.DB.WithContext(ctx).Unscoped().
		Where("account_id = ?", accountID).
		Find(&entities).Error; err != nil {
		return nil, err
	}
	var contacts []model.Contact
	for i := range entities {
		contacts = append(contacts, contactFromEntity(&entities[i]))
	}
	return contacts, nil
}

// GetContactByID loads one of the account's contacts.
func (s *Store) GetContactByID(ctx context.Context, accountID string, id uuid.UUID) (*model.Contact, error) {
	var entity ContactEntity
	if err := s.DB.WithContext(ctx).Unscoped().
		Where("id = ? AND account_id = ?", id, accountID).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	contact := contactFromEntity(&entity)
	return &contact, nil
}

// InsertContact persists a contact method (CreateContactMethod).
func (s *Store) InsertContact(ctx context.Context, c *model.Contact) (*model.Contact, error) {
	now := time.Now().UTC()
	accountID, err := uuid.Parse(c.AccountId)
	if err != nil {
		return nil, err
	}
	entity := &ContactEntity{
		ID: uuid.New(),
		EntityBase: EntityBase{
			CreatedAt: now,
			UpdatedAt: now,
		},
		AccountID: accountID,
		Content:   c.Content,
		IsPrimary: c.IsPrimary,
		IsPublic:  c.IsPublic,
		Type:      c.Type,
	}
	if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
		return nil, err
	}
	c.Id = entity.ID.String()
	c.CreatedAt = model.NewTime(now)
	c.UpdatedAt = model.NewTime(now)
	return c, nil
}

// UpdateContact persists contact flag columns.
func (s *Store) UpdateContact(ctx context.Context, c *model.Contact) error {
	return s.DB.WithContext(ctx).Unscoped().Model(&ContactEntity{}).
		Where("id = ?", c.Id).
		Updates(map[string]any{
			"is_primary":  c.IsPrimary,
			"is_public":   c.IsPublic,
			"verified_at": timeValue(c.VerifiedAt),
			"updated_at":  time.Now().UTC(),
		}).Error
}

// SetContactPrimary unmarks the other same-type contacts and marks the given
// one primary (SetContactMethodPrimary).
func (s *Store) SetContactPrimary(ctx context.Context, accountID string, ctype int, id uuid.UUID) error {
	if err := s.DB.WithContext(ctx).Unscoped().Model(&ContactEntity{}).
		Where("account_id = ? AND type = ?", accountID, ctype).
		Updates(map[string]any{"is_primary": false, "updated_at": time.Now().UTC()}).Error; err != nil {
		return err
	}
	return s.DB.WithContext(ctx).Unscoped().Model(&ContactEntity{}).
		Where("id = ?", id).
		Updates(map[string]any{"is_primary": true, "updated_at": time.Now().UTC()}).Error
}

// DeleteContactRow hard-deletes a contact method.
func (s *Store) DeleteContactRow(ctx context.Context, id uuid.UUID) error {
	return s.DB.WithContext(ctx).Unscoped().
		Where("id = ?", id).
		Delete(&ContactEntity{}).Error
}

// --- Authorized apps ---

// ListAuthorizedApps lists the account's authorized apps, optionally filtered
// by type, ordered by last used (falling back to last authorized).
func (s *Store) ListAuthorizedApps(ctx context.Context, accountID string, typ *model.AuthorizedAppType, take, offset int) ([]model.AuthorizedApp, int, error) {
	base := func() *gorm.DB {
		query := s.DB.WithContext(ctx).Model(&AuthorizedAppEntity{}).
			Where("account_id = ?", accountID)
		if typ != nil {
			query = query.Where("type = ?", int(*typ))
		}
		return query
	}

	var total int64
	if err := base().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var entities []AuthorizedAppEntity
	if err := base().
		Order("COALESCE(last_used_at, last_authorized_at) DESC").
		Limit(take).Offset(offset).
		Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	var apps []model.AuthorizedApp
	for i := range entities {
		entity := &entities[i]
		app := model.AuthorizedApp{
			Id:               entity.ID.String(),
			Type:             model.AuthorizedAppType(entity.Type),
			AccountId:        entity.AccountID.String(),
			AppId:            entity.AppID.String(),
			AppSlug:          entity.AppSlug,
			AppName:          entity.AppName,
			LastAuthorizedAt: timePtr(&entity.LastAuthorizedAt),
			LastUsedAt:       timePtr(entity.LastUsedAt),
			CreatedAt:        timePtr(&entity.CreatedAt),
			UpdatedAt:        timePtr(&entity.UpdatedAt),
			DeletedAt:        deletedTime(entity.DeletedAt),
		}
		_ = decodeJSONValue(entity.Scopes, &app.Scopes)
		apps = append(apps, app)
	}
	return apps, int(total), nil
}

// --- API keys ---

// ApiKeyWithExpiry is the wire row for GET /api/api-keys: the key fields plus
// the backing session's expiry (k.Session.ExpiredAt in the C#).
type ApiKeyWithExpiry struct {
	Id        string
	Label     string
	AppId     *string
	CreatedAt *model.Time
	ExpiredAt *model.Time
}

// ListApiKeys lists the account's non-deleted API keys with their session
// expiry, mirroring ListApiKeys.
func (s *Store) ListApiKeys(ctx context.Context, accountID string) ([]ApiKeyWithExpiry, error) {
	var keys []APIKeyEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ?", accountID).
		Find(&keys).Error; err != nil {
		return nil, err
	}
	// The legacy LEFT JOIN read session expiry without a deleted_at filter on
	// auth_sessions, so the batch load stays Unscoped. A missing session (or
	// missing row) leaves ExpiredAt nil, matching the LEFT JOIN.
	sessionIDs := make([]uuid.UUID, 0, len(keys))
	for i := range keys {
		sessionIDs = append(sessionIDs, keys[i].SessionID)
	}
	sessions := make(map[uuid.UUID]AuthSessionEntity, len(sessionIDs))
	if len(sessionIDs) > 0 {
		var entities []AuthSessionEntity
		if err := s.DB.WithContext(ctx).Unscoped().
			Where("id IN ?", sessionIDs).
			Find(&entities).Error; err != nil {
			return nil, err
		}
		for i := range entities {
			sessions[entities[i].ID] = entities[i]
		}
	}
	var result []ApiKeyWithExpiry
	for i := range keys {
		key := ApiKeyWithExpiry{
			Id:        keys[i].ID.String(),
			Label:     keys[i].Label,
			AppId:     uuidPtrStr(keys[i].AppID),
			CreatedAt: timePtr(&keys[i].CreatedAt),
		}
		if session, ok := sessions[keys[i].SessionID]; ok {
			key.ExpiredAt = timePtr(session.ExpiredAt)
		}
		result = append(result, key)
	}
	return result, nil
}

// --- helpers ---

// --- Security mode ---

// GetSecurityMode loads the per-account security mode.
func (s *Store) GetSecurityMode(ctx context.Context, accountID string) (model.SecurityMode, error) {
	// accounts.security_mode is not mapped on AccountEntity; select it
	// directly. The legacy lookup had no deleted_at filter.
	var mode int
	res := s.DB.WithContext(ctx).Unscoped().Model(&AccountEntity{}).
		Select("security_mode").
		Where("id = ?", accountID).
		Scan(&mode)
	if res.Error != nil {
		return model.SecurityModeDefault, res.Error
	}
	if res.RowsAffected == 0 {
		return model.SecurityModeDefault, ErrNotFound
	}
	return model.SecurityMode(mode), nil
}

// SetSecurityMode persists the per-account security mode.
func (s *Store) SetSecurityMode(ctx context.Context, accountID string, mode model.SecurityMode) error {
	res := s.DB.WithContext(ctx).Unscoped().Model(&AccountEntity{}).
		Where("id = ?", accountID).
		Updates(map[string]any{"security_mode": int(mode), "updated_at": gorm.Expr("now()")})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// GetClientPlatformsByIDs loads platform values for the given client IDs in a
// single query, avoiding N+1 when annotating sessions/devices.
func (s *Store) GetClientPlatformsByIDs(ctx context.Context, ids []uuid.UUID) (map[string]model.ClientPlatform, error) {
	if len(ids) == 0 {
		return map[string]model.ClientPlatform{}, nil
	}
	var entities []AuthClientEntity
	if err := s.DB.WithContext(ctx).Unscoped().
		Select("id, platform").
		Where("id IN ?", ids).
		Find(&entities).Error; err != nil {
		return nil, err
	}
	result := make(map[string]model.ClientPlatform, len(ids))
	for i := range entities {
		result[entities[i].ID.String()] = model.ClientPlatform(entities[i].Platform)
	}
	return result, nil
}

// IsTrustedSession reports whether a session is trusted (native platform +
// recent activity within the maxGap window). Returns false when the session
// has no client or the client was deleted.
func (s *Store) IsTrustedSession(ctx context.Context, session *model.AuthSession, maxGap time.Duration) (bool, error) {
	if session.ClientId == nil {
		return false, nil
	}
	clientID, err := uuid.Parse(*session.ClientId)
	if err != nil {
		return false, nil
	}
	client, err := s.GetClientByID(ctx, clientID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil // deleted device — not trusted
		}
		return false, err
	}
	return model.SessionTrusted(client.Platform, session.LastGrantedAt, time.Now().UTC(), maxGap), nil
}
