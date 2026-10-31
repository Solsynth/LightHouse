// Package tickets is the ported Passport ticket domain: support tickets with
// messages, assignment, status transitions and the on-call admin roster.
//
// Tables keep Passport's legacy names and columns so an existing database can
// be used in place. Account payloads and file references hydrate through the
// core account store and the files port.
package tickets

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// EntityBase mirrors the C# ModelBase: timestamps plus soft delete.
type EntityBase struct {
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at"`
}

// TicketEntity is `tickets` (SnTicket).
type TicketEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Title      string         `gorm:"column:title;size:256;not null"`
	Type       int            `gorm:"column:type;not null"`
	Status     int            `gorm:"column:status;not null"`
	Priority   int            `gorm:"column:priority;not null"`
	CreatorID  uuid.UUID      `gorm:"column:creator_id;type:uuid;not null;index:ix_tickets_creator_id"`
	AssigneeID *uuid.UUID     `gorm:"column:assignee_id;type:uuid;index:ix_tickets_assignee_id"`
	ResolvedAt *time.Time     `gorm:"column:resolved_at"`
	Metadata   datatypes.JSON `gorm:"column:metadata;type:jsonb;not null"`
	Resources  datatypes.JSON `gorm:"column:resources;type:jsonb"`

	Messages []TicketMessageEntity `gorm:"foreignKey:TicketID"`
}

func (TicketEntity) TableName() string { return "tickets" }

// TicketMessageEntity is `ticket_messages` (SnTicketMessage).
type TicketMessageEntity struct {
	ID       uuid.UUID      `gorm:"column:id;primaryKey;type:uuid"`
	TicketID uuid.UUID      `gorm:"column:ticket_id;type:uuid;not null;index:ix_ticket_messages_ticket_id"`
	SenderID uuid.UUID      `gorm:"column:sender_id;type:uuid;not null;index:ix_ticket_messages_sender_id"`
	Content  string         `gorm:"column:content;size:16384;not null"`
	Files    datatypes.JSON `gorm:"column:files;type:jsonb;not null"`
	EntityBase
}

func (TicketMessageEntity) TableName() string { return "ticket_messages" }

// OnCallAdminEntity is `ticket_on_call_admins` (SnTicketOnCallAdmin).
type OnCallAdminEntity struct {
	ID        uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	AccountID uuid.UUID `gorm:"column:account_id;type:uuid;not null"`
	EntityBase
}

func (OnCallAdminEntity) TableName() string { return "ticket_on_call_admins" }
