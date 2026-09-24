package store

// Admin helpers for the Padlock admin HTTP surface (Phase 10). These mirror
// the queries in AccountAdminController.cs, AccountService.cs,
// AccountPunishmentController.cs and AccountGeographyStatsAdminController.cs
// against the snake_case schema from internal/migrate/0001_initial.sql.
//
// Names are prefixed with Admin to avoid collisions with other store files.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// AdminContactSummary carries the primary-email + contact-count aggregates
// shown on the admin account list.
type AdminContactSummary struct {
	PrimaryEmail *string
	Count        int
}

// AdminFactorSummary carries the auth-factor aggregate shown on the admin
// account list.
type AdminFactorSummary struct {
	Count       int
	HasPassword bool
}

// AdminAccountLocation is one account's most recent session location,
// used by the geography stats aggregation.
type AdminAccountLocation struct {
	AccountID     string
	Location      model.GeoPoint
	LastGrantedAt time.Time
}

// AdminEmailRecipient is one account's chosen email contact for admin
// dispatch (primary email, falling back to the most recently verified one).
type AdminEmailRecipient struct {
	AccountID string
	Content   string
	UserName  string
}

// AdminListAccounts pages accounts with an optional name/nick ILIKE filter,
// mirroring AccountAdminController.ListAccounts (soft-deleted accounts are
// excluded via the accounts.deleted_at filter). Returns the page plus the
// total matching count (X-Total).
// AdminAccountFilters carries optional filters for AdminListAccounts.
type AdminAccountFilters struct {
	Activated     *bool
	HasPunishment *bool
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
}

// AdminListAccounts pages accounts with an optional name/nick ILIKE filter,
// mirroring AccountAdminController.ListAccounts (soft-deleted accounts are
// excluded via the accounts.deleted_at filter). Returns the page plus the
// total matching count (X-Total).
func (s *Store) AdminListAccounts(ctx context.Context, query, orderBy string, take, offset int, filters *AdminAccountFilters) ([]model.Account, int, error) {
	base := func() *gorm.DB {
		statement := s.DB.WithContext(ctx).Model(&AccountEntity{})
		if strings.TrimSpace(query) != "" {
			pattern := "%" + strings.TrimSpace(query) + "%"
			statement = statement.Where("name ILIKE ? OR nick ILIKE ?", pattern, pattern)
		}
		if filters != nil {
			if filters.Activated != nil {
				if *filters.Activated {
					statement = statement.Where("activated_at IS NOT NULL")
				} else {
					statement = statement.Where("activated_at IS NULL")
				}
			}
			if filters.HasPunishment != nil && *filters.HasPunishment {
				statement = statement.Where("EXISTS (SELECT 1 FROM punishments p WHERE p.account_id = accounts.id AND p.deleted_at IS NULL AND (p.expired_at IS NULL OR p.expired_at > now()))")
			}
			if filters.CreatedAfter != nil {
				statement = statement.Where("created_at >= ?", *filters.CreatedAfter)
			}
			if filters.CreatedBefore != nil {
				statement = statement.Where("created_at <= ?", *filters.CreatedBefore)
			}
		}
		return statement
	}

	var total int64
	if err := base().Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var order string
	switch orderBy {
	case "name":
		order = "name"
	case "name_desc":
		order = "name DESC"
	case "created_at_desc":
		order = "created_at DESC"
	default:
		order = "id"
	}

	var entities []AccountEntity
	if err := base().Order(order).Limit(take).Offset(offset).Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	var accounts []model.Account
	for i := range entities {
		accounts = append(accounts, *accountFromEntity(&entities[i]))
	}
	return accounts, int(total), nil
}

// AdminLookupAccount resolves an admin route identifier: a GUID is matched
// against account id, otherwise the account name (case-insensitive exact) and
// then a verified-less email/phone contact lookup, mirroring
// LookupAccountAsync + AccountService.LookupAccount.
func (s *Store) AdminLookupAccount(ctx context.Context, identifier string) (*model.Account, error) {
	if id, err := uuid.Parse(strings.TrimSpace(identifier)); err == nil {
		return s.GetAccountByID(ctx, id)
	}
	probe := strings.TrimSpace(identifier)
	var entity AccountEntity
	err := s.DB.WithContext(ctx).Where("name ILIKE ?", probe).First(&entity).Error
	if err == nil {
		return accountFromEntity(&entity), nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	// Fall back to an email/phone contact lookup (contact.Account included).
	var joined AccountEntity
	if err := s.DB.WithContext(ctx).Table("account_contacts c").
		Select(accountColsPrefixed("a")).
		Joins("JOIN accounts a ON a.id = c.account_id").
		Where("(c.type = 0 OR c.type = 1) AND c.content ILIKE ?", probe).
		Where("c.deleted_at IS NULL AND a.deleted_at IS NULL").
		Limit(1).
		Scan(&joined).Error; err != nil {
		return nil, err
	}
	if joined.ID == uuid.Nil {
		return nil, ErrNotFound
	}
	return accountFromEntity(&joined), nil
}

// AdminContactSummaries returns per-account primary email + contact counts.
func (s *Store) AdminContactSummaries(ctx context.Context, accountIDs []uuid.UUID) (map[string]AdminContactSummary, error) {
	result := make(map[string]AdminContactSummary, len(accountIDs))
	if len(accountIDs) == 0 {
		return result, nil
	}
	var entities []ContactEntity
	if err := s.DB.WithContext(ctx).Where("account_id IN ?", accountIDs).Find(&entities).Error; err != nil {
		return nil, err
	}
	for i := range entities {
		entity := &entities[i]
		accountID := entity.AccountID.String()
		summary := result[accountID]
		summary.Count++
		// Primary email: prefer is_primary, then verified_at desc (C#
		// OrderByDescending(IsPrimary).ThenByDescending(VerifiedAt)).
		if summary.PrimaryEmail == nil && entity.IsPrimary {
			email := entity.Content
			summary.PrimaryEmail = &email
		} else if summary.PrimaryEmail == nil && entity.VerifiedAt != nil {
			email := entity.Content
			summary.PrimaryEmail = &email
		}
		result[accountID] = summary
	}
	return result, nil
}

// AdminFactorSummaries returns per-account auth-factor count + has-password.
func (s *Store) AdminFactorSummaries(ctx context.Context, accountIDs []uuid.UUID) (map[string]AdminFactorSummary, error) {
	result := make(map[string]AdminFactorSummary, len(accountIDs))
	if len(accountIDs) == 0 {
		return result, nil
	}
	var entities []AuthFactorEntity
	if err := s.DB.WithContext(ctx).Where("account_id IN ?", accountIDs).Find(&entities).Error; err != nil {
		return nil, err
	}
	for i := range entities {
		entity := &entities[i]
		accountID := entity.AccountID.String()
		summary := result[accountID]
		summary.Count++
		if model.AuthFactorType(entity.Type) == model.AuthFactorTypePassword && entity.EnabledAt != nil {
			summary.HasPassword = true
		}
		result[accountID] = summary
	}
	return result, nil
}

// AdminActiveSessionCounts returns per-account counts of sessions that are
// not expired yet (expired_at IS NULL or future). The legacy query carried no
// deleted_at filter, so the aggregate stays Unscoped.
func (s *Store) AdminActiveSessionCounts(ctx context.Context, accountIDs []uuid.UUID, now time.Time) (map[string]int, error) {
	result := make(map[string]int, len(accountIDs))
	if len(accountIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		AccountID uuid.UUID `gorm:"column:account_id"`
		Count     int       `gorm:"column:count"`
	}
	if err := s.DB.WithContext(ctx).Unscoped().Model(&AuthSessionEntity{}).
		Select("account_id, count(*) AS count").
		Where("account_id IN ? AND (expired_at IS NULL OR expired_at > ?)", accountIDs, now).
		Group("account_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.AccountID.String()] = row.Count
	}
	return result, nil
}

// AdminActiveDeviceCounts returns per-account counts of non-deleted devices.
func (s *Store) AdminActiveDeviceCounts(ctx context.Context, accountIDs []uuid.UUID) (map[string]int, error) {
	result := make(map[string]int, len(accountIDs))
	if len(accountIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		AccountID uuid.UUID `gorm:"column:account_id"`
		Count     int       `gorm:"column:count"`
	}
	if err := s.DB.WithContext(ctx).Model(&AuthClientEntity{}).
		Select("account_id, count(*) AS count").
		Where("account_id IN ?", accountIDs).
		Group("account_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.AccountID.String()] = row.Count
	}
	return result, nil
}

// AdminListActivePunishments returns punishments whose expiry is null/future
// for the given accounts (any type), mirroring the admin list/detail queries.
func (s *Store) AdminListActivePunishments(ctx context.Context, accountIDs []uuid.UUID, now time.Time) ([]model.Punishment, error) {
	if len(accountIDs) == 0 {
		return nil, nil
	}
	return s.adminQueryPunishments(ctx, func(statement *gorm.DB) *gorm.DB {
		return statement.Where("account_id IN ? AND (expired_at IS NULL OR expired_at > ?)", accountIDs, now)
	})
}

// AdminPunishmentGet loads one punishment by id and account.
func (s *Store) AdminPunishmentGet(ctx context.Context, accountID, punishmentID uuid.UUID) (*model.Punishment, error) {
	var entity PunishmentEntity
	if err := s.DB.WithContext(ctx).Where("id = ? AND account_id = ?", punishmentID, accountID).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	punishment := adminPunishmentFromEntity(&entity)
	return &punishment, nil
}

// AdminPunishmentsCreatedBy lists punishments created by the given admin.
func (s *Store) AdminPunishmentsCreatedBy(ctx context.Context, creatorID uuid.UUID, take, offset int) ([]model.Punishment, int, error) {
	var total int64
	if err := s.DB.WithContext(ctx).Model(&PunishmentEntity{}).
		Where("creator_id = ?", creatorID).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	punishments, err := s.adminQueryPunishments(ctx, func(statement *gorm.DB) *gorm.DB {
		return statement.Where("creator_id = ?", creatorID).
			Order("created_at DESC").Limit(take).Offset(offset)
	})
	if err != nil {
		return nil, 0, err
	}
	return punishments, int(total), nil
}

// AdminActivePunishmentsForAccount lists the account's currently active
// punishments (oldest-first by creation, mirroring the user-facing
// AccountPunishmentController).
func (s *Store) AdminActivePunishmentsForAccount(ctx context.Context, accountID uuid.UUID, now time.Time, take, offset int) ([]model.Punishment, int, error) {
	var total int64
	if err := s.DB.WithContext(ctx).Model(&PunishmentEntity{}).
		Where("account_id = ? AND (expired_at IS NULL OR expired_at > ?)", accountID, now).
		Count(&total).Error; err != nil {
		return nil, 0, err
	}
	punishments, err := s.adminQueryPunishments(ctx, func(statement *gorm.DB) *gorm.DB {
		return statement.Where("account_id = ? AND (expired_at IS NULL OR expired_at > ?)", accountID, now).
			Order("created_at DESC").Limit(take).Offset(offset)
	})
	if err != nil {
		return nil, 0, err
	}
	return punishments, int(total), nil
}

// AdminAllPunishmentsForAccount lists every punishment of an account
// (me/punishments), most recent first.
func (s *Store) AdminAllPunishmentsForAccount(ctx context.Context, accountID uuid.UUID, take, offset int) ([]model.Punishment, int, error) {
	var total int64
	if err := s.DB.WithContext(ctx).Model(&PunishmentEntity{}).
		Where("account_id = ?", accountID).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	punishments, err := s.adminQueryPunishments(ctx, func(statement *gorm.DB) *gorm.DB {
		return statement.Where("account_id = ?", accountID).
			Order("created_at DESC").Limit(take).Offset(offset)
	})
	if err != nil {
		return nil, 0, err
	}
	return punishments, int(total), nil
}

// AdminPunishmentOverview returns the most severe active punishment of an
// account, mirroring GetActivePunishmentOverview (null when none).
func (s *Store) AdminPunishmentOverview(ctx context.Context, accountID uuid.UUID, now time.Time) (*model.Punishment, error) {
	punishments, err := s.adminQueryPunishments(ctx, func(statement *gorm.DB) *gorm.DB {
		return statement.Where("account_id = ? AND (expired_at IS NULL OR expired_at > ?)", accountID, now).
			Order("type DESC")
	})
	if err != nil {
		return nil, err
	}
	if len(punishments) == 0 {
		return nil, nil
	}
	return &punishments[SelectMostSeverePunishment(punishments)], nil
}

// SelectMostSeverePunishment returns the index of the most severe punishment
// using the C# priority map (DisableAccount < BlockLogin < PermissionModification < Strike).
func SelectMostSeverePunishment(punishments []model.Punishment) int {
	priority := map[int]int{
		int(model.PunishmentDisableAccount):         0,
		int(model.PunishmentBlockLogin):             1,
		int(model.PunishmentPermissionModification): 2,
		int(model.PunishmentStrike):                 3,
	}
	best := 0
	bestPriority := 99
	for i, p := range punishments {
		prio, ok := priority[int(p.Type)]
		if !ok {
			prio = 99
		}
		if prio < bestPriority {
			best, bestPriority = i, prio
		}
	}
	return best
}

// adminQueryPunishments runs a punishment SELECT through the supplied query
// builder and maps the rows. Soft-deleted punishments are always excluded, so
// the builder receives the scoped statement for punishment entities.
func (s *Store) adminQueryPunishments(ctx context.Context, build func(*gorm.DB) *gorm.DB) ([]model.Punishment, error) {
	var entities []PunishmentEntity
	if err := build(s.DB.WithContext(ctx).Model(&PunishmentEntity{})).Find(&entities).Error; err != nil {
		return nil, err
	}
	var punishments []model.Punishment
	for i := range entities {
		punishments = append(punishments, adminPunishmentFromEntity(&entities[i]))
	}
	return punishments, nil
}

// AdminPunishmentCreate inserts a punishment and returns it.
func (s *Store) AdminPunishmentCreate(ctx context.Context, accountID, creatorID uuid.UUID, reason string, expiredAt *time.Time, ptype int, blocked []string) (*model.Punishment, error) {
	if blocked == nil {
		blocked = []string{}
	}
	blockedPermissions, err := encodeJSON(blocked)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	entity := &PunishmentEntity{
		ID:                 uuid.New(),
		EntityBase:         EntityBase{CreatedAt: now, UpdatedAt: now},
		AccountID:          accountID,
		BlockedPermissions: &blockedPermissions,
		CreatorID:          &creatorID,
		ExpiredAt:          expiredAt,
		Reason:             reason,
		Type:               ptype,
	}
	if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
		return nil, err
	}
	punishment := adminPunishmentFromEntity(entity)
	return &punishment, nil
}

// AdminPunishmentUpdate applies the provided field updates to a punishment.
// A nil field leaves the column untouched; blocked is only applied when
// provided (the C# only updates when request.BlockedPermissions is not null).
func (s *Store) AdminPunishmentUpdate(ctx context.Context, punishmentID uuid.UUID, reason *string, expiredAt *time.Time, ptype *int, blocked []string, hasBlocked bool, creatorID *uuid.UUID) (*model.Punishment, error) {
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if reason != nil {
		updates["reason"] = *reason
	}
	if expiredAt != nil {
		updates["expired_at"] = *expiredAt
	}
	if ptype != nil {
		updates["type"] = *ptype
	}
	if hasBlocked {
		var blockedPermissions *datatypes.JSON
		if blocked != nil {
			encoded, err := encodeJSON(blocked)
			if err != nil {
				return nil, err
			}
			blockedPermissions = &encoded
		}
		updates["blocked_permissions"] = blockedPermissions
	}
	if creatorID != nil {
		updates["creator_id"] = *creatorID
	}
	res := s.DB.WithContext(ctx).Model(&PunishmentEntity{}).
		Where("id = ?", punishmentID).
		Updates(updates)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	var entity PunishmentEntity
	if err := s.DB.WithContext(ctx).Unscoped().Where("id = ?", punishmentID).First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	punishment := adminPunishmentFromEntity(&entity)
	return &punishment, nil
}

// AdminPunishmentDelete soft-deletes a punishment (EF Remove semantics).
func (s *Store) AdminPunishmentDelete(ctx context.Context, accountID, punishmentID uuid.UUID) (*model.Punishment, error) {
	now := time.Now().UTC()
	res := s.DB.WithContext(ctx).Model(&PunishmentEntity{}).
		Where("id = ? AND account_id = ?", punishmentID, accountID).
		Updates(map[string]any{"deleted_at": now, "updated_at": now})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	var entity PunishmentEntity
	if err := s.DB.WithContext(ctx).Unscoped().Where("id = ?", punishmentID).First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	punishment := adminPunishmentFromEntity(&entity)
	return &punishment, nil
}

// AdminListDevices pages the account's auth clients, optionally including
// soft-deleted ones, mirroring ListAccountDevices.
func (s *Store) AdminListDevices(ctx context.Context, accountID uuid.UUID, includeDeleted bool, take, offset int) ([]model.AuthClient, int, error) {
	base := func() *gorm.DB {
		statement := s.DB.WithContext(ctx).Model(&AuthClientEntity{})
		if includeDeleted {
			statement = statement.Unscoped()
		}
		return statement.Where("account_id = ?", accountID)
	}

	var total int64
	if err := base().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var entities []AuthClientEntity
	if err := base().Order("created_at DESC").Limit(take).Offset(offset).Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	var devices []model.AuthClient
	for i := range entities {
		devices = append(devices, authClientFromEntity(&entities[i]))
	}
	return devices, int(total), nil
}

// AdminListDeviceSessions groups the devices' sessions by client id, newest
// last_granted_at first (used to populate SnAuthClientWithSessions). The
// legacy query carried no deleted_at filter, so it stays Unscoped.
func (s *Store) AdminListDeviceSessions(ctx context.Context, clientIDs []uuid.UUID) (map[string][]model.AuthSession, error) {
	result := make(map[string][]model.AuthSession)
	if len(clientIDs) == 0 {
		return result, nil
	}
	var entities []AuthSessionEntity
	if err := s.DB.WithContext(ctx).Unscoped().
		Where("client_id IN ?", clientIDs).
		Order("last_granted_at DESC").
		Find(&entities).Error; err != nil {
		return nil, err
	}
	for i := range entities {
		session := sessionFromEntity(&entities[i])
		if session.ClientId != nil {
			result[*session.ClientId] = append(result[*session.ClientId], *session)
		}
	}
	return result, nil
}

// AdminGetDeviceByDeviceId loads an auth client by its stable device id.
func (s *Store) AdminGetDeviceByDeviceId(ctx context.Context, accountID uuid.UUID, deviceID string) (*model.AuthClient, error) {
	var entity AuthClientEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ? AND device_id = ?", accountID, deviceID).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	device := authClientFromEntity(&entity)
	return &device, nil
}

// AdminUpdateDeviceLabel renames the device (device_name column, mirroring
// UpdateDeviceName).
func (s *Store) AdminUpdateDeviceLabel(ctx context.Context, accountID uuid.UUID, deviceID, label string) error {
	res := s.DB.WithContext(ctx).Model(&AuthClientEntity{}).
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

// AdminDeleteDevice expires all sessions of the client and soft-deletes the
// auth client, mirroring AccountService.DeleteDevice. Both legacy statements
// ran without a deleted_at filter, so they stay Unscoped.
func (s *Store) AdminDeleteDevice(ctx context.Context, accountID uuid.UUID, deviceID string, now time.Time) (*model.AuthClient, error) {
	device, err := s.AdminGetDeviceByDeviceId(ctx, accountID, deviceID)
	if err != nil {
		return nil, err
	}
	if err := s.DB.WithContext(ctx).Unscoped().Model(&AuthSessionEntity{}).
		Where("client_id = ?", device.Id).
		Updates(map[string]any{"expired_at": now, "updated_at": now}).Error; err != nil {
		return nil, err
	}
	if err := s.DB.WithContext(ctx).Unscoped().Model(&AuthClientEntity{}).
		Where("id = ?", device.Id).
		Updates(map[string]any{"deleted_at": now, "updated_at": now}).Error; err != nil {
		return nil, err
	}
	return device, nil
}

// AdminListSessions pages an account's sessions with the admin filters,
// mirroring ListAccountSessions (children excluded unless includeChildren).
// The legacy query carried no deleted_at filter, so it stays Unscoped.
func (s *Store) AdminListSessions(ctx context.Context, accountID uuid.UUID, typ *int, clientID *uuid.UUID, includeChildren, activeOnly bool, take, offset int) ([]model.AuthSession, int, error) {
	base := func() *gorm.DB {
		statement := s.DB.WithContext(ctx).Unscoped().Model(&AuthSessionEntity{}).
			Where("account_id = ?", accountID)
		if !includeChildren {
			statement = statement.Where("parent_session_id IS NULL")
		}
		if typ != nil {
			statement = statement.Where("type = ?", *typ)
		}
		if clientID != nil {
			statement = statement.Where("client_id = ?", *clientID)
		}
		if activeOnly {
			statement = statement.Where("(expired_at IS NULL OR expired_at > ?)", time.Now().UTC())
		}
		return statement
	}

	var total int64
	if err := base().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var entities []AuthSessionEntity
	if err := base().Order("last_granted_at DESC").Limit(take).Offset(offset).Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	var sessions []model.AuthSession
	for i := range entities {
		sessions = append(sessions, *sessionFromEntity(&entities[i]))
	}
	return sessions, int(total), nil
}

// AdminListSessionChildren pages the direct children of one session,
// mirroring ListAccountSessionChildren. The legacy queries carried no
// deleted_at filter, so they stay Unscoped.
func (s *Store) AdminListSessionChildren(ctx context.Context, accountID, parentID uuid.UUID, take, offset int) ([]model.AuthSession, int, error) {
	var total int64
	if err := s.DB.WithContext(ctx).Unscoped().Model(&AuthSessionEntity{}).
		Where("parent_session_id = ? AND account_id = ?", parentID, accountID).
		Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var entities []AuthSessionEntity
	if err := s.DB.WithContext(ctx).Unscoped().Model(&AuthSessionEntity{}).
		Where("parent_session_id = ? AND account_id = ?", parentID, accountID).
		Order("last_granted_at DESC").Limit(take).Offset(offset).
		Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	var sessions []model.AuthSession
	for i := range entities {
		sessions = append(sessions, *sessionFromEntity(&entities[i]))
	}
	return sessions, int(total), nil
}

// AdminGetSession loads one session belonging to the account. The legacy
// query carried no deleted_at filter, so it stays Unscoped.
func (s *Store) AdminGetSession(ctx context.Context, accountID, sessionID uuid.UUID) (*model.AuthSession, error) {
	var entity AuthSessionEntity
	if err := s.DB.WithContext(ctx).Unscoped().
		Where("id = ? AND account_id = ?", sessionID, accountID).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return sessionFromEntity(&entity), nil
}

// AdminRevokeSession expires a single session and bumps its epoch (mirroring
// AccountService.DeleteSession).
func (s *Store) AdminRevokeSession(ctx context.Context, accountID, sessionID uuid.UUID, now time.Time) (*model.AuthSession, error) {
	var entity AuthSessionEntity
	if err := s.DB.WithContext(ctx).
		Where("id = ? AND account_id = ?", sessionID, accountID).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	if err := s.DB.WithContext(ctx).Model(&AuthSessionEntity{}).
		Where("id = ?", entity.ID).
		Updates(map[string]any{"expired_at": now, "epoch": gorm.Expr("epoch + 1"), "updated_at": now}).Error; err != nil {
		return nil, err
	}
	entity.ExpiredAt = &now
	entity.Epoch++
	entity.UpdatedAt = now
	return sessionFromEntity(&entity), nil
}

// AdminRevokeAllSessions expires every live session of the account (mirroring
// AccountService.DeleteAllSessions) and returns the revoked sessions.
func (s *Store) AdminRevokeAllSessions(ctx context.Context, accountID uuid.UUID, now time.Time) ([]model.AuthSession, error) {
	where := "account_id = ? AND expired_at IS NULL"
	var entities []AuthSessionEntity
	if err := s.DB.WithContext(ctx).Where(where, accountID).Find(&entities).Error; err != nil {
		return nil, err
	}
	if len(entities) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, 0, len(entities))
	var sessions []model.AuthSession
	for i := range entities {
		entity := &entities[i]
		ids = append(ids, entity.ID)
		entity.ExpiredAt = &now
		entity.Epoch++
		entity.UpdatedAt = now
		sessions = append(sessions, *sessionFromEntity(entity))
	}
	if err := s.DB.WithContext(ctx).Model(&AuthSessionEntity{}).
		Where("id IN ? AND expired_at IS NULL", ids).
		Updates(map[string]any{"expired_at": now, "epoch": gorm.Expr("epoch + 1"), "updated_at": now}).Error; err != nil {
		return nil, err
	}
	return sessions, nil
}

// AdminCountSessionChildren returns the child count of each session id. The
// legacy query carried no deleted_at filter, so it stays Unscoped.
func (s *Store) AdminCountSessionChildren(ctx context.Context, sessionIDs []uuid.UUID) (map[string]int, error) {
	result := make(map[string]int, len(sessionIDs))
	if len(sessionIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		ParentSessionID uuid.UUID `gorm:"column:parent_session_id"`
		Count           int       `gorm:"column:count"`
	}
	if err := s.DB.WithContext(ctx).Unscoped().Model(&AuthSessionEntity{}).
		Select("parent_session_id, count(*) AS count").
		Where("parent_session_id IN ?", sessionIDs).
		Group("parent_session_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ParentSessionID.String()] = row.Count
	}
	return result, nil
}

// AdminListContacts lists an account's contacts, mirroring the admin ordering
// (primary first, then type, then content).
func (s *Store) AdminListContacts(ctx context.Context, accountID uuid.UUID) ([]model.Contact, error) {
	var entities []ContactEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("is_primary DESC, type, content").
		Find(&entities).Error; err != nil {
		return nil, err
	}
	var contacts []model.Contact
	for i := range entities {
		contacts = append(contacts, contactFromEntity(&entities[i]))
	}
	return contacts, nil
}

// AdminGetContact loads one contact of the account.
func (s *Store) AdminGetContact(ctx context.Context, accountID, contactID uuid.UUID) (*model.Contact, error) {
	var entity ContactEntity
	if err := s.DB.WithContext(ctx).
		Where("id = ? AND account_id = ?", contactID, accountID).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	contact := contactFromEntity(&entity)
	return &contact, nil
}

// AdminCreateContact inserts a non-primary contact (CreateContactMethod).
func (s *Store) AdminCreateContact(ctx context.Context, accountID uuid.UUID, ctype int, content string) (*model.Contact, error) {
	now := time.Now().UTC()
	entity := &ContactEntity{
		ID:         uuid.New(),
		EntityBase: EntityBase{CreatedAt: now, UpdatedAt: now},
		AccountID:  accountID,
		Content:    content,
		IsPrimary:  false,
		IsPublic:   false,
		Type:       ctype,
	}
	if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
		return nil, err
	}
	contact := contactFromEntity(entity)
	return &contact, nil
}

// AdminUpdateContact applies type/content updates, clearing verified_at when
// either changed (UpdateAccountContact semantics).
func (s *Store) AdminUpdateContact(ctx context.Context, accountID, contactID uuid.UUID, ctype *int, content *string) (*model.Contact, error) {
	var entity ContactEntity
	if err := s.DB.WithContext(ctx).
		Where("id = ? AND account_id = ?", contactID, accountID).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	typeChanged := ctype != nil && entity.Type != *ctype
	contentChanged := content != nil && entity.Content != *content
	if ctype != nil {
		entity.Type = *ctype
	}
	if content != nil {
		entity.Content = *content
	}
	if typeChanged || contentChanged {
		entity.VerifiedAt = nil
	}
	now := time.Now().UTC()
	if err := s.DB.WithContext(ctx).Model(&ContactEntity{}).
		Where("id = ?", entity.ID).
		Updates(map[string]any{
			"type":        entity.Type,
			"content":     entity.Content,
			"verified_at": entity.VerifiedAt,
			"updated_at":  now,
		}).Error; err != nil {
		return nil, err
	}
	entity.UpdatedAt = now
	contact := contactFromEntity(&entity)
	return &contact, nil
}

// AdminSetContactVerified marks a contact verified at the given instant,
// keeping the latest verification when one exists (MarkContactMethodVerified).
func (s *Store) AdminSetContactVerified(ctx context.Context, accountID, contactID uuid.UUID, verifiedAt time.Time) (*model.Contact, error) {
	var entity ContactEntity
	if err := s.DB.WithContext(ctx).
		Where("id = ? AND account_id = ?", contactID, accountID).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	if entity.VerifiedAt == nil || entity.VerifiedAt.Before(verifiedAt) {
		entity.VerifiedAt = &verifiedAt
	}
	now := time.Now().UTC()
	if err := s.DB.WithContext(ctx).Model(&ContactEntity{}).
		Where("id = ?", entity.ID).
		Updates(map[string]any{"verified_at": entity.VerifiedAt, "updated_at": now}).Error; err != nil {
		return nil, err
	}
	entity.UpdatedAt = now
	contact := contactFromEntity(&entity)
	return &contact, nil
}

// AdminClearContactVerified nulls the verified_at timestamp.
func (s *Store) AdminClearContactVerified(ctx context.Context, accountID, contactID uuid.UUID) (*model.Contact, error) {
	contact, err := s.AdminGetContact(ctx, accountID, contactID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if err := s.DB.WithContext(ctx).Model(&ContactEntity{}).
		Where("id = ?", contactID).
		Updates(map[string]any{"verified_at": nil, "updated_at": now}).Error; err != nil {
		return nil, err
	}
	contact.VerifiedAt = nil
	contact.UpdatedAt = timePtr(&now)
	return contact, nil
}

// AdminSetContactPrimary clears is_primary for the account's contacts of the
// same type and marks the target primary (SetContactMethodPrimary).
func (s *Store) AdminSetContactPrimary(ctx context.Context, accountID, contactID uuid.UUID) (*model.Contact, error) {
	contact, err := s.AdminGetContact(ctx, accountID, contactID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if err := s.DB.WithContext(ctx).Model(&ContactEntity{}).
		Where("account_id = ? AND type = ?", accountID, contact.Type).
		Updates(map[string]any{"is_primary": false, "updated_at": now}).Error; err != nil {
		return nil, err
	}
	if err := s.DB.WithContext(ctx).Model(&ContactEntity{}).
		Where("id = ? AND account_id = ?", contactID, accountID).
		Updates(map[string]any{"is_primary": true, "updated_at": now}).Error; err != nil {
		return nil, err
	}
	contact.IsPrimary = true
	contact.UpdatedAt = timePtr(&now)
	return contact, nil
}

// AdminSetContactPublic flips the is_public flag.
func (s *Store) AdminSetContactPublic(ctx context.Context, accountID, contactID uuid.UUID, isPublic bool) (*model.Contact, error) {
	contact, err := s.AdminGetContact(ctx, accountID, contactID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if err := s.DB.WithContext(ctx).Model(&ContactEntity{}).
		Where("id = ? AND account_id = ?", contactID, accountID).
		Updates(map[string]any{"is_public": isPublic, "updated_at": now}).Error; err != nil {
		return nil, err
	}
	contact.IsPublic = isPublic
	contact.UpdatedAt = timePtr(&now)
	return contact, nil
}

// AdminDeleteContact soft-deletes a contact (EF Remove semantics).
func (s *Store) AdminDeleteContact(ctx context.Context, accountID, contactID uuid.UUID) error {
	now := time.Now().UTC()
	res := s.DB.WithContext(ctx).Model(&ContactEntity{}).
		Where("id = ? AND account_id = ?", contactID, accountID).
		Updates(map[string]any{"deleted_at": now, "updated_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// AdminListAuthFactors lists all factors of an account (admin view), ordered
// by type then enabled_at desc, mirroring ListAccountAuthFactors.
func (s *Store) AdminListAuthFactors(ctx context.Context, accountID uuid.UUID) ([]model.AuthFactor, error) {
	var entities []AuthFactorEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("type, enabled_at DESC").
		Find(&entities).Error; err != nil {
		return nil, err
	}
	var factors []model.AuthFactor
	for i := range entities {
		factors = append(factors, factorFromEntity(&entities[i]))
	}
	return factors, nil
}

// AdminGetAuthFactor loads one factor of the account.
func (s *Store) AdminGetAuthFactor(ctx context.Context, accountID, factorID uuid.UUID) (*model.AuthFactor, error) {
	var entity AuthFactorEntity
	if err := s.DB.WithContext(ctx).
		Where("id = ? AND account_id = ?", factorID, accountID).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	factor := factorFromEntity(&entity)
	return &factor, nil
}

// AdminCheckAuthFactorExists reports whether the account has a factor of the
// type (any state), mirroring CheckAuthFactorExists.
func (s *Store) AdminCheckAuthFactorExists(ctx context.Context, accountID uuid.UUID, ftype int) (bool, error) {
	var count int64
	if err := s.DB.WithContext(ctx).Model(&AuthFactorEntity{}).
		Where("account_id = ? AND type = ?", accountID, ftype).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// AdminInsertAuthFactor inserts a factor row and returns it.
func (s *Store) AdminInsertAuthFactor(ctx context.Context, f *model.AuthFactor) (*model.AuthFactor, error) {
	accountID, err := ParseUUID(f.AccountId)
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
	now := time.Now().UTC()
	entity := &AuthFactorEntity{
		ID:          uuid.New(),
		EntityBase:  EntityBase{CreatedAt: now, UpdatedAt: now},
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
	factor := factorFromEntity(entity)
	return &factor, nil
}

// AdminUpdateAuthFactor persists factor mutations (secret, config,
// trustworthy, enabled_at, expired_at, created_response fields are computed
// by the handler and stored via the columns below).
func (s *Store) AdminUpdateAuthFactor(ctx context.Context, f *model.AuthFactor) error {
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
	res := s.DB.WithContext(ctx).Model(&AuthFactorEntity{}).
		Where("id = ? AND account_id = ?", f.Id, f.AccountId).
		Updates(map[string]any{
			"secret":      secret,
			"config":      config,
			"trustworthy": f.Trustworthy,
			"enabled_at":  timeValue(f.EnabledAt),
			"expired_at":  timeValue(f.ExpiredAt),
			"updated_at":  time.Now().UTC(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// AdminDeleteAuthFactor soft-deletes a factor (EF Remove semantics).
func (s *Store) AdminDeleteAuthFactor(ctx context.Context, accountID, factorID uuid.UUID) error {
	now := time.Now().UTC()
	res := s.DB.WithContext(ctx).Model(&AuthFactorEntity{}).
		Where("id = ? AND account_id = ?", factorID, accountID).
		Updates(map[string]any{"deleted_at": now, "updated_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// AdminUpsertPasswordFactor creates or resets the account's Password factor,
// mirroring ResetPasswordFactor (bcrypt hash supplied by the caller).
func (s *Store) AdminUpsertPasswordFactor(ctx context.Context, accountID uuid.UUID, hash string, now time.Time) (*model.AuthFactor, error) {
	var existing AuthFactorEntity
	err := s.DB.WithContext(ctx).
		Where("account_id = ? AND type = ?", accountID, int(model.AuthFactorTypePassword)).
		First(&existing).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		// No password factor yet: insert enabled.
		secret := hash
		entity := &AuthFactorEntity{
			ID:          uuid.New(),
			EntityBase:  EntityBase{CreatedAt: now, UpdatedAt: now},
			AccountID:   accountID,
			EnabledAt:   &now,
			Secret:      &secret,
			Trustworthy: 1,
			Type:        int(model.AuthFactorTypePassword),
		}
		if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
			return nil, err
		}
		factor := factorFromEntity(entity)
		return &factor, nil
	case err != nil:
		return nil, err
	}
	// Existing factor: reset secret + enable.
	if err := s.DB.WithContext(ctx).Model(&AuthFactorEntity{}).
		Where("id = ?", existing.ID).
		Updates(map[string]any{
			"secret":     hash,
			"enabled_at": gorm.Expr("COALESCE(enabled_at, ?)", now),
			"expired_at": nil,
			"updated_at": now,
		}).Error; err != nil {
		return nil, err
	}
	secret := hash
	existing.Secret = &secret
	if existing.EnabledAt == nil {
		existing.EnabledAt = &now
	}
	existing.ExpiredAt = nil
	existing.UpdatedAt = now
	factor := factorFromEntity(&existing)
	return &factor, nil
}

// AdminResolveTargetAccountIDs resolves notification/email dispatch targets:
// all non-deleted accounts for broadcast, otherwise the requested set
// intersected with non-deleted accounts.
func (s *Store) AdminResolveTargetAccountIDs(ctx context.Context, requested []uuid.UUID, broadcast bool) ([]uuid.UUID, error) {
	if !broadcast && len(requested) == 0 {
		return nil, nil
	}
	statement := s.DB.WithContext(ctx).Model(&AccountEntity{})
	if !broadcast {
		statement = statement.Where("id IN ?", requested)
	}
	var ids []uuid.UUID
	if err := statement.Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// AdminListEmailContacts returns the dispatchable email contact of each
// target account (primary email preferred, else first verified; or the first
// email contact for the CSV export which also includes unverified ones).
func (s *Store) AdminListEmailContacts(ctx context.Context, accountIDs []uuid.UUID, verifiedOnly bool) ([]AdminEmailRecipient, error) {
	if len(accountIDs) == 0 {
		return nil, nil
	}
	statement := s.DB.WithContext(ctx).Table("account_contacts c").
		Select("c.account_id, c.content, c.is_primary, c.verified_at, c.created_at, a.name, a.nick").
		Joins("JOIN accounts a ON a.id = c.account_id").
		Where("c.account_id IN ? AND c.type = 0 AND c.deleted_at IS NULL AND a.deleted_at IS NULL", accountIDs)
	if verifiedOnly {
		statement = statement.Where("c.verified_at IS NOT NULL")
	}
	var rows []struct {
		AccountID  uuid.UUID  `gorm:"column:account_id"`
		Content    string     `gorm:"column:content"`
		IsPrimary  bool       `gorm:"column:is_primary"`
		VerifiedAt *time.Time `gorm:"column:verified_at"`
		CreatedAt  time.Time  `gorm:"column:created_at"`
		Name       string     `gorm:"column:name"`
		Nick       string     `gorm:"column:nick"`
	}
	if err := statement.Find(&rows).Error; err != nil {
		return nil, err
	}

	// Per account: primary first, then verified_at desc, then created_at asc
	// (matching the C# OrderByDescending(IsPrimary).ThenByDescending(VerifiedAt)
	// for emails and the export's primary → created_at fallback).
	type candidate struct {
		recipient  AdminEmailRecipient
		isPrimary  bool
		verifiedAt *model.Time
	}
	rank := func(c candidate) int {
		switch {
		case c.isPrimary:
			return 0
		case c.verifiedAt != nil:
			return 1
		default:
			return 2
		}
	}
	best := make(map[string]candidate)
	for i := range rows {
		row := &rows[i]
		accountID := row.AccountID.String()
		userName := row.Name
		if strings.TrimSpace(row.Nick) != "" {
			userName = row.Nick
		}
		cand := candidate{
			recipient:  AdminEmailRecipient{AccountID: accountID, Content: row.Content, UserName: userName},
			isPrimary:  row.IsPrimary,
			verifiedAt: timePtr(row.VerifiedAt),
		}
		// Verified-at ordering applies within the same rank tier.
		current, ok := best[accountID]
		if !ok || rank(cand) < rank(current) ||
			(rank(cand) == rank(current) && rank(cand) == 1 && cand.verifiedAt != nil &&
				current.verifiedAt != nil && cand.verifiedAt.Time().After(current.verifiedAt.Time())) {
			best[accountID] = cand
		}
	}
	result := make([]AdminEmailRecipient, 0, len(best))
	for _, c := range best {
		result = append(result, c.recipient)
	}
	return result, nil
}

// AdminLatestAccountLocations returns each account's most recent session
// location since the cutoff, mirroring the geography stats query.
func (s *Store) AdminLatestAccountLocations(ctx context.Context, since time.Time) ([]AdminAccountLocation, error) {
	var rows []struct {
		AccountID     uuid.UUID       `gorm:"column:account_id"`
		Location      *datatypes.JSON `gorm:"column:location"`
		LastGrantedAt time.Time       `gorm:"column:last_granted_at"`
	}
	if err := s.DB.WithContext(ctx).Table("auth_sessions session").
		Select("DISTINCT ON (session.account_id) session.account_id, session.location, session.last_granted_at").
		Where("session.last_granted_at IS NOT NULL AND session.last_granted_at >= ?", since).
		Where("session.location IS NOT NULL AND session.location::text <> 'null' AND session.deleted_at IS NULL").
		Order("session.account_id, session.last_granted_at DESC, session.created_at DESC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	var locations []AdminAccountLocation
	for i := range rows {
		row := &rows[i]
		if row.Location == nil || len(*row.Location) == 0 || string(*row.Location) == "null" {
			continue
		}
		var point model.GeoPoint
		if err := decodeJSON(row.Location, &point); err != nil {
			continue
		}
		locations = append(locations, AdminAccountLocation{
			AccountID:     row.AccountID.String(),
			Location:      point,
			LastGrantedAt: row.LastGrantedAt,
		})
	}
	return locations, nil
}

// AdminLoadProfiles batch-loads account profiles keyed by account id.
func (s *Store) AdminLoadProfiles(ctx context.Context, accountIDs []uuid.UUID) (map[string]*model.Profile, error) {
	result := make(map[string]*model.Profile, len(accountIDs))
	if len(accountIDs) == 0 {
		return result, nil
	}
	var entities []ProfileEntity
	if err := s.DB.WithContext(ctx).Where("account_id IN ?", accountIDs).Find(&entities).Error; err != nil {
		return nil, err
	}
	for i := range entities {
		profile := profileFromEntity(&entities[i])
		result[profile.AccountId] = profile
	}
	return result, nil
}

// AdminLoadAccountsByIds batch-loads accounts keyed by id.
func (s *Store) AdminLoadAccountsByIds(ctx context.Context, ids []uuid.UUID) (map[string]*model.Account, error) {
	result := make(map[string]*model.Account, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var entities []AccountEntity
	if err := s.DB.WithContext(ctx).Where("id IN ?", ids).Find(&entities).Error; err != nil {
		return nil, err
	}
	for i := range entities {
		account := accountFromEntity(&entities[i])
		result[account.Id] = account
	}
	return result, nil
}

// AdminListOwnActionLogs pages one account's action logs (GET /api/actions).
func (s *Store) AdminListOwnActionLogs(ctx context.Context, accountID uuid.UUID, action string, take, offset int) ([]model.ActionLog, int, error) {
	return s.adminQueryActionLogs(ctx, accountID, action, take, offset)
}

// AdminListAccountActionLogs is the admin route backing for listing action
// logs scoped to a specific account. Mirrors AdminListOwnActionLogs with
// an explicit admin-facing name.
func (s *Store) AdminListAccountActionLogs(ctx context.Context, accountID uuid.UUID, action string, take, offset int) ([]model.ActionLog, int, error) {
	return s.adminQueryActionLogs(ctx, accountID, action, take, offset)
}

// adminQueryActionLogs pages the account's action logs, optionally filtered by
// action, newest first.
func (s *Store) adminQueryActionLogs(ctx context.Context, accountID uuid.UUID, action string, take, offset int) ([]model.ActionLog, int, error) {
	base := func() *gorm.DB {
		statement := s.DB.WithContext(ctx).Model(&ActionLogEntity{}).Where("account_id = ?", accountID)
		if strings.TrimSpace(action) != "" {
			statement = statement.Where("action = ?", action)
		}
		return statement
	}

	var total int64
	if err := base().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var entities []ActionLogEntity
	if err := base().Order("created_at DESC").Limit(take).Offset(offset).Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	var logs []model.ActionLog
	for i := range entities {
		logs = append(logs, adminActionLogFromEntity(&entities[i]))
	}
	return logs, int(total), nil
}

// AdminListConnections lists an account's connections (admin view),
// filtering soft-deleted rows.
func (s *Store) AdminListConnections(ctx context.Context, accountID uuid.UUID) ([]model.Connection, error) {
	var entities []ConnectionEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("created_at").
		Find(&entities).Error; err != nil {
		return nil, err
	}
	var connections []model.Connection
	for i := range entities {
		connections = append(connections, connectionFromEntity(&entities[i]))
	}
	return connections, nil
}

// AdminListPasskeys lists an account's passkeys (admin view), filtering
// soft-deleted rows.
func (s *Store) AdminListPasskeys(ctx context.Context, accountID uuid.UUID) ([]model.Passkey, error) {
	var entities []PasskeyEntity
	if err := s.DB.WithContext(ctx).
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

// AdminDeletePasskey hard-deletes a passkey row. Returns ErrNotFound when
// the row does not exist or does not belong to the account.
func (s *Store) AdminDeletePasskey(ctx context.Context, accountID, passkeyID uuid.UUID) error {
	res := s.DB.WithContext(ctx).Unscoped().
		Where("id = ? AND account_id = ?", passkeyID, accountID).
		Delete(&PasskeyEntity{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// AdminListRelationships lists an account's outgoing relationships with
// related-account info, mirroring the admin relationship management surface.
func (s *Store) AdminListRelationships(ctx context.Context, accountID uuid.UUID, status *int, take, offset int) ([]model.Relationship, int, error) {
	base := func() *gorm.DB {
		statement := s.DB.WithContext(ctx).Model(&RelationshipEntity{}).Where("account_id = ?", accountID)
		if status != nil {
			statement = statement.Where("status = ?", *status)
		}
		return statement
	}

	var total int64
	if err := base().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var entities []RelationshipEntity
	if err := base().Order("created_at DESC").Limit(take).Offset(offset).Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	var relationships []model.Relationship
	for i := range entities {
		relationships = append(relationships, relationshipFromEntity(&entities[i]))
	}
	return relationships, int(total), nil
}

// AdminUpdateAccountProfile updates profile fields (admin management). Only
// non-nil fields are written; returns the refreshed profile.
func (s *Store) AdminUpdateAccountProfile(ctx context.Context, accountID uuid.UUID, firstName, middleName, lastName, bio, gender, pronouns, timeZone, location *string) (*model.Profile, error) {
	profile, err := s.GetOrCreateAccountProfile(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if firstName != nil {
		profile.FirstName = firstName
	}
	if middleName != nil {
		profile.MiddleName = middleName
	}
	if lastName != nil {
		profile.LastName = lastName
	}
	if bio != nil {
		profile.Bio = bio
	}
	if gender != nil {
		profile.Gender = gender
	}
	if pronouns != nil {
		profile.Pronouns = pronouns
	}
	if timeZone != nil {
		profile.TimeZone = timeZone
	}
	if location != nil {
		profile.Location = location
	}
	if err := s.SaveProfile(ctx, profile); err != nil {
		return nil, err
	}
	return s.GetProfileByAccount(ctx, accountID)
}

// AdminUpdateAccountBasicInfo updates account basic info (nick, language,
// region) from admin management. Only non-nil fields are written; returns
// the refreshed account.
func (s *Store) AdminUpdateAccountBasicInfo(ctx context.Context, accountID uuid.UUID, nick, language, region *string) (*model.Account, error) {
	return s.UpdateAccountBasicInfo(ctx, accountID, nick, language, region)
}

// AdminListAccountNameHistory lists name history for an account (paid
// renames), ordered by creation time descending. The legacy query carried no
// deleted_at filter, so it stays Unscoped.
func (s *Store) AdminListAccountNameHistory(ctx context.Context, accountID uuid.UUID) ([]map[string]any, error) {
	var entities []AccountNameHistoryEntity
	if err := s.DB.WithContext(ctx).Unscoped().
		Where("account_id = ?", accountID).
		Order("created_at DESC").
		Find(&entities).Error; err != nil {
		return nil, err
	}
	var history []map[string]any
	for i := range entities {
		entity := &entities[i]
		history = append(history, map[string]any{
			"id":         entity.ID.String(),
			"account_id": entity.AccountID.String(),
			"name":       entity.Name,
			"created_at": timePtr(&entity.CreatedAt),
			"updated_at": timePtr(&entity.UpdatedAt),
			"deleted_at": deletedTime(entity.DeletedAt),
		})
	}
	return history, nil
}

// --- Entity → model mappers for the admin surface ---

// adminPunishmentFromEntity maps a punishments row to its wire model.
func adminPunishmentFromEntity(entity *PunishmentEntity) model.Punishment {
	punishment := model.Punishment{
		Id:        entity.ID.String(),
		Reason:    entity.Reason,
		ExpiredAt: timePtr(entity.ExpiredAt),
		Type:      model.PunishmentType(entity.Type),
		AccountId: entity.AccountID.String(),
		CreatorId: uuidPtrStr(entity.CreatorID),
		CreatedAt: timePtr(&entity.CreatedAt),
		UpdatedAt: timePtr(&entity.UpdatedAt),
		DeletedAt: deletedTime(entity.DeletedAt),
	}
	_ = decodeJSON(entity.BlockedPermissions, &punishment.BlockedPermissions)
	return punishment
}

// adminActionLogFromEntity maps an action_logs row to its wire model.
func adminActionLogFromEntity(entity *ActionLogEntity) model.ActionLog {
	log := model.ActionLog{
		Id:        entity.ID.String(),
		Action:    entity.Action,
		UserAgent: entity.UserAgent,
		IpAddress: entity.IPAddress,
		AccountId: entity.AccountID.String(),
		SessionId: uuidPtrStr(entity.SessionID),
		CreatedAt: timePtr(&entity.CreatedAt),
		UpdatedAt: timePtr(&entity.UpdatedAt),
		DeletedAt: deletedTime(entity.DeletedAt),
	}
	_ = decodeJSONValue(entity.Meta, &log.Meta)
	_ = decodeJSON(entity.Location, &log.Location)
	return log
}

// adminRelationshipFromEntity was replaced by relationships.go's shared
// relationshipFromEntity mapper.

// AdminActivateAccount activates an account and (re)grants the `verified`
// group membership, mirroring ActivateAccountAndGrantDefaultPermissions.
// Unlike ActivateAccountAndGrantVerified (the spell path, which only fires
// once), the admin endpoint refreshes activated_at when it is older than the
// requested timestamp.
func (s *Store) AdminActivateAccount(ctx context.Context, accountID uuid.UUID, activatedAt time.Time) error {
	now := time.Now().UTC()
	if err := s.DB.WithContext(ctx).Model(&AccountEntity{}).
		Where("id = ? AND (activated_at IS NULL OR activated_at < ?)", accountID, activatedAt).
		Updates(map[string]any{"activated_at": activatedAt, "updated_at": now}).Error; err != nil {
		return err
	}
	var group PermissionGroupEntity
	if err := s.DB.WithContext(ctx).Where(`"key" = ?`, "verified").First(&group).Error; err != nil {
		return mapNotFound(err)
	}
	return s.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "group_id"}, {Name: "actor"}},
		DoUpdates: clause.Assignments(map[string]any{"affected_at": nil, "expired_at": nil, "updated_at": now}),
	}).Create(&PermissionGroupMemberEntity{
		GroupID:    group.ID,
		Actor:      accountID.String(),
		EntityBase: EntityBase{CreatedAt: now, UpdatedAt: now},
	}).Error
}

// AdminSoftDeleteAccount tombstones the account's sessions and then the
// account itself (the admin delete endpoint). Sessions are soft-deleted
// (deleted_at), not expired — distinct from SoftDeleteAccountAndSessions,
// which the gRPC bot flow uses.
func (s *Store) AdminSoftDeleteAccount(ctx context.Context, accountID uuid.UUID, now time.Time) error {
	if err := s.DB.WithContext(ctx).Unscoped().Model(&AuthSessionEntity{}).
		Where("account_id = ?", accountID).
		Updates(map[string]any{"deleted_at": now, "updated_at": now}).Error; err != nil {
		return err
	}
	return s.DB.WithContext(ctx).Unscoped().Model(&AccountEntity{}).
		Where("id = ? AND deleted_at IS NULL", accountID).
		Updates(map[string]any{"deleted_at": now, "updated_at": now}).Error
}
