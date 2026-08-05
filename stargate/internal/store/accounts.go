package store

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// Account lookup helpers for the public accounts surface (search, contacts,
// connections, batch loads).

// GetAccountWithProfileByNameFold resolves an account by name with the
// case-insensitive semantics of Passport's LookupAccount (the Padlock gRPC
// search is matched with OrdinalIgnoreCase on Name).
func (s *Store) GetAccountWithProfileByNameFold(ctx context.Context, name string) (*model.Account, error) {
	q := `SELECT ` + accountColsPrefixed("a") + `, ` + profileColsPrefixed("p") + ` FROM accounts a
		LEFT JOIN account_profiles p ON p.account_id = a.id AND p.deleted_at IS NULL
		WHERE LOWER(a.name) = LOWER($1) AND a.deleted_at IS NULL`
	return scanAccountWithProfile(s.DB.QueryRow(ctx, q, name))
}

// SearchAccounts runs the Padlock account search ported locally: exact
// ILIKE %query% on name/nick, plus pg_trgm similarity when the query has at
// least 3 characters (CreateSearchContext). Results are ordered like the C#
// gRPC SearchAccount: ILIKE hits first, then similarity(name), similarity
// (nick), then name. The result is capped like the C# transport (100).
func (s *Store) SearchAccounts(ctx context.Context, query string, limit int) ([]model.Account, error) {
	normalized := strings.TrimSpace(query)
	if normalized == "" {
		return []model.Account{}, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	pattern := "%" + normalized + "%"

	var (
		rows pgx.Rows
		err  error
	)
	if len(normalized) < 3 {
		rows, err = s.DB.Query(ctx, `SELECT `+accountColumns+` FROM accounts
			WHERE deleted_at IS NULL AND (name ILIKE $1 OR nick ILIKE $1)
			ORDER BY name LIMIT $2`, pattern, limit)
	} else {
		rows, err = s.DB.Query(ctx, `SELECT `+accountColumns+` FROM accounts
			WHERE deleted_at IS NULL AND (
				name ILIKE $1 OR nick ILIKE $1 OR name % $2 OR nick % $2)
			ORDER BY (name ILIKE $1 OR nick ILIKE $1) DESC,
				similarity(name, $2) DESC, similarity(nick, $2) DESC, name
			LIMIT $3`, pattern, normalized, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []model.Account
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, *account)
	}
	return accounts, rows.Err()
}

// GetAccountsByIDs loads accounts by id, preserving the requested order and
// skipping missing/deleted rows (used for close-friends and mutual-friends
// lists).
func (s *Store) GetAccountsByIDs(ctx context.Context, ids []uuid.UUID) ([]model.Account, error) {
	if len(ids) == 0 {
		return []model.Account{}, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT `+accountColumns+` FROM accounts
		WHERE id = ANY($1) AND deleted_at IS NULL ORDER BY created_at`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []model.Account
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, *account)
	}
	return accounts, rows.Err()
}

// ListPublicContacts lists the account's public contacts (is_public = true).
func (s *Store) ListPublicContacts(ctx context.Context, accountID uuid.UUID) ([]model.Contact, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, type, verified_at, is_primary, is_public, content, account_id, created_at, updated_at, deleted_at
		FROM account_contacts WHERE account_id = $1 AND is_public AND deleted_at IS NULL
		ORDER BY created_at`, accountID)
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

// ListPublicConnections lists the account's public connections
// (is_public = true) with the secret token columns excluded.
func (s *Store) ListPublicConnections(ctx context.Context, accountID uuid.UUID) ([]model.Connection, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, provider, provided_identifier, meta, last_used_at, is_public, account_id, created_at, updated_at, deleted_at
		FROM account_connections WHERE account_id = $1 AND is_public AND deleted_at IS NULL
		ORDER BY created_at`, accountID)
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
		_ = unmarshalMeta(meta, &c.Meta)
		connections = append(connections, c)
	}
	return connections, rows.Err()
}

// unmarshalMeta decodes a jsonb column into a map, tolerating NULL/empty.
func unmarshalMeta(raw []byte, dest *map[string]any) error {
	if len(raw) == 0 || string(raw) == "null" {
		*dest = map[string]any{}
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		*dest = map[string]any{}
		return err
	}
	*dest = m
	return nil
}
