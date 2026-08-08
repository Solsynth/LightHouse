package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// This file adds the auth-challenge / account-registration query helpers used
// by internal/httpserver/authctl. It never touches existing store files.

const challengeColumns = `id, account_id, approved_at, approved_by_session_id, audiences, blacklist_factors,
	created_at, declined_at, deleted_at, device_id, device_name, expired_at, failed_attempts, ip_address,
	location, nonce, platform, scopes, step_remain, step_total, updated_at, user_agent`

func scanChallenge(row rowScanner) (*model.AuthChallenge, error) {
	ch := &model.AuthChallenge{}
	var (
		accountID                    *string
		audiences, scopes, blacklist []string
		location                     []byte
		approvedBySessionID          *uuid.UUID
	)
	err := row.Scan(
		&ch.Id, &accountID, &ch.ApprovedAt, &approvedBySessionID,
		&audiences, &blacklist, &ch.CreatedAt, &ch.DeclinedAt, &ch.DeletedAt,
		&ch.DeviceId, &ch.DeviceName, &ch.ExpiredAt, &ch.FailedAttempts, &ch.IpAddress,
		&location, &ch.Nonce, &ch.Platform, &scopes, &ch.StepRemain, &ch.StepTotal,
		&ch.UpdatedAt, &ch.UserAgent,
	)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	ch.AccountId = accountIDOrSentinel(accountID)
	ch.Audiences = audiences
	ch.Scopes = scopes
	ch.BlacklistFactors = blacklist
	ch.ApprovedBySessionId = uuidPtrStr(approvedBySessionID)
	if len(location) > 0 && string(location) != "null" {
		var gp model.GeoPoint
		if err := json.Unmarshal(location, &gp); err == nil {
			ch.Location = &gp
		}
	}
	return ch, nil
}

// GetAuthChallenge loads a challenge by id.
func (s *Store) GetAuthChallenge(ctx context.Context, id uuid.UUID) (*model.AuthChallenge, error) {
	row := s.queryRow(ctx, `SELECT `+challengeColumns+` FROM auth_challenges WHERE id = $1`, id)
	return scanChallenge(row)
}

// nullableAccountID maps the all-zero-UUID sentinel to NULL so anonymous
// challenges (discoverable passkey login, QR login) can be stored without an
// accounts row (account_id is nullable; see 0003 migration).
func nullableAccountID(accountID string) any {
	if accountID == "" || accountID == uuid.Nil.String() {
		return nil
	}
	return accountID
}

// accountIDOrSentinel normalizes a NULL account_id back to the sentinel so
// handlers keep treating uuid.Nil.String() as the "no account yet" marker.
func accountIDOrSentinel(accountID *string) string {
	if accountID == nil {
		return uuid.Nil.String()
	}
	return *accountID
}

// CreateAuthChallenge inserts a challenge (ids and timestamps must be set).
func (s *Store) CreateAuthChallenge(ctx context.Context, ch *model.AuthChallenge) error {
	locationJSON, _ := json.Marshal(ch.Location)
	audiences := jsonbOrEmpty(ch.Audiences)
	scopes := jsonbOrEmpty(ch.Scopes)
	blacklist := jsonbOrEmpty(ch.BlacklistFactors)
	_, err := s.exec(ctx, `INSERT INTO auth_challenges
		(id, account_id, approved_at, approved_by_session_id, audiences, blacklist_factors, created_at,
		 declined_at, deleted_at, device_id, device_name, expired_at, failed_attempts, ip_address,
		 location, nonce, platform, scopes, step_remain, step_total, updated_at, user_agent)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)`,
		ch.Id, nullableAccountID(ch.AccountId), ch.ApprovedAt, ch.ApprovedBySessionId, audiences, blacklist, ch.CreatedAt,
		ch.DeclinedAt, ch.DeletedAt, ch.DeviceId, ch.DeviceName, ch.ExpiredAt, ch.FailedAttempts, ch.IpAddress,
		locationJSON, ch.Nonce, int(ch.Platform), scopes, ch.StepRemain, ch.StepTotal, ch.UpdatedAt, ch.UserAgent)
	return err
}

// jsonbOrEmpty marshals a slice as JSON, using '[]' for nil so jsonb NOT NULL
// columns never receive NULL.
func jsonbOrEmpty[T any](v []T) []byte {
	if v == nil {
		return []byte("[]")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("[]")
	}
	return b
}

// UpdateAuthChallenge persists the mutable challenge fields.
func (s *Store) UpdateAuthChallenge(ctx context.Context, ch *model.AuthChallenge) error {
	_, err := s.exec(ctx, `UPDATE auth_challenges SET
		account_id = $2, approved_at = $3, approved_by_session_id = $4, blacklist_factors = $5,
		declined_at = $6, expired_at = $7, failed_attempts = $8, step_remain = $9, updated_at = $10
		WHERE id = $1`,
		ch.Id, nullableAccountID(ch.AccountId), ch.ApprovedAt, ch.ApprovedBySessionId, ch.BlacklistFactors,
		ch.DeclinedAt, ch.ExpiredAt, ch.FailedAttempts, ch.StepRemain, ch.UpdatedAt)
	return err
}

// FindLiveChallenge returns the newest live challenge for the same
// (account, ip, user-agent, device) triple, mirroring the reuse semantics of
// AuthController.CreateChallenge.
func (s *Store) FindLiveChallenge(ctx context.Context, accountID, ipAddress, userAgent, deviceID string) (*model.AuthChallenge, error) {
	row := s.queryRow(ctx, `SELECT `+challengeColumns+` FROM auth_challenges
		WHERE account_id = $1 AND ip_address = $2 AND user_agent = $3 AND device_id = $4
		  AND step_remain > 0 AND expired_at IS NOT NULL AND expired_at > now() AND deleted_at IS NULL
		ORDER BY created_at DESC LIMIT 1`,
		accountID, ipAddress, userAgent, deviceID)
	return scanChallenge(row)
}

// ListPendingChallenges lists the account's pending (unapproved, undeclined,
// live) challenges newest first.
func (s *Store) ListPendingChallenges(ctx context.Context, accountID string) ([]model.AuthChallenge, error) {
	rows, err := s.query(ctx, `SELECT `+challengeColumns+` FROM auth_challenges
		WHERE account_id = $1 AND approved_at IS NULL AND declined_at IS NULL AND step_remain > 0
		  AND (expired_at IS NULL OR expired_at > now()) AND deleted_at IS NULL
		ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var challenges []model.AuthChallenge
	for rows.Next() {
		ch, err := scanChallenge(rows)
		if err != nil {
			return nil, err
		}
		challenges = append(challenges, *ch)
	}
	return challenges, rows.Err()
}

// UpdateAccountBasicInfo updates the nullable basic-info fields and returns
// enabled state (callers check EnabledAt themselves).
func (s *Store) GetAuthFactorByType(ctx context.Context, accountID string, ftype model.AuthFactorType) (*model.AuthFactor, error) {
	var f model.AuthFactor
	var secret *string
	var config []byte
	err := s.queryRow(ctx, `SELECT id, type, secret, config, trustworthy, enabled_at, expired_at, account_id, created_at, updated_at, deleted_at
		FROM account_auth_factors
		WHERE account_id = $1 AND type = $2 AND deleted_at IS NULL
		ORDER BY created_at LIMIT 1`,
		accountID, int(ftype)).Scan(&f.Id, &f.Type, &secret, &config, &f.Trustworthy, &f.EnabledAt,
		&f.ExpiredAt, &f.AccountId, &f.CreatedAt, &f.UpdatedAt, &f.DeletedAt)
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

// LookupAccount resolves an account by name (case-insensitive) then by
// email/phone contact, mirroring AccountService.LookupAccount.
func (s *Store) LookupAccount(ctx context.Context, probe string) (*model.Account, error) {
	row := s.queryRow(ctx, `SELECT `+accountColumns+` FROM accounts
		WHERE name ILIKE $1 AND deleted_at IS NULL LIMIT 1`, probe)
	account, err := scanAccount(row)
	if err == nil {
		return account, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	var id uuid.UUID
	err = s.queryRow(ctx, `SELECT c.account_id FROM account_contacts c
		WHERE c.type IN ($1, $2) AND c.deleted_at IS NULL AND c.content ILIKE $3 LIMIT 1`,
		int(model.ContactTypeEmail), int(model.ContactTypePhoneNumber), probe).Scan(&id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return s.GetAccountByID(ctx, id)
}

// CheckAccountNameTaken reports whether the name is already used
// (case-insensitive), mirroring CheckAccountNameHasTaken.
func (s *Store) CheckAccountNameTaken(ctx context.Context, name string) (bool, error) {
	var exists bool
	err := s.queryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM accounts WHERE lower(name) = lower($1))`, name).Scan(&exists)
	return exists, err
}

// CheckEmailUsed reports whether an email contact already exists
// (case-insensitive), mirroring CheckEmailHasBeenUsed.
func (s *Store) CheckEmailUsed(ctx context.Context, email string) (bool, error) {
	var exists bool
	err := s.queryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM account_contacts c WHERE c.type = $1 AND c.deleted_at IS NULL AND c.content ILIKE $2)`,
		int(model.ContactTypeEmail), email).Scan(&exists)
	return exists, err
}

// CreateAccountWithRegistration atomically creates the account, its primary
// email contact, its password auth factor (bcrypt hash) and the `default`
// permission-group membership, mirroring AccountService.CreateAccount.
func (s *Store) CreateAccountWithRegistration(ctx context.Context, acc *model.Account, email string, passwordHash string) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	now := time.Now().UTC()
	_, err = tx.Exec(ctx, `INSERT INTO accounts
		(id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,false,$6,$6)`,
		acc.Id, acc.Name, acc.Nick, acc.Language, acc.Region, now)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO account_contacts
		(id, account_id, content, is_primary, is_public, type, created_at, updated_at)
		VALUES ($1,$2,$3,true,false,$4,$5,$5)`,
		uuid.NewString(), acc.Id, email, int(model.ContactTypeEmail), now)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO account_auth_factors
		(id, account_id, type, secret, trustworthy, enabled_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,1,$5,$5,$5)`,
		uuid.NewString(), acc.Id, int(model.AuthFactorTypePassword), passwordHash, now)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO permission_group_members (group_id, actor, created_at, updated_at)
		SELECT g.id, $1, now(), now() FROM permission_groups g
		WHERE g.key = 'default' AND g.deleted_at IS NULL
		ON CONFLICT DO NOTHING`, acc.Id)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RecentSessionInfo carries the fields DetectChallengeRisk needs from the
// account's recent sessions.
type RecentSessionInfo struct {
	ID            uuid.UUID
	LastGrantedAt *model.Time
	ChallengeID   *uuid.UUID
	ClientID      *uuid.UUID
	CreatedAt     time.Time
}

// ListRecentSessions returns the account's most recent sessions (by
// last_granted_at desc), mirroring DetectChallengeRisk's query.
func (s *Store) ListRecentSessions(ctx context.Context, accountID string, limit int) ([]RecentSessionInfo, error) {
	rows, err := s.query(ctx, `SELECT id, last_granted_at, challenge_id, client_id, created_at
		FROM auth_sessions WHERE account_id = $1 AND last_granted_at IS NOT NULL AND deleted_at IS NULL
		ORDER BY last_granted_at DESC LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []RecentSessionInfo
	for rows.Next() {
		var s RecentSessionInfo
		if err := rows.Scan(&s.ID, &s.LastGrantedAt, &s.ChallengeID, &s.ClientID, &s.CreatedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// ChallengeProbe carries the ip/user-agent history used by
// DetectChallengeRisk.
type ChallengeProbe struct {
	ID        uuid.UUID
	IpAddress *string
	UserAgent *string
}

// ListChallengesByIDs loads the ip/user-agent of the given challenges.
func (s *Store) ListChallengesByIDs(ctx context.Context, ids []uuid.UUID) ([]ChallengeProbe, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.query(ctx, `SELECT id, ip_address, user_agent FROM auth_challenges WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var probes []ChallengeProbe
	for rows.Next() {
		var p ChallengeProbe
		if err := rows.Scan(&p.ID, &p.IpAddress, &p.UserAgent); err != nil {
			return nil, err
		}
		probes = append(probes, p)
	}
	return probes, rows.Err()
}

// SumRecentFailedChallengeAttempts sums failed_attempts across challenges
// created after since, mirroring DetectChallengeRisk's risk component.
func (s *Store) SumRecentFailedChallengeAttempts(ctx context.Context, accountID string, since time.Time) (int, error) {
	var total int
	err := s.queryRow(ctx, `SELECT COALESCE(SUM(failed_attempts), 0) FROM auth_challenges
		WHERE account_id = $1 AND created_at > $2 AND failed_attempts > 0`, accountID, since).Scan(&total)
	return total, err
}

// PunishmentOverview mirrors SnAccountPunishment minus the hydrated account.
type PunishmentOverview struct {
	Type   model.PunishmentType
	Reason string
}

// GetActivePunishmentOverview returns the most severe active punishment
// (DisableAccount > BlockLogin > PermissionModification > Strike), mirroring
// AccountService.GetActivePunishmentOverview.
func (s *Store) GetActivePunishmentOverview(ctx context.Context, accountID string) (*PunishmentOverview, error) {
	var p PunishmentOverview
	err := s.queryRow(ctx, `SELECT type, reason FROM punishments
		WHERE account_id = $1 AND deleted_at IS NULL AND (expired_at IS NULL OR expired_at > now())
		ORDER BY CASE type WHEN 2 THEN 0 WHEN 1 THEN 1 WHEN 0 THEN 2 WHEN 3 THEN 3 ELSE 99 END
		LIMIT 1`, accountID).Scan(&p.Type, &p.Reason)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

const passkeyColumns = `id, account_id, label, credential_id, credential, created_at, updated_at, deleted_at`

func scanPasskey(row rowScanner) (*model.Passkey, error) {
	p := &model.Passkey{}
	err := row.Scan(&p.Id, &p.AccountId, &p.Label, &p.CredentialId, &p.Credential,
		&p.CreatedAt, &p.UpdatedAt, &p.DeletedAt)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

// ListPasskeysByAccount lists the account's registered passkeys.
func (s *Store) ListPasskeysByAccount(ctx context.Context, accountID string) ([]model.Passkey, error) {
	rows, err := s.query(ctx, `SELECT `+passkeyColumns+` FROM account_passkeys
		WHERE account_id = $1 AND deleted_at IS NULL`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var passkeys []model.Passkey
	for rows.Next() {
		p, err := scanPasskey(rows)
		if err != nil {
			return nil, err
		}
		passkeys = append(passkeys, *p)
	}
	return passkeys, rows.Err()
}

// GetPasskeyByCredentialID loads a passkey by its normalized credential id.
func (s *Store) GetPasskeyByCredentialID(ctx context.Context, credentialID string) (*model.Passkey, error) {
	row := s.queryRow(ctx, `SELECT `+passkeyColumns+` FROM account_passkeys
		WHERE credential_id = $1 AND deleted_at IS NULL`, credentialID)
	return scanPasskey(row)
}

// GetPasskeyByAccountAndCredentialID loads a passkey scoped to the account.
func (s *Store) GetPasskeyByAccountAndCredentialID(ctx context.Context, accountID, credentialID string) (*model.Passkey, error) {
	row := s.queryRow(ctx, `SELECT `+passkeyColumns+` FROM account_passkeys
		WHERE account_id = $1 AND credential_id = $2 AND deleted_at IS NULL`, accountID, credentialID)
	return scanPasskey(row)
}
