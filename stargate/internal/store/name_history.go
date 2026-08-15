package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// ErrNameTaken reports that the requested account name is already in use.
var ErrNameTaken = errors.New("account name taken")

// RenameAccount renames an account and records the former name in
// account_name_history for redirect fallback. The old name is freed
// immediately (the account row is renamed in the same transaction), and any
// active history row for the old name is soft-deleted so this account becomes
// the most recent former owner. Availability is checked case-insensitively,
// mirroring CheckAccountNameTaken (the DB unique index is case-sensitive).
func (s *Store) RenameAccount(ctx context.Context, accountID uuid.UUID, newName string) (*model.Account, error) {
	account, err := s.GetAccountByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(account.Name, newName) {
		return nil, errors.New("new name is the same as the current name")
	}
	var taken bool
	if err := s.queryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM accounts WHERE lower(name) = lower($1) AND id <> $2)`,
		newName, accountID).Scan(&taken); err != nil {
		return nil, err
	}
	if taken {
		return nil, ErrNameTaken
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	now := time.Now().UTC()
	if _, err := tx.Exec(ctx,
		`UPDATE account_name_history SET deleted_at = now(), updated_at = now()
		 WHERE lower(name) = lower($1) AND deleted_at IS NULL`, account.Name); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO account_name_history (id, account_id, name, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$4)`,
		uuid.NewString(), accountID, account.Name, now); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE accounts SET name = $2, updated_at = now() WHERE id = $1`, accountID, newName); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetAccountWithProfile(ctx, accountID)
}

// GetAccountNameHistoryOwner resolves the most recent active former owner of
// a name, or ErrNotFound when the name was never held (or its holder was
// soft-deleted).
func (s *Store) GetAccountNameHistoryOwner(ctx context.Context, name string) (*model.Account, error) {
	var accountID uuid.UUID
	if err := s.queryRow(ctx,
		`SELECT account_id FROM account_name_history
		 WHERE lower(name) = lower($1) AND deleted_at IS NULL
		 ORDER BY created_at DESC LIMIT 1`, name).Scan(&accountID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return s.GetAccountByID(ctx, accountID)
}
