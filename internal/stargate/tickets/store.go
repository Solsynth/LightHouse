package tickets

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ErrNotFound reports a missing ticket row.
var ErrNotFound = errors.New("ticket: not found")

// Store owns the three ticket tables. Account hydration is the service's job.
type Store struct {
	DB *gorm.DB
}

// NewStore builds the persistence layer over the module's database handle.
func NewStore(db *gorm.DB) *Store { return &Store{DB: db} }

func mapNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

// CreateTicket writes the ticket and its first message atomically.
func (s *Store) CreateTicket(ctx context.Context, ticket *TicketEntity, message *TicketMessageEntity) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(ticket).Error; err != nil {
			return err
		}
		message.TicketID = ticket.ID
		return tx.Create(message).Error
	})
}

// GetTicket loads a ticket with its messages.
func (s *Store) GetTicket(ctx context.Context, id uuid.UUID) (*TicketEntity, error) {
	var entity TicketEntity
	err := s.DB.WithContext(ctx).
		Preload("Messages", func(db *gorm.DB) *gorm.DB { return db.Order("created_at") }).
		First(&entity, "id = ?", id).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &entity, nil
}

// ListTickets pages tickets newest first, then by priority, mirroring
// TicketService.GetTicketsAsync.
func (s *Store) ListTickets(ctx context.Context, filter Filter) ([]TicketEntity, error) {
	if filter.Take <= 0 {
		return []TicketEntity{}, nil
	}
	query := s.DB.WithContext(ctx).Model(&TicketEntity{}).
		Preload("Messages", func(db *gorm.DB) *gorm.DB { return db.Order("created_at") })
	if filter.CreatorId != nil {
		query = query.Where("creator_id = ?", *filter.CreatorId)
	}
	if filter.AssigneeId != nil {
		query = query.Where("assignee_id = ?", *filter.AssigneeId)
	}
	if filter.Type != nil {
		query = query.Where("type = ?", *filter.Type)
	}
	if filter.Status != nil {
		query = query.Where("status = ?", *filter.Status)
	}
	if filter.Priority != nil {
		query = query.Where("priority = ?", *filter.Priority)
	}
	var entities []TicketEntity
	if err := query.Order("created_at DESC").Order("priority DESC").
		Offset(filter.Offset).Limit(filter.Take).Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// CountTickets counts tickets matching the count filter.
func (s *Store) CountTickets(ctx context.Context, filter CountFilter) (int64, error) {
	query := s.DB.WithContext(ctx).Model(&TicketEntity{})
	if filter.CreatorId != nil {
		query = query.Where("creator_id = ?", *filter.CreatorId)
	}
	if filter.AssigneeId != nil {
		query = query.Where("assignee_id = ?", *filter.AssigneeId)
	}
	if filter.Status != nil {
		query = query.Where("status = ?", *filter.Status)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// AddMessage appends one message to a ticket.
func (s *Store) AddMessage(ctx context.Context, message *TicketMessageEntity) error {
	return s.DB.WithContext(ctx).Create(message).Error
}

// UpdateStatus writes the status and, for Resolved/Closed, the resolved time.
func (s *Store) UpdateStatus(ctx context.Context, id uuid.UUID, status int, resolvedAt *time.Time) error {
	updates := map[string]any{"status": status, "updated_at": time.Now().UTC()}
	if resolvedAt != nil {
		updates["resolved_at"] = *resolvedAt
	}
	result := s.DB.WithContext(ctx).Model(&TicketEntity{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// Assign sets or clears the assignee.
func (s *Store) Assign(ctx context.Context, id uuid.UUID, assigneeID *uuid.UUID) error {
	result := s.DB.WithContext(ctx).Model(&TicketEntity{}).Where("id = ?", id).
		Updates(map[string]any{"assignee_id": assigneeID, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateFields writes the editable ticket columns.
func (s *Store) UpdateFields(ctx context.Context, id uuid.UUID, updates map[string]any) error {
	updates["updated_at"] = time.Now().UTC()
	result := s.DB.WithContext(ctx).Model(&TicketEntity{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteTicket soft-deletes a ticket.
func (s *Store) DeleteTicket(ctx context.Context, id uuid.UUID) error {
	result := s.DB.WithContext(ctx).Delete(&TicketEntity{}, "id = ?", id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// OnCallRoster lists on-call admins oldest first.
func (s *Store) OnCallRoster(ctx context.Context) ([]OnCallAdminEntity, error) {
	var entities []OnCallAdminEntity
	if err := s.DB.WithContext(ctx).Order("created_at").Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// FindOnCall resolves the live on-call row for an account.
func (s *Store) FindOnCall(ctx context.Context, accountID uuid.UUID) (*OnCallAdminEntity, error) {
	var entity OnCallAdminEntity
	if err := s.DB.WithContext(ctx).Where("account_id = ?", accountID).First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &entity, nil
}

// AddOnCall inserts an on-call admin.
func (s *Store) AddOnCall(ctx context.Context, entity *OnCallAdminEntity) error {
	return s.DB.WithContext(ctx).Create(entity).Error
}

// RemoveOnCall soft-deletes every live row for the account.
func (s *Store) RemoveOnCall(ctx context.Context, accountID uuid.UUID) (bool, error) {
	result := s.DB.WithContext(ctx).Where("account_id = ?", accountID).Delete(&OnCallAdminEntity{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}
