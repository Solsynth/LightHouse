// Package ticketsctl mounts Passport's ticket HTTP surface: /api/tickets and
// /api/admin/tickets/on-call.
//
// Route paths, snake_case DTOs, error codes and the [AskPermission] keys match
// TicketController / TicketOnCallAdminController.
package ticketsctl

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/permission"
	"src.solsynth.dev/sosys/stargate/ports"
	"src.solsynth.dev/sosys/stargate/tickets"
)

// Deps is the dependency set the ticket controllers need.
type Deps struct {
	Service *tickets.Service
	Store   *tickets.Store
	Perm    middleware.PermissionChecker
	// Contacts resolves the creator's primary verified email for status
	// notifications. *store.Store implements it.
	Contacts ContactLookup
	// Superusers is the fallback notification audience when the on-call
	// roster is empty.
	Superusers func(ctx context.Context) ([]model.Account, error)
	Push       ports.Push
	Email      ports.Email
	SiteURL    string
	Log        *slog.Logger
}

// ContactLookup reads an account's contact methods.
type ContactLookup interface {
	ListContacts(ctx context.Context, accountID string) ([]model.Contact, error)
}

// Register mounts the ticket routes on the /api group.
func Register(api *gin.RouterGroup, d Deps) {
	group := api.Group("/tickets")
	group.POST("", middleware.RequireAuth(), middleware.AskPermission(d.Perm, permission.TicketsCreate), d.createTicket)
	group.GET("", middleware.RequireAuth(), d.listTickets)
	group.GET("/me", middleware.RequireAuth(), d.listMyTickets)
	group.GET("/count", middleware.RequireAuth(), d.countTickets)
	group.GET("/:id", middleware.RequireAuth(), d.getTicket)
	group.PUT("/:id", middleware.RequireAuth(), middleware.AskPermission(d.Perm, permission.TicketsUpdate), d.updateTicket)
	group.DELETE("/:id", middleware.RequireAuth(), middleware.AskPermission(d.Perm, permission.TicketsDelete), d.deleteTicket)
	group.POST("/:id/messages", middleware.RequireAuth(), middleware.AskPermission(d.Perm, permission.TicketsMessagesCreate), d.addMessage)
	group.POST("/:id/status", middleware.RequireAuth(), middleware.AskPermission(d.Perm, permission.TicketsStatusUpdate), d.updateStatus)
	group.POST("/:id/assign", middleware.RequireAuth(), middleware.AskPermission(d.Perm, permission.TicketsAssign), d.assignTicket)

	admin := api.Group("/admin/tickets/on-call")
	admin.GET("", middleware.RequireAuth(), middleware.AskPermission(d.Perm, permission.TicketsOnCallManage), d.listOnCall)
	admin.POST("", middleware.RequireAuth(), middleware.AskPermission(d.Perm, permission.TicketsOnCallManage), d.addOnCall)
	admin.DELETE("/:accountId", middleware.RequireAuth(), middleware.AskPermission(d.Perm, permission.TicketsOnCallManage), d.removeOnCall)
}

// ─────────────────────────── helpers ───────────────────────────

func currentAccount(c *gin.Context) *model.Account {
	account := middleware.CurrentUser(c.Request.Context())
	if account == nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return nil
	}
	return account
}

func accountUUID(account *model.Account) (uuid.UUID, bool) {
	id, err := uuid.Parse(account.Id)
	return id, err == nil
}

// isAdmin mirrors GetCurrentUserAsync: a superuser, or the literal
// tickets.admin permission.
func (d Deps) isAdmin(ctx context.Context, account *model.Account) (bool, error) {
	if account == nil {
		return false, nil
	}
	if account.IsSuperuser {
		return true, nil
	}
	if d.Perm == nil {
		return false, nil
	}
	id, ok := accountUUID(account)
	if !ok {
		return false, nil
	}
	return d.Perm.HasPermission(ctx, id, permission.TicketsAdmin)
}

func forbidden(c *gin.Context, message string) {
	c.JSON(http.StatusForbidden, errs.Forbidden(message))
}

func ticketNotFound(c *gin.Context) {
	c.JSON(http.StatusNotFound, errs.New("PASSPORT_TICKET_NOT_FOUND", "Ticket not found.", http.StatusNotFound))
}

func badRequest(c *gin.Context, code, message string) {
	c.JSON(http.StatusBadRequest, errs.New(code, message, http.StatusBadRequest))
}

func internalError(c *gin.Context, log *slog.Logger, err error) {
	if log != nil {
		log.Error("ticket request failed", "error", err)
	}
	c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Internal server error.", http.StatusInternalServerError))
}

func bindJSON(c *gin.Context, target any) bool {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Invalid request body."}}))
		return false
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return true
	}
	if err := json.Unmarshal(body, target); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Invalid request body."}}))
		return false
	}
	return true
}

func parseInt(raw string, fallback int) int {
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

// parseOptionalUUID parses an optional query UUID. ok is false when the value
// is non-empty but invalid.
func parseOptionalUUID(raw string) (*uuid.UUID, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, true
	}
	value, err := uuid.Parse(raw)
	if err != nil {
		return nil, false
	}
	return &value, true
}

func parsePathUUID(c *gin.Context, name string) (uuid.UUID, bool) {
	value, err := uuid.Parse(c.Param(name))
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{name: {"The value is not a valid UUID."}}))
		return uuid.Nil, false
	}
	return value, true
}

// parseEnum accepts either the numeric enum value or its case-insensitive C#
// name.
func parseEnum(raw string, names []string) (*int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true
	}
	if value, err := strconv.Atoi(raw); err == nil {
		return &value, true
	}
	for i, name := range names {
		if strings.EqualFold(name, raw) {
			value := i
			return &value, true
		}
	}
	return nil, false
}

func isSelfScopedTicketQuery(account *model.Account, creatorID, assigneeID *uuid.UUID) bool {
	current, ok := accountUUID(account)
	if !ok {
		return false
	}
	hasScope := false
	if creatorID != nil {
		hasScope = true
		if *creatorID != current {
			return false
		}
	}
	if assigneeID != nil {
		hasScope = true
		if *assigneeID != current {
			return false
		}
	}
	return hasScope
}

func (d Deps) canAccessTicket(ctx context.Context, account *model.Account, ticket *tickets.Ticket) (bool, error) {
	admin, err := d.isAdmin(ctx, account)
	if err != nil {
		return false, err
	}
	if admin {
		return true, nil
	}
	id, ok := accountUUID(account)
	if !ok {
		return false, nil
	}
	if ticket.CreatorId == id {
		return true, nil
	}
	return ticket.AssigneeId != nil && *ticket.AssigneeId == id, nil
}

// writeServiceError maps a domain error to the ticket error envelope.
func writeServiceError(c *gin.Context, code string, err error) {
	if errors.Is(err, tickets.ErrNotFound) {
		ticketNotFound(c)
		return
	}
	badRequest(c, code, err.Error())
}
