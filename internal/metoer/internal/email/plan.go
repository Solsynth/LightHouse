package email

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/go/pkg/models"

	"src.solsynth.dev/sosys/metoer/internal/model"
	"src.solsynth.dev/sosys/metoer/internal/store"
)

// CreateEmailSendingPlanCommand mirrors
// EmailSendingPlanService.CreateEmailSendingPlanCommand.
type CreateEmailSendingPlanCommand struct {
	AccountId           *uuid.UUID
	AccountIds          []uuid.UUID
	BroadcastToAll      bool
	Subject             string
	HtmlBody            string
	SendingPlanKey      *string
	PlannedStartAt      *time.Time
	MaxEmailsPerInterval int
	IntervalMinutes     int
	MaxEmailsPerDay     *int
}

// PlanService is the EmailSendingPlanService port.
type PlanService struct {
	st      *store.Store
	accounts gen.DyAccountServiceClient
	email   *Service
	log     *slog.Logger
}

// NewPlanService builds the sending-plan service.
func NewPlanService(st *store.Store, accounts gen.DyAccountServiceClient, email *Service, log *slog.Logger) *PlanService {
	return &PlanService{st: st, accounts: accounts, email: email, log: log}
}

type targetAccount struct {
	accountID    uuid.UUID
	recipientName string
}

// CreatePlanAsync mirrors CreatePlanAsync.
func (s *PlanService) CreatePlanAsync(ctx context.Context, command CreateEmailSendingPlanCommand, createdByAccountID uuid.UUID) (*model.EmailSendingPlanView, error) {
	targets, err := s.resolveTargetAccounts(ctx, command)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, errors.New("No valid target accounts were resolved.")
	}

	now := time.Now().UTC()
	plannedStartAt := now
	if command.PlannedStartAt != nil {
		plannedStartAt = command.PlannedStartAt.UTC()
	}
	var sendingPlanKey *string
	if command.SendingPlanKey != nil && strings.TrimSpace(*command.SendingPlanKey) != "" {
		key := strings.TrimSpace(*command.SendingPlanKey)
		sendingPlanKey = &key
	}

	plan := &model.EmailSendingPlan{
		ModelBase:             model.ModelBase{Id: uuid.New(), CreatedAt: model.NowTime(), UpdatedAt: model.NowTime()},
		SendingPlanKey:        sendingPlanKey,
		CreatedByAccountId:    createdByAccountID,
		Subject:               command.Subject,
		HtmlBody:              command.HtmlBody,
		BroadcastToAll:        command.BroadcastToAll,
		RecipientCount:        len(targets),
		MaxEmailsPerInterval:  command.MaxEmailsPerInterval,
		IntervalMinutes:       command.IntervalMinutes,
		MaxEmailsPerDay:       command.MaxEmailsPerDay,
		Status:                model.EmailSendingPlanScheduled,
		PlannedStartAt:        models.Time(plannedStartAt),
		NextIntervalAt:        model.TimePtr(plannedStartAt),
	}

	recipients := make([]*model.EmailSendingPlanRecipient, 0, len(targets))
	for _, target := range targets {
		name := target.recipientName
		recipients = append(recipients, &model.EmailSendingPlanRecipient{
			ModelBase:            model.ModelBase{Id: uuid.New(), CreatedAt: model.NowTime(), UpdatedAt: model.NowTime()},
			PlanId:               plan.Id,
			AccountId:            target.accountID,
			RecipientNameSnapshot: &name,
			Status:               model.EmailSendingPlanRecipientPending,
		})
	}

	if err := s.st.CreateEmailPlan(ctx, plan, recipients); err != nil {
		return nil, err
	}
	return s.GetPlanAsync(ctx, plan.Id)
}

// ListPlansAsync mirrors ListPlansAsync.
func (s *PlanService) ListPlansAsync(ctx context.Context, offset, take int, status *model.EmailSendingPlanStatus) ([]*model.EmailSendingPlanView, int, error) {
	plans, total, err := s.st.ListEmailPlans(ctx, offset, take, status)
	if err != nil {
		return nil, 0, err
	}
	views, err := s.buildPlanViews(ctx, plans, false, 20)
	if err != nil {
		return nil, 0, err
	}
	return views, total, nil
}

// GetPlanAsync mirrors GetPlanAsync (nil when the plan is missing).
func (s *PlanService) GetPlanAsync(ctx context.Context, planID uuid.UUID) (*model.EmailSendingPlanView, error) {
	plan, err := s.st.GetEmailPlan(ctx, planID)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, nil
	}
	views, err := s.buildPlanViews(ctx, []*model.EmailSendingPlan{plan}, true, 20)
	if err != nil {
		return nil, err
	}
	return views[0], nil
}

// PausePlanAsync mirrors PausePlanAsync.
func (s *PlanService) PausePlanAsync(ctx context.Context, planID uuid.UUID) (*model.EmailSendingPlanView, error) {
	plan, err := s.getTrackedPlan(ctx, planID)
	if err != nil {
		return nil, err
	}
	if plan.Status == model.EmailSendingPlanCompleted {
		return nil, errors.New("Completed plans cannot be paused.")
	}
	plan.Status = model.EmailSendingPlanPaused
	now := model.NowTime()
	plan.PausedAt = &now
	plan.UpdatedAt = model.NowTime()
	if err := s.st.UpdateEmailPlanStatus(ctx, plan); err != nil {
		return nil, err
	}
	return s.GetPlanAsync(ctx, planID)
}

// ResumePlanAsync mirrors ResumePlanAsync.
func (s *PlanService) ResumePlanAsync(ctx context.Context, planID uuid.UUID) (*model.EmailSendingPlanView, error) {
	plan, err := s.getTrackedPlan(ctx, planID)
	if err != nil {
		return nil, err
	}
	if plan.Status == model.EmailSendingPlanCompleted {
		return nil, errors.New("Completed plans cannot be resumed.")
	}
	now := time.Now().UTC()
	plan.Status = model.EmailSendingPlanScheduled
	plan.PausedAt = nil
	if plan.NextIntervalAt == nil || plan.NextIntervalAt.Time().Before(now) {
		plan.NextIntervalAt = model.TimePtr(now)
	}
	plan.UpdatedAt = model.NowTime()
	if err := s.st.UpdateEmailPlanStatus(ctx, plan); err != nil {
		return nil, err
	}
	return s.GetPlanAsync(ctx, planID)
}

// AdvancePlanIntervalAsync mirrors AdvancePlanIntervalAsync, porting every
// branch verbatim.
func (s *PlanService) AdvancePlanIntervalAsync(ctx context.Context, planID uuid.UUID, isManual bool) (*model.EmailSendingPlanView, error) {
	plan, err := s.getTrackedPlan(ctx, planID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()

	if plan.Status == model.EmailSendingPlanCompleted {
		return nil, errors.New("Completed plans cannot be advanced.")
	}
	if !isManual && plan.Status == model.EmailSendingPlanPaused {
		return nil, errors.New("Paused plans cannot be advanced automatically.")
	}
	if !isManual && plan.NextIntervalAt != nil && plan.NextIntervalAt.Time().After(now) {
		return nil, errors.New("The next interval is not due yet.")
	}

	pendingCountBefore, err := s.st.CountPendingRecipients(ctx, planID)
	if err != nil {
		return nil, err
	}
	if pendingCountBefore == 0 {
		plan.Status = model.EmailSendingPlanCompleted
		if plan.CompletedAt == nil {
			completed := model.NowTime()
			plan.CompletedAt = &completed
		}
		plan.NextIntervalAt = nil
		plan.UpdatedAt = model.NowTime()
		if err := s.st.UpdateEmailPlanStatus(ctx, plan); err != nil {
			return nil, err
		}
		return s.GetPlanAsync(ctx, planID)
	}

	remainingDailyCapacity, err := s.getRemainingDailyCapacity(ctx, plan, now)
	if err != nil {
		return nil, err
	}
	if remainingDailyCapacity != nil && *remainingDailyCapacity == 0 {
		if plan.Status != model.EmailSendingPlanPaused {
			next := getStartOfNextUtcDay(now)
			plan.NextIntervalAt = model.TimePtr(next)
		}
		plan.UpdatedAt = model.NowTime()
		if err := s.st.UpdateEmailPlanStatus(ctx, plan); err != nil {
			return nil, err
		}
		return s.GetPlanAsync(ctx, planID)
	}

	sendCapacity := plan.MaxEmailsPerInterval
	if remainingDailyCapacity != nil && *remainingDailyCapacity < sendCapacity {
		sendCapacity = *remainingDailyCapacity
	}

	recipients, err := s.st.ListPendingRecipients(ctx, planID)
	if err != nil {
		return nil, err
	}

	intervalNumber := plan.AdvancedIntervalsCount + 1
	attemptedCount := 0
	sentCount := 0
	skippedCount := 0
	failedCount := 0

	for _, recipient := range recipients {
		if attemptedCount >= sendCapacity {
			break
		}

		recipient.AttemptCount += 1
		recipient.LastIntervalNumber = new(intervalNumber)

		contacts, err := s.listContacts(ctx, recipient.AccountId)
		if err != nil {
			return nil, err
		}
		var contact *modelContact
		for _, c := range contacts {
			if contact == nil {
				contact = c
				continue
			}
			if c.IsPrimary != contact.IsPrimary {
				if c.IsPrimary {
					contact = c
				}
				continue
			}
			if verifiedAfter(c, contact) {
				contact = c
			}
		}

		if contact == nil {
			recipient.Status = model.EmailSendingPlanRecipientSkipped
			lastError := "No verified email contact is available."
			recipient.LastError = &lastError
			processed := model.NowTime()
			recipient.ProcessedAt = &processed
			recipient.UpdatedAt = model.NowTime()
			skippedCount += 1
			if err := s.st.UpdateRecipient(ctx, recipient); err != nil {
				return nil, err
			}
			continue
		}

		recipient.LastResolvedEmail = &contact.Content
		err = s.emailSend(ctx, recipient, contact.Content, plan.Subject, plan.HtmlBody)
		if err != nil {
			s.log.Warn("failed to send email plan",
				"plan_id", plan.Id, "interval", intervalNumber, "account_id", recipient.AccountId, "error", err)
			recipient.Status = model.EmailSendingPlanRecipientFailed
			truncated := truncate(err.Error(), 4096)
			recipient.LastError = &truncated
			processed := model.NowTime()
			recipient.ProcessedAt = &processed
			recipient.UpdatedAt = model.NowTime()
			attemptedCount += 1
			failedCount += 1
		} else {
			recipient.Status = model.EmailSendingPlanRecipientSent
			recipient.LastError = nil
			processed := model.NowTime()
			recipient.ProcessedAt = &processed
			recipient.UpdatedAt = model.NowTime()
			attemptedCount += 1
			sentCount += 1
		}
		if err := s.st.UpdateRecipient(ctx, recipient); err != nil {
			return nil, err
		}
	}

	pendingCountAfter := 0
	for _, recipient := range recipients {
		if recipient.Status == model.EmailSendingPlanRecipientPending {
			pendingCountAfter++
		}
	}

	if attemptedCount > 0 || skippedCount > 0 || failedCount > 0 {
		advance := &model.EmailSendingPlanAdvance{
			ModelBase:         model.ModelBase{Id: uuid.New(), CreatedAt: model.NowTime(), UpdatedAt: model.NowTime()},
			PlanId:            plan.Id,
			IntervalNumber:    intervalNumber,
			IsManual:          isManual,
			AttemptedCount:    attemptedCount,
			SentCount:         sentCount,
			SkippedCount:      skippedCount,
			FailedCount:       failedCount,
			PendingCountAfter: pendingCountAfter,
			StartedAt:         models.Time(now),
			CompletedAt:       model.NowTime(),
		}
		if err := s.st.InsertAdvance(ctx, advance); err != nil {
			return nil, err
		}
		plan.AdvancedIntervalsCount = intervalNumber
		lastAdvanced := model.NowTime()
		plan.LastAdvancedAt = &lastAdvanced
	}

	if pendingCountAfter == 0 {
		plan.Status = model.EmailSendingPlanCompleted
		completed := model.NowTime()
		plan.CompletedAt = &completed
		plan.NextIntervalAt = nil
	} else if plan.Status == model.EmailSendingPlanPaused {
		next := now.Add(time.Duration(plan.IntervalMinutes) * time.Minute)
		plan.NextIntervalAt = model.TimePtr(next)
	} else {
		var remainingCapacityAfter *int
		if remainingDailyCapacity != nil {
			v := *remainingDailyCapacity - attemptedCount
			if v < 0 {
				v = 0
			}
			remainingCapacityAfter = &v
		}
		plan.Status = model.EmailSendingPlanScheduled
		if plan.MaxEmailsPerDay != nil && remainingCapacityAfter != nil && *remainingCapacityAfter == 0 {
			plan.NextIntervalAt = model.TimePtr(getStartOfNextUtcDay(now))
		} else {
			next := now.Add(time.Duration(plan.IntervalMinutes) * time.Minute)
			plan.NextIntervalAt = model.TimePtr(next)
		}
	}
	plan.UpdatedAt = model.NowTime()
	if err := s.st.UpdateEmailPlanStatus(ctx, plan); err != nil {
		return nil, err
	}
	return s.GetPlanAsync(ctx, planID)
}

// AdvanceDuePlansAsync mirrors AdvanceDuePlansAsync: per-plan errors are
// logged and the loop continues.
func (s *PlanService) AdvanceDuePlansAsync(ctx context.Context) error {
	now := time.Now().UTC()
	plans, err := s.st.ListDuePlans(ctx, now)
	if err != nil {
		return err
	}
	for _, plan := range plans {
		if _, err := s.AdvancePlanIntervalAsync(ctx, plan.Id, false); err != nil {
			s.log.Error("failed to advance due email sending plan", "plan_id", plan.Id, "error", err)
		}
	}
	return nil
}

func (s *PlanService) getTrackedPlan(ctx context.Context, planID uuid.UUID) (*model.EmailSendingPlan, error) {
	plan, err := s.st.GetEmailPlan(ctx, planID)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, fmt.Errorf("email sending plan %s was not found", planID)
	}
	return plan, nil
}

// GetPlanOrThrow mirrors GetPlanOrThrowAsync (KeyNotFoundException for
// missing plans).
func (s *PlanService) GetPlanOrThrow(ctx context.Context, planID uuid.UUID) (*model.EmailSendingPlanView, error) {
	view, err := s.GetPlanAsync(ctx, planID)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, fmt.Errorf("email sending plan %s was not found", planID)
	}
	return view, nil
}

func (s *PlanService) resolveTargetAccounts(ctx context.Context, command CreateEmailSendingPlanCommand) ([]targetAccount, error) {
	if command.BroadcastToAll {
		if s.accounts == nil {
			return nil, errors.New("account service is not configured")
		}
		var results []targetAccount
		pageToken := ""
		for {
			resp, err := s.accounts.ListAccounts(ctx, &gen.DyListAccountsRequest{PageSize: 500, PageToken: pageToken})
			if err != nil {
				return nil, err
			}
			if resp != nil {
				for _, account := range resp.Accounts {
					if t, ok := toTargetAccount(account); ok {
						results = append(results, t)
					}
				}
				pageToken = resp.NextPageToken
			} else {
				pageToken = ""
			}
			if pageToken == "" {
				break
			}
		}
		return dedupeAccounts(results), nil
	}

	requested := map[uuid.UUID]struct{}{}
	if command.AccountId != nil {
		requested[*command.AccountId] = struct{}{}
	}
	for _, id := range command.AccountIds {
		requested[id] = struct{}{}
	}
	if len(requested) == 0 {
		return nil, nil
	}
	if s.accounts == nil {
		return nil, errors.New("account service is not configured")
	}

	ids := make([]string, 0, len(requested))
	for id := range requested {
		ids = append(ids, id.String())
	}
	resp, err := s.accounts.GetAccountBatch(ctx, &gen.DyGetAccountBatchRequest{Id: ids})
	if err != nil {
		return nil, err
	}
	var results []targetAccount
	if resp != nil {
		for _, account := range resp.Accounts {
			if t, ok := toTargetAccount(account); ok {
				results = append(results, t)
			}
		}
	}
	return dedupeAccounts(results), nil
}

// toTargetAccount mirrors ToTargetAccount: recipientName = Nick || Name.
func toTargetAccount(account *gen.DyAccount) (targetAccount, bool) {
	if account == nil || account.Id == "" {
		return targetAccount{}, false
	}
	id, err := uuid.Parse(account.Id)
	if err != nil {
		return targetAccount{}, false
	}
	name := account.Name
	if account.Nick != "" {
		name = account.Nick
	}
	return targetAccount{accountID: id, recipientName: name}, true
}

func dedupeAccounts(accounts []targetAccount) []targetAccount {
	seen := map[uuid.UUID]struct{}{}
	var out []targetAccount
	for _, a := range accounts {
		if _, ok := seen[a.accountID]; ok {
			continue
		}
		seen[a.accountID] = struct{}{}
		out = append(out, a)
	}
	return out
}

// modelContact is the subset of SnAccountContact the advance loop reads.
type modelContact struct {
	Content   string
	IsPrimary bool
	VerifiedAt *time.Time
}

func verifiedAfter(a, b *modelContact) bool {
	if a.VerifiedAt == nil {
		return false
	}
	if b.VerifiedAt == nil {
		return true
	}
	return a.VerifiedAt.After(*b.VerifiedAt)
}

// listContacts mirrors RemoteAccountContactService.ListContactsAsync with
// verifiedOnly: true; failures degrade to an empty list (the C# logs and
// returns []).
func (s *PlanService) listContacts(ctx context.Context, accountID uuid.UUID) ([]*modelContact, error) {
	if s.accounts == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	resp, err := s.accounts.ListContacts(ctx, &gen.DyListContactsRequest{
		AccountId:    accountID.String(),
		Type:         gen.DyAccountContactType_DY_EMAIL,
		VerifiedOnly: true,
	})
	if err != nil {
		s.log.Warn("failed to fetch account contacts", "account_id", accountID, "error", err)
		return nil, nil
	}
	var out []*modelContact
	for _, contact := range resp.Contacts {
		c := &modelContact{Content: contact.Content, IsPrimary: contact.IsPrimary}
		if contact.VerifiedAt != nil {
			t := contact.VerifiedAt.AsTime()
			c.VerifiedAt = &t
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *PlanService) buildPlanViews(ctx context.Context, plans []*model.EmailSendingPlan, includeAdvances bool, advanceTake int) ([]*model.EmailSendingPlanView, error) {
	if len(plans) == 0 {
		return nil, nil
	}
	planIDs := make([]uuid.UUID, 0, len(plans))
	for _, plan := range plans {
		planIDs = append(planIDs, plan.Id)
	}

	countLookup, err := s.st.CountRecipientsByStatus(ctx, planIDs)
	if err != nil {
		return nil, err
	}

	advanceLookup := map[uuid.UUID][]*model.EmailSendingPlanAdvance{}
	if includeAdvances {
		advances, err := s.st.ListAdvancesByPlans(ctx, planIDs)
		if err != nil {
			return nil, err
		}
		for _, a := range advances {
			if len(advanceLookup[a.PlanId]) >= advanceTake {
				continue
			}
			advanceLookup[a.PlanId] = append(advanceLookup[a.PlanId], a)
		}
	}

	views := make([]*model.EmailSendingPlanView, 0, len(plans))
	for _, plan := range plans {
		counts := countLookup[plan.Id]
		view := &model.EmailSendingPlanView{
			Id:                    plan.Id,
			SendingPlanKey:        plan.SendingPlanKey,
			CreatedByAccountId:    plan.CreatedByAccountId,
			Subject:               plan.Subject,
			BroadcastToAll:        plan.BroadcastToAll,
			RecipientCount:        plan.RecipientCount,
			MaxEmailsPerInterval:  plan.MaxEmailsPerInterval,
			IntervalMinutes:       plan.IntervalMinutes,
			MaxEmailsPerDay:       plan.MaxEmailsPerDay,
			Status:                plan.Status,
			AdvancedIntervalsCount: plan.AdvancedIntervalsCount,
			PlannedStartAt:        plan.PlannedStartAt,
			NextIntervalAt:        plan.NextIntervalAt,
			LastAdvancedAt:        plan.LastAdvancedAt,
			PausedAt:              plan.PausedAt,
			CompletedAt:           plan.CompletedAt,
			Counts:                counts,
			Advances:              []model.EmailSendingPlanAdvanceView{},
		}
		if includeAdvances {
			for _, a := range advanceLookup[plan.Id] {
				view.Advances = append(view.Advances, model.EmailSendingPlanAdvanceView{
					Id:                a.Id,
					IntervalNumber:    a.IntervalNumber,
					IsManual:          a.IsManual,
					AttemptedCount:    a.AttemptedCount,
					SentCount:         a.SentCount,
					SkippedCount:      a.SkippedCount,
					FailedCount:       a.FailedCount,
					PendingCountAfter: a.PendingCountAfter,
					StartedAt:         a.StartedAt,
					CompletedAt:       a.CompletedAt,
				})
			}
		}
		views = append(views, view)
	}
	return views, nil
}

func (s *PlanService) getRemainingDailyCapacity(ctx context.Context, plan *model.EmailSendingPlan, now time.Time) (*int, error) {
	if plan.MaxEmailsPerDay == nil {
		return nil, nil
	}
	startOfDay := getStartOfUtcDay(now)
	endOfDay := getStartOfNextUtcDay(now)
	attemptedToday, err := s.st.SumAttemptedToday(ctx, plan.Id, startOfDay, endOfDay)
	if err != nil {
		return nil, err
	}
	remaining := *plan.MaxEmailsPerDay - attemptedToday
	if remaining < 0 {
		remaining = 0
	}
	return &remaining, nil
}

func getStartOfUtcDay(t time.Time) time.Time {
	utc := t.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
}

func getStartOfNextUtcDay(t time.Time) time.Time {
	return getStartOfUtcDay(t).Add(24 * time.Hour)
}

func truncate(value string, maxLength int) string {
	if len(value) <= maxLength {
		return value
	}
	return value[:maxLength]
}

// emailSend dispatches to the SMTP sender, degrading to an error when the
// email service is not configured (dependency-down behavior).
func (s *PlanService) emailSend(ctx context.Context, recipient *model.EmailSendingPlanRecipient, toAddress, subject, htmlBody string) error {
	if s.email == nil {
		return errors.New("Email service is not configured.")
	}
	return s.email.SendEmail(ctx, recipientName(recipient), toAddress, subject, htmlBody, "sending_plan")
}

// recipientName mirrors the C# nullable recipient name passed to
// MailboxAddress.
func recipientName(r *model.EmailSendingPlanRecipient) string {
	if r == nil || r.RecipientNameSnapshot == nil {
		return ""
	}
	return *r.RecipientNameSnapshot
}
