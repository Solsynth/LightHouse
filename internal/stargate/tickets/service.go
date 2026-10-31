package tickets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"

	"src.solsynth.dev/sosys/stargate/model"
)

// ErrAlreadyOnCall reports a duplicate on-call roster entry.
var ErrAlreadyOnCall = errors.New("ticket: account is already on call")

// AccountLookup hydrates creators, assignees and message senders. *store.Store
// implements it.
type AccountLookup interface {
	GetAccountsByIDs(ctx context.Context, ids []uuid.UUID) ([]model.Account, error)
}

// FileResolver resolves file ids through the Drive port.
type FileResolver interface {
	Available() bool
	Resolve(ctx context.Context, fileID string) (*model.SnCloudFileReferenceObject, error)
}

// Service is the ticket domain logic.
type Service struct {
	Store    *Store
	Accounts AccountLookup
	Files    FileResolver
	Log      *slog.Logger
}

// NewService builds the ticket service.
func NewService(store *Store, accounts AccountLookup, files FileResolver, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{Store: store, Accounts: accounts, Files: files, Log: log}
}

// ─────────────────────────── tickets ───────────────────────────

// Create writes a ticket and its first message, mirroring
// TicketService.CreateTicketAsync.
func (s *Service) Create(ctx context.Context, input CreateInput) (*Ticket, error) {
	if s.Accounts == nil {
		return nil, errors.New("ticket: account store unavailable")
	}
	accounts, err := s.Accounts.GetAccountsByIDs(ctx, []uuid.UUID{input.CreatorId})
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, fmt.Errorf("creator %s not found", input.CreatorId)
	}
	files, err := s.resolveFiles(ctx, input.FileIds)
	if err != nil {
		return nil, err
	}
	metadata, err := json.Marshal(map[string]any{})
	if err != nil {
		return nil, err
	}
	resources, err := encodeResources(input.Resources)
	if err != nil {
		return nil, err
	}
	fileJSON, err := json.Marshal(files)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	ticket := &TicketEntity{
		ID: uuid.New(), EntityBase: EntityBase{CreatedAt: now, UpdatedAt: now},
		Title: input.Title, Type: input.Type, Status: TicketStatusOpen,
		Priority: input.Priority, CreatorID: input.CreatorId,
		Metadata: metadata, Resources: resources,
	}
	message := &TicketMessageEntity{
		ID: uuid.New(), EntityBase: EntityBase{CreatedAt: now, UpdatedAt: now},
		SenderID: input.CreatorId, Content: input.Content, Files: fileJSON,
	}
	if err := s.Store.CreateTicket(ctx, ticket, message); err != nil {
		return nil, err
	}
	s.Log.Info("ticket created", "ticket", ticket.ID, "creator", input.CreatorId)

	entity, err := s.Store.GetTicket(ctx, ticket.ID)
	if err != nil {
		return nil, err
	}
	hydrated := ticketFromEntity(entity)
	if err := s.hydrate(ctx, []*Ticket{hydrated}); err != nil {
		return nil, err
	}
	return hydrated, nil
}

// Get loads one ticket with its messages and hydrated accounts.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Ticket, error) {
	entity, err := s.Store.GetTicket(ctx, id)
	if err != nil {
		return nil, err
	}
	ticket := ticketFromEntity(entity)
	if err := s.hydrate(ctx, []*Ticket{ticket}); err != nil {
		return nil, err
	}
	return ticket, nil
}

// List pages tickets matching the filter.
func (s *Service) List(ctx context.Context, filter Filter) ([]*Ticket, error) {
	entities, err := s.Store.ListTickets(ctx, filter)
	if err != nil {
		return nil, err
	}
	tickets := make([]*Ticket, 0, len(entities))
	for i := range entities {
		tickets = append(tickets, ticketFromEntity(&entities[i]))
	}
	if err := s.hydrate(ctx, tickets); err != nil {
		return nil, err
	}
	return tickets, nil
}

// Count counts tickets matching the filter.
func (s *Service) Count(ctx context.Context, filter CountFilter) (int64, error) {
	return s.Store.CountTickets(ctx, filter)
}

// AddMessage appends and hydrates one message.
func (s *Service) AddMessage(ctx context.Context, ticketID, senderID uuid.UUID, content string, fileIDs []string) (*TicketMessage, error) {
	if _, err := s.Store.GetTicket(ctx, ticketID); err != nil {
		return nil, err
	}
	sender, err := s.account(ctx, senderID)
	if err != nil {
		return nil, err
	}
	if sender == nil {
		return nil, fmt.Errorf("sender %s not found", senderID)
	}
	files, err := s.resolveFiles(ctx, fileIDs)
	if err != nil {
		return nil, err
	}
	fileJSON, err := json.Marshal(files)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	entity := &TicketMessageEntity{
		ID: uuid.New(), EntityBase: EntityBase{CreatedAt: now, UpdatedAt: now},
		TicketID: ticketID, SenderID: senderID, Content: content, Files: fileJSON,
	}
	if err := s.Store.AddMessage(ctx, entity); err != nil {
		return nil, err
	}
	s.Log.Info("ticket message added", "ticket", ticketID, "sender", senderID)
	message := messageFromEntity(entity)
	message.Sender = sender
	return message, nil
}

// UpdateStatus changes the ticket status, stamping resolved_at for the two
// terminal statuses exactly like TicketService.UpdateStatusAsync.
func (s *Service) UpdateStatus(ctx context.Context, id uuid.UUID, status int) (*Ticket, error) {
	var resolvedAt *time.Time
	if status == TicketStatusResolved || status == TicketStatusClosed {
		now := time.Now().UTC()
		resolvedAt = &now
	}
	if err := s.Store.UpdateStatus(ctx, id, status, resolvedAt); err != nil {
		return nil, err
	}
	s.Log.Info("ticket status updated", "ticket", id, "status", status)
	return s.Get(ctx, id)
}

// Assign sets or clears the assignee.
func (s *Service) Assign(ctx context.Context, id uuid.UUID, assigneeID *uuid.UUID) (*Ticket, error) {
	if assigneeID != nil {
		account, err := s.account(ctx, *assigneeID)
		if err != nil {
			return nil, err
		}
		if account == nil {
			return nil, fmt.Errorf("assignee %s not found", *assigneeID)
		}
	}
	if err := s.Store.Assign(ctx, id, assigneeID); err != nil {
		return nil, err
	}
	s.Log.Info("ticket assigned", "ticket", id, "assignee", assigneeID)
	return s.Get(ctx, id)
}

// Update writes the editable ticket fields.
func (s *Service) Update(ctx context.Context, id uuid.UUID, input UpdateInput) (*Ticket, error) {
	updates := map[string]any{}
	if input.Title != nil {
		updates["title"] = *input.Title
	}
	if input.Type != nil {
		updates["type"] = *input.Type
	}
	if input.Priority != nil {
		updates["priority"] = *input.Priority
	}
	if input.Resources != nil {
		raw, err := encodeResources(*input.Resources)
		if err != nil {
			return nil, err
		}
		updates["resources"] = raw
	}
	if err := s.Store.UpdateFields(ctx, id, updates); err != nil {
		return nil, err
	}
	s.Log.Info("ticket updated", "ticket", id)
	return s.Get(ctx, id)
}

// Delete soft-deletes a ticket.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	if err := s.Store.DeleteTicket(ctx, id); err != nil {
		return err
	}
	s.Log.Info("ticket deleted", "ticket", id)
	return nil
}

// ─────────────────────────── on-call roster ───────────────────────────

// Roster lists on-call admins with their account payloads.
func (s *Service) Roster(ctx context.Context) ([]*OnCallAdmin, error) {
	entities, err := s.Store.OnCallRoster(ctx)
	if err != nil {
		return nil, err
	}
	roster := make([]*OnCallAdmin, 0, len(entities))
	ids := make([]uuid.UUID, 0, len(entities))
	for i := range entities {
		roster = append(roster, onCallFromEntity(&entities[i]))
		ids = append(ids, entities[i].AccountID)
	}
	accounts, err := s.accountsByID(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, entry := range roster {
		if account, ok := accounts[entry.AccountId]; ok {
			copied := account
			entry.Account = &copied
		}
	}
	return roster, nil
}

// AddOnCall adds an account to the on-call roster. A duplicate is an error so
// the controller can answer PASSPORT_TICKET_ON_CALL_ADD_FAILED.
func (s *Service) AddOnCall(ctx context.Context, accountID uuid.UUID) (*OnCallAdmin, error) {
	account, err := s.account(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, errors.New("account not found")
	}
	if _, err := s.Store.FindOnCall(ctx, accountID); err == nil {
		return nil, ErrAlreadyOnCall
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	now := time.Now().UTC()
	entity := &OnCallAdminEntity{
		ID: uuid.New(), AccountID: accountID,
		EntityBase: EntityBase{CreatedAt: now, UpdatedAt: now},
	}
	if err := s.Store.AddOnCall(ctx, entity); err != nil {
		return nil, err
	}
	s.Log.Info("ticket on-call admin added", "account", accountID)
	entry := onCallFromEntity(entity)
	entry.Account = account
	return entry, nil
}

// RemoveOnCall soft-deletes the account's roster entry, reporting whether a
// live row was removed.
func (s *Service) RemoveOnCall(ctx context.Context, accountID uuid.UUID) (bool, error) {
	removed, err := s.Store.RemoveOnCall(ctx, accountID)
	if err != nil {
		return false, err
	}
	if removed {
		s.Log.Info("ticket on-call admin removed", "account", accountID)
	}
	return removed, nil
}

// ─────────────────────────── hydration ───────────────────────────

func (s *Service) account(ctx context.Context, id uuid.UUID) (*model.Account, error) {
	accounts, err := s.accountsByID(ctx, []uuid.UUID{id})
	if err != nil {
		return nil, err
	}
	account, ok := accounts[id]
	if !ok {
		return nil, nil
	}
	return &account, nil
}

func (s *Service) accountsByID(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]model.Account, error) {
	out := map[uuid.UUID]model.Account{}
	if s.Accounts == nil || len(ids) == 0 {
		return out, nil
	}
	accounts, err := s.Accounts.GetAccountsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, account := range accounts {
		id, err := uuid.Parse(account.Id)
		if err != nil {
			continue
		}
		out[id] = account
	}
	return out, nil
}

// hydrate attaches creators, assignees and message senders in one account
// read for the whole page.
func (s *Service) hydrate(ctx context.Context, tickets []*Ticket) error {
	ids := make([]uuid.UUID, 0, len(tickets)*2)
	for _, ticket := range tickets {
		ids = append(ids, ticket.CreatorId)
		if ticket.AssigneeId != nil {
			ids = append(ids, *ticket.AssigneeId)
		}
		for _, message := range ticket.Messages {
			ids = append(ids, message.SenderId)
		}
	}
	accounts, err := s.accountsByID(ctx, ids)
	if err != nil {
		return err
	}
	for _, ticket := range tickets {
		if account, ok := accounts[ticket.CreatorId]; ok {
			copied := account
			ticket.Creator = &copied
		}
		if ticket.AssigneeId != nil {
			if account, ok := accounts[*ticket.AssigneeId]; ok {
				copied := account
				ticket.Assignee = &copied
			}
		}
		for i := range ticket.Messages {
			if account, ok := accounts[ticket.Messages[i].SenderId]; ok {
				copied := account
				ticket.Messages[i].Sender = &copied
			}
		}
	}
	return nil
}

// resolveFiles resolves the posted file ids one by one. A missing file is
// skipped (Passport's batch call dropped unknown ids the same way).
func (s *Service) resolveFiles(ctx context.Context, fileIDs []string) ([]model.SnCloudFileReferenceObject, error) {
	files := make([]model.SnCloudFileReferenceObject, 0, len(fileIDs))
	if s.Files == nil || !s.Files.Available() {
		return files, nil
	}
	for _, fileID := range fileIDs {
		if fileID == "" {
			continue
		}
		file, err := s.Files.Resolve(ctx, fileID)
		if err != nil || file == nil {
			if err != nil {
				s.Log.Warn("ticket file resolution failed", "file", fileID, "error", err)
			}
			continue
		}
		files = append(files, *file)
	}
	return files, nil
}

// ─────────────────────────── conversions ───────────────────────────

func ticketFromEntity(entity *TicketEntity) *Ticket {
	ticket := &Ticket{
		Id: entity.ID, Title: entity.Title, Type: entity.Type,
		Status: entity.Status, Priority: entity.Priority, CreatorId: entity.CreatorID,
		Metadata:  decodeMetadata(entity.Metadata),
		Resources: decodeResources(entity.Resources),
		Messages:  make([]TicketMessage, 0, len(entity.Messages)),
	}
	if entity.AssigneeID != nil {
		id := *entity.AssigneeID
		ticket.AssigneeId = &id
	}
	if entity.ResolvedAt != nil {
		ticket.ResolvedAt = model.NewTime(*entity.ResolvedAt)
	}
	if !entity.CreatedAt.IsZero() {
		ticket.CreatedAt = model.NewTime(entity.CreatedAt)
	}
	if !entity.UpdatedAt.IsZero() {
		ticket.UpdatedAt = model.NewTime(entity.UpdatedAt)
	}
	if entity.DeletedAt.Valid {
		ticket.DeletedAt = model.NewTime(entity.DeletedAt.Time)
	}
	for i := range entity.Messages {
		ticket.Messages = append(ticket.Messages, *messageFromEntity(&entity.Messages[i]))
	}
	return ticket
}

func messageFromEntity(entity *TicketMessageEntity) *TicketMessage {
	message := &TicketMessage{
		Id: entity.ID, TicketId: entity.TicketID, SenderId: entity.SenderID,
		Content: entity.Content, Files: decodeFiles(entity.Files),
	}
	if !entity.CreatedAt.IsZero() {
		message.CreatedAt = model.NewTime(entity.CreatedAt)
	}
	if !entity.UpdatedAt.IsZero() {
		message.UpdatedAt = model.NewTime(entity.UpdatedAt)
	}
	if entity.DeletedAt.Valid {
		message.DeletedAt = model.NewTime(entity.DeletedAt.Time)
	}
	return message
}

func onCallFromEntity(entity *OnCallAdminEntity) *OnCallAdmin {
	entry := &OnCallAdmin{Id: entity.ID, AccountId: entity.AccountID}
	if !entity.CreatedAt.IsZero() {
		entry.CreatedAt = model.NewTime(entity.CreatedAt)
	}
	if !entity.UpdatedAt.IsZero() {
		entry.UpdatedAt = model.NewTime(entity.UpdatedAt)
	}
	if entity.DeletedAt.Valid {
		entry.DeletedAt = model.NewTime(entity.DeletedAt.Time)
	}
	return entry
}

func decodeMetadata(raw datatypes.JSON) map[string]any {
	out := map[string]any{}
	if len(raw) == 0 {
		return out
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded == nil {
		return out
	}
	return decoded
}

func decodeResources(raw datatypes.JSON) []*string {
	out := make([]*string, 0)
	if len(raw) == 0 {
		return out
	}
	var decoded []*string
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded == nil {
		return out
	}
	return decoded
}

func decodeFiles(raw datatypes.JSON) []model.SnCloudFileReferenceObject {
	out := make([]model.SnCloudFileReferenceObject, 0)
	if len(raw) == 0 {
		return out
	}
	var decoded []model.SnCloudFileReferenceObject
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded == nil {
		return out
	}
	return decoded
}

func encodeResources(resources []*string) (datatypes.JSON, error) {
	if resources == nil {
		resources = []*string{}
	}
	return json.Marshal(resources)
}
