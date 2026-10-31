package ticketsctl

import (
	"errors"
	"net/http"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/tickets"
)

// ─────────────────────────── request bodies ───────────────────────────

type createTicketRequest struct {
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	Type      int       `json:"type"`
	Priority  *int      `json:"priority"`
	FileIds   []string  `json:"file_ids"`
	Resources []*string `json:"resources"`
}

type updateTicketRequest struct {
	Title     *string    `json:"title"`
	Type      *int       `json:"type"`
	Priority  *int       `json:"priority"`
	Resources *[]*string `json:"resources"`
}

type addMessageRequest struct {
	Content string   `json:"content"`
	FileIds []string `json:"file_ids"`
}

type assignRequest struct {
	AssigneeId *uuid.UUID `json:"assignee_id"`
}

type updateStatusRequest struct {
	Status int `json:"status"`
}

type addOnCallRequest struct {
	AccountId uuid.UUID `json:"account_id"`
}

func invalid(c *gin.Context, field, message string) {
	c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{field: {message}}))
}

// ─────────────────────────── tickets ───────────────────────────

// createTicket mirrors POST /api/tickets.
func (d Deps) createTicket(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	creatorID, ok := accountUUID(account)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return
	}
	var request createTicketRequest
	if !bindJSON(c, &request) {
		return
	}
	if count := utf8.RuneCountInString(request.Title); count < 3 || count > 256 {
		invalid(c, "title", "The title must be between 3 and 256 characters.")
		return
	}
	if utf8.RuneCountInString(request.Content) > 16384 {
		invalid(c, "content", "The content must be at most 16384 characters.")
		return
	}
	priority := tickets.TicketPriorityMedium
	if request.Priority != nil {
		priority = *request.Priority
	}
	ticket, err := d.Service.Create(c.Request.Context(), tickets.CreateInput{
		Title: request.Title, Content: request.Content, Type: request.Type,
		Priority: priority, CreatorId: creatorID,
		FileIds: request.FileIds, Resources: request.Resources,
	})
	if err != nil {
		writeServiceError(c, "PASSPORT_TICKET_CREATE_FAILED", err)
		return
	}
	d.notifyCreated(c.Request.Context(), ticket)
	c.JSON(http.StatusOK, ticket)
}

// listTickets mirrors GET /api/tickets.
func (d Deps) listTickets(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	creatorID, ok := parseOptionalUUID(c.Query("creatorId"))
	if !ok {
		invalid(c, "creatorId", "The value is not a valid UUID.")
		return
	}
	assigneeID, ok := parseOptionalUUID(c.Query("assigneeId"))
	if !ok {
		invalid(c, "assigneeId", "The value is not a valid UUID.")
		return
	}
	ticketType, ok := parseEnum(c.Query("type"), tickets.TicketTypeNames)
	if !ok {
		invalid(c, "type", "The value is not a valid ticket type.")
		return
	}
	status, ok := parseEnum(c.Query("status"), tickets.TicketStatusNames)
	if !ok {
		invalid(c, "status", "The value is not a valid ticket status.")
		return
	}
	priority, ok := parseEnum(c.Query("priority"), tickets.TicketPriorityNames)
	if !ok {
		invalid(c, "priority", "The value is not a valid ticket priority.")
		return
	}
	admin, err := d.isAdmin(c.Request.Context(), account)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if !admin && !isSelfScopedTicketQuery(account, creatorID, assigneeID) {
		forbidden(c, "You do not have permission to view these tickets.")
		return
	}
	offset := parseInt(c.DefaultQuery("offset", "0"), 0)
	take := parseInt(c.DefaultQuery("take", "20"), 20)
	ticketsList, err := d.Service.List(c.Request.Context(), tickets.Filter{
		CreatorId: creatorID, AssigneeId: assigneeID, Type: ticketType,
		Status: status, Priority: priority, Offset: offset, Take: take,
	})
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, ticketsList)
}

// listMyTickets mirrors GET /api/tickets/me.
func (d Deps) listMyTickets(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	id, ok := accountUUID(account)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return
	}
	status, ok := parseEnum(c.Query("status"), tickets.TicketStatusNames)
	if !ok {
		invalid(c, "status", "The value is not a valid ticket status.")
		return
	}
	offset := parseInt(c.DefaultQuery("offset", "0"), 0)
	take := parseInt(c.DefaultQuery("take", "20"), 20)
	ticketsList, err := d.Service.List(c.Request.Context(), tickets.Filter{
		CreatorId: &id, Status: status, Offset: offset, Take: take,
	})
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, ticketsList)
}

// getTicket mirrors GET /api/tickets/{id}.
func (d Deps) getTicket(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	id, ok := parsePathUUID(c, "id")
	if !ok {
		return
	}
	ticket, err := d.Service.Get(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, tickets.ErrNotFound) {
			ticketNotFound(c)
			return
		}
		internalError(c, d.Log, err)
		return
	}
	allowed, err := d.canAccessTicket(c.Request.Context(), account, ticket)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if !allowed {
		forbidden(c, "You do not have permission to view this ticket.")
		return
	}
	c.JSON(http.StatusOK, ticket)
}

// updateTicket mirrors PUT /api/tickets/{id}: admin only.
func (d Deps) updateTicket(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	admin, err := d.isAdmin(c.Request.Context(), account)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if !admin {
		forbidden(c, "You do not have permission to update tickets.")
		return
	}
	id, ok := parsePathUUID(c, "id")
	if !ok {
		return
	}
	var request updateTicketRequest
	if !bindJSON(c, &request) {
		return
	}
	if request.Title != nil {
		if count := utf8.RuneCountInString(*request.Title); count < 3 || count > 256 {
			invalid(c, "title", "The title must be between 3 and 256 characters.")
			return
		}
	}
	ticket, err := d.Service.Update(c.Request.Context(), id, tickets.UpdateInput{
		Title: request.Title, Type: request.Type, Priority: request.Priority,
		Resources: request.Resources,
	})
	if err != nil {
		writeServiceError(c, "PASSPORT_TICKET_UPDATE_FAILED", err)
		return
	}
	c.JSON(http.StatusOK, ticket)
}

// deleteTicket mirrors DELETE /api/tickets/{id}: admin only.
func (d Deps) deleteTicket(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	admin, err := d.isAdmin(c.Request.Context(), account)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if !admin {
		forbidden(c, "You do not have permission to delete tickets.")
		return
	}
	id, ok := parsePathUUID(c, "id")
	if !ok {
		return
	}
	if err := d.Service.Delete(c.Request.Context(), id); err != nil {
		writeServiceError(c, "PASSPORT_TICKET_DELETE_FAILED", err)
		return
	}
	c.Status(http.StatusNoContent)
}

// addMessage mirrors POST /api/tickets/{id}/messages.
func (d Deps) addMessage(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	senderID, ok := accountUUID(account)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return
	}
	id, ok := parsePathUUID(c, "id")
	if !ok {
		return
	}
	var request addMessageRequest
	if !bindJSON(c, &request) {
		return
	}
	if request.Content == "" {
		invalid(c, "content", "The content field is required.")
		return
	}
	if utf8.RuneCountInString(request.Content) > 16384 {
		invalid(c, "content", "The content must be at most 16384 characters.")
		return
	}
	ticket, err := d.Service.Get(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, tickets.ErrNotFound) {
			ticketNotFound(c)
			return
		}
		internalError(c, d.Log, err)
		return
	}
	allowed, err := d.canAccessTicket(c.Request.Context(), account, ticket)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if !allowed {
		forbidden(c, "You do not have permission to reply to tickets.")
		return
	}
	message, err := d.Service.AddMessage(c.Request.Context(), id, senderID, request.Content, request.FileIds)
	if err != nil {
		writeServiceError(c, "PASSPORT_TICKET_MESSAGE_FAILED", err)
		return
	}
	d.notifyNewMessage(c.Request.Context(), ticket, account)
	c.JSON(http.StatusOK, message)
}

// updateStatus mirrors POST /api/tickets/{id}/status.
func (d Deps) updateStatus(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	id, ok := parsePathUUID(c, "id")
	if !ok {
		return
	}
	var request updateStatusRequest
	if !bindJSON(c, &request) {
		return
	}
	existing, err := d.Service.Get(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, tickets.ErrNotFound) {
			ticketNotFound(c)
			return
		}
		internalError(c, d.Log, err)
		return
	}
	allowed, err := d.canAccessTicket(c.Request.Context(), account, existing)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if !allowed {
		forbidden(c, "You do not have permission to update ticket status.")
		return
	}
	oldStatus := existing.Status
	ticket, err := d.Service.UpdateStatus(c.Request.Context(), id, request.Status)
	if err != nil {
		writeServiceError(c, "PASSPORT_TICKET_STATUS_UPDATE_FAILED", err)
		return
	}
	if oldStatus != request.Status {
		d.notifyStatusChanged(c.Request.Context(), ticket, oldStatus, request.Status, account)
		d.emailStatusChange(c.Request.Context(), ticket, oldStatus, request.Status, account)
	}
	c.JSON(http.StatusOK, ticket)
}

// assignTicket mirrors POST /api/tickets/{id}/assign: admin only.
func (d Deps) assignTicket(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	admin, err := d.isAdmin(c.Request.Context(), account)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if !admin {
		forbidden(c, "You do not have permission to assign tickets.")
		return
	}
	id, ok := parsePathUUID(c, "id")
	if !ok {
		return
	}
	var request assignRequest
	if !bindJSON(c, &request) {
		return
	}
	existing, err := d.Service.Get(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, tickets.ErrNotFound) {
			ticketNotFound(c)
			return
		}
		internalError(c, d.Log, err)
		return
	}
	ticket, err := d.Service.Assign(c.Request.Context(), id, request.AssigneeId)
	if err != nil {
		writeServiceError(c, "PASSPORT_TICKET_ASSIGN_FAILED", err)
		return
	}
	if request.AssigneeId != nil && ticket.Assignee != nil {
		d.notifyAssigned(c.Request.Context(), ticket, existing.Assignee, ticket.Assignee, account)
	}
	c.JSON(http.StatusOK, ticket)
}

// countTickets mirrors GET /api/tickets/count.
func (d Deps) countTickets(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	creatorID, ok := parseOptionalUUID(c.Query("creatorId"))
	if !ok {
		invalid(c, "creatorId", "The value is not a valid UUID.")
		return
	}
	assigneeID, ok := parseOptionalUUID(c.Query("assigneeId"))
	if !ok {
		invalid(c, "assigneeId", "The value is not a valid UUID.")
		return
	}
	status, ok := parseEnum(c.Query("status"), tickets.TicketStatusNames)
	if !ok {
		invalid(c, "status", "The value is not a valid ticket status.")
		return
	}
	admin, err := d.isAdmin(c.Request.Context(), account)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if !admin && !isSelfScopedTicketQuery(account, creatorID, assigneeID) {
		forbidden(c, "You do not have permission to view these tickets.")
		return
	}
	count, err := d.Service.Count(c.Request.Context(), tickets.CountFilter{
		CreatorId: creatorID, AssigneeId: assigneeID, Status: status,
	})
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"count": count})
}

// ─────────────────────────── on-call roster ───────────────────────────

// listOnCall mirrors GET /api/admin/tickets/on-call.
func (d Deps) listOnCall(c *gin.Context) {
	roster, err := d.Service.Roster(c.Request.Context())
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, roster)
}

// addOnCall mirrors POST /api/admin/tickets/on-call.
func (d Deps) addOnCall(c *gin.Context) {
	var request addOnCallRequest
	if !bindJSON(c, &request) {
		return
	}
	entry, err := d.Service.AddOnCall(c.Request.Context(), request.AccountId)
	if err != nil {
		badRequest(c, "PASSPORT_TICKET_ON_CALL_ADD_FAILED", err.Error())
		return
	}
	c.JSON(http.StatusOK, entry)
}

// removeOnCall mirrors DELETE /api/admin/tickets/on-call/{accountId}.
func (d Deps) removeOnCall(c *gin.Context) {
	accountID, ok := parsePathUUID(c, "accountId")
	if !ok {
		return
	}
	if _, err := d.Service.RemoveOnCall(c.Request.Context(), accountID); err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.Status(http.StatusNoContent)
}
