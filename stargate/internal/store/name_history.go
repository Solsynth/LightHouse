package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

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
	if err := s.DB.WithContext(ctx).Model(&AccountEntity{}).
		Select("EXISTS(SELECT 1 FROM accounts WHERE lower(name) = lower(?) AND id <> ?)", newName, accountID).
		Scan(&taken).Error; err != nil {
		return nil, err
	}
	if taken {
		return nil, ErrNameTaken
	}
	now := time.Now().UTC()
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Model(&AccountNameHistoryEntity{}).
			Where("lower(name) = lower(?) AND deleted_at IS NULL", account.Name).
			Updates(map[string]any{"deleted_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Create(&AccountNameHistoryEntity{
			ID:         uuid.New(),
			EntityBase: EntityBase{CreatedAt: now, UpdatedAt: now},
			AccountID:  accountID,
			Name:       account.Name,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&AccountEntity{}).Where("id = ?", accountID).
			Updates(map[string]any{"name": newName, "updated_at": now}).Error
	})
	if err != nil {
		return nil, err
	}
	return s.GetAccountWithProfile(ctx, accountID)
}

// GetAccountNameHistoryOwner resolves the most recent active former owner of
// a name, or ErrNotFound when the name was never held (or its holder was
// soft-deleted).
func (s *Store) GetAccountNameHistoryOwner(ctx context.Context, name string) (*model.Account, error) {
	var entity AccountNameHistoryEntity
	if err := s.DB.WithContext(ctx).
		Where("lower(name) = lower(?)", name).
		Order("created_at DESC").
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return s.GetAccountByID(ctx, entity.AccountID)
}
