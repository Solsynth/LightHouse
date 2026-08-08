package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

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

// PermissionGroupList pages groups with counts (ILIKE key filter).
func (s *Store) PermissionGroupList(ctx context.Context, query string, take, offset int) ([]PermissionGroupSummary, int, error) {
	where := `WHERE g.deleted_at IS NULL`
	args := []any{}
	if strings.TrimSpace(query) != "" {
		args = append(args, query)
		where += ` AND g."key" ILIKE '%' || $1 || '%'`
	}
	var total int
	if err := s.queryRow(ctx, `SELECT count(*) FROM permission_groups g `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, take, offset)
	rows, err := s.query(ctx, `SELECT g.id, g."key", g.created_at, g.updated_at, g.deleted_at,
		(SELECT count(*) FROM permission_nodes n WHERE n.group_id = g.id AND n.deleted_at IS NULL) AS node_count,
		(SELECT count(*) FROM permission_group_members m WHERE m.group_id = g.id AND m.deleted_at IS NULL) AS member_count
		FROM permission_groups g `+where+` ORDER BY g."key" LIMIT $`+itoa(len(args)-1)+` OFFSET $`+itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var groups []PermissionGroupSummary
	for rows.Next() {
		var g PermissionGroupSummary
		if err := rows.Scan(&g.Id, &g.Key, &g.CreatedAt, &g.UpdatedAt, &g.DeletedAt, &g.NodeCount, &g.MemberCount); err != nil {
			return nil, 0, err
		}
		groups = append(groups, g)
	}
	return groups, total, rows.Err()
}

// PermissionGroupGet loads a group by id.
func (s *Store) PermissionGroupGet(ctx context.Context, groupID uuid.UUID) (*PermissionGroup, error) {
	var g PermissionGroup
	err := s.queryRow(ctx, `SELECT id, "key", created_at, updated_at, deleted_at FROM permission_groups WHERE id = $1 AND deleted_at IS NULL`, groupID).
		Scan(&g.Id, &g.Key, &g.CreatedAt, &g.UpdatedAt, &g.DeletedAt)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &g, nil
}

// PermissionGroupByKey loads a group by key.
func (s *Store) PermissionGroupByKey(ctx context.Context, key string) (*PermissionGroup, error) {
	var g PermissionGroup
	err := s.queryRow(ctx, `SELECT id, "key", created_at, updated_at, deleted_at FROM permission_groups WHERE "key" = $1 AND deleted_at IS NULL`, key).
		Scan(&g.Id, &g.Key, &g.CreatedAt, &g.UpdatedAt, &g.DeletedAt)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &g, nil
}

// PermissionGroupKeyExists reports whether a group key is in use.
func (s *Store) PermissionGroupKeyExists(ctx context.Context, key string, excludeID *uuid.UUID) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM permission_groups WHERE "key" = $1 AND deleted_at IS NULL`
	args := []any{key}
	if excludeID != nil {
		query += ` AND id <> $2`
		args = append(args, *excludeID)
	}
	query += `)`
	var exists bool
	err := s.queryRow(ctx, query, args...).Scan(&exists)
	return exists, err
}

// PermissionGroupCreate inserts a group.
func (s *Store) PermissionGroupCreate(ctx context.Context, key string) (*PermissionGroup, error) {
	var g PermissionGroup
	err := s.queryRow(ctx, `INSERT INTO permission_groups (id, "key", created_at, updated_at)
		VALUES (gen_random_uuid(), $1, now(), now()) RETURNING id, "key", created_at, updated_at, deleted_at`, key).
		Scan(&g.Id, &g.Key, &g.CreatedAt, &g.UpdatedAt, &g.DeletedAt)
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// PermissionGroupUpdateKey renames a group and rewrites its nodes' actors.
func (s *Store) PermissionGroupUpdateKey(ctx context.Context, groupID uuid.UUID, key string) (*PermissionGroup, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE permission_groups SET "key" = $1, updated_at = now() WHERE id = $2`, key, groupID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE permission_nodes SET actor = $1, updated_at = now() WHERE group_id = $2 AND deleted_at IS NULL`,
		"group:"+key, groupID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.PermissionGroupGet(ctx, groupID)
}

// PermissionGroupDelete soft-deletes a group, its nodes and members; returns
// the member actors (for notification).
func (s *Store) PermissionGroupDelete(ctx context.Context, groupID uuid.UUID) ([]string, error) {
	rows, err := s.query(ctx, `SELECT actor FROM permission_group_members WHERE group_id = $1 AND deleted_at IS NULL`, groupID)
	if err != nil {
		return nil, err
	}
	var actors []string
	for rows.Next() {
		var actor string
		if err := rows.Scan(&actor); err != nil {
			rows.Close()
			return nil, err
		}
		actors = append(actors, actor)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE permission_groups SET deleted_at = now(), updated_at = now() WHERE id = $1`, groupID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE permission_nodes SET deleted_at = now(), updated_at = now() WHERE group_id = $1 AND deleted_at IS NULL`, groupID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE permission_group_members SET deleted_at = now(), updated_at = now() WHERE group_id = $1 AND deleted_at IS NULL`, groupID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return actors, nil
}

// PermissionNodeList pages a group's nodes.
func (s *Store) PermissionNodeList(ctx context.Context, groupID uuid.UUID, take, offset int) ([]PermissionNode, int, error) {
	var total int
	if err := s.queryRow(ctx, `SELECT count(*) FROM permission_nodes WHERE group_id = $1 AND deleted_at IS NULL`, groupID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.query(ctx, `SELECT id, type, actor, "key", value, expired_at, affected_at, group_id, created_at, updated_at, deleted_at
		FROM permission_nodes WHERE group_id = $1 AND deleted_at IS NULL
		ORDER BY "key" LIMIT $2 OFFSET $3`, groupID, take, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var nodes []PermissionNode
	for rows.Next() {
		n, err := scanPermissionNode(rows)
		if err != nil {
			return nil, 0, err
		}
		nodes = append(nodes, *n)
	}
	return nodes, total, rows.Err()
}

// PermissionNodeGet loads one node by group+key.
func (s *Store) PermissionNodeGet(ctx context.Context, groupID uuid.UUID, key string) (*PermissionNode, error) {
	row := s.queryRow(ctx, `SELECT id, type, actor, "key", value, expired_at, affected_at, group_id, created_at, updated_at, deleted_at
		FROM permission_nodes WHERE group_id = $1 AND "key" = $2 AND deleted_at IS NULL`, groupID, key)
	n, err := scanPermissionNode(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return n, nil
}

// PermissionNodeUpsert inserts or updates a node.
func (s *Store) PermissionNodeUpsert(ctx context.Context, groupID uuid.UUID, key string, value []byte, actor string, nodeType int, expiredAt, affectedAt *model.Time) (*PermissionNode, error) {
	var n PermissionNode
	err := s.queryRow(ctx, `INSERT INTO permission_nodes (id, type, actor, "key", value, expired_at, affected_at, group_id, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, now(), now())
		ON CONFLICT DO NOTHING
		RETURNING id, type, actor, "key", value, expired_at, affected_at, group_id, created_at, updated_at, deleted_at`,
		nodeType, actor, key, value, expiredAt, affectedAt, groupID).Scan(
		&n.Id, &n.Type, &n.Actor, &n.Key, &n.Value, &n.ExpiredAt, &n.AffectedAt, &n.GroupId, &n.CreatedAt, &n.UpdatedAt, &n.DeletedAt)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// Conflict: update the existing row.
			return s.updatePermissionNode(ctx, groupID, key, value, expiredAt, affectedAt)
		}
		return nil, err
	}
	return &n, nil
}

func (s *Store) updatePermissionNode(ctx context.Context, groupID uuid.UUID, key string, value []byte, expiredAt, affectedAt *model.Time) (*PermissionNode, error) {
	row := s.queryRow(ctx, `UPDATE permission_nodes SET value = $1, expired_at = $2, affected_at = $3, updated_at = now()
		WHERE group_id = $4 AND "key" = $5 AND deleted_at IS NULL
		RETURNING id, type, actor, "key", value, expired_at, affected_at, group_id, created_at, updated_at, deleted_at`,
		value, expiredAt, affectedAt, groupID, key)
	n, err := scanPermissionNode(row)
	if err != nil {
		return nil, err
	}
	return n, nil
}

// PermissionNodeDelete soft-deletes a node.
func (s *Store) PermissionNodeDelete(ctx context.Context, groupID uuid.UUID, key string) error {
	_, err := s.exec(ctx, `UPDATE permission_nodes SET deleted_at = now(), updated_at = now()
		WHERE group_id = $1 AND "key" = $2 AND deleted_at IS NULL`, groupID, key)
	return err
}

// PermissionMemberList pages a group's members.
func (s *Store) PermissionMemberList(ctx context.Context, groupID uuid.UUID, take, offset int) ([]PermissionMember, int, error) {
	var total int
	if err := s.queryRow(ctx, `SELECT count(*) FROM permission_group_members WHERE group_id = $1 AND deleted_at IS NULL`, groupID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.query(ctx, `SELECT group_id, actor, expired_at, affected_at, created_at, updated_at, deleted_at
		FROM permission_group_members WHERE group_id = $1 AND deleted_at IS NULL ORDER BY actor LIMIT $2 OFFSET $3`, groupID, take, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var members []PermissionMember
	for rows.Next() {
		var m PermissionMember
		if err := rows.Scan(&m.GroupId, &m.Actor, &m.ExpiredAt, &m.AffectedAt, &m.CreatedAt, &m.UpdatedAt, &m.DeletedAt); err != nil {
			return nil, 0, err
		}
		members = append(members, m)
	}
	return members, total, rows.Err()
}

// PermissionMemberGet loads one member.
func (s *Store) PermissionMemberGet(ctx context.Context, groupID uuid.UUID, actor string) (*PermissionMember, error) {
	var m PermissionMember
	err := s.queryRow(ctx, `SELECT group_id, actor, expired_at, affected_at, created_at, updated_at, deleted_at
		FROM permission_group_members WHERE group_id = $1 AND actor = $2 AND deleted_at IS NULL`, groupID, actor).
		Scan(&m.GroupId, &m.Actor, &m.ExpiredAt, &m.AffectedAt, &m.CreatedAt, &m.UpdatedAt, &m.DeletedAt)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &m, nil
}

// PermissionMemberUpsert inserts or revives a membership.
func (s *Store) PermissionMemberUpsert(ctx context.Context, groupID uuid.UUID, actor string, expiredAt, affectedAt *model.Time) (*PermissionMember, error) {
	var m PermissionMember
	err := s.queryRow(ctx, `INSERT INTO permission_group_members (group_id, actor, expired_at, affected_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, now(), now())
		ON CONFLICT (group_id, actor) DO UPDATE SET expired_at = EXCLUDED.expired_at, affected_at = EXCLUDED.affected_at, deleted_at = NULL, updated_at = now()
		RETURNING group_id, actor, expired_at, affected_at, created_at, updated_at, deleted_at`,
		groupID, actor, expiredAt, affectedAt).Scan(
		&m.GroupId, &m.Actor, &m.ExpiredAt, &m.AffectedAt, &m.CreatedAt, &m.UpdatedAt, &m.DeletedAt)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// PermissionMemberDelete soft-deletes a membership.
func (s *Store) PermissionMemberDelete(ctx context.Context, groupID uuid.UUID, actor string) error {
	_, err := s.exec(ctx, `UPDATE permission_group_members SET deleted_at = now(), updated_at = now()
		WHERE group_id = $1 AND actor = $2 AND deleted_at IS NULL`, groupID, actor)
	return err
}

// PermissionGroupMemberActors lists a group's member actors.
func (s *Store) PermissionGroupMemberActors(ctx context.Context, groupID uuid.UUID) ([]string, error) {
	rows, err := s.query(ctx, `SELECT actor FROM permission_group_members WHERE group_id = $1 AND deleted_at IS NULL ORDER BY actor`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var actors []string
	for rows.Next() {
		var actor string
		if err := rows.Scan(&actor); err != nil {
			return nil, err
		}
		actors = append(actors, actor)
	}
	return actors, rows.Err()
}

// PermissionMembersForActor lists an actor's memberships with group keys.
func (s *Store) PermissionMembersForActor(ctx context.Context, actor string) ([]PermissionMemberWithGroup, error) {
	rows, err := s.query(ctx, `SELECT m.group_id, m.actor, m.expired_at, m.affected_at, m.created_at, m.updated_at, m.deleted_at, g."key"
		FROM permission_group_members m
		JOIN permission_groups g ON g.id = m.group_id AND g.deleted_at IS NULL
		WHERE m.actor = $1 AND m.deleted_at IS NULL ORDER BY g."key"`, actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []PermissionMemberWithGroup
	for rows.Next() {
		var m PermissionMemberWithGroup
		if err := rows.Scan(&m.GroupId, &m.Actor, &m.ExpiredAt, &m.AffectedAt, &m.CreatedAt, &m.UpdatedAt, &m.DeletedAt, &m.GroupKey); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// PermissionDirectNodes lists an actor's direct nodes.
func (s *Store) PermissionDirectNodes(ctx context.Context, actor string) ([]PermissionNode, error) {
	rows, err := s.query(ctx, `SELECT id, type, actor, "key", value, expired_at, affected_at, group_id, created_at, updated_at, deleted_at
		FROM permission_nodes WHERE actor = $1 AND group_id IS NULL AND deleted_at IS NULL ORDER BY "key"`, actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []PermissionNode
	for rows.Next() {
		n, err := scanPermissionNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, *n)
	}
	return nodes, rows.Err()
}

// PermissionEffectiveNodes lists the actor's effective nodes (direct + group).
func (s *Store) PermissionEffectiveNodes(ctx context.Context, actor string, now time.Time) ([]PermissionNode, error) {
	rows, err := s.query(ctx, `SELECT n.id, n.type, n.actor, n."key", n.value, n.expired_at, n.affected_at, n.group_id, n.created_at, n.updated_at, n.deleted_at
		FROM permission_nodes n
		WHERE n.deleted_at IS NULL
		AND (n.expired_at IS NULL OR n.expired_at > $2)
		AND (n.affected_at IS NULL OR n.affected_at <= $2)
		AND (
			(n.group_id IS NULL AND n.actor = $1 AND n.type = 0)
			OR (n.group_id IS NOT NULL AND n.type = 1 AND EXISTS (
				SELECT 1 FROM permission_group_members gm
				WHERE gm.group_id = n.group_id AND gm.actor = $1 AND gm.deleted_at IS NULL
				AND (gm.expired_at IS NULL OR gm.expired_at > $2)
				AND (gm.affected_at IS NULL OR gm.affected_at <= $2)
			))
		)
		ORDER BY n."key"`, actor, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []PermissionNode
	for rows.Next() {
		n, err := scanPermissionNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, *n)
	}
	return nodes, rows.Err()
}

func scanPermissionNode(row rowScanner) (*PermissionNode, error) {
	var n PermissionNode
	err := row.Scan(&n.Id, &n.Type, &n.Actor, &n.Key, &n.Value, &n.ExpiredAt, &n.AffectedAt, &n.GroupId, &n.CreatedAt, &n.UpdatedAt, &n.DeletedAt)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// InsertPermissionNode inserts an actor-scoped node (group_id NULL),
// mirroring PermissionService.AddPermissionNode.
func (s *Store) InsertPermissionNode(ctx context.Context, actor string, nodeType int, key string, value []byte, expiredAt, affectedAt *model.Time) (*PermissionNode, error) {
	var n PermissionNode
	err := s.queryRow(ctx, `INSERT INTO permission_nodes (id, type, actor, "key", value, expired_at, affected_at, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, now(), now())
		RETURNING id, type, actor, "key", value, expired_at, affected_at, group_id, created_at, updated_at, deleted_at`,
		nodeType, actor, key, value, expiredAt, affectedAt).Scan(
		&n.Id, &n.Type, &n.Actor, &n.Key, &n.Value, &n.ExpiredAt, &n.AffectedAt, &n.GroupId, &n.CreatedAt, &n.UpdatedAt, &n.DeletedAt)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// RemovePermissionNode soft-deletes an actor-scoped node (group_id NULL),
// optionally filtered by type.
func (s *Store) RemovePermissionNode(ctx context.Context, actor, key string, nodeType *int, now time.Time) error {
	query := `UPDATE permission_nodes SET deleted_at = $1, updated_at = $1
		WHERE actor = $2 AND "key" = $3 AND group_id IS NULL AND deleted_at IS NULL`
	args := []any{now, actor, key}
	if nodeType != nil {
		query += ` AND type = $4`
		args = append(args, *nodeType)
	}
	_, err := s.exec(ctx, query, args...)
	return err
}

// RemovePermissionNodeFromGroup soft-deletes a group node by actor+key
// (type pinned to Group), mirroring PermissionService.RemovePermissionNodeFromGroup.
func (s *Store) RemovePermissionNodeFromGroup(ctx context.Context, groupID uuid.UUID, actor, key string, now time.Time) error {
	_, err := s.exec(ctx, `UPDATE permission_nodes SET deleted_at = $1, updated_at = $1
		WHERE group_id = $2 AND actor = $3 AND "key" = $4 AND type = 1 AND deleted_at IS NULL`,
		now, groupID, actor, key)
	return err
}
