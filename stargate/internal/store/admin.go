package store

// Admin helpers for the Padlock admin HTTP surface (Phase 10). These mirror
// the queries in AccountAdminController.cs, AccountService.cs,
// AccountPunishmentController.cs and AccountGeographyStatsAdminController.cs
// against the snake_case schema from internal/migrate/0001_initial.sql.
//
// Names are prefixed with Admin to avoid collisions with other store files.

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

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
func (s *Store) AdminListAccounts(ctx context.Context, query, orderBy string, take, offset int) ([]model.Account, int, error) {
	where := `WHERE a.deleted_at IS NULL`
	args := []any{}
	if strings.TrimSpace(query) != "" {
		args = append(args, "%"+strings.TrimSpace(query)+"%")
		where += ` AND (a.name ILIKE $1 OR a.nick ILIKE $1)`
	}
	var order string
	switch orderBy {
	case "name":
		order = `a.name`
	case "name_desc":
		order = `a.name DESC`
	case "created_at_desc":
		order = `a.created_at DESC`
	default:
		order = `a.id`
	}

	var total int
	if err := s.queryRow(ctx, `SELECT count(*) FROM accounts a `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, take, offset)
	rows, err := s.query(ctx, `SELECT `+accountColumns+` FROM accounts a `+where+` ORDER BY `+order+` LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var accounts []model.Account
	for rows.Next() {
		account := &model.Account{}
		var automatedID *uuid.UUID
		if err := rows.Scan(&account.Id, &account.Name, &account.Nick, &account.Language, &account.Region,
			&account.ActivatedAt, &account.IsSuperuser, &automatedID, &account.CreatedAt, &account.UpdatedAt, &account.DeletedAt); err != nil {
			return nil, 0, err
		}
		account.AutomatedId = uuidPtrStr(automatedID)
		accounts = append(accounts, *account)
	}
	return accounts, total, rows.Err()
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
	row := s.queryRow(ctx, `SELECT `+accountColumns+` FROM accounts WHERE name ILIKE $1 AND deleted_at IS NULL`, probe)
	account, err := scanAccount(row)
	if err == nil {
		return account, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	// Fall back to an email/phone contact lookup (contact.Account included).
	row = s.queryRow(ctx, `SELECT a.id, a.name, a.nick, a.language, a.region, a.activated_at, a.is_superuser,
		a.automated_id, a.created_at, a.updated_at, a.deleted_at
		FROM account_contacts c
		JOIN accounts a ON a.id = c.account_id
		WHERE (c.type = 0 OR c.type = 1) AND c.content ILIKE $1
		  AND c.deleted_at IS NULL AND a.deleted_at IS NULL
		LIMIT 1`, probe)
	contactAccount, err := scanAccount(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return contactAccount, nil
}

// AdminContactSummaries returns per-account primary email + contact counts.
func (s *Store) AdminContactSummaries(ctx context.Context, accountIDs []uuid.UUID) (map[string]AdminContactSummary, error) {
	result := make(map[string]AdminContactSummary, len(accountIDs))
	if len(accountIDs) == 0 {
		return result, nil
	}
	rows, err := s.query(ctx, `SELECT account_id, is_primary, verified_at, content FROM account_contacts
		WHERE account_id = ANY($1) AND deleted_at IS NULL`, accountIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID string
		var isPrimary bool
		var verifiedAt *model.Time
		var content string
		if err := rows.Scan(&accountID, &isPrimary, &verifiedAt, &content); err != nil {
			return nil, err
		}
		summary := result[accountID]
		summary.Count++
		// Primary email: prefer is_primary, then verified_at desc (C#
		// OrderByDescending(IsPrimary).ThenByDescending(VerifiedAt)).
		if summary.PrimaryEmail == nil && isPrimary {
			email := content
			summary.PrimaryEmail = &email
		} else if summary.PrimaryEmail == nil && verifiedAt != nil {
			email := content
			summary.PrimaryEmail = &email
		}
		result[accountID] = summary
	}
	return result, rows.Err()
}

// AdminFactorSummaries returns per-account auth-factor count + has-password.
func (s *Store) AdminFactorSummaries(ctx context.Context, accountIDs []uuid.UUID) (map[string]AdminFactorSummary, error) {
	result := make(map[string]AdminFactorSummary, len(accountIDs))
	if len(accountIDs) == 0 {
		return result, nil
	}
	rows, err := s.query(ctx, `SELECT account_id, type, enabled_at FROM account_auth_factors
		WHERE account_id = ANY($1) AND deleted_at IS NULL`, accountIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID string
		var ftype int
		var enabledAt *model.Time
		if err := rows.Scan(&accountID, &ftype, &enabledAt); err != nil {
			return nil, err
		}
		summary := result[accountID]
		summary.Count++
		if ftype == int(model.AuthFactorTypePassword) && enabledAt != nil {
			summary.HasPassword = true
		}
		result[accountID] = summary
	}
	return result, rows.Err()
}

// AdminActiveSessionCounts returns per-account counts of sessions that are
// not expired yet (expired_at IS NULL or future).
func (s *Store) AdminActiveSessionCounts(ctx context.Context, accountIDs []uuid.UUID, now time.Time) (map[string]int, error) {
	result := make(map[string]int, len(accountIDs))
	if len(accountIDs) == 0 {
		return result, nil
	}
	rows, err := s.query(ctx, `SELECT account_id, count(*) FROM auth_sessions
		WHERE account_id = ANY($1) AND (expired_at IS NULL OR expired_at > $2)
		GROUP BY account_id`, accountIDs, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID string
		var count int
		if err := rows.Scan(&accountID, &count); err != nil {
			return nil, err
		}
		result[accountID] = count
	}
	return result, rows.Err()
}

// AdminActiveDeviceCounts returns per-account counts of non-deleted devices.
func (s *Store) AdminActiveDeviceCounts(ctx context.Context, accountIDs []uuid.UUID) (map[string]int, error) {
	result := make(map[string]int, len(accountIDs))
	if len(accountIDs) == 0 {
		return result, nil
	}
	rows, err := s.query(ctx, `SELECT account_id, count(*) FROM auth_clients
		WHERE account_id = ANY($1) AND deleted_at IS NULL
		GROUP BY account_id`, accountIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID string
		var count int
		if err := rows.Scan(&accountID, &count); err != nil {
			return nil, err
		}
		result[accountID] = count
	}
	return result, rows.Err()
}

// AdminListActivePunishments returns punishments whose expiry is null/future
// for the given accounts (any type), mirroring the admin list/detail queries.
func (s *Store) AdminListActivePunishments(ctx context.Context, accountIDs []uuid.UUID, now time.Time) ([]model.Punishment, error) {
	if len(accountIDs) == 0 {
		return nil, nil
	}
	return s.adminQueryPunishments(ctx, `WHERE p.account_id = ANY($1) AND p.deleted_at IS NULL
		AND (p.expired_at IS NULL OR p.expired_at > $2)`, accountIDs, now)
}

// AdminPunishmentGet loads one punishment by id and account.
func (s *Store) AdminPunishmentGet(ctx context.Context, accountID, punishmentID uuid.UUID) (*model.Punishment, error) {
	punishments, err := s.adminQueryPunishments(ctx, `WHERE p.id = $1 AND p.account_id = $2 AND p.deleted_at IS NULL`, punishmentID, accountID)
	if err != nil {
		return nil, err
	}
	if len(punishments) == 0 {
		return nil, ErrNotFound
	}
	return &punishments[0], nil
}

// AdminPunishmentsCreatedBy lists punishments created by the given admin.
func (s *Store) AdminPunishmentsCreatedBy(ctx context.Context, creatorID uuid.UUID, take, offset int) ([]model.Punishment, int, error) {
	var total int
	if err := s.queryRow(ctx, `SELECT count(*) FROM punishments WHERE creator_id = $1 AND deleted_at IS NULL`, creatorID).Scan(&total); err != nil {
		return nil, 0, err
	}
	punishments, err := s.adminQueryPunishments(ctx, `WHERE p.creator_id = $1 AND p.deleted_at IS NULL
		ORDER BY p.created_at DESC LIMIT $2 OFFSET $3`, creatorID, take, offset)
	if err != nil {
		return nil, 0, err
	}
	return punishments, total, nil
}

// AdminActivePunishmentsForAccount lists the account's currently active
// punishments (oldest-first by creation, mirroring the user-facing
// AccountPunishmentController).
func (s *Store) AdminActivePunishmentsForAccount(ctx context.Context, accountID uuid.UUID, now time.Time, take, offset int) ([]model.Punishment, int, error) {
	var total int
	if err := s.queryRow(ctx, `SELECT count(*) FROM punishments
		WHERE account_id = $1 AND deleted_at IS NULL AND (expired_at IS NULL OR expired_at > $2)`, accountID, now).Scan(&total); err != nil {
		return nil, 0, err
	}
	punishments, err := s.adminQueryPunishments(ctx, `WHERE p.account_id = $1 AND p.deleted_at IS NULL
		AND (p.expired_at IS NULL OR p.expired_at > $2)
		ORDER BY p.created_at DESC LIMIT $3 OFFSET $4`, accountID, now, take, offset)
	if err != nil {
		return nil, 0, err
	}
	return punishments, total, nil
}

// AdminAllPunishmentsForAccount lists every punishment of an account
// (me/punishments), most recent first.
func (s *Store) AdminAllPunishmentsForAccount(ctx context.Context, accountID uuid.UUID, take, offset int) ([]model.Punishment, int, error) {
	var total int
	if err := s.queryRow(ctx, `SELECT count(*) FROM punishments WHERE account_id = $1 AND deleted_at IS NULL`, accountID).Scan(&total); err != nil {
		return nil, 0, err
	}
	punishments, err := s.adminQueryPunishments(ctx, `WHERE p.account_id = $1 AND p.deleted_at IS NULL
		ORDER BY p.created_at DESC LIMIT $2 OFFSET $3`, accountID, take, offset)
	if err != nil {
		return nil, 0, err
	}
	return punishments, total, nil
}

// AdminPunishmentOverview returns the most severe active punishment of an
// account, mirroring GetActivePunishmentOverview (null when none).
func (s *Store) AdminPunishmentOverview(ctx context.Context, accountID uuid.UUID, now time.Time) (*model.Punishment, error) {
	punishments, err := s.adminQueryPunishments(ctx, `WHERE p.account_id = $1 AND p.deleted_at IS NULL
		AND (p.expired_at IS NULL OR p.expired_at > $2) ORDER BY p.type DESC`, accountID, now)
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

// adminQueryPunishments runs a punishment SELECT. The WHERE clause must start
// with "WHERE p." and any extra params must follow the first argument. The
// first argument is always the account/creator scope value.
func (s *Store) adminQueryPunishments(ctx context.Context, where string, args ...any) ([]model.Punishment, error) {
	query := `SELECT p.id, p.reason, p.expired_at, p.type, p.blocked_permissions, p.account_id,
		p.creator_id, p.created_at, p.updated_at, p.deleted_at
		FROM punishments p ` + where
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var punishments []model.Punishment
	for rows.Next() {
		var p model.Punishment
		var blocked []string
		var creatorID *uuid.UUID
		if err := rows.Scan(&p.Id, &p.Reason, &p.ExpiredAt, &p.Type, &blocked, &p.AccountId,
			&creatorID, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt); err != nil {
			return nil, err
		}
		p.BlockedPermissions = blocked
		p.CreatorId = uuidPtrStr(creatorID)
		punishments = append(punishments, p)
	}
	return punishments, rows.Err()
}

// AdminPunishmentCreate inserts a punishment and returns it.
func (s *Store) AdminPunishmentCreate(ctx context.Context, accountID, creatorID uuid.UUID, reason string, expiredAt *time.Time, ptype int, blocked []string) (*model.Punishment, error) {
	if blocked == nil {
		blocked = []string{}
	}
	row := s.queryRow(ctx, `INSERT INTO punishments (id, account_id, creator_id, reason, expired_at, type, blocked_permissions, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, now(), now())
		RETURNING id, reason, expired_at, type, blocked_permissions, account_id, creator_id, created_at, updated_at, deleted_at`,
		accountID, creatorID, reason, expiredAt, ptype, blocked)
	p, err := scanPunishment(row)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// AdminPunishmentUpdate applies the provided field updates to a punishment.
// A nil field leaves the column untouched; blocked is only applied when
// provided (the C# only updates when request.BlockedPermissions is not null).
func (s *Store) AdminPunishmentUpdate(ctx context.Context, punishmentID uuid.UUID, reason *string, expiredAt *time.Time, ptype *int, blocked []string, hasBlocked bool, creatorID *uuid.UUID) (*model.Punishment, error) {
	fields := []string{"updated_at = now()"}
	args := []any{}
	if reason != nil {
		args = append(args, *reason)
		fields = append(fields, "reason = $"+strconv.Itoa(len(args)))
	}
	if expiredAt != nil {
		args = append(args, *expiredAt)
		fields = append(fields, "expired_at = $"+strconv.Itoa(len(args)))
	}
	if ptype != nil {
		args = append(args, *ptype)
		fields = append(fields, "type = $"+strconv.Itoa(len(args)))
	}
	if hasBlocked {
		args = append(args, blocked)
		fields = append(fields, "blocked_permissions = $"+strconv.Itoa(len(args)))
	}
	if creatorID != nil {
		args = append(args, *creatorID)
		fields = append(fields, "creator_id = $"+strconv.Itoa(len(args)))
	}
	args = append(args, punishmentID)
	row := s.queryRow(ctx, `UPDATE punishments SET `+strings.Join(fields, ", ")+`
		WHERE id = $`+strconv.Itoa(len(args))+` AND deleted_at IS NULL
		RETURNING id, reason, expired_at, type, blocked_permissions, account_id, creator_id, created_at, updated_at, deleted_at`, args...)
	p, err := scanPunishment(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

// AdminPunishmentDelete soft-deletes a punishment (EF Remove semantics).
func (s *Store) AdminPunishmentDelete(ctx context.Context, accountID, punishmentID uuid.UUID) (*model.Punishment, error) {
	row := s.queryRow(ctx, `UPDATE punishments SET deleted_at = now(), updated_at = now()
		WHERE id = $1 AND account_id = $2 AND deleted_at IS NULL
		RETURNING id, reason, expired_at, type, blocked_permissions, account_id, creator_id, created_at, updated_at, deleted_at`, punishmentID, accountID)
	p, err := scanPunishment(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

func scanPunishment(row rowScanner) (*model.Punishment, error) {
	var p model.Punishment
	var blocked []string
	var creatorID *uuid.UUID
	err := row.Scan(&p.Id, &p.Reason, &p.ExpiredAt, &p.Type, &blocked, &p.AccountId,
		&creatorID, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.BlockedPermissions = blocked
	p.CreatorId = uuidPtrStr(creatorID)
	return &p, nil
}

// AdminListDevices pages the account's auth clients, optionally including
// soft-deleted ones, mirroring ListAccountDevices.
func (s *Store) AdminListDevices(ctx context.Context, accountID uuid.UUID, includeDeleted bool, take, offset int) ([]model.AuthClient, int, error) {
	where := `WHERE account_id = $1`
	if !includeDeleted {
		where += ` AND deleted_at IS NULL`
	}
	var total int
	if err := s.queryRow(ctx, `SELECT count(*) FROM auth_clients `+where, accountID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.query(ctx, `SELECT id, device_id, device_name, device_label, account_id, platform, created_at, updated_at, deleted_at
		FROM auth_clients `+where+` ORDER BY created_at DESC LIMIT $2 OFFSET $3`, accountID, take, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var devices []model.AuthClient
	for rows.Next() {
		var d model.AuthClient
		if err := rows.Scan(&d.Id, &d.DeviceId, &d.DeviceName, &d.DeviceLabel, &d.AccountId, &d.Platform,
			&d.CreatedAt, &d.UpdatedAt, &d.DeletedAt); err != nil {
			return nil, 0, err
		}
		devices = append(devices, d)
	}
	return devices, total, rows.Err()
}

// AdminListDeviceSessions groups the devices' sessions by client id, newest
// last_granted_at first (used to populate SnAuthClientWithSessions).
func (s *Store) AdminListDeviceSessions(ctx context.Context, clientIDs []uuid.UUID) (map[string][]model.AuthSession, error) {
	result := make(map[string][]model.AuthSession)
	if len(clientIDs) == 0 {
		return result, nil
	}
	rows, err := s.query(ctx, `SELECT s.id, s.type, s.last_granted_at, s.expired_at, s.audiences, s.scopes,
		s.ip_address, s.user_agent, s.location, s.account_id, s.client_id, s.parent_session_id, s.challenge_id,
		s.app_id, s.epoch, s.created_at, s.updated_at, s.deleted_at
		FROM auth_sessions s WHERE s.client_id = ANY($1)
		ORDER BY s.last_granted_at DESC`, clientIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		session, err := scanAdminSession(rows)
		if err != nil {
			return nil, err
		}
		if session.ClientId != nil {
			result[*session.ClientId] = append(result[*session.ClientId], *session)
		}
	}
	return result, rows.Err()
}

// AdminGetDeviceByDeviceId loads an auth client by its stable device id.
func (s *Store) AdminGetDeviceByDeviceId(ctx context.Context, accountID uuid.UUID, deviceID string) (*model.AuthClient, error) {
	row := s.queryRow(ctx, `SELECT id, device_id, device_name, device_label, account_id, platform, created_at, updated_at, deleted_at
		FROM auth_clients WHERE account_id = $1 AND device_id = $2 AND deleted_at IS NULL`, accountID, deviceID)
	var d model.AuthClient
	err := row.Scan(&d.Id, &d.DeviceId, &d.DeviceName, &d.DeviceLabel, &d.AccountId, &d.Platform,
		&d.CreatedAt, &d.UpdatedAt, &d.DeletedAt)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &d, nil
}

// AdminUpdateDeviceLabel renames the device (device_name column, mirroring
// UpdateDeviceName).
func (s *Store) AdminUpdateDeviceLabel(ctx context.Context, accountID uuid.UUID, deviceID, label string) error {
	tag, err := s.exec(ctx, `UPDATE auth_clients SET device_name = $1, updated_at = now()
		WHERE account_id = $2 AND device_id = $3 AND deleted_at IS NULL`, label, accountID, deviceID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AdminDeleteDevice expires all sessions of the client and soft-deletes the
// auth client, mirroring AccountService.DeleteDevice.
func (s *Store) AdminDeleteDevice(ctx context.Context, accountID uuid.UUID, deviceID string, now time.Time) (*model.AuthClient, error) {
	device, err := s.AdminGetDeviceByDeviceId(ctx, accountID, deviceID)
	if err != nil {
		return nil, err
	}
	if _, err := s.exec(ctx, `UPDATE auth_sessions SET expired_at = $1, updated_at = $1
		WHERE client_id = $2`, now, device.Id); err != nil {
		return nil, err
	}
	if _, err := s.exec(ctx, `UPDATE auth_clients SET deleted_at = $1, updated_at = $1 WHERE id = $2`, now, device.Id); err != nil {
		return nil, err
	}
	return device, nil
}

// AdminListSessions pages an account's sessions with the admin filters,
// mirroring ListAccountSessions (children excluded unless includeChildren).
func (s *Store) AdminListSessions(ctx context.Context, accountID uuid.UUID, typ *int, clientID *uuid.UUID, includeChildren, activeOnly bool, take, offset int) ([]model.AuthSession, int, error) {
	where := `WHERE s.account_id = $1`
	args := []any{accountID}
	if !includeChildren {
		where += ` AND s.parent_session_id IS NULL`
	}
	if typ != nil {
		args = append(args, *typ)
		where += ` AND s.type = $` + strconv.Itoa(len(args))
	}
	if clientID != nil {
		args = append(args, *clientID)
		where += ` AND s.client_id = $` + strconv.Itoa(len(args))
	}
	if activeOnly {
		args = append(args, time.Now().UTC())
		where += ` AND (s.expired_at IS NULL OR s.expired_at > $` + strconv.Itoa(len(args)) + `)`
	}
	var total int
	if err := s.queryRow(ctx, `SELECT count(*) FROM auth_sessions s `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, take, offset)
	rows, err := s.query(ctx, `SELECT s.id, s.type, s.last_granted_at, s.expired_at, s.audiences, s.scopes,
		s.ip_address, s.user_agent, s.location, s.account_id, s.client_id, s.parent_session_id, s.challenge_id,
		s.app_id, s.epoch, s.created_at, s.updated_at, s.deleted_at
		FROM auth_sessions s `+where+` ORDER BY s.last_granted_at DESC LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var sessions []model.AuthSession
	for rows.Next() {
		session, err := scanAdminSession(rows)
		if err != nil {
			return nil, 0, err
		}
		sessions = append(sessions, *session)
	}
	return sessions, total, rows.Err()
}

// AdminListSessionChildren pages the direct children of one session,
// mirroring ListAccountSessionChildren.
func (s *Store) AdminListSessionChildren(ctx context.Context, accountID, parentID uuid.UUID, take, offset int) ([]model.AuthSession, int, error) {
	var total int
	if err := s.queryRow(ctx, `SELECT count(*) FROM auth_sessions
		WHERE parent_session_id = $1 AND account_id = $2`, parentID, accountID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.query(ctx, `SELECT s.id, s.type, s.last_granted_at, s.expired_at, s.audiences, s.scopes,
		s.ip_address, s.user_agent, s.location, s.account_id, s.client_id, s.parent_session_id, s.challenge_id,
		s.app_id, s.epoch, s.created_at, s.updated_at, s.deleted_at
		FROM auth_sessions s WHERE s.parent_session_id = $1 AND s.account_id = $2
		ORDER BY s.last_granted_at DESC LIMIT $3 OFFSET $4`, parentID, accountID, take, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var sessions []model.AuthSession
	for rows.Next() {
		session, err := scanAdminSession(rows)
		if err != nil {
			return nil, 0, err
		}
		sessions = append(sessions, *session)
	}
	return sessions, total, rows.Err()
}

// AdminGetSession loads one session belonging to the account.
func (s *Store) AdminGetSession(ctx context.Context, accountID, sessionID uuid.UUID) (*model.AuthSession, error) {
	row := s.queryRow(ctx, `SELECT s.id, s.type, s.last_granted_at, s.expired_at, s.audiences, s.scopes,
		s.ip_address, s.user_agent, s.location, s.account_id, s.client_id, s.parent_session_id, s.challenge_id,
		s.app_id, s.epoch, s.created_at, s.updated_at, s.deleted_at
		FROM auth_sessions s WHERE s.id = $1 AND s.account_id = $2`, sessionID, accountID)
	return scanAdminSession(row)
}

// AdminRevokeSession expires a single session and bumps its epoch (mirroring
// AccountService.DeleteSession).
func (s *Store) AdminRevokeSession(ctx context.Context, accountID, sessionID uuid.UUID, now time.Time) (*model.AuthSession, error) {
	row := s.queryRow(ctx, `UPDATE auth_sessions SET expired_at = $1, epoch = epoch + 1, updated_at = $1
		WHERE id = $2 AND account_id = $3 AND deleted_at IS NULL
		RETURNING id, type, last_granted_at, expired_at, audiences, scopes, ip_address, user_agent, location,
		account_id, client_id, parent_session_id, challenge_id, app_id, epoch, created_at, updated_at, deleted_at`, now, sessionID, accountID)
	return scanAdminSession(row)
}

// AdminRevokeAllSessions expires every live session of the account (mirroring
// AccountService.DeleteAllSessions) and returns the revoked sessions.
func (s *Store) AdminRevokeAllSessions(ctx context.Context, accountID uuid.UUID, now time.Time) ([]model.AuthSession, error) {
	rows, err := s.query(ctx, `UPDATE auth_sessions SET expired_at = $1, epoch = epoch + 1, updated_at = $1
		WHERE account_id = $2 AND expired_at IS NULL AND deleted_at IS NULL
		RETURNING id, type, last_granted_at, expired_at, audiences, scopes, ip_address, user_agent, location,
		account_id, client_id, parent_session_id, challenge_id, app_id, epoch, created_at, updated_at, deleted_at`, now, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []model.AuthSession
	for rows.Next() {
		session, err := scanAdminSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, *session)
	}
	return sessions, rows.Err()
}

// AdminCountSessionChildren returns the child count of each session id.
func (s *Store) AdminCountSessionChildren(ctx context.Context, sessionIDs []uuid.UUID) (map[string]int, error) {
	result := make(map[string]int, len(sessionIDs))
	if len(sessionIDs) == 0 {
		return result, nil
	}
	rows, err := s.query(ctx, `SELECT parent_session_id, count(*) FROM auth_sessions
		WHERE parent_session_id = ANY($1) GROUP BY parent_session_id`, sessionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var parentID string
		var count int
		if err := rows.Scan(&parentID, &count); err != nil {
			return nil, err
		}
		result[parentID] = count
	}
	return result, rows.Err()
}

func scanAdminSession(row rowScanner) (*model.AuthSession, error) {
	session := &model.AuthSession{}
	var (
		audiences, scopes                             []string
		location                                      []byte
		clientID, parentSessionID, challengeID, appID *uuid.UUID
		epoch                                         int
	)
	err := row.Scan(
		&session.Id, &session.Type, &session.LastGrantedAt, &session.ExpiredAt, &audiences, &scopes,
		&session.IpAddress, &session.UserAgent, &location, &session.AccountId,
		&clientID, &parentSessionID, &challengeID, &appID, &epoch,
		&session.CreatedAt, &session.UpdatedAt, &session.DeletedAt,
	)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	session.Audiences = audiences
	session.Scopes = scopes
	if len(location) > 0 && string(location) != "null" {
		var gp model.GeoPoint
		if err := json.Unmarshal(location, &gp); err == nil {
			session.Location = &gp
		}
	}
	session.ClientId = uuidPtrStr(clientID)
	session.ParentSessionId = uuidPtrStr(parentSessionID)
	session.ChallengeId = uuidPtrStr(challengeID)
	session.AppId = uuidPtrStr(appID)
	session.Epoch = epoch
	return session, nil
}

// AdminListContacts lists an account's contacts, mirroring the admin ordering
// (primary first, then type, then content).
func (s *Store) AdminListContacts(ctx context.Context, accountID uuid.UUID) ([]model.Contact, error) {
	rows, err := s.query(ctx, `SELECT id, type, verified_at, is_primary, is_public, content, account_id, created_at, updated_at, deleted_at
		FROM account_contacts WHERE account_id = $1 AND deleted_at IS NULL
		ORDER BY is_primary DESC, type, content`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var contacts []model.Contact
	for rows.Next() {
		var c model.Contact
		if err := rows.Scan(&c.Id, &c.Type, &c.VerifiedAt, &c.IsPrimary, &c.IsPublic, &c.Content, &c.AccountId,
			&c.CreatedAt, &c.UpdatedAt, &c.DeletedAt); err != nil {
			return nil, err
		}
		contacts = append(contacts, c)
	}
	return contacts, rows.Err()
}

// AdminGetContact loads one contact of the account.
func (s *Store) AdminGetContact(ctx context.Context, accountID, contactID uuid.UUID) (*model.Contact, error) {
	row := s.queryRow(ctx, `SELECT id, type, verified_at, is_primary, is_public, content, account_id, created_at, updated_at, deleted_at
		FROM account_contacts WHERE id = $1 AND account_id = $2 AND deleted_at IS NULL`, contactID, accountID)
	var c model.Contact
	err := row.Scan(&c.Id, &c.Type, &c.VerifiedAt, &c.IsPrimary, &c.IsPublic, &c.Content, &c.AccountId,
		&c.CreatedAt, &c.UpdatedAt, &c.DeletedAt)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

// AdminCreateContact inserts a non-primary contact (CreateContactMethod).
func (s *Store) AdminCreateContact(ctx context.Context, accountID uuid.UUID, ctype int, content string) (*model.Contact, error) {
	row := s.queryRow(ctx, `INSERT INTO account_contacts (id, account_id, type, content, is_primary, is_public, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, false, false, now(), now())
		RETURNING id, type, verified_at, is_primary, is_public, content, account_id, created_at, updated_at, deleted_at`,
		accountID, ctype, content)
	return scanContact(row)
}

// AdminUpdateContact applies type/content updates, clearing verified_at when
// either changed (UpdateAccountContact semantics).
func (s *Store) AdminUpdateContact(ctx context.Context, accountID, contactID uuid.UUID, ctype *int, content *string) (*model.Contact, error) {
	var current model.Contact
	row := s.queryRow(ctx, `SELECT id, type, verified_at, is_primary, is_public, content, account_id, created_at, updated_at, deleted_at
		FROM account_contacts WHERE id = $1 AND account_id = $2 AND deleted_at IS NULL`, contactID, accountID)
	cur, err := scanContact(row)
	if err != nil {
		return nil, err
	}
	current = *cur

	typeChanged := ctype != nil && current.Type != *ctype
	contentChanged := content != nil && current.Content != *content
	if ctype != nil {
		current.Type = *ctype
	}
	if content != nil {
		current.Content = *content
	}
	if typeChanged || contentChanged {
		current.VerifiedAt = nil
	}
	updated := s.queryRow(ctx, `UPDATE account_contacts SET type = $1, content = $2, verified_at = $3, updated_at = now()
		WHERE id = $4 AND account_id = $5 AND deleted_at IS NULL
		RETURNING id, type, verified_at, is_primary, is_public, content, account_id, created_at, updated_at, deleted_at`,
		current.Type, current.Content, current.VerifiedAt, contactID, accountID)
	return scanContact(updated)
}

// AdminSetContactVerified marks a contact verified at the given instant,
// keeping the latest verification when one exists (MarkContactMethodVerified).
func (s *Store) AdminSetContactVerified(ctx context.Context, accountID, contactID uuid.UUID, verifiedAt time.Time) (*model.Contact, error) {
	row := s.queryRow(ctx, `UPDATE account_contacts SET verified_at = CASE
			WHEN verified_at IS NULL OR verified_at < $1 THEN $1 ELSE verified_at END,
			updated_at = now()
		WHERE id = $2 AND account_id = $3 AND deleted_at IS NULL
		RETURNING id, type, verified_at, is_primary, is_public, content, account_id, created_at, updated_at, deleted_at`,
		verifiedAt, contactID, accountID)
	c, err := scanContact(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return c, nil
}

// AdminClearContactVerified nulls the verified_at timestamp.
func (s *Store) AdminClearContactVerified(ctx context.Context, accountID, contactID uuid.UUID) (*model.Contact, error) {
	row := s.queryRow(ctx, `UPDATE account_contacts SET verified_at = NULL, updated_at = now()
		WHERE id = $1 AND account_id = $2 AND deleted_at IS NULL
		RETURNING id, type, verified_at, is_primary, is_public, content, account_id, created_at, updated_at, deleted_at`,
		contactID, accountID)
	c, err := scanContact(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return c, nil
}

// AdminSetContactPrimary clears is_primary for the account's contacts of the
// same type and marks the target primary (SetContactMethodPrimary).
func (s *Store) AdminSetContactPrimary(ctx context.Context, accountID, contactID uuid.UUID) (*model.Contact, error) {
	contact, err := s.AdminGetContact(ctx, accountID, contactID)
	if err != nil {
		return nil, err
	}
	if _, err := s.exec(ctx, `UPDATE account_contacts SET is_primary = false, updated_at = now()
		WHERE account_id = $1 AND type = $2 AND deleted_at IS NULL`, accountID, contact.Type); err != nil {
		return nil, err
	}
	row := s.queryRow(ctx, `UPDATE account_contacts SET is_primary = true, updated_at = now()
		WHERE id = $1 AND account_id = $2 AND deleted_at IS NULL
		RETURNING id, type, verified_at, is_primary, is_public, content, account_id, created_at, updated_at, deleted_at`,
		contactID, accountID)
	return scanContact(row)
}

// AdminSetContactPublic flips the is_public flag.
func (s *Store) AdminSetContactPublic(ctx context.Context, accountID, contactID uuid.UUID, isPublic bool) (*model.Contact, error) {
	row := s.queryRow(ctx, `UPDATE account_contacts SET is_public = $1, updated_at = now()
		WHERE id = $2 AND account_id = $3 AND deleted_at IS NULL
		RETURNING id, type, verified_at, is_primary, is_public, content, account_id, created_at, updated_at, deleted_at`,
		isPublic, contactID, accountID)
	c, err := scanContact(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return c, nil
}

// AdminDeleteContact soft-deletes a contact (EF Remove semantics).
func (s *Store) AdminDeleteContact(ctx context.Context, accountID, contactID uuid.UUID) error {
	tag, err := s.exec(ctx, `UPDATE account_contacts SET deleted_at = now(), updated_at = now()
		WHERE id = $1 AND account_id = $2 AND deleted_at IS NULL`, contactID, accountID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanContact(row rowScanner) (*model.Contact, error) {
	var c model.Contact
	err := row.Scan(&c.Id, &c.Type, &c.VerifiedAt, &c.IsPrimary, &c.IsPublic, &c.Content, &c.AccountId,
		&c.CreatedAt, &c.UpdatedAt, &c.DeletedAt)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

// AdminListAuthFactors lists all factors of an account (admin view), ordered
// by type then enabled_at desc, mirroring ListAccountAuthFactors.
func (s *Store) AdminListAuthFactors(ctx context.Context, accountID uuid.UUID) ([]model.AuthFactor, error) {
	rows, err := s.query(ctx, `SELECT id, type, secret, config, trustworthy, enabled_at, expired_at, account_id, created_at, updated_at, deleted_at
		FROM account_auth_factors WHERE account_id = $1 AND deleted_at IS NULL
		ORDER BY type, enabled_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var factors []model.AuthFactor
	for rows.Next() {
		f, err := scanAdminAuthFactor(rows)
		if err != nil {
			return nil, err
		}
		factors = append(factors, *f)
	}
	return factors, rows.Err()
}

// AdminGetAuthFactor loads one factor of the account.
func (s *Store) AdminGetAuthFactor(ctx context.Context, accountID, factorID uuid.UUID) (*model.AuthFactor, error) {
	row := s.queryRow(ctx, `SELECT id, type, secret, config, trustworthy, enabled_at, expired_at, account_id, created_at, updated_at, deleted_at
		FROM account_auth_factors WHERE id = $1 AND account_id = $2 AND deleted_at IS NULL`, factorID, accountID)
	return scanAdminAuthFactor(row)
}

// AdminCheckAuthFactorExists reports whether the account has a factor of the
// type (any state), mirroring CheckAuthFactorExists.
func (s *Store) AdminCheckAuthFactorExists(ctx context.Context, accountID uuid.UUID, ftype int) (bool, error) {
	var exists bool
	err := s.queryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_auth_factors
		WHERE account_id = $1 AND type = $2 AND deleted_at IS NULL)`, accountID, ftype).Scan(&exists)
	return exists, err
}

// AdminInsertAuthFactor inserts a factor row and returns it.
func (s *Store) AdminInsertAuthFactor(ctx context.Context, f *model.AuthFactor) (*model.AuthFactor, error) {
	config, _ := json.Marshal(f.Config)
	row := s.queryRow(ctx, `INSERT INTO account_auth_factors (id, account_id, type, secret, config, trustworthy, enabled_at, expired_at, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, now(), now())
		RETURNING id, type, secret, config, trustworthy, enabled_at, expired_at, account_id, created_at, updated_at, deleted_at`,
		f.AccountId, int(f.Type), f.Secret, config, f.Trustworthy, f.EnabledAt, f.ExpiredAt)
	return scanAdminAuthFactor(row)
}

// AdminUpdateAuthFactor persists factor mutations (secret, config,
// trustworthy, enabled_at, expired_at, created_response fields are computed
// by the handler and stored via the columns below).
func (s *Store) AdminUpdateAuthFactor(ctx context.Context, f *model.AuthFactor) error {
	config, _ := json.Marshal(f.Config)
	tag, err := s.exec(ctx, `UPDATE account_auth_factors
		SET secret = $1, config = $2, trustworthy = $3, enabled_at = $4, expired_at = $5, updated_at = now()
		WHERE id = $6 AND account_id = $7 AND deleted_at IS NULL`,
		f.Secret, config, f.Trustworthy, f.EnabledAt, f.ExpiredAt, f.Id, f.AccountId)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AdminDeleteAuthFactor soft-deletes a factor (EF Remove semantics).
func (s *Store) AdminDeleteAuthFactor(ctx context.Context, accountID, factorID uuid.UUID) error {
	tag, err := s.exec(ctx, `UPDATE account_auth_factors SET deleted_at = now(), updated_at = now()
		WHERE id = $1 AND account_id = $2 AND deleted_at IS NULL`, factorID, accountID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AdminUpsertPasswordFactor creates or resets the account's Password factor,
// mirroring ResetPasswordFactor (bcrypt hash supplied by the caller).
func (s *Store) AdminUpsertPasswordFactor(ctx context.Context, accountID uuid.UUID, hash string, now time.Time) (*model.AuthFactor, error) {
	var existingID uuid.UUID
	err := s.queryRow(ctx, `SELECT id FROM account_auth_factors
		WHERE account_id = $1 AND type = 0 AND deleted_at IS NULL LIMIT 1`, accountID).Scan(&existingID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if errors.Is(err, ErrNotFound) {
		// No password factor yet: insert enabled.
		row := s.queryRow(ctx, `INSERT INTO account_auth_factors (id, account_id, type, secret, trustworthy, enabled_at, created_at, updated_at)
			VALUES (gen_random_uuid(), $1, 0, $2, 1, $3, now(), now())
			RETURNING id, type, secret, config, trustworthy, enabled_at, expired_at, account_id, created_at, updated_at, deleted_at`,
			accountID, hash, now)
		return scanAdminAuthFactor(row)
	}
	// Existing factor: reset secret + enable.
	row := s.queryRow(ctx, `UPDATE account_auth_factors
		SET secret = $1, enabled_at = COALESCE(enabled_at, $2), expired_at = NULL, updated_at = now()
		WHERE id = $3 AND account_id = $4 AND deleted_at IS NULL
		RETURNING id, type, secret, config, trustworthy, enabled_at, expired_at, account_id, created_at, updated_at, deleted_at`,
		hash, now, existingID, accountID)
	return scanAdminAuthFactor(row)
}

func scanAdminAuthFactor(row rowScanner) (*model.AuthFactor, error) {
	var f model.AuthFactor
	var secret *string
	var config []byte
	err := row.Scan(&f.Id, &f.Type, &secret, &config, &f.Trustworthy, &f.EnabledAt, &f.ExpiredAt,
		&f.AccountId, &f.CreatedAt, &f.UpdatedAt, &f.DeletedAt)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if secret != nil {
		f.Secret = *secret
	}
	if len(config) > 0 {
		_ = json.Unmarshal(config, &f.Config)
	}
	return &f, nil
}

// AdminResolveTargetAccountIDs resolves notification/email dispatch targets:
// all non-deleted accounts for broadcast, otherwise the requested set
// intersected with non-deleted accounts.
func (s *Store) AdminResolveTargetAccountIDs(ctx context.Context, requested []uuid.UUID, broadcast bool) ([]uuid.UUID, error) {
	query := `SELECT id FROM accounts WHERE deleted_at IS NULL`
	args := []any{}
	if !broadcast {
		if len(requested) == 0 {
			return nil, nil
		}
		args = append(args, requested)
		query += ` AND id = ANY($1)`
	}
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := make(map[uuid.UUID]struct{})
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// AdminListEmailContacts returns the dispatchable email contact of each
// target account (primary email preferred, else first verified; or the first
// email contact for the CSV export which also includes unverified ones).
func (s *Store) AdminListEmailContacts(ctx context.Context, accountIDs []uuid.UUID, verifiedOnly bool) ([]AdminEmailRecipient, error) {
	if len(accountIDs) == 0 {
		return nil, nil
	}
	query := `SELECT c.account_id, c.content, c.is_primary, c.verified_at, c.created_at,
		a.name, a.nick
		FROM account_contacts c
		JOIN accounts a ON a.id = c.account_id
		WHERE c.account_id = ANY($1) AND c.type = 0 AND c.deleted_at IS NULL AND a.deleted_at IS NULL`
	if verifiedOnly {
		query += ` AND c.verified_at IS NOT NULL`
	}
	rows, err := s.query(ctx, query, accountIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

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
	for rows.Next() {
		var accountID, content, name, nick string
		var isPrimary bool
		var verifiedAt *model.Time
		var createdAt time.Time
		if err := rows.Scan(&accountID, &content, &isPrimary, &verifiedAt, &createdAt, &name, &nick); err != nil {
			return nil, err
		}
		userName := name
		if strings.TrimSpace(nick) != "" {
			userName = nick
		}
		cand := candidate{
			recipient:  AdminEmailRecipient{AccountID: accountID, Content: content, UserName: userName},
			isPrimary:  isPrimary,
			verifiedAt: verifiedAt,
		}
		// Verified-at ordering applies within the same rank tier.
		current, ok := best[accountID]
		if !ok || rank(cand) < rank(current) ||
			(rank(cand) == rank(current) && rank(cand) == 1 && cand.verifiedAt != nil &&
				current.verifiedAt != nil && cand.verifiedAt.Time().After(current.verifiedAt.Time())) {
			best[accountID] = cand
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
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
	rows, err := s.query(ctx, `SELECT DISTINCT ON (session.account_id) session.account_id, session.location,
		session.last_granted_at
		FROM auth_sessions session
		WHERE session.last_granted_at IS NOT NULL AND session.last_granted_at >= $1
		  AND session.location IS NOT NULL AND session.location::text <> 'null'
		  AND session.deleted_at IS NULL
		ORDER BY session.account_id, session.last_granted_at DESC, session.created_at DESC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var locations []AdminAccountLocation
	for rows.Next() {
		var accountID string
		var locationBytes []byte
		var lastGrantedAt time.Time
		if err := rows.Scan(&accountID, &locationBytes, &lastGrantedAt); err != nil {
			return nil, err
		}
		var gp model.GeoPoint
		if len(locationBytes) > 0 && string(locationBytes) != "null" {
			if err := json.Unmarshal(locationBytes, &gp); err != nil {
				continue
			}
		} else {
			continue
		}
		locations = append(locations, AdminAccountLocation{AccountID: accountID, Location: gp, LastGrantedAt: lastGrantedAt})
	}
	return locations, rows.Err()
}

// AdminLoadProfiles batch-loads account profiles keyed by account id.
func (s *Store) AdminLoadProfiles(ctx context.Context, accountIDs []uuid.UUID) (map[string]*model.Profile, error) {
	result := make(map[string]*model.Profile, len(accountIDs))
	if len(accountIDs) == 0 {
		return result, nil
	}
	rows, err := s.query(ctx, `SELECT `+profileColumns+` FROM account_profiles p
		WHERE p.account_id = ANY($1) AND p.deleted_at IS NULL`, accountIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		profile := &model.Profile{}
		var (
			links, usernameColor, verification, activeBadge, picture, background       []byte
			profileID, profileAccountID                                                *string
			firstName, middleName, lastName, bio, gender, pronouns, timeZone, location *string
			birthday, lastSeenAt                                                       *model.Time
			experience                                                                 int
			socialCredits                                                              float64
			profileCreated, profileUpdated, profileDeleted                             *model.Time
		)
		if err := rows.Scan(
			&profileID, &firstName, &middleName, &lastName, &bio, &gender, &pronouns, &timeZone, &location,
			&links, &usernameColor, &birthday, &lastSeenAt, &verification, &activeBadge, &experience, &socialCredits,
			&picture, &background, &profileAccountID, &profileCreated, &profileUpdated, &profileDeleted,
		); err != nil {
			return nil, err
		}
		if profileID == nil {
			continue
		}
		profile.Id = *profileID
		profile.FirstName = firstName
		profile.MiddleName = middleName
		profile.LastName = lastName
		profile.Bio = bio
		profile.Gender = gender
		profile.Pronouns = pronouns
		profile.TimeZone = timeZone
		profile.Location = location
		profile.Birthday = birthday
		profile.LastSeenAt = lastSeenAt
		profile.Experience = experience
		profile.SocialCredits = socialCredits
		if profileAccountID != nil {
			profile.AccountId = *profileAccountID
		}
		profile.CreatedAt = profileCreated
		profile.UpdatedAt = profileUpdated
		profile.DeletedAt = profileDeleted
		_ = json.Unmarshal(links, &profile.Links)
		_ = json.Unmarshal(usernameColor, &profile.UsernameColor)
		_ = json.Unmarshal(verification, &profile.Verification)
		_ = json.Unmarshal(picture, &profile.Picture)
		_ = json.Unmarshal(background, &profile.Background)
		result[profile.AccountId] = profile
	}
	return result, rows.Err()
}

// AdminLoadAccountsByIds batch-loads accounts keyed by id.
func (s *Store) AdminLoadAccountsByIds(ctx context.Context, ids []uuid.UUID) (map[string]*model.Account, error) {
	result := make(map[string]*model.Account, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := s.query(ctx, `SELECT `+accountColumns+` FROM accounts WHERE id = ANY($1) AND deleted_at IS NULL`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		account := &model.Account{}
		var automatedID *uuid.UUID
		if err := rows.Scan(&account.Id, &account.Name, &account.Nick, &account.Language, &account.Region,
			&account.ActivatedAt, &account.IsSuperuser, &automatedID, &account.CreatedAt, &account.UpdatedAt, &account.DeletedAt); err != nil {
			return nil, err
		}
		account.AutomatedId = uuidPtrStr(automatedID)
		result[account.Id] = account
	}
	return result, rows.Err()
}

// AdminListOwnActionLogs pages one account's action logs (GET /api/actions).
func (s *Store) AdminListOwnActionLogs(ctx context.Context, accountID uuid.UUID, action string, take, offset int) ([]model.ActionLog, int, error) {
	where := `WHERE account_id = $1 AND deleted_at IS NULL`
	args := []any{accountID}
	if strings.TrimSpace(action) != "" {
		args = append(args, action)
		where += ` AND action = $` + strconv.Itoa(len(args))
	}
	var total int
	if err := s.queryRow(ctx, `SELECT count(*) FROM action_logs `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, take, offset)
	rows, err := s.query(ctx, `SELECT id, action, meta, user_agent, ip_address, location, account_id, session_id, created_at, updated_at, deleted_at
		FROM action_logs `+where+` ORDER BY created_at DESC LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var logs []model.ActionLog
	for rows.Next() {
		log, err := scanAdminActionLog(rows)
		if err != nil {
			return nil, 0, err
		}
		logs = append(logs, *log)
	}
	return logs, total, rows.Err()
}

func scanAdminActionLog(row rowScanner) (*model.ActionLog, error) {
	var log model.ActionLog
	var meta []byte
	var location []byte
	var sessionID *uuid.UUID
	err := row.Scan(&log.Id, &log.Action, &meta, &log.UserAgent, &log.IpAddress, &location, &log.AccountId,
		&sessionID, &log.CreatedAt, &log.UpdatedAt, &log.DeletedAt)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if len(meta) > 0 && string(meta) != "null" {
		_ = json.Unmarshal(meta, &log.Meta)
	}
	if len(location) > 0 && string(location) != "null" {
		var gp model.GeoPoint
		if err := json.Unmarshal(location, &gp); err == nil {
			log.Location = &gp
		}
	}
	log.SessionId = uuidPtrStr(sessionID)
	return &log, nil
}
