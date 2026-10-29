package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// Permission admin CRUD helpers (permission_groups / permission_nodes /
// permission_group_members), ported from PermissionAdminController.cs.

// PermissionGroup mirrors the permission_groups row.
type PermissionGroup struct {
	Id        string      `json:"id"`
	Key       string      `json:"key"`
	CreatedAt *model.Time `json:"created_at,omitempty"`
	UpdatedAt *model.Time `json:"updated_at,omitempty"`
	DeletedAt *model.Time `json:"deleted_at,omitempty"`
}

// PermissionNode mirrors the permission_nodes row.
type PermissionNode struct {
	Id         string          `json:"id"`
	Type       int             `json:"type"`
	Actor      string          `json:"actor"`
	Key        string          `json:"key"`
	Value      json.RawMessage `json:"value,omitempty"`
	ExpiredAt  *model.Time     `json:"expired_at,omitempty"`
	AffectedAt *model.Time     `json:"affected_at,omitempty"`
	GroupId    *string         `json:"group_id,omitempty"`
	CreatedAt  *model.Time     `json:"created_at,omitempty"`
	UpdatedAt  *model.Time     `json:"updated_at,omitempty"`
	DeletedAt  *model.Time     `json:"deleted_at,omitempty"`
}

// PermissionMember mirrors the permission_group_members row.
type PermissionMember struct {
	GroupId    string      `json:"group_id"`
	Actor      string      `json:"actor"`
	ExpiredAt  *model.Time `json:"expired_at,omitempty"`
	AffectedAt *model.Time `json:"affected_at,omitempty"`
	CreatedAt  *model.Time `json:"created_at,omitempty"`
	UpdatedAt  *model.Time `json:"updated_at,omitempty"`
	DeletedAt  *model.Time `json:"deleted_at,omitempty"`
}

// PermissionMemberWithGroup adds the group key to a member row.
type PermissionMemberWithGroup struct {
	PermissionMember
	GroupKey string `json:"group_key"`
}

// PermissionGroupSummary is a group row with node/member counts.
type PermissionGroupSummary struct {
	PermissionGroup
	NodeCount   int `json:"node_count"`
	MemberCount int `json:"member_count"`
}

// permissionGroupSummaryRow is the flat scan target of the paged group query:
// the wire type carries string ids and instant-typed timestamps, and
// deleted_at is never selected because the query only returns live rows.
type permissionGroupSummaryRow struct {
	ID          uuid.UUID
	Key         string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	NodeCount   int
	MemberCount int
}

// permissionMemberWithGroupRow is the flat scan target of the membership
// query joined against permission_groups (only live rows of both tables are
// returned, so no deleted_at is selected).
type permissionMemberWithGroupRow struct {
	GroupID    uuid.UUID
	Actor      string
	ExpiredAt  *time.Time
	AffectedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
	GroupKey   string
}

func permissionGroupFromEntity(entity *PermissionGroupEntity) *PermissionGroup {
	if entity == nil {
		return nil
	}
	return &PermissionGroup{
		Id:        entity.ID.String(),
		Key:       entity.Key,
		CreatedAt: timePtr(&entity.CreatedAt),
		UpdatedAt: timePtr(&entity.UpdatedAt),
		DeletedAt: deletedTime(entity.DeletedAt),
	}
}

func permissionGroupSummaryFromRow(row permissionGroupSummaryRow) PermissionGroupSummary {
	return PermissionGroupSummary{
		PermissionGroup: PermissionGroup{
			Id:        row.ID.String(),
			Key:       row.Key,
			CreatedAt: timePtr(&row.CreatedAt),
			UpdatedAt: timePtr(&row.UpdatedAt),
		},
		NodeCount:   row.NodeCount,
		MemberCount: row.MemberCount,
	}
}

func permissionNodeFromEntity(entity *PermissionNodeEntity) *PermissionNode {
	if entity == nil {
		return nil
	}
	return &PermissionNode{
		Id:         entity.ID.String(),
		Type:       entity.Type,
		Actor:      entity.Actor,
		Key:        entity.Key,
		Value:      json.RawMessage(entity.Value),
		ExpiredAt:  timePtr(entity.ExpiredAt),
		AffectedAt: timePtr(entity.AffectedAt),
		GroupId:    uuidPtrStr(entity.GroupID),
		CreatedAt:  timePtr(&entity.CreatedAt),
		UpdatedAt:  timePtr(&entity.UpdatedAt),
		DeletedAt:  deletedTime(entity.DeletedAt),
	}
}

func permissionMemberFromEntity(entity *PermissionGroupMemberEntity) *PermissionMember {
	if entity == nil {
		return nil
	}
	return &PermissionMember{
		GroupId:    entity.GroupID.String(),
		Actor:      entity.Actor,
		ExpiredAt:  timePtr(entity.ExpiredAt),
		AffectedAt: timePtr(entity.AffectedAt),
		CreatedAt:  timePtr(&entity.CreatedAt),
		UpdatedAt:  timePtr(&entity.UpdatedAt),
		DeletedAt:  deletedTime(entity.DeletedAt),
	}
}

// permissionNodeValue wraps a wire JSON payload for the NOT NULL jsonb column:
// a nil datatypes.JSON would be sent as SQL NULL and rejected by Postgres, so
// an absent payload is stored as the JSON literal null.
func permissionNodeValue(value []byte) datatypes.JSON {
	if len(value) == 0 {
		return datatypes.JSON("null")
	}
	return datatypes.JSON(value)
}

// permissionSoftDeleteUpdates are the assignments of a soft delete: GORM's
// Delete would leave updated_at untouched, which these rows must keep in sync.
func permissionSoftDeleteUpdates(at time.Time) map[string]any {
	return map[string]any{"deleted_at": at, "updated_at": at}
}

// permissionNonEmptyActors preserves the wire shape of the raw scans these
// replace: a query with no matching rows returned a nil slice, while GORM's
// Pluck materializes an (empty) slice.
func permissionNonEmptyActors(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return values
}

// PermissionGroupList pages groups with counts (ILIKE key filter).
func (s *Store) PermissionGroupList(ctx context.Context, query string, take, offset int) ([]PermissionGroupSummary, int, error) {
	base := func() *gorm.DB {
		db := s.DB.WithContext(ctx).Model(&PermissionGroupEntity{})
		if strings.TrimSpace(query) != "" {
			db = db.Where(`"key" ILIKE '%' || ? || '%'`, query)
		}
		return db
	}

	var total int64
	if err := base().Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []permissionGroupSummaryRow
	if err := base().
		Select(`permission_groups.id, permission_groups."key", permission_groups.created_at, permission_groups.updated_at,
			(SELECT count(*) FROM permission_nodes n WHERE n.group_id = permission_groups.id AND n.deleted_at IS NULL) AS node_count,
			(SELECT count(*) FROM permission_group_members m WHERE m.group_id = permission_groups.id AND m.deleted_at IS NULL) AS member_count`).
		Order(`"key"`).
		Limit(take).
		Offset(offset).
		Scan(&rows).Error; err != nil {
		return nil, 0, err
	}

	var groups []PermissionGroupSummary
	for _, row := range rows {
		groups = append(groups, permissionGroupSummaryFromRow(row))
	}
	return groups, int(total), nil
}

// PermissionGroupGet loads a group by id.
func (s *Store) PermissionGroupGet(ctx context.Context, groupID uuid.UUID) (*PermissionGroup, error) {
	var group PermissionGroupEntity
	if err := s.DB.WithContext(ctx).Where("id = ?", groupID).First(&group).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return permissionGroupFromEntity(&group), nil
}

// PermissionGroupByKey loads a group by key.
func (s *Store) PermissionGroupByKey(ctx context.Context, key string) (*PermissionGroup, error) {
	var group PermissionGroupEntity
	if err := s.DB.WithContext(ctx).Where(`"key" = ?`, key).First(&group).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return permissionGroupFromEntity(&group), nil
}

// PermissionGroupKeyExists reports whether a group key is in use.
func (s *Store) PermissionGroupKeyExists(ctx context.Context, key string, excludeID *uuid.UUID) (bool, error) {
	query := s.DB.WithContext(ctx).Model(&PermissionGroupEntity{}).Where(`"key" = ?`, key)
	if excludeID != nil {
		query = query.Where("id <> ?", *excludeID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// PermissionGroupCreate inserts a group.
func (s *Store) PermissionGroupCreate(ctx context.Context, key string) (*PermissionGroup, error) {
	now := time.Now().UTC()
	group := &PermissionGroupEntity{
		ID:         uuid.New(),
		Key:        key,
		EntityBase: EntityBase{CreatedAt: now, UpdatedAt: now},
	}
	if err := s.DB.WithContext(ctx).Create(group).Error; err != nil {
		return nil, err
	}
	return permissionGroupFromEntity(group), nil
}

// PermissionGroupUpdateKey renames a group and rewrites its nodes' actors.
func (s *Store) PermissionGroupUpdateKey(ctx context.Context, groupID uuid.UUID, key string) (*PermissionGroup, error) {
	now := time.Now().UTC()
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&PermissionGroupEntity{}).Where("id = ?", groupID).
			Updates(map[string]any{"key": key, "updated_at": now}).Error; err != nil {
			return err
		}
		return tx.Model(&PermissionNodeEntity{}).Where("group_id = ?", groupID).
			Updates(map[string]any{"actor": "group:" + key, "updated_at": now}).Error
	})
	if err != nil {
		return nil, err
	}
	return s.PermissionGroupGet(ctx, groupID)
}

// PermissionGroupDelete soft-deletes a group, its nodes and members; returns
// the member actors (for notification).
func (s *Store) PermissionGroupDelete(ctx context.Context, groupID uuid.UUID) ([]string, error) {
	var actors []string
	if err := s.DB.WithContext(ctx).Model(&PermissionGroupMemberEntity{}).
		Where("group_id = ?", groupID).
		Pluck("actor", &actors).Error; err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&PermissionGroupEntity{}).Where("id = ?", groupID).
			Updates(permissionSoftDeleteUpdates(now)).Error; err != nil {
			return err
		}
		if err := tx.Model(&PermissionNodeEntity{}).Where("group_id = ?", groupID).
			Updates(permissionSoftDeleteUpdates(now)).Error; err != nil {
			return err
		}
		return tx.Model(&PermissionGroupMemberEntity{}).Where("group_id = ?", groupID).
			Updates(permissionSoftDeleteUpdates(now)).Error
	})
	if err != nil {
		return nil, err
	}
	return permissionNonEmptyActors(actors), nil
}

// PermissionNodeList pages a group's nodes.
func (s *Store) PermissionNodeList(ctx context.Context, groupID uuid.UUID, take, offset int) ([]PermissionNode, int, error) {
	base := func() *gorm.DB {
		return s.DB.WithContext(ctx).Model(&PermissionNodeEntity{}).Where("group_id = ?", groupID)
	}

	var total int64
	if err := base().Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var entities []PermissionNodeEntity
	if err := base().Order(`"key"`).Limit(take).Offset(offset).Find(&entities).Error; err != nil {
		return nil, 0, err
	}

	var nodes []PermissionNode
	for index := range entities {
		nodes = append(nodes, *permissionNodeFromEntity(&entities[index]))
	}
	return nodes, int(total), nil
}

// PermissionNodeGet loads one node by group+key.
func (s *Store) PermissionNodeGet(ctx context.Context, groupID uuid.UUID, key string) (*PermissionNode, error) {
	var entity PermissionNodeEntity
	if err := s.DB.WithContext(ctx).Where(`group_id = ? AND "key" = ?`, groupID, key).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return permissionNodeFromEntity(&entity), nil
}

// PermissionNodeUpsert inserts or updates a node.
func (s *Store) PermissionNodeUpsert(ctx context.Context, groupID uuid.UUID, key string, value []byte, actor string, nodeType int, expiredAt, affectedAt *model.Time) (*PermissionNode, error) {
	now := time.Now().UTC()
	entity := &PermissionNodeEntity{
		ID:         uuid.New(),
		Type:       nodeType,
		Actor:      actor,
		Key:        key,
		Value:      permissionNodeValue(value),
		ExpiredAt:  timeValue(expiredAt),
		AffectedAt: timeValue(affectedAt),
		GroupID:    &groupID,
		EntityBase: EntityBase{CreatedAt: now, UpdatedAt: now},
	}
	result := s.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(entity)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		// Conflict: update the existing row.
		return s.updatePermissionNode(ctx, groupID, key, value, expiredAt, affectedAt)
	}
	return permissionNodeFromEntity(entity), nil
}

func (s *Store) updatePermissionNode(ctx context.Context, groupID uuid.UUID, key string, value []byte, expiredAt, affectedAt *model.Time) (*PermissionNode, error) {
	entity := &PermissionNodeEntity{}
	result := s.DB.WithContext(ctx).Model(entity).
		Clauses(clause.Returning{}).
		Where(`group_id = ? AND "key" = ?`, groupID, key).
		Updates(map[string]any{
			"value":       permissionNodeValue(value),
			"expired_at":  timeValue(expiredAt),
			"affected_at": timeValue(affectedAt),
			"updated_at":  time.Now().UTC(),
		})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return permissionNodeFromEntity(entity), nil
}

// PermissionNodeDelete soft-deletes a node.
func (s *Store) PermissionNodeDelete(ctx context.Context, groupID uuid.UUID, key string) error {
	return s.DB.WithContext(ctx).Model(&PermissionNodeEntity{}).
		Where(`group_id = ? AND "key" = ?`, groupID, key).
		Updates(permissionSoftDeleteUpdates(time.Now().UTC())).Error
}

// PermissionMemberList pages a group's members.
func (s *Store) PermissionMemberList(ctx context.Context, groupID uuid.UUID, take, offset int) ([]PermissionMember, int, error) {
	base := func() *gorm.DB {
		return s.DB.WithContext(ctx).Model(&PermissionGroupMemberEntity{}).Where("group_id = ?", groupID)
	}

	var total int64
	if err := base().Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var entities []PermissionGroupMemberEntity
	if err := base().Order("actor").Limit(take).Offset(offset).Find(&entities).Error; err != nil {
		return nil, 0, err
	}

	var members []PermissionMember
	for index := range entities {
		members = append(members, *permissionMemberFromEntity(&entities[index]))
	}
	return members, int(total), nil
}

// PermissionMemberGet loads one member.
func (s *Store) PermissionMemberGet(ctx context.Context, groupID uuid.UUID, actor string) (*PermissionMember, error) {
	var entity PermissionGroupMemberEntity
	if err := s.DB.WithContext(ctx).Where("group_id = ? AND actor = ?", groupID, actor).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return permissionMemberFromEntity(&entity), nil
}

// PermissionMemberUpsert inserts or revives a membership.
func (s *Store) PermissionMemberUpsert(ctx context.Context, groupID uuid.UUID, actor string, expiredAt, affectedAt *model.Time) (*PermissionMember, error) {
	now := time.Now().UTC()
	entity := &PermissionGroupMemberEntity{
		GroupID:    groupID,
		Actor:      actor,
		ExpiredAt:  timeValue(expiredAt),
		AffectedAt: timeValue(affectedAt),
		EntityBase: EntityBase{CreatedAt: now, UpdatedAt: now},
	}
	result := s.DB.WithContext(ctx).Clauses(
		clause.OnConflict{
			Columns: []clause.Column{{Name: "group_id"}, {Name: "actor"}},
			DoUpdates: clause.Assignments(map[string]any{
				"expired_at":  timeValue(expiredAt),
				"affected_at": timeValue(affectedAt),
				"deleted_at":  nil,
				"updated_at":  now,
			}),
		},
		clause.Returning{},
	).Create(entity)
	if result.Error != nil {
		return nil, result.Error
	}
	return permissionMemberFromEntity(entity), nil
}

// PermissionMemberDelete soft-deletes a membership.
func (s *Store) PermissionMemberDelete(ctx context.Context, groupID uuid.UUID, actor string) error {
	return s.DB.WithContext(ctx).Model(&PermissionGroupMemberEntity{}).
		Where("group_id = ? AND actor = ?", groupID, actor).
		Updates(permissionSoftDeleteUpdates(time.Now().UTC())).Error
}

// PermissionGroupMemberActors lists a group's member actors.
func (s *Store) PermissionGroupMemberActors(ctx context.Context, groupID uuid.UUID) ([]string, error) {
	var actors []string
	if err := s.DB.WithContext(ctx).Model(&PermissionGroupMemberEntity{}).
		Where("group_id = ?", groupID).
		Order("actor").
		Pluck("actor", &actors).Error; err != nil {
		return nil, err
	}
	return permissionNonEmptyActors(actors), nil
}

// PermissionMembersForActor lists an actor's memberships with group keys.
func (s *Store) PermissionMembersForActor(ctx context.Context, actor string) ([]PermissionMemberWithGroup, error) {
	var rows []permissionMemberWithGroupRow
	if err := s.DB.WithContext(ctx).Model(&PermissionGroupMemberEntity{}).
		Select(`permission_group_members.group_id, permission_group_members.actor, permission_group_members.expired_at,
			permission_group_members.affected_at, permission_group_members.created_at, permission_group_members.updated_at,
			permission_groups."key" AS group_key`).
		Joins(`JOIN permission_groups ON permission_groups.id = permission_group_members.group_id AND permission_groups.deleted_at IS NULL`).
		Where("permission_group_members.actor = ?", actor).
		Order(`permission_groups."key"`).
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	var members []PermissionMemberWithGroup
	for _, row := range rows {
		members = append(members, PermissionMemberWithGroup{
			PermissionMember: PermissionMember{
				GroupId:    row.GroupID.String(),
				Actor:      row.Actor,
				ExpiredAt:  timePtr(row.ExpiredAt),
				AffectedAt: timePtr(row.AffectedAt),
				CreatedAt:  timePtr(&row.CreatedAt),
				UpdatedAt:  timePtr(&row.UpdatedAt),
			},
			GroupKey: row.GroupKey,
		})
	}
	return members, nil
}

// PermissionDirectNodes lists an actor's direct nodes.
func (s *Store) PermissionDirectNodes(ctx context.Context, actor string) ([]PermissionNode, error) {
	var entities []PermissionNodeEntity
	if err := s.DB.WithContext(ctx).
		Where("actor = ? AND group_id IS NULL", actor).
		Order(`"key"`).
		Find(&entities).Error; err != nil {
		return nil, err
	}

	var nodes []PermissionNode
	for index := range entities {
		nodes = append(nodes, *permissionNodeFromEntity(&entities[index]))
	}
	return nodes, nil
}

// PermissionEffectiveNodes lists the actor's effective nodes (direct + group).
func (s *Store) PermissionEffectiveNodes(ctx context.Context, actor string, now time.Time) ([]PermissionNode, error) {
	var entities []PermissionNodeEntity
	if err := s.DB.WithContext(ctx).
		Where("(expired_at IS NULL OR expired_at > ?)", now).
		Where("(affected_at IS NULL OR affected_at <= ?)", now).
		Where(`((group_id IS NULL AND actor = ? AND type = 0)
			OR (group_id IS NOT NULL AND type = 1 AND EXISTS (
				SELECT 1 FROM permission_group_members gm
				WHERE gm.group_id = permission_nodes.group_id AND gm.actor = ? AND gm.deleted_at IS NULL
				AND (gm.expired_at IS NULL OR gm.expired_at > ?)
				AND (gm.affected_at IS NULL OR gm.affected_at <= ?)
			)))`, actor, actor, now, now).
		Order(`"key"`).
		Find(&entities).Error; err != nil {
		return nil, err
	}

	var nodes []PermissionNode
	for index := range entities {
		nodes = append(nodes, *permissionNodeFromEntity(&entities[index]))
	}
	return nodes, nil
}

// InsertPermissionNode inserts an actor-scoped node (group_id NULL),
// mirroring PermissionService.AddPermissionNode.
func (s *Store) InsertPermissionNode(ctx context.Context, actor string, nodeType int, key string, value []byte, expiredAt, affectedAt *model.Time) (*PermissionNode, error) {
	now := time.Now().UTC()
	entity := &PermissionNodeEntity{
		ID:         uuid.New(),
		Type:       nodeType,
		Actor:      actor,
		Key:        key,
		Value:      permissionNodeValue(value),
		ExpiredAt:  timeValue(expiredAt),
		AffectedAt: timeValue(affectedAt),
		EntityBase: EntityBase{CreatedAt: now, UpdatedAt: now},
	}
	if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
		return nil, err
	}
	return permissionNodeFromEntity(entity), nil
}

// RemovePermissionNode soft-deletes an actor-scoped node (group_id NULL),
// optionally filtered by type.
func (s *Store) RemovePermissionNode(ctx context.Context, actor, key string, nodeType *int, now time.Time) error {
	query := s.DB.WithContext(ctx).Model(&PermissionNodeEntity{}).
		Where(`actor = ? AND "key" = ? AND group_id IS NULL`, actor, key)
	if nodeType != nil {
		query = query.Where("type = ?", *nodeType)
	}
	return query.Updates(permissionSoftDeleteUpdates(now)).Error
}

// RemovePermissionNodeFromGroup soft-deletes a group node by actor+key
// (type pinned to Group), mirroring PermissionService.RemovePermissionNodeFromGroup.
func (s *Store) RemovePermissionNodeFromGroup(ctx context.Context, groupID uuid.UUID, actor, key string, now time.Time) error {
	return s.DB.WithContext(ctx).Model(&PermissionNodeEntity{}).
		Where(`group_id = ? AND actor = ? AND "key" = ? AND type = 1`, groupID, actor, key).
		Updates(permissionSoftDeleteUpdates(now)).Error
}
