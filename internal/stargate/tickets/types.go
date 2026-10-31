package tickets

import (
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/model"
)

// ModuleID is the compiled module identifier.
const ModuleID = "app.solian.id.tickets"

// Ticket types (TicketType in DysonNetwork.Shared.Models.TicketEnums).
const (
	TicketTypeSupport        = 0
	TicketTypeBugReport      = 1
	TicketTypeFeatureRequest = 2
	TicketTypeBilling        = 3
	TicketTypeOther          = 4
)

// Ticket statuses (TicketStatus).
const (
	TicketStatusOpen                      = 0
	TicketStatusInProgress                = 1
	TicketStatusResolved                  = 2
	TicketStatusClosed                    = 3
	TicketStatusWaitingForCustomer        = 4
	TicketStatusWaitingForMoreInformation = 5
)

// Ticket priorities (TicketPriority).
const (
	TicketPriorityLow      = 0
	TicketPriorityMedium   = 1
	TicketPriorityHigh     = 2
	TicketPriorityCritical = 3
)

// TicketTypeNames maps the C# enum values to their names; the status
// notification payload uses the same PascalCase names as Enum.ToString().
var TicketTypeNames = []string{"Support", "BugReport", "FeatureRequest", "Billing", "Other"}

// TicketStatusNames maps TicketStatus values to their names.
var TicketStatusNames = []string{
	"Open", "InProgress", "Resolved", "Closed", "WaitingForCustomer", "WaitingForMoreInformation",
}

// TicketPriorityNames maps TicketPriority values to their names.
var TicketPriorityNames = []string{"Low", "Medium", "High", "Critical"}

// StatusName returns the C# enum name, falling back to an empty string for an
// out-of-range value.
func StatusName(status int) string {
	if status >= 0 && status < len(TicketStatusNames) {
		return TicketStatusNames[status]
	}
	return ""
}

// Ticket is SnTicket's wire shape. Creator, Assignee, Messages and Files are
// hydrated navigations; Metadata and Resources are always JSON values, never
// null, matching the C# defaults.
type Ticket struct {
	Id         uuid.UUID       `json:"id"`
	Title      string          `json:"title"`
	Type       int             `json:"type"`
	Status     int             `json:"status"`
	Priority   int             `json:"priority"`
	CreatorId  uuid.UUID       `json:"creator_id"`
	Creator    *model.Account  `json:"creator"`
	AssigneeId *uuid.UUID      `json:"assignee_id,omitempty"`
	Assignee   *model.Account  `json:"assignee"`
	ResolvedAt *model.Time     `json:"resolved_at,omitempty"`
	Messages   []TicketMessage `json:"messages"`
	Metadata   map[string]any  `json:"metadata"`
	Resources  []*string       `json:"resources"`
	CreatedAt  *model.Time     `json:"created_at"`
	UpdatedAt  *model.Time     `json:"updated_at"`
	DeletedAt  *model.Time     `json:"deleted_at"`
}

// TicketMessage is SnTicketMessage's wire shape. Ticket is [JsonIgnore] in C#.
type TicketMessage struct {
	Id        uuid.UUID                          `json:"id"`
	TicketId  uuid.UUID                          `json:"ticket_id"`
	SenderId  uuid.UUID                          `json:"sender_id"`
	Sender    *model.Account                     `json:"sender"`
	Content   string                             `json:"content"`
	Files     []model.SnCloudFileReferenceObject `json:"files"`
	CreatedAt *model.Time                        `json:"created_at"`
	UpdatedAt *model.Time                        `json:"updated_at"`
	DeletedAt *model.Time                        `json:"deleted_at"`
}

// OnCallAdmin is SnTicketOnCallAdmin's wire shape.
type OnCallAdmin struct {
	Id        uuid.UUID      `json:"id"`
	AccountId uuid.UUID      `json:"account_id"`
	Account   *model.Account `json:"account"`
	CreatedAt *model.Time    `json:"created_at"`
	UpdatedAt *model.Time    `json:"updated_at"`
	DeletedAt *model.Time    `json:"deleted_at"`
}

// CreateInput is the POST /tickets body after validation.
type CreateInput struct {
	Title     string
	Content   string
	Type      int
	Priority  int
	CreatorId uuid.UUID
	FileIds   []string
	Resources []*string
}

// UpdateInput is the PUT /tickets/:id body. Nil fields are left untouched.
type UpdateInput struct {
	Title     *string
	Type      *int
	Priority  *int
	Resources *[]*string
}

// Filter is the GET /tickets query.
type Filter struct {
	CreatorId  *uuid.UUID
	AssigneeId *uuid.UUID
	Type       *int
	Status     *int
	Priority   *int
	Offset     int
	Take       int
}

// CountFilter is the GET /tickets/count query.
type CountFilter struct {
	CreatorId  *uuid.UUID
	AssigneeId *uuid.UUID
	Status     *int
}
