package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// Relationship helpers for the Passport-moved social graph
// (account_relationships; PK is (account_id, related_id), soft delete via
// deleted_at, expired_at for timed blocks/mutes/friend requests).

// RelationshipDelta mirrors RelationshipService.RelationshipDelta.
type RelationshipDelta struct {
	Added           []model.Relationship
	Updated         []model.Relationship
	Removed         []string
	ServerTimestamp time.Time
}

func relationshipFromEntity(entity *RelationshipEntity) model.Relationship {
	relationship := model.Relationship{
		AccountId: entity.AccountID.String(),
		RelatedId: entity.RelatedID.String(),
		Alias:     entity.Alias,
		ExpiredAt: timePtr(entity.ExpiredAt),
		Status:    model.RelationshipStatus(entity.Status),
		CreatedAt: timePtr(&entity.CreatedAt),
		UpdatedAt: timePtr(&entity.UpdatedAt),
		DeletedAt: deletedTime(entity.DeletedAt),
	}
	if entity.DegradeToStatus != nil {
		status := model.RelationshipStatus(*entity.DegradeToStatus)
		relationship.DegradeToStatus = &status
	}
	return relationship
}

func relationshipToEntity(relationship *model.Relationship) (*RelationshipEntity, error) {
	accountID, err := ParseUUID(relationship.AccountId)
	if err != nil {
		return nil, err
	}
	relatedID, err := ParseUUID(relationship.RelatedId)
	if err != nil {
		return nil, err
	}
	entity := &RelationshipEntity{
		AccountID: accountID,
		RelatedID: relatedID,
		Alias:     relationship.Alias,
		ExpiredAt: timeValue(relationship.ExpiredAt),
		Status:    int16(relationship.Status),
	}
	if relationship.DegradeToStatus != nil {
		status := int16(*relationship.DegradeToStatus)
		entity.DegradeToStatus = &status
	}
	if relationship.DeletedAt != nil {
		entity.DeletedAt = gorm.DeletedAt{Time: time.Time(*relationship.DeletedAt), Valid: true}
	}
	return entity, nil
}

// GetRelationship loads one directed relationship row. status/ignoreExpired/
// includeDeleted mirror BuildRelationshipQuery.
func (s *Store) GetRelationship(ctx context.Context, accountID, relatedID uuid.UUID, status *model.RelationshipStatus, ignoreExpired, includeDeleted bool) (*model.Relationship, error) {
	query := s.DB.WithContext(ctx)
	if includeDeleted {
		query = query.Unscoped()
	}
	query = query.Where("account_id = ? AND related_id = ?", accountID, relatedID)
	if !ignoreExpired {
		query = query.Where("(expired_at IS NULL OR expired_at > now())")
	}
	if status != nil {
		query = query.Where("status = ?", int16(*status))
	}
	var entity RelationshipEntity
	if err := query.First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	relationship := relationshipFromEntity(&entity)
	return &relationship, nil
}

// HasExistingRelationship reports whether a non-deleted relationship exists
// in either direction (mirrors RelationshipService.HasExistingRelationship).
func (s *Store) HasExistingRelationship(ctx context.Context, accountID, relatedID uuid.UUID) (bool, error) {
	var count int64
	err := s.DB.WithContext(ctx).Model(&RelationshipEntity{}).
		Where("(account_id = ? AND related_id = ?) OR (account_id = ? AND related_id = ?)",
			accountID, relatedID, relatedID, accountID).
		Count(&count).Error
	return count > 0, err
}

// InsertRelationship inserts a new relationship row (CreatedAt/UpdatedAt set
// server-side like the C# SaveChanges auditable interceptor).
func (s *Store) InsertRelationship(ctx context.Context, r *model.Relationship) error {
	now := time.Now().UTC()
	entity, err := relationshipToEntity(r)
	if err != nil {
		return err
	}
	entity.CreatedAt = now
	entity.UpdatedAt = now
	if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
		return err
	}
	r.CreatedAt = model.NewTime(now)
	r.UpdatedAt = model.NewTime(now)
	return nil
}

// SaveRelationship writes the mutable columns of an existing row
// (alias, expired_at, status, degrade_to_status, deleted_at, updated_at).
func (s *Store) SaveRelationship(ctx context.Context, r *model.Relationship) error {
	entity, err := relationshipToEntity(r)
	if err != nil {
		return err
	}
	// Unscoped: the row may currently be soft-deleted and this write can
	// revive it (deleted_at = NULL) or soft-delete it again.
	return s.DB.WithContext(ctx).Unscoped().Model(&RelationshipEntity{}).
		Where("account_id = ? AND related_id = ?", entity.AccountID, entity.RelatedID).
		Updates(map[string]any{
			"alias":             entity.Alias,
			"expired_at":        entity.ExpiredAt,
			"status":            entity.Status,
			"degrade_to_status": entity.DegradeToStatus,
			"deleted_at":        timeValue(r.DeletedAt),
			"updated_at":        time.Now().UTC(),
		}).Error
}

// HardDeleteRelationship physically deletes matching rows (mirrors
// ExecuteDeleteAsync used by DeleteFriendRequest).
func (s *Store) HardDeleteRelationship(ctx context.Context, accountID, relatedID uuid.UUID, status *model.RelationshipStatus) (int64, error) {
	query := s.DB.WithContext(ctx).Unscoped().Where("account_id = ? AND related_id = ?", accountID, relatedID)
	if status != nil {
		query = query.Where("status = ?", int16(*status))
	}
	result := query.Delete(&RelationshipEntity{})
	return result.RowsAffected, result.Error
}

// ListRelationshipsPage lists the account's outgoing non-pending
// relationships ordered by created_at desc (mirrors ListRelationships) and
// returns the total count for X-Total.
func (s *Store) ListRelationshipsPage(ctx context.Context, accountID uuid.UUID, offset, take int) ([]model.Relationship, int, error) {
	var total int64
	if err := s.DB.WithContext(ctx).Model(&RelationshipEntity{}).
		Where("account_id = ? AND status != ?", accountID, int16(model.RelationshipPending)).
		Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var entities []RelationshipEntity
	if err := s.DB.WithContext(ctx).Model(&RelationshipEntity{}).
		Where("account_id = ? AND status != ?", accountID, int16(model.RelationshipPending)).
		Order("created_at DESC").Offset(offset).Limit(take).
		Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	var relationships []model.Relationship
	for i := range entities {
		relationships = append(relationships, relationshipFromEntity(&entities[i]))
	}
	return relationships, int(total), nil
}

// ListRelationshipRequests lists pending relationships where the account is
// either side (mirrors ListRelationshipRequests).
func (s *Store) ListRelationshipRequests(ctx context.Context, accountID uuid.UUID) ([]model.Relationship, error) {
	var entities []RelationshipEntity
	if err := s.DB.WithContext(ctx).Model(&RelationshipEntity{}).
		Where("status = ?", int16(model.RelationshipPending)).
		Where("account_id = ? OR related_id = ?", accountID, accountID).
		Where("(expired_at IS NULL OR expired_at > now())").
		Order("created_at").
		Find(&entities).Error; err != nil {
		return nil, err
	}
	var relationships []model.Relationship
	for i := range entities {
		relationships = append(relationships, relationshipFromEntity(&entities[i]))
	}
	return relationships, nil
}

// CountRelationshipsByStatus counts non-deleted rows with the given status
// (used for the 200 close-friend cap).
func (s *Store) CountRelationshipsByStatus(ctx context.Context, accountID uuid.UUID, status model.RelationshipStatus) (int, error) {
	var count int64
	err := s.DB.WithContext(ctx).Model(&RelationshipEntity{}).
		Where("account_id = ? AND status = ?", accountID, int16(status)).
		Count(&count).Error
	return int(count), err
}

// CountRelationshipDirections counts the non-expired friend/close-friend rows
// on each side of the given account: followers (rows where the account is the
// related side) and following (rows where the account is the account side).
// It is the total-only companion to ListFollowers/ListFollowing, used by the
// public profile summary where the accounts themselves are not needed.
func (s *Store) CountRelationshipDirections(ctx context.Context, accountID uuid.UUID) (followers, following int, err error) {
	type row struct {
		RelatedCount int64 `gorm:"column:related_count"`
		AccountCount int64 `gorm:"column:account_count"`
	}
	var counts row
	if err := s.DB.WithContext(ctx).Model(&RelationshipEntity{}).
		Select(`COUNT(*) FILTER (WHERE related_id = ?) AS related_count,
			COUNT(*) FILTER (WHERE account_id = ?) AS account_count`, accountID, accountID).
		Where("account_id = ? OR related_id = ?", accountID, accountID).
		Where("(expired_at IS NULL OR expired_at > now())").
		Where("status = ? OR status = ?", int16(model.RelationshipFriends), int16(model.RelationshipCloseFriend)).
		Scan(&counts).Error; err != nil {
		return 0, 0, err
	}
	return int(counts.RelatedCount), int(counts.AccountCount), nil
}

// CountMutualFriends counts accounts that hold a friend/close-friend row
// toward both accounts. The directed friendship rows are written on accept in
// both directions, so intersecting the two outgoing sets matches what the
// mutual-friends endpoint returns.
func (s *Store) CountMutualFriends(ctx context.Context, accountID, otherID uuid.UUID) (int, error) {
	if accountID == otherID {
		return 0, nil
	}
	var count int64
	err := s.DB.WithContext(ctx).Model(&RelationshipEntity{}).
		Where("account_id = ?", accountID).
		Where("related_id IN (?)", s.DB.WithContext(ctx).Model(&RelationshipEntity{}).
			Select("related_id").
			Where("account_id = ?", otherID).
			Where("(expired_at IS NULL OR expired_at > now())").
			Where("status = ? OR status = ?", int16(model.RelationshipFriends), int16(model.RelationshipCloseFriend))).
		Where("(expired_at IS NULL OR expired_at > now())").
		Where("status = ? OR status = ?", int16(model.RelationshipFriends), int16(model.RelationshipCloseFriend)).
		Count(&count).Error
	return int(count), err
}

// ListRelatedAccountIDs returns the non-expired related account IDs for a
// directed relationship status (mirrors GetCachedRelationships; the Friends
// status includes CloseFriend rows). isRelated=true flips the direction
// (accounts that have the given account as the related side).
func (s *Store) ListRelatedAccountIDs(ctx context.Context, accountID uuid.UUID, status model.RelationshipStatus, isRelated bool) ([]string, error) {
	selectCol, whereCol := "related_id", "account_id"
	if isRelated {
		selectCol, whereCol = "account_id", "related_id"
	}
	query := s.DB.WithContext(ctx).Model(&RelationshipEntity{}).
		Select(selectCol).
		Where(whereCol+" = ?", accountID).
		Where("(expired_at IS NULL OR expired_at > now())")
	if status == model.RelationshipFriends {
		query = query.Where("status = ? OR status = ?", int16(model.RelationshipFriends), int16(model.RelationshipCloseFriend))
	} else {
		query = query.Where("status = ?", int16(status))
	}
	var entities []RelationshipEntity
	if err := query.Find(&entities).Error; err != nil {
		return nil, err
	}
	var ids []string
	for i := range entities {
		if isRelated {
			ids = append(ids, entities[i].AccountID.String())
		} else {
			ids = append(ids, entities[i].RelatedID.String())
		}
	}
	return ids, nil
}

// IsBlockedEitherDirection reports whether either account blocks the other
// with a non-expired row (mirrors RelationshipService.IsBlockedEitherDirection).
func (s *Store) IsBlockedEitherDirection(ctx context.Context, accountID, relatedID uuid.UUID) (bool, error) {
	var count int64
	err := s.DB.WithContext(ctx).Model(&RelationshipEntity{}).
		Where("status = ?", int16(model.RelationshipBlocked)).
		Where("(expired_at IS NULL OR expired_at > now())").
		Where("(account_id = ? AND related_id = ?) OR (account_id = ? AND related_id = ?)",
			accountID, relatedID, relatedID, accountID).
		Count(&count).Error
	return count > 0, err
}

// ListAllBlockedAccountIDs returns the distinct non-expired account IDs
// blocked in either direction (mirrors ListAllBlockedAccountIds).
func (s *Store) ListAllBlockedAccountIDs(ctx context.Context, accountID uuid.UUID) ([]string, error) {
	var entities []RelationshipEntity
	if err := s.DB.WithContext(ctx).Model(&RelationshipEntity{}).
		Select("account_id", "related_id").
		Where("status = ?", int16(model.RelationshipBlocked)).
		Where("(expired_at IS NULL OR expired_at > now())").
		Where("account_id = ? OR related_id = ?", accountID, accountID).
		Find(&entities).Error; err != nil {
		return nil, err
	}
	var ids []string
	seen := make(map[string]bool, len(entities))
	for i := range entities {
		blocked := entities[i].AccountID
		if blocked == accountID {
			blocked = entities[i].RelatedID
		}
		id := blocked.String()
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}

// GetRelationshipDelta computes the added/updated/removed sets since a
// timestamp (mirrors RelationshipService.GetRelationshipDelta).
func (s *Store) GetRelationshipDelta(ctx context.Context, accountID uuid.UUID, since time.Time) (*RelationshipDelta, error) {
	delta := &RelationshipDelta{ServerTimestamp: time.Now().UTC()}

	var addedEntities []RelationshipEntity
	if err := s.DB.WithContext(ctx).Model(&RelationshipEntity{}).
		Where("account_id = ? AND created_at > ?", accountID, since).
		Find(&addedEntities).Error; err != nil {
		return nil, err
	}
	for i := range addedEntities {
		delta.Added = append(delta.Added, relationshipFromEntity(&addedEntities[i]))
	}

	var updatedEntities []RelationshipEntity
	if err := s.DB.WithContext(ctx).Model(&RelationshipEntity{}).
		Where("account_id = ? AND updated_at > ? AND created_at <= ?", accountID, since, since).
		Find(&updatedEntities).Error; err != nil {
		return nil, err
	}
	for i := range updatedEntities {
		delta.Updated = append(delta.Updated, relationshipFromEntity(&updatedEntities[i]))
	}

	var removedEntities []RelationshipEntity
	if err := s.DB.WithContext(ctx).Unscoped().Model(&RelationshipEntity{}).
		Select("related_id").
		Where("account_id = ? AND deleted_at IS NOT NULL AND deleted_at > ?", accountID, since).
		Find(&removedEntities).Error; err != nil {
		return nil, err
	}
	for i := range removedEntities {
		delta.Removed = append(delta.Removed, removedEntities[i].RelatedID.String())
	}
	return delta, nil
}

// ListOutgoingRelationships lists all non-deleted outgoing rows for an
// account (mirrors the inspect query — no status/expiry filter).
func (s *Store) ListOutgoingRelationships(ctx context.Context, accountID uuid.UUID) ([]model.Relationship, error) {
	var entities []RelationshipEntity
	if err := s.DB.WithContext(ctx).Model(&RelationshipEntity{}).
		Where("account_id = ?", accountID).
		Find(&entities).Error; err != nil {
		return nil, err
	}
	var relationships []model.Relationship
	for i := range entities {
		relationships = append(relationships, relationshipFromEntity(&entities[i]))
	}
	return relationships, nil
}

// ListFollowers returns the accounts following the given account (incoming
// friend/close-friend rows) with a total count.
func (s *Store) ListFollowers(ctx context.Context, accountID uuid.UUID, offset, take int) ([]model.Account, int, error) {
	return s.listFollowPage(ctx, accountID, offset, take, false)
}

// ListFollowing returns the accounts the given account follows (outgoing
// friend/close-friend rows) with a total count.
func (s *Store) ListFollowing(ctx context.Context, accountID uuid.UUID, offset, take int) ([]model.Account, int, error) {
	return s.listFollowPage(ctx, accountID, offset, take, true)
}

func (s *Store) listFollowPage(ctx context.Context, accountID uuid.UUID, offset, take int, isFollowing bool) ([]model.Account, int, error) {
	// Followers of X: incoming rows (r.related_id = X); the follower is on
	// the account_id side of the join.
	// Following of X: outgoing rows (r.account_id = X); the followed is on
	// the related_id side of the join.
	joinCol, whereCol := "account_id", "related_id"
	if isFollowing {
		joinCol, whereCol = "related_id", "account_id"
	}
	var total int64
	if err := s.DB.WithContext(ctx).Model(&RelationshipEntity{}).
		Where(whereCol+" = ?", accountID).
		Where("(expired_at IS NULL OR expired_at > now())").
		Where("status = ? OR status = ?", int16(model.RelationshipFriends), int16(model.RelationshipCloseFriend)).
		Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var entities []RelationshipEntity
	if err := s.DB.WithContext(ctx).Model(&RelationshipEntity{}).
		Select(joinCol).
		Where(whereCol+" = ?", accountID).
		Where("(expired_at IS NULL OR expired_at > now())").
		Where("status = ? OR status = ?", int16(model.RelationshipFriends), int16(model.RelationshipCloseFriend)).
		Order("created_at DESC").Offset(offset).Limit(take).
		Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	var relatedIDs []uuid.UUID
	for i := range entities {
		if isFollowing {
			relatedIDs = append(relatedIDs, entities[i].RelatedID)
		} else {
			relatedIDs = append(relatedIDs, entities[i].AccountID)
		}
	}
	if len(relatedIDs) == 0 {
		return nil, int(total), nil
	}
	// The join filtered soft-deleted accounts (a.deleted_at IS NULL), which
	// the GORM default scope applies here too; the total above counts the
	// relationship rows only, exactly like the C# COUNT.
	var accountEntities []AccountEntity
	if err := s.DB.WithContext(ctx).Where("id IN ?", relatedIDs).Find(&accountEntities).Error; err != nil {
		return nil, 0, err
	}
	byID := make(map[uuid.UUID]*AccountEntity, len(accountEntities))
	for i := range accountEntities {
		byID[accountEntities[i].ID] = &accountEntities[i]
	}
	var accounts []model.Account
	for _, id := range relatedIDs {
		if entity, ok := byID[id]; ok {
			accounts = append(accounts, *accountFromEntity(entity))
		}
	}
	return accounts, int(total), nil
}

// RelationshipSweepCounts reports the work the expiry sweeper has to do.
type RelationshipSweepCounts struct {
	Degraded int64
	Deleted  int64
}

// SweepExpiredRelationships applies expired_at to the graph, mirroring
// RelationshipService.ProcessExpiredRelationshipsAsync (the Quartz
// RelationshipExpiryJob that ran every five minutes):
//
//   - a row with degrade_to_status lapses into that status and clears
//     expired_at/degrade_to_status (the "timed mute/block, then back to
//     Friends" flow);
//   - every other row is soft-deleted, not removed: the C# db.Remove is
//     rewritten to DeletedAt = now by ApplyAuditableAndSoftDelete, and
//     ModelBase.DeletedAt is a global query filter there.
//
// The soft delete is load-bearing for the sync surface:
// GetRelationshipDelta builds Removed from `deleted_at IS NOT NULL AND
// deleted_at > since`, so physically deleting the row would silently drop
// the removal from every client's incremental sync (delta.Added/Updated use
// the default scope and ignore soft-deleted rows). Writes revive such a row
// rather than conflicting, because SaveRelationship is Unscoped and
// restoreRelationship clears DeletedAt.
//
// It returns the rows it changed so the caller can purge the shared caches;
// the table and the C# fleet's keys are the same, so a sweep that skipped
// the purge would leave a lapsed block answering from Redis for an hour.
func (s *Store) SweepExpiredRelationships(ctx context.Context, limit int) ([]model.Relationship, RelationshipSweepCounts, error) {
	var counts RelationshipSweepCounts

	var expired []RelationshipEntity
	if err := s.DB.WithContext(ctx).Model(&RelationshipEntity{}).
		Where("expired_at IS NOT NULL AND expired_at <= now()").
		Order("expired_at").
		Limit(limit).
		Find(&expired).Error; err != nil {
		return nil, counts, err
	}
	if len(expired) == 0 {
		return nil, counts, nil
	}

	now := time.Now().UTC()
	// changed carries every row this pass touched, degraded or deleted: the
	// caller purges both accounts' cache keys from it, and a lapsed mute that
	// degraded back to Friends lives in the same accounts:friends: entry.
	changed := make([]model.Relationship, 0, len(expired))
	for i := range expired {
		entity := &expired[i]
		row := relationshipFromEntity(entity)
		if entity.DegradeToStatus != nil {
			counts.Degraded++
			if err := s.DB.WithContext(ctx).Unscoped().Model(&RelationshipEntity{}).
				Where("account_id = ? AND related_id = ?", entity.AccountID, entity.RelatedID).
				Updates(map[string]any{
					"status":            *entity.DegradeToStatus,
					"expired_at":        nil,
					"degrade_to_status": nil,
					"updated_at":        now,
				}).Error; err != nil {
				return changed, counts, err
			}
			changed = append(changed, row)
			continue
		}
		counts.Deleted++
		if err := s.DB.WithContext(ctx).Unscoped().Model(&RelationshipEntity{}).
			Where("account_id = ? AND related_id = ?", entity.AccountID, entity.RelatedID).
			Updates(map[string]any{
				"deleted_at": now,
				"updated_at": now,
			}).Error; err != nil {
			return changed, counts, err
		}
		changed = append(changed, row)
	}
	return changed, counts, nil
}

// PurgeAccountRelationships physically removes every row naming the account on
// either side and returns them, so the caller can purge the shared caches.
//
// This mirrors the delete Passport's account-removal flow used to run against
// its own table (MagicSpellService.ExecuteSpellAccountRemoval deleted
// AccountRelationships both ways). The rows go away with the account: an
// account is never revived, so a soft delete would only leave a row that
// still satisfies HasExistingRelationship (which is Unscoped) and blocks the
// surviving side from ever relating to another account with the same id.
func (s *Store) PurgeAccountRelationships(ctx context.Context, accountID uuid.UUID) ([]model.Relationship, error) {
	var entities []RelationshipEntity
	if err := s.DB.WithContext(ctx).Unscoped().Model(&RelationshipEntity{}).
		Where("account_id = ? OR related_id = ?", accountID, accountID).
		Find(&entities).Error; err != nil {
		return nil, err
	}
	if len(entities) == 0 {
		return nil, nil
	}
	rows := make([]model.Relationship, 0, len(entities))
	for i := range entities {
		rows = append(rows, relationshipFromEntity(&entities[i]))
	}
	if err := s.DB.WithContext(ctx).Unscoped().
		Where("account_id = ? OR related_id = ?", accountID, accountID).
		Delete(&RelationshipEntity{}).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
