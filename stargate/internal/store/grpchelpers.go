package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// Helpers for the inbound gRPC servers (AccountServiceGrpc / PermissionServiceGrpc
// / ActionLogServiceGrpc ports).

// GetAccountByAutomatedID loads a bot account by automated_id.
func (s *Store) GetAccountByAutomatedID(ctx context.Context, automatedID uuid.UUID) (*model.Account, error) {
	var entity AccountEntity
	if err := s.DB.WithContext(ctx).Where("automated_id = ?", automatedID).Take(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return accountFromEntity(&entity), nil
}

// GetAccountsByAutomatedIDs loads bot accounts by automated ids.
func (s *Store) GetAccountsByAutomatedIDs(ctx context.Context, ids []uuid.UUID) ([]model.Account, error) {
	var entities []AccountEntity
	if err := s.DB.WithContext(ctx).Where("automated_id IN ?", ids).Find(&entities).Error; err != nil {
		return nil, err
	}
	accounts := make([]model.Account, 0, len(entities))
	for i := range entities {
		accounts = append(accounts, *accountFromEntity(&entities[i]))
	}
	return accounts, nil
}

// GetAccountsByNames loads accounts by name. Direct matches come first; a
// name with no current holder falls back to the most recent former owner
// recorded in account_name_history (paid renames), so stale links keep
// resolving without shadowing a new user who took the name.
func (s *Store) GetAccountsByNames(ctx context.Context, names []string) ([]model.Account, error) {
	var entities []AccountEntity
	if err := s.DB.WithContext(ctx).Where("name IN ?", names).Find(&entities).Error; err != nil {
		return nil, err
	}
	accounts := make([]model.Account, 0, len(entities))
	matched := make(map[string]bool, len(names))
	for i := range entities {
		account := accountFromEntity(&entities[i])
		accounts = append(accounts, *account)
		matched[account.Name] = true
	}
	for _, name := range names {
		if matched[name] {
			continue
		}
		owner, err := s.GetAccountNameHistoryOwner(ctx, name)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		accounts = append(accounts, *owner)
	}
	return accounts, nil
}

// ListConnections lists an account's connections (full rows incl. tokens).
func (s *Store) ListConnectionsWithTokens(ctx context.Context, accountID uuid.UUID, provider *string) ([]model.Connection, error) {
	query := s.DB.WithContext(ctx).Model(&ConnectionEntity{}).Where("account_id = ?", accountID)
	if provider != nil {
		query = query.Where("provider = ?", *provider)
	}
	var entities []ConnectionEntity
	if err := query.Find(&entities).Error; err != nil {
		return nil, err
	}
	connections := make([]model.Connection, 0, len(entities))
	for i := range entities {
		connections = append(connections, connectionFromEntity(&entities[i]))
	}
	return connections, nil
}

// GetConnectionFullByID loads a connection by id (full row incl. tokens).
func (s *Store) GetConnectionFullByID(ctx context.Context, id uuid.UUID) (*model.Connection, error) {
	var entity ConnectionEntity
	if err := s.DB.WithContext(ctx).Where("id = ?", id).Take(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	connection := connectionFromEntity(&entity)
	return &connection, nil
}

// GetConnectionByProviderAndIdentifier loads a connection by provider+identifier.
func (s *Store) GetConnectionByProviderAndIdentifier(ctx context.Context, provider, providedIdentifier string) (*model.Connection, error) {
	var entity ConnectionEntity
	// Unscoped: the previous statement resolved soft-deleted connections too,
	// preferring live rows through its ORDER BY rather than filtering them out.
	if err := s.DB.WithContext(ctx).Unscoped().
		Where("LOWER(provider) = LOWER(?)", provider).
		Where("provided_identifier = ?", providedIdentifier).
		Order("(deleted_at IS NULL) DESC, updated_at DESC").
		Take(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	connection := connectionFromEntity(&entity)
	return &connection, nil
}

// UpdateConnectionAccessToken refreshes a connection's access token.
func (s *Store) UpdateConnectionAccessToken(ctx context.Context, id string, accessToken string, now time.Time) error {
	// Unscoped: the previous statement updated the row regardless of its
	// soft-delete state.
	return s.DB.WithContext(ctx).Unscoped().Model(&ConnectionEntity{}).Where("id = ?", id).
		Updates(map[string]any{"access_token": accessToken, "last_used_at": now, "updated_at": now}).Error
}

// GetSuperuserActorIDs returns actors of superuser/root groups.
func (s *Store) GetSuperuserActorIDs(ctx context.Context) ([]string, error) {
	var actors []string
	err := s.DB.WithContext(ctx).Model(&PermissionGroupMemberEntity{}).
		Joins(`JOIN permission_groups g ON g.id = permission_group_members.group_id AND g.deleted_at IS NULL`).
		Where(`g."key" IN ?`, []string{"superuser", "root"}).
		Distinct().
		Pluck("permission_group_members.actor", &actors).Error
	if err != nil {
		return nil, err
	}
	return actors, nil
}

// CreateApiKeyWithSession persists an API key and its backing session in one
// GORM transaction. API-key sessions have no requested audiences or scopes.
func (s *Store) CreateApiKeyWithSession(ctx context.Context, accountID uuid.UUID, label string, expiredAt *time.Time, appID, parentSessionID *uuid.UUID) (uuid.UUID, uuid.UUID, error) {
	now := time.Now().UTC()
	sessionID := uuid.New()
	keyID := uuid.New()
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&AuthSessionEntity{
			ID:              sessionID,
			CreatedAt:       now,
			UpdatedAt:       now,
			AccountID:       accountID,
			AppID:           appID,
			Audiences:       datatypes.JSON([]byte("[]")),
			Epoch:           0,
			ExpiredAt:       expiredAt,
			LastGrantedAt:   &now,
			ParentSessionID: parentSessionID,
			Scopes:          datatypes.JSON([]byte("[]")),
			Type:            int(model.SessionTypeApiKey),
		}).Error; err != nil {
			return err
		}
		return tx.Create(&APIKeyEntity{
			ID: keyID,
			EntityBase: EntityBase{
				CreatedAt: now,
				UpdatedAt: now,
			},
			AccountID: accountID,
			AppID:     appID,
			Label:     label,
			SessionID: sessionID,
		}).Error
	})
	return sessionID, keyID, err
}

// ListApiKeysByAccount lists an account's keys with their backing session
// expiry and session ids.
func (s *Store) ListApiKeysByAccount(ctx context.Context, accountID string) ([]model.ApiKey, error) {
	var entities []APIKeyEntity
	if err := s.DB.WithContext(ctx).Where("account_id = ?", accountID).Order("created_at").Find(&entities).Error; err != nil {
		return nil, err
	}
	sessionIDs := make([]uuid.UUID, 0, len(entities))
	for i := range entities {
		sessionIDs = append(sessionIDs, entities[i].SessionID)
	}
	// Batch-load the backing sessions. Unscoped: the previous LEFT JOIN read
	// auth_sessions regardless of its soft-delete state; a missing (or
	// deleted) session row simply leaves the key's expiry NULL.
	expiries := make(map[uuid.UUID]*time.Time, len(sessionIDs))
	if len(sessionIDs) > 0 {
		var sessions []AuthSessionEntity
		if err := s.DB.WithContext(ctx).Unscoped().Select("id", "expired_at").
			Where("id IN ?", sessionIDs).Find(&sessions).Error; err != nil {
			return nil, err
		}
		for i := range sessions {
			expiries[sessions[i].ID] = sessions[i].ExpiredAt
		}
	}
	keys := make([]model.ApiKey, 0, len(entities))
	for i := range entities {
		entity := &entities[i]
		keys = append(keys, model.ApiKey{
			Id:        entity.ID.String(),
			Label:     entity.Label,
			AccountId: entity.AccountID.String(),
			AppId:     uuidPtrStr(entity.AppID),
			SessionId: entity.SessionID.String(),
			ExpiredAt: timePtr(expiries[entity.SessionID]),
			CreatedAt: timePtr(&entity.CreatedAt),
			UpdatedAt: timePtr(&entity.UpdatedAt),
			DeletedAt: deletedTime(entity.DeletedAt),
		})
	}
	return keys, nil
}

// UpdateApiKeyLabel renames an api key.
func (s *Store) UpdateApiKeyLabel(ctx context.Context, id string, label string, now time.Time) error {
	// Unscoped: the previous statement renamed the row regardless of its
	// soft-delete state.
	return s.DB.WithContext(ctx).Unscoped().Model(&APIKeyEntity{}).Where("id = ?", id).
		Updates(map[string]any{"label": label, "updated_at": now}).Error
}

// nodeActorScopeWhere is the actor scope of the FindPermissionNodeAsync
// translation: direct nodes (group_id IS NULL, actor, node type) plus the
// nodes of groups the actor currently belongs to (group nodes are stored with
// type 1; membership expiry/affected_at are checked here, the outer node
// filters are spelled by the caller). Placeholders: actor, node type, actor,
// now, now.
const nodeActorScopeWhere = `(
	(group_id IS NULL AND actor = ? AND type = ?)
	OR (group_id IS NOT NULL AND type = 1 AND EXISTS (
		SELECT 1 FROM permission_group_members gm
		WHERE gm.group_id = permission_nodes.group_id AND gm.actor = ?
		AND gm.deleted_at IS NULL
		AND (gm.expired_at IS NULL OR gm.expired_at > ?)
		AND (gm.affected_at IS NULL OR gm.affected_at <= ?)
	))
)`

// wildcardCandidateLimit caps the wildcard candidates, matching
// PermissionServiceOptions.MaxWildcardMatches.
const wildcardCandidateLimit = 100

// FindPermissionNodeValue resolves the effective node value for (actor, key),
// mirroring PermissionService.FindPermissionNodeAsync: exact match first, then
// the best wildcard among 100 candidates; actor scope = direct nodes + group
// memberships (expiry/affected_at respected). Returns the raw jsonb value,
// the matched key, and whether a node granted the permission exists.
func (s *Store) FindPermissionNodeValue(ctx context.Context, actor string, nodeType int, key string, now time.Time) ([]byte, string, bool, error) {
	var node PermissionNodeEntity
	err := s.DB.WithContext(ctx).Model(&PermissionNodeEntity{}).
		Select("value", "key").
		Where(`"key" = ?`, key).
		Where("(expired_at IS NULL OR expired_at > ?)", now).
		Where("(affected_at IS NULL OR affected_at <= ?)", now).
		Where(nodeActorScopeWhere, actor, nodeType, actor, now, now).
		Take(&node).Error
	if err == nil {
		return []byte(node.Value), node.Key, true, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, "", false, err
	}

	// Best wildcard match among 100 candidates (C# takes 100 ordered by key).
	var candidates []PermissionNodeEntity
	err = s.DB.WithContext(ctx).Model(&PermissionNodeEntity{}).
		Select("value", "key").
		Where(`"key" LIKE ?`, "%*%").
		Where("(expired_at IS NULL OR expired_at > ?)", now).
		Where("(affected_at IS NULL OR affected_at <= ?)", now).
		Where(nodeActorScopeWhere, actor, nodeType, actor, now, now).
		Order(`"key"`).Limit(wildcardCandidateLimit).
		Find(&candidates).Error
	if err != nil {
		return nil, "", false, err
	}
	bestScore := 0
	var bestValue []byte
	var bestKey string
	for i := range candidates {
		candidate := &candidates[i]
		if score := patternMatchScore(candidate.Key, key); score > bestScore {
			bestScore = score
			bestValue = []byte(candidate.Value)
			bestKey = candidate.Key
		}
	}
	if bestKey == "" {
		return nil, "", false, nil
	}
	return bestValue, bestKey, true, nil
}

// patternMatchScore mirrors the C# wildcard scoring: a pattern with fewer
// wildcards and closer length wins.
func patternMatchScore(pattern, target string) int {
	if !wildcardMatch(pattern, target) {
		return 0
	}
	wildcards := strings.Count(pattern, "*")
	score := 1000 - wildcards*100 - (len(pattern) - len(target))
	if score < 1 {
		return 1
	}
	return score
}

func wildcardMatch(pattern, target string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == target
	}
	if !strings.HasPrefix(target, parts[0]) {
		return false
	}
	rest := target[len(parts[0]):]
	for _, part := range parts[1 : len(parts)-1] {
		idx := strings.Index(rest, part)
		if idx < 0 {
			return false
		}
		rest = rest[idx+len(part):]
	}
	return strings.HasSuffix(rest, parts[len(parts)-1]) || parts[len(parts)-1] == ""
}

// GetBlockedPermissionKeys returns an actor's punishment-blocked keys.
func (s *Store) GetBlockedPermissionKeys(ctx context.Context, actor string, now time.Time) ([]string, error) {
	var punishments []PunishmentEntity
	if err := s.DB.WithContext(ctx).Model(&PunishmentEntity{}).
		Select("blocked_permissions").
		Where("account_id = ?", actor).
		Where("type = ?", model.PunishmentPermissionModification).
		Where("(expired_at IS NULL OR expired_at > ?)", now).
		Find(&punishments).Error; err != nil {
		return nil, err
	}
	var keys []string
	for i := range punishments {
		raw := punishments[i].BlockedPermissions
		if raw == nil || len(*raw) == 0 || string(*raw) == "null" {
			continue
		}
		var blocked []string
		_ = json.Unmarshal(*raw, &blocked)
		keys = append(keys, blocked...)
	}
	return keys, nil
}

// UpsertDefaultGroupMember enrolls an account in the `default` group,
// reviving soft-deleted memberships.
func (s *Store) UpsertDefaultGroupMember(ctx context.Context, accountID string, now time.Time) (bool, error) {
	var affected int64
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var group PermissionGroupEntity
		if err := tx.Where(`"key" = ?`, "default").Take(&group).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// Without a `default` group the previous INSERT ... SELECT
				// matched no rows and reported zero affected rows.
				return nil
			}
			return err
		}
		result := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "group_id"}, {Name: "actor"}},
			DoUpdates: clause.Assignments(map[string]any{"deleted_at": nil, "updated_at": now}),
		}).Create(&PermissionGroupMemberEntity{
			GroupID: group.ID,
			Actor:   accountID,
			EntityBase: EntityBase{
				CreatedAt: now,
				UpdatedAt: now,
			},
		})
		if result.Error != nil {
			return result.Error
		}
		affected = result.RowsAffected
		return nil
	})
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// Bot account helpers (BotAccountReceiverGrpc).

// CountAccountsByAutomatedID counts bot accounts with the automated id.
func (s *Store) CountAccountsByAutomatedID(ctx context.Context, automatedID uuid.UUID) (int, error) {
	var count int64
	if err := s.DB.WithContext(ctx).Model(&AccountEntity{}).
		Where("automated_id = ?", automatedID).Count(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}

// CountAccountsByNameCI counts accounts with the name (case-insensitive).
func (s *Store) CountAccountsByNameCI(ctx context.Context, name string) (int, error) {
	var count int64
	if err := s.DB.WithContext(ctx).Model(&AccountEntity{}).
		Where("LOWER(name) = LOWER(?)", name).Count(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}

// InsertAccountWithProfile inserts an account with its profile row.
func (s *Store) InsertAccountWithProfile(ctx context.Context, account *model.Account, now time.Time) error {
	accountID, err := uuid.Parse(account.Id)
	if err != nil {
		return err
	}
	var automatedID *uuid.UUID
	if account.AutomatedId != nil {
		if id, err := uuid.Parse(*account.AutomatedId); err == nil {
			automatedID = &id
		}
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&AccountEntity{
			ID: accountID,
			EntityBase: EntityBase{
				CreatedAt: now,
				UpdatedAt: now,
			},
			ActivatedAt: timeValue(account.ActivatedAt),
			AutomatedID: automatedID,
			IsSuperuser: account.IsSuperuser,
			Language:    account.Language,
			Name:        account.Name,
			Nick:        account.Nick,
			Region:      account.Region,
		}).Error; err != nil {
			return err
		}
		return tx.Create(&ProfileEntity{
			ID: uuid.New(),
			EntityBase: EntityBase{
				CreatedAt: now,
				UpdatedAt: now,
			},
			AccountID:     accountID,
			Experience:    0,
			SocialCredits: 100,
		}).Error
	})
}

// UpdateAccountWithProfile updates an account row.
func (s *Store) UpdateAccountWithProfile(ctx context.Context, account *model.Account, now time.Time) error {
	var automatedID *uuid.UUID
	if account.AutomatedId != nil {
		if id, err := uuid.Parse(*account.AutomatedId); err == nil {
			automatedID = &id
		}
	}
	// Unscoped: the previous statement updated the row regardless of its
	// soft-delete state.
	return s.DB.WithContext(ctx).Unscoped().Model(&AccountEntity{}).Where("id = ?", account.Id).
		Updates(map[string]any{
			"name":         account.Name,
			"nick":         account.Nick,
			"language":     account.Language,
			"region":       account.Region,
			"activated_at": account.ActivatedAt,
			"is_superuser": account.IsSuperuser,
			"automated_id": automatedID,
			"updated_at":   now,
		}).Error
}

// SoftDeleteAccountAndSessions soft-deletes an account and revokes sessions.
func (s *Store) SoftDeleteAccountAndSessions(ctx context.Context, accountID string, now time.Time) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Unscoped: the previous statements rewrote the rows regardless of
		// their soft-delete state (a re-deleted account refreshes its
		// updated_at, an already-revoked session still bumps its epoch).
		if err := tx.Unscoped().Model(&AccountEntity{}).Where("id = ?", accountID).
			Updates(map[string]any{"deleted_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		return tx.Unscoped().Model(&AuthSessionEntity{}).Where("account_id = ?", accountID).
			Updates(map[string]any{
				"expired_at": now,
				"epoch":      gorm.Expr("epoch + 1"),
				"updated_at": now,
			}).Error
	})
}

// SearchActionLogs queries action logs with optional filters.
func (s *Store) SearchActionLogs(ctx context.Context, accountID *uuid.UUID, actions []string, createdAfter, createdBefore *time.Time, orderDesc bool, offset, limit int) ([]model.ActionLog, error) {
	query := s.DB.WithContext(ctx).Model(&ActionLogEntity{})
	if accountID != nil {
		query = query.Where("account_id = ?", *accountID)
	}
	if len(actions) > 0 {
		query = query.Where("action IN ?", actions)
	}
	if createdAfter != nil {
		query = query.Where("created_at >= ?", *createdAfter)
	}
	if createdBefore != nil {
		query = query.Where("created_at <= ?", *createdBefore)
	}
	order := "created_at DESC, id DESC"
	if !orderDesc {
		order = "created_at ASC, id ASC"
	}
	var entities []ActionLogEntity
	if err := query.Order(order).Limit(limit).Offset(offset).Find(&entities).Error; err != nil {
		return nil, err
	}
	logs := make([]model.ActionLog, 0, len(entities))
	for i := range entities {
		logs = append(logs, *actionLogFromEntity(&entities[i]))
	}
	return logs, nil
}

func actionLogFromEntity(entity *ActionLogEntity) *model.ActionLog {
	log := &model.ActionLog{
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
	if len(entity.Meta) > 0 && string(entity.Meta) != "null" {
		_ = json.Unmarshal(entity.Meta, &log.Meta)
	}
	if entity.Location != nil && len(*entity.Location) > 0 && string(*entity.Location) != "null" {
		var point model.GeoPoint
		if err := json.Unmarshal(*entity.Location, &point); err == nil {
			log.Location = &point
		}
	}
	return log
}
