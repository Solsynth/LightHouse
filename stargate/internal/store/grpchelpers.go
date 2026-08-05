package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// Helpers for the inbound gRPC servers (AccountServiceGrpc / PermissionServiceGrpc
// / ActionLogServiceGrpc ports).

// GetAccountByAutomatedID loads a bot account by automated_id.
func (s *Store) GetAccountByAutomatedID(ctx context.Context, automatedID uuid.UUID) (*model.Account, error) {
	row := s.DB.QueryRow(ctx, `SELECT `+accountColumns+` FROM accounts WHERE automated_id = $1 AND deleted_at IS NULL`, automatedID)
	return scanAccount(row)
}

// GetAccountsByAutomatedIDs loads bot accounts by automated ids.
func (s *Store) GetAccountsByAutomatedIDs(ctx context.Context, ids []uuid.UUID) ([]model.Account, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+accountColumns+` FROM accounts WHERE automated_id = ANY($1) AND deleted_at IS NULL`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []model.Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, *a)
	}
	return accounts, rows.Err()
}

// GetAccountsByNames loads accounts by name.
func (s *Store) GetAccountsByNames(ctx context.Context, names []string) ([]model.Account, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+accountColumns+` FROM accounts WHERE name = ANY($1) AND deleted_at IS NULL`, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []model.Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, *a)
	}
	return accounts, rows.Err()
}

// ListConnections lists an account's connections (full rows incl. tokens).
func (s *Store) ListConnectionsWithTokens(ctx context.Context, accountID uuid.UUID, provider *string) ([]model.Connection, error) {
	query := `SELECT id, provider, provided_identifier, meta, access_token, refresh_token, last_used_at, is_public, account_id, created_at, updated_at, deleted_at
		FROM account_connections WHERE account_id = $1 AND deleted_at IS NULL`
	args := []any{accountID}
	if provider != nil {
		query += ` AND provider = $2`
		args = append(args, *provider)
	}
	rows, err := s.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var connections []model.Connection
	for rows.Next() {
		c, err := scanConnectionFull(rows)
		if err != nil {
			return nil, err
		}
		connections = append(connections, *c)
	}
	return connections, rows.Err()
}

// GetConnectionFullByID loads a connection by id (full row incl. tokens).
func (s *Store) GetConnectionFullByID(ctx context.Context, id uuid.UUID) (*model.Connection, error) {
	row := s.DB.QueryRow(ctx, `SELECT id, provider, provided_identifier, meta, access_token, refresh_token, last_used_at, is_public, account_id, created_at, updated_at, deleted_at
		FROM account_connections WHERE id = $1 AND deleted_at IS NULL`, id)
	c, err := scanConnectionFull(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return c, nil
}

// GetConnectionByProviderAndIdentifier loads a connection by provider+identifier.
func (s *Store) GetConnectionByProviderAndIdentifier(ctx context.Context, provider, providedIdentifier string) (*model.Connection, error) {
	row := s.DB.QueryRow(ctx, `SELECT id, provider, provided_identifier, meta, access_token, refresh_token, last_used_at, is_public, account_id, created_at, updated_at, deleted_at
		FROM account_connections WHERE LOWER(provider) = LOWER($1) AND provided_identifier = $2 AND deleted_at IS NULL LIMIT 1`, provider, providedIdentifier)
	c, err := scanConnectionFull(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return c, nil
}

// UpdateConnectionAccessToken refreshes a connection's access token.
func (s *Store) UpdateConnectionAccessToken(ctx context.Context, id string, accessToken string, now time.Time) error {
	_, err := s.DB.Exec(ctx, `UPDATE account_connections SET access_token = $1, last_used_at = $2, updated_at = $2 WHERE id = $3`,
		accessToken, now, id)
	return err
}

// GetSuperuserActorIDs returns actors of superuser/root groups.
func (s *Store) GetSuperuserActorIDs(ctx context.Context) ([]string, error) {
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT m.actor FROM permission_group_members m
		JOIN permission_groups g ON g.id = m.group_id AND g.deleted_at IS NULL
		WHERE g."key" IN ('superuser', 'root') AND m.deleted_at IS NULL`)
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

// ListApiKeysByAccount lists an account's keys with session ids.
func (s *Store) ListApiKeysByAccount(ctx context.Context, accountID string) ([]model.ApiKey, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, label, account_id, app_id, session_id, created_at, updated_at, expired_at, deleted_at
		FROM api_keys WHERE account_id = $1 AND deleted_at IS NULL ORDER BY created_at`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []model.ApiKey
	for rows.Next() {
		var k model.ApiKey
		var appID *uuid.UUID
		var sessionID uuid.UUID
		if err := rows.Scan(&k.Id, &k.Label, &k.AccountId, &appID, &sessionID, &k.CreatedAt, &k.UpdatedAt, &k.ExpiredAt, &k.DeletedAt); err != nil {
			return nil, err
		}
		k.AppId = uuidPtrStr(appID)
		k.SessionId = sessionID.String()
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// UpdateApiKeyLabel renames an api key.
func (s *Store) UpdateApiKeyLabel(ctx context.Context, id string, label string, now time.Time) error {
	_, err := s.DB.Exec(ctx, `UPDATE api_keys SET label = $1, updated_at = $2 WHERE id = $3`, label, now, id)
	return err
}

// FindPermissionNodeValue resolves the effective node value for (actor, key),
// mirroring PermissionService.FindPermissionNodeAsync: exact match first, then
// the best wildcard among 100 candidates; actor scope = direct nodes + group
// memberships (expiry/affected_at respected). Returns the raw jsonb value,
// the matched key, and whether a node granted the permission exists.
func (s *Store) FindPermissionNodeValue(ctx context.Context, actor string, nodeType int, key string, now time.Time) ([]byte, string, bool, error) {
	var value []byte
	var matched string
	err := s.DB.QueryRow(ctx, `SELECT n.value, n."key" FROM permission_nodes n
		WHERE n.deleted_at IS NULL
		AND (n.expired_at IS NULL OR n.expired_at > $3)
		AND (n.affected_at IS NULL OR n.affected_at <= $3)
		AND n."key" = $2
		AND (
			(n.group_id IS NULL AND n.actor = $1 AND n.type = $4)
			OR (n.group_id IS NOT NULL AND n.type = 1 AND EXISTS (
				SELECT 1 FROM permission_group_members gm
				WHERE gm.group_id = n.group_id AND gm.actor = $1 AND gm.deleted_at IS NULL
				AND (gm.expired_at IS NULL OR gm.expired_at > $3)
				AND (gm.affected_at IS NULL OR gm.affected_at <= $3)
			))
		)
		LIMIT 1`, actor, key, now, nodeType).Scan(&value, &matched)
	if err == nil {
		return value, matched, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, "", false, err
	}

	// Best wildcard match among 100 candidates (C# takes 100 ordered by key).
	rows, err := s.DB.Query(ctx, `SELECT n.value, n."key" FROM permission_nodes n
		WHERE n.deleted_at IS NULL
		AND (n.expired_at IS NULL OR n.expired_at > $3)
		AND (n.affected_at IS NULL OR n.affected_at <= $3)
		AND n."key" LIKE '%*%'
		AND (
			(n.group_id IS NULL AND n.actor = $1 AND n.type = $4)
			OR (n.group_id IS NOT NULL AND n.type = 1 AND EXISTS (
				SELECT 1 FROM permission_group_members gm
				WHERE gm.group_id = n.group_id AND gm.actor = $1 AND gm.deleted_at IS NULL
				AND (gm.expired_at IS NULL OR gm.expired_at > $3)
				AND (gm.affected_at IS NULL OR gm.affected_at <= $3)
			))
		)
		ORDER BY n."key" LIMIT 100`, actor, key, now, nodeType)
	if err != nil {
		return nil, "", false, err
	}
	defer rows.Close()
	bestScore := 0
	var bestValue []byte
	var bestKey string
	for rows.Next() {
		var v []byte
		var k string
		if err := rows.Scan(&v, &k); err != nil {
			return nil, "", false, err
		}
		if score := patternMatchScore(k, key); score > bestScore {
			bestScore = score
			bestValue = v
			bestKey = k
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, err
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
	rows, err := s.DB.Query(ctx, `SELECT blocked_permissions FROM punishments
		WHERE account_id = $1 AND type = 0 AND deleted_at IS NULL AND (expired_at IS NULL OR expired_at > $2)`, actor, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var blocked []string
		if len(raw) > 0 && string(raw) != "null" {
			_ = json.Unmarshal(raw, &blocked)
		}
		keys = append(keys, blocked...)
	}
	return keys, rows.Err()
}

// UpsertDefaultGroupMember enrolls an account in the `default` group,
// reviving soft-deleted memberships.
func (s *Store) UpsertDefaultGroupMember(ctx context.Context, accountID string, now time.Time) (bool, error) {
	tag, err := s.DB.Exec(ctx, `INSERT INTO permission_group_members (group_id, actor, created_at, updated_at)
		SELECT id, $1, $2, $2 FROM permission_groups WHERE "key" = 'default' AND deleted_at IS NULL
		ON CONFLICT (group_id, actor) DO UPDATE SET deleted_at = NULL, updated_at = $2`, accountID, now)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Bot account helpers (BotAccountReceiverGrpc).

// CountAccountsByAutomatedID counts bot accounts with the automated id.
func (s *Store) CountAccountsByAutomatedID(ctx context.Context, automatedID uuid.UUID) (int, error) {
	var n int
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE automated_id = $1 AND deleted_at IS NULL`, automatedID).Scan(&n)
	return n, err
}

// CountAccountsByNameCI counts accounts with the name (case-insensitive).
func (s *Store) CountAccountsByNameCI(ctx context.Context, name string) (int, error) {
	var n int
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE LOWER(name) = LOWER($1) AND deleted_at IS NULL`, name).Scan(&n)
	return n, err
}

// InsertAccountWithProfile inserts an account with its profile row.
func (s *Store) InsertAccountWithProfile(ctx context.Context, account *model.Account, now time.Time) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var automatedID *uuid.UUID
	if account.AutomatedId != nil {
		id, err := uuid.Parse(*account.AutomatedId)
		if err == nil {
			automatedID = &id
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO accounts
		(id, name, nick, language, region, activated_at, is_superuser, automated_id, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)`,
		account.Id, account.Name, account.Nick, account.Language, account.Region,
		account.ActivatedAt, account.IsSuperuser, automatedID, now); err != nil {
		return err
	}
	profileID := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO account_profiles
		(id, account_id, experience, social_credits, created_at, updated_at)
		VALUES ($1,$2,0,100,$3,$3)`, profileID, account.Id, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UpdateAccountWithProfile updates an account row.
func (s *Store) UpdateAccountWithProfile(ctx context.Context, account *model.Account, now time.Time) error {
	var automatedID *uuid.UUID
	if account.AutomatedId != nil {
		id, err := uuid.Parse(*account.AutomatedId)
		if err == nil {
			automatedID = &id
		}
	}
	_, err := s.DB.Exec(ctx, `UPDATE accounts SET
		name = $2, nick = $3, language = $4, region = $5, activated_at = $6, is_superuser = $7, automated_id = $8, updated_at = $9
		WHERE id = $1`,
		account.Id, account.Name, account.Nick, account.Language, account.Region,
		account.ActivatedAt, account.IsSuperuser, automatedID, now)
	return err
}

// SoftDeleteAccountAndSessions soft-deletes an account and revokes sessions.
func (s *Store) SoftDeleteAccountAndSessions(ctx context.Context, accountID string, now time.Time) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE accounts SET deleted_at = $1, updated_at = $1 WHERE id = $2`, now, accountID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE auth_sessions SET expired_at = $1, epoch = epoch + 1, updated_at = $1 WHERE account_id = $2`, now, accountID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SearchActionLogs queries action logs with optional filters.
func (s *Store) SearchActionLogs(ctx context.Context, accountID *uuid.UUID, actions []string, createdAfter, createdBefore *time.Time, orderDesc bool, offset, limit int) ([]model.ActionLog, error) {
	where := `WHERE deleted_at IS NULL`
	args := []any{}
	if accountID != nil {
		args = append(args, *accountID)
		where += ` AND account_id = $` + itoa(len(args))
	}
	if len(actions) > 0 {
		args = append(args, actions)
		where += ` AND action = ANY($` + itoa(len(args)) + `)`
	}
	if createdAfter != nil {
		args = append(args, *createdAfter)
		where += ` AND created_at >= $` + itoa(len(args))
	}
	if createdBefore != nil {
		args = append(args, *createdBefore)
		where += ` AND created_at <= $` + itoa(len(args))
	}
	order := `ORDER BY created_at DESC, id DESC`
	if !orderDesc {
		order = `ORDER BY created_at ASC, id ASC`
	}
	args = append(args, limit, offset)
	rows, err := s.DB.Query(ctx, `SELECT id, action, meta, user_agent, ip_address, location, account_id, session_id, created_at, updated_at, deleted_at
		FROM action_logs `+where+` `+order+` LIMIT $`+itoa(len(args)-1)+` OFFSET $`+itoa(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var logs []model.ActionLog
	for rows.Next() {
		log, err := scanActionLog(rows)
		if err != nil {
			return nil, err
		}
		logs = append(logs, *log)
	}
	return logs, rows.Err()
}

func scanConnectionFull(row pgx.Row) (*model.Connection, error) {
	var c model.Connection
	var meta, accessToken, refreshToken *[]byte
	err := row.Scan(&c.Id, &c.Provider, &c.ProvidedIdentifier, &meta, &accessToken, &refreshToken,
		&c.LastUsedAt, &c.IsPublic, &c.AccountId, &c.CreatedAt, &c.UpdatedAt, &c.DeletedAt)
	if err != nil {
		return nil, err
	}
	if meta != nil && len(*meta) > 0 && string(*meta) != "null" {
		_ = json.Unmarshal(*meta, &c.Meta)
	}
	if accessToken != nil {
		c.AccessToken = string(*accessToken)
	}
	if refreshToken != nil {
		c.RefreshToken = string(*refreshToken)
	}
	return &c, nil
}

func scanActionLog(row pgx.Row) (*model.ActionLog, error) {
	var l model.ActionLog
	var meta []byte
	var location []byte
	var sessionID *uuid.UUID
	err := row.Scan(&l.Id, &l.Action, &meta, &l.UserAgent, &l.IpAddress, &location, &l.AccountId, &sessionID,
		&l.CreatedAt, &l.UpdatedAt, &l.DeletedAt)
	if err != nil {
		return nil, err
	}
	if len(meta) > 0 && string(meta) != "null" {
		_ = json.Unmarshal(meta, &l.Meta)
	}
	if len(location) > 0 && string(location) != "null" {
		var gp model.GeoPoint
		if err := json.Unmarshal(location, &gp); err == nil {
			l.Location = &gp
		}
	}
	if sessionID != nil {
		v := sessionID.String()
		l.SessionId = &v
	}
	return &l, nil
}
