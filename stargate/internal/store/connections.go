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

// Connection helpers for the social-login surface (account_connections).

// GetConnectionWithAccount loads a connection joined with its account.
func (s *Store) GetConnectionWithAccount(ctx context.Context, provider, providedIdentifier string) (*model.Connection, *model.Account, error) {
	var c model.Connection
	var meta []byte
	var account model.Account
	var automatedID *uuid.UUID
	err := s.DB.QueryRow(ctx, `SELECT c.id, c.provider, c.provided_identifier, c.meta, c.last_used_at,
		c.is_public, c.account_id, c.created_at, c.updated_at, c.deleted_at,
		a.id, a.name, a.nick, a.language, a.region, a.activated_at, a.is_superuser, a.automated_id, a.created_at, a.updated_at, a.deleted_at
		FROM account_connections c
		JOIN accounts a ON a.id = c.account_id
		WHERE LOWER(c.provider) = LOWER($1) AND c.provided_identifier = $2 AND c.deleted_at IS NULL
		LIMIT 1`, provider, providedIdentifier).Scan(
		&c.Id, &c.Provider, &c.ProvidedIdentifier, &meta, &c.LastUsedAt,
		&c.IsPublic, &c.AccountId, &c.CreatedAt, &c.UpdatedAt, &c.DeletedAt,
		&account.Id, &account.Name, &account.Nick, &account.Language, &account.Region, &account.ActivatedAt,
		&account.IsSuperuser, &automatedID, &account.CreatedAt, &account.UpdatedAt, &account.DeletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	if len(meta) > 0 {
		_ = json.Unmarshal(meta, &c.Meta)
	}
	account.AutomatedId = uuidPtrStr(automatedID)
	return &c, &account, nil
}

// GetConnectionByProviderIdentifier loads a connection by provider+identifier.
func (s *Store) GetConnectionByProviderIdentifier(ctx context.Context, provider, providedIdentifier string) (*model.Connection, error) {
	var c model.Connection
	var meta []byte
	err := s.DB.QueryRow(ctx, `SELECT id, provider, provided_identifier, meta, last_used_at, is_public, account_id, created_at, updated_at, deleted_at
		FROM account_connections
		WHERE LOWER(provider) = LOWER($1) AND provided_identifier = $2 AND deleted_at IS NULL
		LIMIT 1`, provider, providedIdentifier).Scan(
		&c.Id, &c.Provider, &c.ProvidedIdentifier, &meta, &c.LastUsedAt,
		&c.IsPublic, &c.AccountId, &c.CreatedAt, &c.UpdatedAt, &c.DeletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if len(meta) > 0 {
		_ = json.Unmarshal(meta, &c.Meta)
	}
	return &c, nil
}

// GetConnectionByAccountAndProvider loads the account's newest connection for
// a provider (case-insensitive provider match).
func (s *Store) GetConnectionByAccountAndProvider(ctx context.Context, accountID, provider string) (*model.Connection, error) {
	var c model.Connection
	var meta []byte
	err := s.DB.QueryRow(ctx, `SELECT id, provider, provided_identifier, meta, last_used_at, is_public, account_id, created_at, updated_at, deleted_at
		FROM account_connections
		WHERE account_id = $1 AND LOWER(provider) = LOWER($2) AND deleted_at IS NULL
		ORDER BY created_at LIMIT 1`, accountID, provider).Scan(
		&c.Id, &c.Provider, &c.ProvidedIdentifier, &meta, &c.LastUsedAt,
		&c.IsPublic, &c.AccountId, &c.CreatedAt, &c.UpdatedAt, &c.DeletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if len(meta) > 0 {
		_ = json.Unmarshal(meta, &c.Meta)
	}
	return &c, nil
}

// InsertConnection creates a new connection row.
func (s *Store) InsertConnection(ctx context.Context, accountID, provider, providedIdentifier, accessToken, refreshToken string, meta map[string]any, now time.Time) error {
	metaJSON, _ := json.Marshal(meta)
	_, err := s.DB.Exec(ctx, `INSERT INTO account_connections
		(id, provider, provided_identifier, meta, access_token, refresh_token, last_used_at, is_public, account_id, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,false,$8,$9,$9)`,
		uuid.NewString(), provider, providedIdentifier, metaJSON, nullStr(accessToken), nullStr(refreshToken), now, accountID, now)
	return err
}

// UpsertConnection updates an existing connection or inserts a new one.
// Returns whether a row was created.
func (s *Store) UpsertConnection(ctx context.Context, accountID, provider, providedIdentifier, accessToken, refreshToken string, meta map[string]any, now time.Time) (bool, error) {
	existing, err := s.GetConnectionByAccountAndProvider(ctx, accountID, provider)
	if err == nil {
		// Existing: refresh last_used_at + meta (tokens only when provided).
		metaJSON, _ := json.Marshal(meta)
		_, err := s.DB.Exec(ctx, `UPDATE account_connections SET last_used_at = $1, meta = $2, updated_at = $1 WHERE id = $3`,
			now, metaJSON, existing.Id)
		return false, err
	}
	if !errors.Is(err, ErrNotFound) {
		return false, err
	}
	if err := s.InsertConnection(ctx, accountID, provider, providedIdentifier, accessToken, refreshToken, meta, now); err != nil {
		return false, err
	}
	return true, nil
}

// TouchConnectionTokens updates or inserts a connection with fresh tokens.
// Returns whether a row was created.
func (s *Store) TouchConnectionTokens(ctx context.Context, accountID, provider, providedIdentifier, accessToken, refreshToken string, meta map[string]any, now time.Time) (bool, error) {
	existing, err := s.GetConnectionByAccountAndProvider(ctx, accountID, provider)
	if err == nil {
		metaJSON, _ := json.Marshal(meta)
		_, err := s.DB.Exec(ctx, `UPDATE account_connections
			SET access_token = COALESCE($1, access_token), refresh_token = COALESCE($2, refresh_token),
				last_used_at = $3, meta = $4, updated_at = $3 WHERE id = $5`,
			nullStr(accessToken), nullStr(refreshToken), now, metaJSON, existing.Id)
		return false, err
	}
	if !errors.Is(err, ErrNotFound) {
		return false, err
	}
	if err := s.InsertConnection(ctx, accountID, provider, providedIdentifier, accessToken, refreshToken, meta, now); err != nil {
		return false, err
	}
	return true, nil
}

// CreateOidcSession inserts an Oidc-typed session (type=2).
func (s *Store) CreateOidcSession(ctx context.Context, accountID string, clientID, parentSessionID *uuid.UUID, expiredAt, now time.Time) (*model.AuthSession, error) {
	var sessionID uuid.UUID
	err := s.DB.QueryRow(ctx, `INSERT INTO auth_sessions
		(id, type, created_at, last_granted_at, expired_at, account_id, app_id, client_id, parent_session_id, scopes, audiences, epoch, updated_at)
		VALUES (gen_random_uuid(),2,$1,$1,$2,$3,$4,$5,$6,'[]','[]',0,$1) RETURNING id`,
		now, expiredAt, accountID, clientID, clientID, parentSessionID).Scan(&sessionID)
	if err != nil {
		return nil, err
	}
	return &model.AuthSession{
		Id:              sessionID.String(),
		Type:            model.SessionTypeOidc,
		AccountId:       accountID,
		CreatedAt:       model.NewTime(now),
		LastGrantedAt:   model.NewTime(now),
		ExpiredAt:       model.NewTime(expiredAt),
		Scopes:          []string{},
		Audiences:       []string{},
		ParentSessionId: uuidPtrStr(parentSessionID),
		AppId:           uuidPtrStr(clientID),
	}, nil
}

// CreateAccountFromSocial creates a session-less account with a primary email
// contact and default-group membership, mirroring
// AccountService.CreateAccount(name, nick, email, null, isEmailVerified).
func (s *Store) CreateAccountFromSocial(ctx context.Context, name, nick, email string, emailVerified bool, now time.Time) (*model.Account, error) {
	used, err := s.CheckEmailUsed(ctx, email)
	if err != nil {
		return nil, err
	}
	if used {
		return nil, errors.New("Account email has already been used.")
	}

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	account := &model.Account{
		Id:        uuid.NewString(),
		Name:      name,
		Nick:      nick,
		Language:  "en-US",
		Region:    "en",
		CreatedAt: model.NewTime(now),
		UpdatedAt: model.NewTime(now),
	}
	var automatedID *uuid.UUID
	err = tx.QueryRow(ctx, `INSERT INTO accounts
		(id, name, nick, language, region, activated_at, is_superuser, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,NULL,false,$6,$6) RETURNING automated_id`,
		account.Id, account.Name, account.Nick, account.Language, account.Region, now).Scan(&automatedID)
	if err != nil {
		return nil, err
	}
	account.AutomatedId = uuidPtrStr(automatedID)

	contactID := uuid.NewString()
	var verifiedAt *time.Time
	if emailVerified {
		verifiedAt = &now
	}
	if _, err := tx.Exec(ctx, `INSERT INTO account_contacts
		(id, type, content, is_primary, is_public, verified_at, account_id, created_at, updated_at)
		VALUES ($1,0,$2,true,false,$3,$4,$5,$5)`,
		contactID, email, verifiedAt, account.Id, now); err != nil {
		return nil, err
	}

	// Enroll in the `default` permission group.
	if _, err := tx.Exec(ctx, `INSERT INTO permission_group_members (group_id, actor, created_at, updated_at)
		SELECT id, $1, $2, $2 FROM permission_groups WHERE key = 'default' AND deleted_at IS NULL
		ON CONFLICT (group_id, actor) DO NOTHING`, account.Id, now); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return account, nil
}

func nullStr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// Connection controller helpers (ConnectionController.cs port).

// ListConnections lists an account's connections WITHOUT a soft-delete filter
// (C# GetConnections has none).
func (s *Store) ListConnections(ctx context.Context, accountID string) ([]model.Connection, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, provider, provided_identifier, meta, last_used_at, is_public, account_id, created_at, updated_at, deleted_at
		FROM account_connections WHERE account_id = $1`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var connections []model.Connection
	for rows.Next() {
		var c model.Connection
		var meta []byte
		if err := rows.Scan(&c.Id, &c.Provider, &c.ProvidedIdentifier, &meta, &c.LastUsedAt,
			&c.IsPublic, &c.AccountId, &c.CreatedAt, &c.UpdatedAt, &c.DeletedAt); err != nil {
			return nil, err
		}
		if len(meta) > 0 && string(meta) != "null" {
			_ = json.Unmarshal(meta, &c.Meta)
		}
		connections = append(connections, c)
	}
	return connections, rows.Err()
}

// GetConnectionByID loads a connection scoped to the account.
func (s *Store) GetConnectionByID(ctx context.Context, accountID string, id uuid.UUID) (*model.Connection, error) {
	var c model.Connection
	var meta []byte
	err := s.DB.QueryRow(ctx, `SELECT id, provider, provided_identifier, meta, last_used_at, is_public, account_id, created_at, updated_at, deleted_at
		FROM account_connections WHERE id = $1 AND account_id = $2`, id, accountID).Scan(
		&c.Id, &c.Provider, &c.ProvidedIdentifier, &meta, &c.LastUsedAt,
		&c.IsPublic, &c.AccountId, &c.CreatedAt, &c.UpdatedAt, &c.DeletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if len(meta) > 0 && string(meta) != "null" {
		_ = json.Unmarshal(meta, &c.Meta)
	}
	return &c, nil
}

// UpdateConnection applies the visibility toggle (is_public).
func (s *Store) UpdateConnection(ctx context.Context, c *model.Connection) error {
	_, err := s.DB.Exec(ctx, `UPDATE account_connections SET is_public = $1, updated_at = now() WHERE id = $2`, c.IsPublic, c.Id)
	return err
}

// DeleteConnectionRow hard-deletes a connection row.
func (s *Store) DeleteConnectionRow(ctx context.Context, id uuid.UUID) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM account_connections WHERE id = $1`, id)
	return err
}

// SetConnectionVisibility toggles a connection's public flag.
func (s *Store) SetConnectionVisibility(ctx context.Context, id uuid.UUID, isPublic bool) error {
	_, err := s.DB.Exec(ctx, `UPDATE account_connections SET is_public = $1, updated_at = now() WHERE id = $2`, isPublic, id)
	return err
}
