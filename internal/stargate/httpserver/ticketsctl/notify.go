// Package ticketsctl mounts Passport's ticket HTTP surface: /api/tickets and
// /api/admin/tickets/on-call.
//
// Route paths, snake_case DTOs, error codes and the [AskPermission] keys match
// TicketController / TicketOnCallAdminController.
package ticketsctl

import (
	"context"
	"html"
	"strings"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/ports"
	"src.solsynth.dev/sosys/stargate/tickets"
)

// staff returns the on-call roster, falling back to every superuser when the
// roster is empty, and excluding the actor.
func (d Deps) staff(ctx context.Context, exclude uuid.UUID) []model.Account {
	var staff []model.Account
	if d.Service != nil {
		roster, err := d.Service.Roster(ctx)
		if err != nil && d.Log != nil {
			d.Log.Error("failed to load ticket on-call roster", "error", err)
		}
		for _, entry := range roster {
			if entry.Account == nil || entry.AccountId == exclude {
				continue
			}
			staff = append(staff, *entry.Account)
		}
	}
	if len(staff) > 0 {
		return staff
	}
	if d.Superusers == nil {
		return nil
	}
	superusers, err := d.Superusers(ctx)
	if err != nil {
		if d.Log != nil {
			d.Log.Error("failed to load superusers for ticket notification", "error", err)
		}
		return nil
	}
	for _, account := range superusers {
		if id, err := uuid.Parse(account.Id); err == nil && id == exclude {
			continue
		}
		staff = append(staff, account)
	}
	return staff
}

func displayName(account *model.Account) string {
	if account == nil {
		return ""
	}
	if strings.TrimSpace(account.Nick) != "" {
		return account.Nick
	}
	return account.Name
}

func dedupeUsers(users []model.Account) []model.Account {
	seen := map[string]struct{}{}
	out := make([]model.Account, 0, len(users))
	for _, user := range users {
		if user.Id == "" {
			continue
		}
		if _, ok := seen[user.Id]; ok {
			continue
		}
		seen[user.Id] = struct{}{}
		out = append(out, user)
	}
	return out
}

// pushUsers sends one localized push per recipient. Failures never propagate.
func (d Deps) pushUsers(ctx context.Context, users []model.Account, topic, titleKey, bodyKey string, args map[string]string) {
	if d.Push == nil || !d.Push.Available() {
		return
	}
	for _, user := range dedupeUsers(users) {
		title := localize(user.Language, titleKey, nil)
		body := localize(user.Language, bodyKey, args)
		if err := d.Push.SendToUser(ctx, user.Id, title, body); err != nil && d.Log != nil {
			d.Log.Error("failed to send ticket notification",
				"topic", topic, "account", user.Id, "error", err)
		}
	}
}

// notifyCreated tells the staff that a ticket opened.
func (d Deps) notifyCreated(ctx context.Context, ticket *tickets.Ticket) {
	staff := d.staff(ctx, ticket.CreatorId)
	if len(staff) == 0 {
		return
	}
	d.pushUsers(ctx, staff, "ticket.created", "ticketCreatedTitle", "ticketCreatedBody", map[string]string{
		"creatorName": displayName(ticket.Creator),
		"ticketTitle": ticket.Title,
	})
}

// notifyNewMessage tells the creator, assignee and staff about a reply.
func (d Deps) notifyNewMessage(ctx context.Context, ticket *tickets.Ticket, sender *model.Account) {
	senderID, _ := uuid.Parse(sender.Id)
	var users []model.Account
	if ticket.Creator != nil && ticket.CreatorId != senderID {
		users = append(users, *ticket.Creator)
	}
	if ticket.Assignee != nil && ticket.AssigneeId != nil && *ticket.AssigneeId != senderID {
		users = append(users, *ticket.Assignee)
	}
	if !sender.IsSuperuser {
		users = append(users, d.staff(ctx, senderID)...)
	}
	d.pushUsers(ctx, users, "ticket.message", "ticketNewMessageTitle", "ticketNewMessageBody", map[string]string{
		"senderName":  displayName(sender),
		"ticketTitle": ticket.Title,
	})
}

// notifyStatusChanged tells the creator, assignee and staff about a transition.
func (d Deps) notifyStatusChanged(ctx context.Context, ticket *tickets.Ticket, oldStatus, newStatus int, updater *model.Account) {
	updaterID, _ := uuid.Parse(updater.Id)
	var users []model.Account
	if ticket.Creator != nil && ticket.CreatorId != updaterID {
		users = append(users, *ticket.Creator)
	}
	if ticket.Assignee != nil && ticket.AssigneeId != nil && *ticket.AssigneeId != updaterID {
		users = append(users, *ticket.Assignee)
	}
	if !updater.IsSuperuser {
		users = append(users, d.staff(ctx, updaterID)...)
	}
	d.pushUsers(ctx, users, "ticket.status", "ticketStatusUpdatedTitle", "ticketStatusUpdatedBody", map[string]string{
		"ticketTitle": ticket.Title,
		"oldStatus":   tickets.StatusName(oldStatus),
		"newStatus":   tickets.StatusName(newStatus),
		"updaterName": displayName(updater),
	})
}

// notifyAssigned tells the creator, the previous assignee and staff.
func (d Deps) notifyAssigned(ctx context.Context, ticket *tickets.Ticket, oldAssignee, assignee *model.Account, assigner *model.Account) {
	assignerID, _ := uuid.Parse(assigner.Id)
	var users []model.Account
	if ticket.Creator != nil && ticket.CreatorId != assignerID {
		users = append(users, *ticket.Creator)
	}
	if oldAssignee != nil {
		if id, err := uuid.Parse(oldAssignee.Id); err == nil && id != assignerID {
			users = append(users, *oldAssignee)
		}
	}
	if !assigner.IsSuperuser {
		users = append(users, d.staff(ctx, assignerID)...)
	}
	d.pushUsers(ctx, users, "ticket.assign", "ticketAssignedTitle", "ticketAssignedBody", map[string]string{
		"ticketTitle":  ticket.Title,
		"assigneeName": displayName(assignee),
		"assignerName": displayName(assigner),
	})
}

// emailStatusChange sends the creator the same status-update email Passport
// sent for terminal and waiting statuses.
func (d Deps) emailStatusChange(ctx context.Context, ticket *tickets.Ticket, oldStatus, newStatus int, updater *model.Account) {
	switch newStatus {
	case tickets.TicketStatusResolved, tickets.TicketStatusClosed,
		tickets.TicketStatusWaitingForCustomer, tickets.TicketStatusWaitingForMoreInformation:
	default:
		return
	}
	if ticket.Creator == nil {
		return
	}
	args := map[string]string{
		"ticketTitle": ticket.Title,
		"oldStatus":   tickets.StatusName(oldStatus),
		"newStatus":   tickets.StatusName(newStatus),
		"updaterName": displayName(updater),
	}
	subject := localize(ticket.Creator.Language, "ticketStatusUpdatedTitle", nil)
	message := localize(ticket.Creator.Language, "ticketStatusUpdatedBody", args)

	var latest *string
	if newStatus == tickets.TicketStatusWaitingForCustomer || newStatus == tickets.TicketStatusWaitingForMoreInformation {
		latest = latestAdminMessage(ticket)
	}
	d.emailCreator(ctx, ticket, subject, message, latest)
}

// latestAdminMessage returns the newest message not sent by the creator.
func latestAdminMessage(ticket *tickets.Ticket) *string {
	var latest *tickets.TicketMessage
	for i := range ticket.Messages {
		message := &ticket.Messages[i]
		if message.SenderId == ticket.CreatorId {
			continue
		}
		if latest == nil || timeAfter(message.CreatedAt, latest.CreatedAt) {
			latest = message
		}
	}
	if latest == nil {
		return nil
	}
	return &latest.Content
}

func timeAfter(a, b *model.Time) bool {
	if a == nil {
		return false
	}
	if b == nil {
		return true
	}
	return time.Time(*a).After(time.Time(*b))
}

// emailCreator sends the status email to the creator's primary verified email
// contact. Missing contacts and transport failures are logged, never surfaced.
func (d Deps) emailCreator(ctx context.Context, ticket *tickets.Ticket, subject, message string, latest *string) {
	if d.Email == nil || !d.Email.Available() || d.Contacts == nil || ticket.Creator == nil {
		return
	}
	contacts, err := d.Contacts.ListContacts(ctx, ticket.CreatorId.String())
	if err != nil {
		if d.Log != nil {
			d.Log.Error("failed to list ticket creator contacts", "ticket", ticket.Id, "error", err)
		}
		return
	}
	var chosen *model.Contact
	for i := range contacts {
		contact := &contacts[i]
		if contact.DeletedAt != nil {
			continue
		}
		if contact.Type != int(model.ContactTypeEmail) || contact.VerifiedAt == nil {
			continue
		}
		if chosen == nil || (contact.IsPrimary && !chosen.IsPrimary) {
			chosen = contact
		}
	}
	if chosen == nil {
		return
	}
	recipient := displayName(ticket.Creator)
	link := strings.TrimRight(d.SiteURL, "/") + "/tickets/" + ticket.Id.String()
	greeting := "Hi " + recipient + ","
	text := greeting + "\n\n" + message
	if latest != nil && strings.TrimSpace(*latest) != "" {
		text += "\n\n" + *latest
	}
	text += "\n\n" + link
	htmlBody := "<p>" + html.EscapeString(greeting) + "</p>"
	htmlBody += "<p>" + html.EscapeString(message) + "</p>"
	if latest != nil && strings.TrimSpace(*latest) != "" {
		htmlBody += "<blockquote>" + html.EscapeString(*latest) + "</blockquote>"
	}
	htmlBody += `<p><a href="` + html.EscapeString(link) + `">` + html.EscapeString(link) + "</a></p>"
	msg := ports.EmailMessage{To: chosen.Content, Subject: subject, HTML: htmlBody, Text: text}
	if err := d.Email.Send(ctx, msg); err != nil {
		if d.Log != nil {
			d.Log.Error("failed to email ticket update", "ticket", ticket.Id, "account", ticket.CreatorId, "error", err)
		}
	}
}
