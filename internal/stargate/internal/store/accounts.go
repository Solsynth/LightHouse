package store

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

func (s *Store) GetAccountWithProfileByNameFold(ctx context.Context, name string) (*model.Account, error) {
	var entity AccountEntity
	if err := s.DB.WithContext(ctx).Where("LOWER(name) = LOWER(?)", name).First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	account := accountFromEntity(&entity)
	profile, err := s.loadProfileForAccount(ctx, entity.ID)
	if err != nil {
		return nil, err
	}
	account.Profile = profile
	return account, nil
}

func (s *Store) SearchAccounts(ctx context.Context, query string, limit int) ([]model.Account, error) {
	normalized := strings.TrimSpace(query)
	if normalized == "" {
		return []model.Account{}, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	pattern := "%" + normalized + "%"
	statement := s.DB.WithContext(ctx).Where("name ILIKE ? OR nick ILIKE ?", pattern, pattern)
	if len(normalized) >= 3 {
		statement = statement.Or("name % ? OR nick % ?", normalized, normalized).
			Order(gorm.Expr("(name ILIKE ? OR nick ILIKE ?) DESC", pattern, pattern)).
			Order(gorm.Expr("similarity(name, ?) DESC", normalized)).
			Order(gorm.Expr("similarity(nick, ?) DESC", normalized))
	}
	var entities []AccountEntity
	if err := statement.Order("name").Limit(limit).Find(&entities).Error; err != nil {
		return nil, err
	}
	accounts := make([]model.Account, 0, len(entities))
	for i := range entities {
		accounts = append(accounts, *accountFromEntity(&entities[i]))
	}
	return accounts, nil
}

func (s *Store) GetAccountsByIDs(ctx context.Context, ids []uuid.UUID) ([]model.Account, error) {
	if len(ids) == 0 {
		return []model.Account{}, nil
	}
	var entities []AccountEntity
	if err := s.DB.WithContext(ctx).Where("id IN ?", ids).Order("created_at").Find(&entities).Error; err != nil {
		return nil, err
	}
	accounts := make([]model.Account, 0, len(entities))
	for i := range entities {
		accounts = append(accounts, *accountFromEntity(&entities[i]))
	}
	return accounts, nil
}

func (s *Store) ListPublicContacts(ctx context.Context, accountID uuid.UUID) ([]model.Contact, error) {
	var entities []ContactEntity
	if err := s.DB.WithContext(ctx).Where("account_id = ? AND is_public = ?", accountID, true).
		Order("created_at").Find(&entities).Error; err != nil {
		return nil, err
	}
	contacts := make([]model.Contact, 0, len(entities))
	for i := range entities {
		contacts = append(contacts, contactFromEntity(&entities[i]))
	}
	return contacts, nil
}

func (s *Store) ListPublicConnections(ctx context.Context, accountID uuid.UUID) ([]model.Connection, error) {
	var entities []ConnectionEntity
	if err := s.DB.WithContext(ctx).Where("account_id = ? AND is_public = ?", accountID, true).
		Order("created_at").Find(&entities).Error; err != nil {
		return nil, err
	}
	connections := make([]model.Connection, 0, len(entities))
	for i := range entities {
		connections = append(connections, connectionFromEntity(&entities[i]))
	}
	return connections, nil
}
