package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// Relationship helpers for the Passport-moved social graph
// (account_relationships; PK is (account_id, related_id), soft delete via
// deleted_at, expired_at for timed blocks/mutes/friend requests).

const relationshipColumns = `account_id, related_id, alias, created_at, updated_at, deleted_at, expired_at, status, degrade_to_status`

// accountJoinColumns qualifies the accounts columns for JOIN queries where
// created_at/updated_at/deleted_at exist on both sides.
const accountJoinColumns = `a.id, a.name, a.nick, a.language, a.region, a.activated_at, a.is_superuser, a.automated_id, a.created_at, a.updated_at, a.deleted_at`

// RelationshipDelta mirrors RelationshipService.RelationshipDelta.
type RelationshipDelta struct {
	Added           []model.Relationship
	Updated         []model.Relationship
	Removed         []string
	ServerTimestamp time.Time
}

// GetRelationship loads one directed relationship row. status/ignoreExpired/
// includeDeleted mirror BuildRelationshipQuery.
func (s *Store) GetRelationship(ctx context.Context, accountID, relatedID uuid.UUID, status *model.RelationshipStatus, ignoreExpired, includeDeleted bool) (*model.Relationship, error) {
	q := `SELECT ` + relationshipColumns + ` FROM account_relationships
		WHERE account_id = $1 AND related_id = $2`
	args := []any{accountID, relatedID}
	if !includeDeleted {
		q += ` AND deleted_at IS NULL`
	}
	if !ignoreExpired {
		q += ` AND (expired_at IS NULL OR expired_at > now())`
	}
	if status != nil {
		q += ` AND status = $3`
		args = append(args, *status)
	}
	return scanRelationship(s.DB.QueryRow(ctx, q, args...))
}

// HasExistingRelationship reports whether a non-deleted relationship exists
// in either direction (mirrors RelationshipService.HasExistingRelationship).
func (s *Store) HasExistingRelationship(ctx context.Context, accountID, relatedID uuid.UUID) (bool, error) {
	var count int
	err := s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM account_relationships
		WHERE deleted_at IS NULL AND (
			(account_id = $1 AND related_id = $2) OR
			(account_id = $2 AND related_id = $1))`, accountID, relatedID).Scan(&count)
	return count > 0, err
}

// InsertRelationship inserts a new relationship row (CreatedAt/UpdatedAt set
// server-side like the C# SaveChanges auditable interceptor).
func (s *Store) InsertRelationship(ctx context.Context, r *model.Relationship) error {
	now := time.Now().UTC()
	_, err := s.DB.Exec(ctx, `INSERT INTO account_relationships
		(account_id, related_id, alias, created_at, updated_at, expired_at, status, degrade_to_status)
		VALUES ($1, $2, $3, $4, $4, $5, $6, $7)`,
		r.AccountId, r.RelatedId, r.Alias, now, r.ExpiredAt, r.Status, r.DegradeToStatus)
	if err != nil {
		return err
	}
	r.CreatedAt = model.NewTime(now)
	r.UpdatedAt = model.NewTime(now)
	return nil
}

// SaveRelationship writes the mutable columns of an existing row
// (alias, expired_at, status, degrade_to_status, deleted_at, updated_at).
func (s *Store) SaveRelationship(ctx context.Context, r *model.Relationship) error {
	_, err := s.DB.Exec(ctx, `UPDATE account_relationships SET
		alias = $3, expired_at = $4, status = $5, degrade_to_status = $6,
		deleted_at = $7, updated_at = $8
		WHERE account_id = $1 AND related_id = $2`,
		r.AccountId, r.RelatedId, r.Alias, r.ExpiredAt, r.Status, r.DegradeToStatus,
		r.DeletedAt, time.Now().UTC())
	return err
}

// HardDeleteRelationship physically deletes matching rows (mirrors
// ExecuteDeleteAsync used by DeleteFriendRequest).
func (s *Store) HardDeleteRelationship(ctx context.Context, accountID, relatedID uuid.UUID, status *model.RelationshipStatus) (int64, error) {
	q := `DELETE FROM account_relationships WHERE account_id = $1 AND related_id = $2`
	args := []any{accountID, relatedID}
	if status != nil {
		q += ` AND status = $3`
		args = append(args, *status)
	}
	tag, err := s.DB.Exec(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ListRelationshipsPage lists the account's outgoing non-pending
// relationships ordered by created_at desc (mirrors ListRelationships) and
// returns the total count for X-Total.
func (s *Store) ListRelationshipsPage(ctx context.Context, accountID uuid.UUID, offset, take int) ([]model.Relationship, int, error) {
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM account_relationships
		WHERE account_id = $1 AND deleted_at IS NULL AND status != $2`,
		accountID, model.RelationshipPending).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.Query(ctx, `SELECT `+relationshipColumns+` FROM account_relationships
		WHERE account_id = $1 AND deleted_at IS NULL AND status != $2
		ORDER BY created_at DESC OFFSET $3 LIMIT $4`,
		accountID, model.RelationshipPending, offset, take)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	rels, err := collectRelationships(rows)
	return rels, total, err
}

// ListRelationshipRequests lists pending relationships where the account is
// either side (mirrors ListRelationshipRequests).
func (s *Store) ListRelationshipRequests(ctx context.Context, accountID uuid.UUID) ([]model.Relationship, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+relationshipColumns+` FROM account_relationships
		WHERE deleted_at IS NULL AND status = $1 AND (account_id = $2 OR related_id = $2)
		ORDER BY created_at`, model.RelationshipPending, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectRelationships(rows)
}

// CountRelationshipsByStatus counts non-deleted rows with the given status
// (used for the 200 close-friend cap).
func (s *Store) CountRelationshipsByStatus(ctx context.Context, accountID uuid.UUID, status model.RelationshipStatus) (int, error) {
	var count int
	err := s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM account_relationships
		WHERE account_id = $1 AND deleted_at IS NULL AND status = $2`, accountID, status).Scan(&count)
	return count, err
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
	q := `SELECT ` + selectCol + ` FROM account_relationships
		WHERE deleted_at IS NULL AND (expired_at IS NULL OR expired_at > now()) AND ` + whereCol + ` = $1`
	args := []any{accountID}
	if status == model.RelationshipFriends {
		q += ` AND (status = $2 OR status = $3)`
		args = append(args, model.RelationshipFriends, model.RelationshipCloseFriend)
	} else {
		q += ` AND status = $2`
		args = append(args, status)
	}
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListAllBlockedAccountIDs returns the distinct non-expired account IDs
// blocked in either direction (mirrors ListAllBlockedAccountIds).
func (s *Store) ListAllBlockedAccountIDs(ctx context.Context, accountID uuid.UUID) ([]string, error) {
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT
			CASE WHEN account_id = $1 THEN related_id ELSE account_id END
		FROM account_relationships
		WHERE deleted_at IS NULL AND status = $2
			AND (expired_at IS NULL OR expired_at > now())
			AND (account_id = $1 OR related_id = $1)`, accountID, model.RelationshipBlocked)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetRelationshipDelta computes the added/updated/removed sets since a
// timestamp (mirrors RelationshipService.GetRelationshipDelta).
func (s *Store) GetRelationshipDelta(ctx context.Context, accountID uuid.UUID, since time.Time) (*RelationshipDelta, error) {
	delta := &RelationshipDelta{ServerTimestamp: time.Now().UTC()}

	addedRows, err := s.DB.Query(ctx, `SELECT `+relationshipColumns+` FROM account_relationships
		WHERE account_id = $1 AND deleted_at IS NULL AND created_at > $2`, accountID, since)
	if err != nil {
		return nil, err
	}
	delta.Added, err = collectRelationships(addedRows)
	addedRows.Close()
	if err != nil {
		return nil, err
	}

	updatedRows, err := s.DB.Query(ctx, `SELECT `+relationshipColumns+` FROM account_relationships
		WHERE account_id = $1 AND deleted_at IS NULL AND updated_at > $2 AND created_at <= $2`,
		accountID, since)
	if err != nil {
		return nil, err
	}
	delta.Updated, err = collectRelationships(updatedRows)
	updatedRows.Close()
	if err != nil {
		return nil, err
	}

	removedRows, err := s.DB.Query(ctx, `SELECT related_id FROM account_relationships
		WHERE account_id = $1 AND deleted_at IS NOT NULL AND deleted_at > $2`, accountID, since)
	if err != nil {
		return nil, err
	}
	defer removedRows.Close()
	for removedRows.Next() {
		var id string
		if err := removedRows.Scan(&id); err != nil {
			return nil, err
		}
		delta.Removed = append(delta.Removed, id)
	}
	return delta, removedRows.Err()
}

// ListOutgoingRelationships lists all non-deleted outgoing rows for an
// account (mirrors the inspect query — no status/expiry filter).
func (s *Store) ListOutgoingRelationships(ctx context.Context, accountID uuid.UUID) ([]model.Relationship, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+relationshipColumns+` FROM account_relationships
		WHERE account_id = $1 AND deleted_at IS NULL`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectRelationships(rows)
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
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM account_relationships r
		WHERE r.deleted_at IS NULL AND (r.expired_at IS NULL OR r.expired_at > now())
			AND r.`+whereCol+` = $1 AND (r.status = $2 OR r.status = $3)`,
		accountID, model.RelationshipFriends, model.RelationshipCloseFriend).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.Query(ctx, `SELECT `+accountJoinColumns+` FROM account_relationships r
		JOIN accounts a ON a.id = r.`+joinCol+`
		WHERE r.deleted_at IS NULL AND (r.expired_at IS NULL OR r.expired_at > now())
			AND r.`+whereCol+` = $1 AND (r.status = $2 OR r.status = $3)
			AND a.deleted_at IS NULL
		ORDER BY r.created_at DESC OFFSET $4 LIMIT $5`,
		accountID, model.RelationshipFriends, model.RelationshipCloseFriend, offset, take)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var accounts []model.Account
	for rows.Next() {
		account, err := scanAccountJoin(rows)
		if err != nil {
			return nil, 0, err
		}
		accounts = append(accounts, *account)
	}
	return accounts, total, rows.Err()
}

func scanRelationship(row pgx.Row) (*model.Relationship, error) {
	r := &model.Relationship{}
	err := row.Scan(&r.AccountId, &r.RelatedId, &r.Alias, &r.CreatedAt, &r.UpdatedAt,
		&r.DeletedAt, &r.ExpiredAt, &r.Status, &r.DegradeToStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return r, nil
}

func collectRelationships(rows pgx.Rows) ([]model.Relationship, error) {
	var rels []model.Relationship
	for rows.Next() {
		r, err := scanRelationship(rows)
		if err != nil {
			return nil, err
		}
		rels = append(rels, *r)
	}
	return rels, rows.Err()
}

func scanAccountJoin(row pgx.Row) (*model.Account, error) {
	account := &model.Account{}
	var automatedID *uuid.UUID
	err := row.Scan(&account.Id, &account.Name, &account.Nick, &account.Language, &account.Region,
		&account.ActivatedAt, &account.IsSuperuser, &automatedID, &account.CreatedAt, &account.UpdatedAt, &account.DeletedAt)
	if err != nil {
		return nil, err
	}
	account.AutomatedId = uuidPtrStr(automatedID)
	return account, nil
}
