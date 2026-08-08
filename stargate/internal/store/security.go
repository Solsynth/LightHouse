package store

// Security-domain store helpers for the AccountSecurityController /
// ApiKeyController / ConnectionController port (internal/httpserver/securityctl).
// Query shapes mirror the EF Core LINQ in the C# controllers exactly,
// including which soft-delete filters are (or are not) applied.

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

const sessionColumns = `id, type, last_granted_at, expired_at, audiences, scopes, ip_address, user_agent,
	location, account_id, client_id, parent_session_id, app_id, challenge_id, epoch, created_at, updated_at, deleted_at`

const clientColumns = `id, device_id, device_name, device_label, account_id, platform, created_at, updated_at, deleted_at`

// --- Sessions ---

// ListSessions lists the account's sessions with pagination. When
// includeChildren is false only root sessions (no parent) are returned,
// mirroring GetSessions: by default includeChildren=false shows roots only.
func (s *Store) ListSessions(ctx context.Context, accountID string, typ *model.SessionType, clientID *uuid.UUID, includeChildren bool, take, offset int) ([]model.AuthSession, int, error) {
	where := `account_id = $1`
	args := []any{accountID}
	if !includeChildren {
		where += ` AND parent_session_id IS NULL`
	}
	if typ != nil {
		args = append(args, int(*typ))
		where += ` AND type = $` + itoa(len(args))
	}
	if clientID != nil {
		args = append(args, *clientID)
		where += ` AND client_id = $` + itoa(len(args))
	}
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM auth_sessions WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	pageArgs := append(append([]any{}, args...), take, offset)
	rows, err := s.DB.Query(ctx, `SELECT `+sessionColumns+` FROM auth_sessions WHERE `+where+
		` ORDER BY last_granted_at DESC NULLS LAST LIMIT $`+itoa(len(args)+1)+` OFFSET $`+itoa(len(args)+2), pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	sessions, err := scanSessions(rows)
	if err != nil {
		return nil, 0, err
	}
	if err := s.fillChildrenCounts(ctx, sessions); err != nil {
		return nil, 0, err
	}
	return sessions, total, nil
}

// ListSessionChildren lists the direct children of a parent session with
// pagination (GetSessionChildren).
func (s *Store) ListSessionChildren(ctx context.Context, accountID string, parentID uuid.UUID, take, offset int) ([]model.AuthSession, int, error) {
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM auth_sessions
		WHERE parent_session_id = $1 AND account_id = $2`, parentID, accountID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.Query(ctx, `SELECT `+sessionColumns+` FROM auth_sessions
		WHERE parent_session_id = $1 AND account_id = $2
		ORDER BY created_at DESC LIMIT $3 OFFSET $4`, parentID, accountID, take, offset)
	if err != nil {
		return nil, 0, err
	}
	children, err := scanSessions(rows)
	if err != nil {
		return nil, 0, err
	}
	if err := s.fillChildrenCounts(ctx, children); err != nil {
		return nil, 0, err
	}
	return children, total, nil
}

// GetOwnedSession loads a session scoped to an account (DeleteSession /
// GetSessionChildren parent check).
func (s *Store) GetOwnedSession(ctx context.Context, accountID string, id uuid.UUID) (*model.AuthSession, error) {
	row := s.DB.QueryRow(ctx, `SELECT `+sessionColumns+` FROM auth_sessions WHERE id = $1 AND account_id = $2`, id, accountID)
	session, err := scanSession(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return session, nil
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
	rows, err := s.DB.Query(ctx, `SELECT parent_session_id, COUNT(*) FROM auth_sessions
		WHERE parent_session_id = ANY($1) GROUP BY parent_session_id`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var parentID uuid.UUID
		var count int
		if err := rows.Scan(&parentID, &count); err != nil {
			return err
		}
		counts[parentID.String()] = count
	}
	if err := rows.Err(); err != nil {
		return err
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
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM auth_clients
		WHERE account_id = $1 AND deleted_at IS NULL`, accountID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.Query(ctx, `SELECT `+clientColumns+` FROM auth_clients
		WHERE account_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC LIMIT $2 OFFSET $3`, accountID, take, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var devices []model.AuthClient
	for rows.Next() {
		device, err := scanClient(rows)
		if err != nil {
			return nil, 0, err
		}
		devices = append(devices, *device)
	}
	return devices, total, rows.Err()
}

// ListSessionsByClientIDs loads sessions grouped by client id for the given
// devices (the sessions attached to SnAuthClientWithSessions).
func (s *Store) ListSessionsByClientIDs(ctx context.Context, clientIDs []uuid.UUID) (map[string][]model.AuthSession, error) {
	if len(clientIDs) == 0 {
		return map[string][]model.AuthSession{}, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT `+sessionColumns+` FROM auth_sessions WHERE client_id = ANY($1)`, clientIDs)
	if err != nil {
		return nil, err
	}
	sessions, err := scanSessions(rows)
	if err != nil {
		return nil, err
	}
	grouped := map[string][]model.AuthSession{}
	for _, session := range sessions {
		if session.ClientId == nil {
			continue
		}
		grouped[*session.ClientId] = append(grouped[*session.ClientId], session)
	}
	return grouped, nil
}

// GetClientByDeviceID loads a device by (account_id, device_id).
func (s *Store) GetClientByDeviceID(ctx context.Context, accountID, deviceID string) (*model.AuthClient, error) {
	row := s.DB.QueryRow(ctx, `SELECT `+clientColumns+` FROM auth_clients
		WHERE account_id = $1 AND device_id = $2`, accountID, deviceID)
	device, err := scanClient(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return device, nil
}

// GetClientByID loads a device by id (UpdateCurrentDeviceLabel).
func (s *Store) GetClientByID(ctx context.Context, id uuid.UUID) (*model.AuthClient, error) {
	row := s.DB.QueryRow(ctx, `SELECT `+clientColumns+` FROM auth_clients WHERE id = $1`, id)
	device, err := scanClient(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return device, nil
}

// DeleteDevice expires every session bound to the device and soft-deletes
// the device (AccountService.DeleteDevice).
func (s *Store) DeleteDevice(ctx context.Context, accountID, deviceID string, now time.Time) error {
	var clientID uuid.UUID
	err := s.DB.QueryRow(ctx, `SELECT id FROM auth_clients WHERE account_id = $1 AND device_id = $2`,
		accountID, deviceID).Scan(&clientID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := s.DB.Exec(ctx, `UPDATE auth_sessions SET expired_at = $1, updated_at = $1 WHERE client_id = $2`, now, clientID); err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `UPDATE auth_clients SET deleted_at = $1, updated_at = $1 WHERE id = $2`, now, clientID)
	return err
}

// UpdateDeviceName renames the device's display name (UpdateDeviceName).
func (s *Store) UpdateDeviceName(ctx context.Context, accountID, deviceID, label string) error {
	tag, err := s.DB.Exec(ctx, `UPDATE auth_clients SET device_name = $1, updated_at = $2
		WHERE account_id = $3 AND device_id = $4`, label, time.Now().UTC(), accountID, deviceID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Auth factors ---

// ListAllFactors lists every auth factor of the account (GetAuthFactors; no
// soft-delete filter, matching the C# query).
func (s *Store) ListAllFactors(ctx context.Context, accountID uuid.UUID) ([]model.AuthFactor, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, type, secret, config, trustworthy, enabled_at, expired_at, account_id, created_at, updated_at, deleted_at
		FROM account_auth_factors WHERE account_id = $1 ORDER BY created_at`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var factors []model.AuthFactor
	for rows.Next() {
		factor, err := scanFactor(rows)
		if err != nil {
			return nil, err
		}
		factors = append(factors, *factor)
	}
	return factors, rows.Err()
}

// GetAuthFactorByID loads one of the account's factors.
func (s *Store) GetAuthFactorByID(ctx context.Context, accountID string, id uuid.UUID) (*model.AuthFactor, error) {
	row := s.DB.QueryRow(ctx, `SELECT id, type, secret, config, trustworthy, enabled_at, expired_at, account_id, created_at, updated_at, deleted_at
		FROM account_auth_factors WHERE account_id = $1 AND id = $2`, accountID, id)
	factor, err := scanFactor(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return factor, nil
}

// CheckAuthFactorExists reports whether the account already has a factor of
// the given type (CheckAuthFactorExists).
func (s *Store) CheckAuthFactorExists(ctx context.Context, accountID string, ftype model.AuthFactorType) (bool, error) {
	var exists bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM account_auth_factors WHERE account_id = $1 AND type = $2)`,
		accountID, int(ftype)).Scan(&exists)
	return exists, err
}

// InsertAuthFactor persists a new factor row.
func (s *Store) InsertAuthFactor(ctx context.Context, f *model.AuthFactor) (*model.AuthFactor, error) {
	now := time.Now().UTC()
	var config []byte
	if len(f.Config) > 0 {
		config, _ = json.Marshal(f.Config)
	}
	var secret *string
	if f.Secret != "" {
		secret = &f.Secret
	}
	var id uuid.UUID
	err := s.DB.QueryRow(ctx, `INSERT INTO account_auth_factors
		(type, secret, config, trustworthy, enabled_at, expired_at, account_id, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8) RETURNING id`,
		int(f.Type), secret, config, f.Trustworthy, f.EnabledAt, f.ExpiredAt, f.AccountId, now).Scan(&id)
	if err != nil {
		return nil, err
	}
	f.Id = id.String()
	f.CreatedAt = model.NewTime(now)
	f.UpdatedAt = model.NewTime(now)
	return f, nil
}

// UpdateAuthFactor persists the mutable factor columns.
func (s *Store) UpdateAuthFactor(ctx context.Context, f *model.AuthFactor) error {
	var config []byte
	if len(f.Config) > 0 {
		config, _ = json.Marshal(f.Config)
	}
	var secret *string
	if f.Secret != "" {
		secret = &f.Secret
	}
	_, err := s.DB.Exec(ctx, `UPDATE account_auth_factors SET
		secret = $2, config = $3, trustworthy = $4, enabled_at = $5, expired_at = $6, updated_at = $7
		WHERE id = $1`, f.Id, secret, config, f.Trustworthy, f.EnabledAt, f.ExpiredAt, time.Now().UTC())
	return err
}

// DeleteAuthFactorRow hard-deletes a factor row (DeleteAuthFactor).
func (s *Store) DeleteAuthFactorRow(ctx context.Context, id uuid.UUID) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM account_auth_factors WHERE id = $1`, id)
	return err
}

// DeletePasskeysByAccount deletes every passkey of the account (used when the
// Passkey factor itself is deleted).
func (s *Store) DeletePasskeysByAccount(ctx context.Context, accountID string) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM account_passkeys WHERE account_id = $1`, accountID)
	return err
}

// --- Passkeys ---

// ListPasskeys lists the account's passkeys ordered by creation time.
func (s *Store) ListPasskeys(ctx context.Context, accountID string) ([]model.Passkey, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, account_id, label, credential_id, credential, created_at, updated_at, deleted_at
		FROM account_passkeys WHERE account_id = $1 ORDER BY created_at`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var passkeys []model.Passkey
	for rows.Next() {
		var p model.Passkey
		if err := rows.Scan(&p.Id, &p.AccountId, &p.Label, &p.CredentialId, &p.Credential,
			&p.CreatedAt, &p.UpdatedAt, &p.DeletedAt); err != nil {
			return nil, err
		}
		passkeys = append(passkeys, p)
	}
	return passkeys, rows.Err()
}

// GetPasskeyByID loads one of the account's passkeys.
func (s *Store) GetPasskeyByID(ctx context.Context, accountID string, id uuid.UUID) (*model.Passkey, error) {
	var p model.Passkey
	err := s.DB.QueryRow(ctx, `SELECT id, account_id, label, credential_id, credential, created_at, updated_at, deleted_at
		FROM account_passkeys WHERE id = $1 AND account_id = $2`, id, accountID).
		Scan(&p.Id, &p.AccountId, &p.Label, &p.CredentialId, &p.Credential, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

// PasskeyCredentialIDExists checks the partial-unique credential id index.
func (s *Store) PasskeyCredentialIDExists(ctx context.Context, credentialID string) (bool, error) {
	var exists bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM account_passkeys WHERE credential_id = $1 AND deleted_at IS NULL)`,
		credentialID).Scan(&exists)
	return exists, err
}

// InsertPasskey persists a passkey row.
func (s *Store) InsertPasskey(ctx context.Context, p *model.Passkey) (*model.Passkey, error) {
	now := time.Now().UTC()
	var id uuid.UUID
	err := s.DB.QueryRow(ctx, `INSERT INTO account_passkeys
		(id, account_id, label, credential_id, credential, created_at, updated_at)
		VALUES (gen_random_uuid(),$1,$2,$3,$4,$5,$5) RETURNING id`,
		p.AccountId, p.Label, p.CredentialId, p.Credential, now).Scan(&id)
	if err != nil {
		return nil, err
	}
	p.Id = id.String()
	p.CreatedAt = model.NewTime(now)
	p.UpdatedAt = model.NewTime(now)
	return p, nil
}

// UpdatePasskeyLabel renames the passkey.
func (s *Store) UpdatePasskeyLabel(ctx context.Context, id uuid.UUID, label string) (*model.Passkey, error) {
	var p model.Passkey
	err := s.DB.QueryRow(ctx, `UPDATE account_passkeys SET label = $1, updated_at = $2 WHERE id = $3
		RETURNING id, account_id, label, credential_id, credential, created_at, updated_at, deleted_at`,
		label, time.Now().UTC(), id).
		Scan(&p.Id, &p.AccountId, &p.Label, &p.CredentialId, &p.Credential, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

// DeletePasskeyRow hard-deletes a passkey row.
func (s *Store) DeletePasskeyRow(ctx context.Context, id uuid.UUID) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM account_passkeys WHERE id = $1`, id)
	return err
}

// --- Contacts ---

// ListContacts lists the account's contact methods.
func (s *Store) ListContacts(ctx context.Context, accountID string) ([]model.Contact, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, type, verified_at, is_primary, is_public, content, account_id, created_at, updated_at, deleted_at
		FROM account_contacts WHERE account_id = $1`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var contacts []model.Contact
	for rows.Next() {
		var c model.Contact
		if err := rows.Scan(&c.Id, &c.Type, &c.VerifiedAt, &c.IsPrimary, &c.IsPublic, &c.Content,
			&c.AccountId, &c.CreatedAt, &c.UpdatedAt, &c.DeletedAt); err != nil {
			return nil, err
		}
		contacts = append(contacts, c)
	}
	return contacts, rows.Err()
}

// GetContactByID loads one of the account's contacts.
func (s *Store) GetContactByID(ctx context.Context, accountID string, id uuid.UUID) (*model.Contact, error) {
	var c model.Contact
	err := s.DB.QueryRow(ctx, `SELECT id, type, verified_at, is_primary, is_public, content, account_id, created_at, updated_at, deleted_at
		FROM account_contacts WHERE id = $1 AND account_id = $2`, id, accountID).
		Scan(&c.Id, &c.Type, &c.VerifiedAt, &c.IsPrimary, &c.IsPublic, &c.Content,
			&c.AccountId, &c.CreatedAt, &c.UpdatedAt, &c.DeletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

// InsertContact persists a contact method (CreateContactMethod).
func (s *Store) InsertContact(ctx context.Context, c *model.Contact) (*model.Contact, error) {
	now := time.Now().UTC()
	var id uuid.UUID
	err := s.DB.QueryRow(ctx, `INSERT INTO account_contacts
		(id, type, content, is_primary, is_public, account_id, created_at, updated_at)
		VALUES (gen_random_uuid(),$1,$2,$3,$4,$5,$6,$6) RETURNING id`,
		c.Type, c.Content, c.IsPrimary, c.IsPublic, c.AccountId, now).Scan(&id)
	if err != nil {
		return nil, err
	}
	c.Id = id.String()
	c.CreatedAt = model.NewTime(now)
	c.UpdatedAt = model.NewTime(now)
	return c, nil
}

// UpdateContact persists contact flag columns.
func (s *Store) UpdateContact(ctx context.Context, c *model.Contact) error {
	_, err := s.DB.Exec(ctx, `UPDATE account_contacts SET
		is_primary = $2, is_public = $3, verified_at = $4, updated_at = $5 WHERE id = $1`,
		c.Id, c.IsPrimary, c.IsPublic, c.VerifiedAt, time.Now().UTC())
	return err
}

// SetContactPrimary unmarks the other same-type contacts and marks the given
// one primary (SetContactMethodPrimary).
func (s *Store) SetContactPrimary(ctx context.Context, accountID string, ctype int, id uuid.UUID) error {
	if _, err := s.DB.Exec(ctx, `UPDATE account_contacts SET is_primary = false, updated_at = $3
		WHERE account_id = $1 AND type = $2`, accountID, ctype, time.Now().UTC()); err != nil {
		return err
	}
	_, err := s.DB.Exec(ctx, `UPDATE account_contacts SET is_primary = true, updated_at = $2 WHERE id = $1`,
		id, time.Now().UTC())
	return err
}

// DeleteContactRow hard-deletes a contact method.
func (s *Store) DeleteContactRow(ctx context.Context, id uuid.UUID) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM account_contacts WHERE id = $1`, id)
	return err
}

// --- Authorized apps ---

// ListAuthorizedApps lists the account's authorized apps, optionally filtered
// by type, ordered by last used (falling back to last authorized).
func (s *Store) ListAuthorizedApps(ctx context.Context, accountID string, typ *model.AuthorizedAppType) ([]model.AuthorizedApp, error) {
	query := `SELECT id, type, account_id, app_id, app_slug, app_name, scopes, last_authorized_at, last_used_at, created_at, updated_at, deleted_at
		FROM authorized_apps WHERE account_id = $1 AND deleted_at IS NULL`
	args := []any{accountID}
	if typ != nil {
		args = append(args, int(*typ))
		query += ` AND type = $2`
	}
	query += ` ORDER BY COALESCE(last_used_at, last_authorized_at) DESC`
	rows, err := s.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var apps []model.AuthorizedApp
	for rows.Next() {
		var app model.AuthorizedApp
		if err := rows.Scan(&app.Id, &app.Type, &app.AccountId, &app.AppId, &app.AppSlug, &app.AppName,
			&app.Scopes, &app.LastAuthorizedAt, &app.LastUsedAt, &app.CreatedAt, &app.UpdatedAt, &app.DeletedAt); err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
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
	rows, err := s.DB.Query(ctx, `SELECT k.id, k.label, k.app_id, k.created_at, sess.expired_at
		FROM api_keys k LEFT JOIN auth_sessions sess ON sess.id = k.session_id
		WHERE k.account_id = $1 AND k.deleted_at IS NULL`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []ApiKeyWithExpiry
	for rows.Next() {
		var key ApiKeyWithExpiry
		if err := rows.Scan(&key.Id, &key.Label, &key.AppId, &key.CreatedAt, &key.ExpiredAt); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// --- scan helpers ---

func scanSession(row pgx.Row) (*model.AuthSession, error) {
	session := &model.AuthSession{}
	var (
		audiences, scopes                             []string
		location                                      []byte
		clientID, parentSessionID, appID, challengeID *uuid.UUID
		epoch                                         int
	)
	err := row.Scan(
		&session.Id, &session.Type, &session.LastGrantedAt, &session.ExpiredAt, &audiences, &scopes,
		&session.IpAddress, &session.UserAgent, &location, &session.AccountId,
		&clientID, &parentSessionID, &appID, &challengeID, &epoch,
		&session.CreatedAt, &session.UpdatedAt, &session.DeletedAt,
	)
	if err != nil {
		return nil, err
	}
	session.Audiences = audiences
	session.Scopes = scopes
	if len(location) > 0 && string(location) != "null" {
		var gp model.GeoPoint
		if json.Unmarshal(location, &gp) == nil {
			session.Location = &gp
		}
	}
	session.ClientId = uuidPtrStr(clientID)
	session.ParentSessionId = uuidPtrStr(parentSessionID)
	session.AppId = uuidPtrStr(appID)
	session.ChallengeId = uuidPtrStr(challengeID)
	session.Epoch = epoch
	return session, nil
}

func scanSessions(rows pgx.Rows) ([]model.AuthSession, error) {
	defer rows.Close()
	var sessions []model.AuthSession
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, *session)
	}
	return sessions, rows.Err()
}

func scanClient(row pgx.Row) (*model.AuthClient, error) {
	device := &model.AuthClient{}
	err := row.Scan(&device.Id, &device.DeviceId, &device.DeviceName, &device.DeviceLabel,
		&device.AccountId, &device.Platform, &device.CreatedAt, &device.UpdatedAt, &device.DeletedAt)
	if err != nil {
		return nil, err
	}
	return device, nil
}

func scanFactor(row pgx.Row) (*model.AuthFactor, error) {
	factor := &model.AuthFactor{}
	var secret *string
	var config []byte
	err := row.Scan(&factor.Id, &factor.Type, &secret, &config, &factor.Trustworthy, &factor.EnabledAt,
		&factor.ExpiredAt, &factor.AccountId, &factor.CreatedAt, &factor.UpdatedAt, &factor.DeletedAt)
	if err != nil {
		return nil, err
	}
	if secret != nil {
		factor.Secret = *secret
	}
	if len(config) > 0 {
		_ = json.Unmarshal(config, &factor.Config)
	}
	return factor, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
