package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/metoer/internal/model"
	"src.solsynth.dev/sosys/go/pkg/models"
)

// CreateEmailPlan inserts the plan and its recipients in one transaction
// (CreatePlanAsync's SaveChanges).
func (s *Store) CreateEmailPlan(ctx context.Context, plan *model.EmailSendingPlan, recipients []*model.EmailSendingPlanRecipient) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `INSERT INTO email_sending_plans
		(id, sending_plan_key, created_by_account_id, subject, html_body, broadcast_to_all, recipient_count,
		 max_emails_per_interval, interval_minutes, max_emails_per_day, status, advanced_intervals_count,
		 planned_start_at, next_interval_at, last_advanced_at, paused_at, completed_at, created_at, updated_at, deleted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $18, $19)`,
		plan.Id, plan.SendingPlanKey, plan.CreatedByAccountId, plan.Subject, plan.HtmlBody, plan.BroadcastToAll,
		plan.RecipientCount, plan.MaxEmailsPerInterval, plan.IntervalMinutes, plan.MaxEmailsPerDay,
		int(plan.Status), plan.AdvancedIntervalsCount, plan.PlannedStartAt, plan.NextIntervalAt,
		plan.LastAdvancedAt, plan.PausedAt, plan.CompletedAt, plan.CreatedAt, plan.DeletedAt)
	if err != nil {
		return err
	}

	for _, r := range recipients {
		_, err = tx.Exec(ctx, `INSERT INTO email_sending_plan_recipients
			(id, plan_id, account_id, recipient_name_snapshot, status, attempt_count, last_interval_number,
			 last_resolved_email, last_error, processed_at, created_at, updated_at, deleted_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11, $12)`,
			r.Id, r.PlanId, r.AccountId, r.RecipientNameSnapshot, int(r.Status), r.AttemptCount,
			r.LastIntervalNumber, r.LastResolvedEmail, r.LastError, r.ProcessedAt, r.CreatedAt, r.DeletedAt)
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// GetEmailPlan loads one plan (nil when missing; the global soft-delete
// filter applies).
func (s *Store) GetEmailPlan(ctx context.Context, planID uuid.UUID) (*model.EmailSendingPlan, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+emailPlanColumns+` FROM email_sending_plans WHERE id = $1 AND deleted_at IS NULL`, planID)
	plan, err := scanEmailPlan(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return plan, nil
}

// ListEmailPlans returns one page of plans (created_at DESC) plus the total
// count, with an optional status filter (ListPlansAsync).
func (s *Store) ListEmailPlans(ctx context.Context, offset, take int, status *model.EmailSendingPlanStatus) ([]*model.EmailSendingPlan, int, error) {
	where := `WHERE deleted_at IS NULL`
	args := []any{}
	if status != nil {
		where += ` AND status = $1`
		args = append(args, int(*status))
	}
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM email_sending_plans `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	pageArgs := append(append([]any{}, args...), offset, take)
	query := `SELECT ` + emailPlanColumns + ` FROM email_sending_plans ` + where + ` ORDER BY created_at DESC OFFSET $`
	query += fmt.Sprint(len(args) + 1) + ` LIMIT $` + fmt.Sprint(len(args) + 2)
	rows, err := s.pool.Query(ctx, query, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var items []*model.EmailSendingPlan
	for rows.Next() {
		plan, err := scanEmailPlan(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, plan)
	}
	return items, total, rows.Err()
}

// CountRecipientsByStatus groups recipient counts per plan by status
// (BuildPlanViewsAsync's countRows).
func (s *Store) CountRecipientsByStatus(ctx context.Context, planIDs []uuid.UUID) (map[uuid.UUID]model.EmailSendingPlanCounts, error) {
	if len(planIDs) == 0 {
		return map[uuid.UUID]model.EmailSendingPlanCounts{}, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT plan_id, status, count(*) FROM email_sending_plan_recipients
		 WHERE plan_id = ANY($1) AND deleted_at IS NULL GROUP BY plan_id, status`, planIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[uuid.UUID]model.EmailSendingPlanCounts{}
	for rows.Next() {
		var planID uuid.UUID
		var status, count int
		if err := rows.Scan(&planID, &status, &count); err != nil {
			return nil, err
		}
		c := counts[planID]
		c.Total += count
		switch model.EmailSendingPlanRecipientStatus(status) {
		case model.EmailSendingPlanRecipientPending:
			c.Pending += count
		case model.EmailSendingPlanRecipientSent:
			c.Sent += count
		case model.EmailSendingPlanRecipientSkipped:
			c.Skipped += count
		case model.EmailSendingPlanRecipientFailed:
			c.Failed += count
		}
		counts[planID] = c
	}
	return counts, rows.Err()
}

// ListAdvancesByPlans loads advances for the plans, newest interval first
// (BuildPlanViewsAsync's advances fetch).
func (s *Store) ListAdvancesByPlans(ctx context.Context, planIDs []uuid.UUID) ([]*model.EmailSendingPlanAdvance, error) {
	if len(planIDs) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+advanceColumns+` FROM email_sending_plan_advances
		 WHERE plan_id = ANY($1) AND deleted_at IS NULL ORDER BY interval_number DESC`, planIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []*model.EmailSendingPlanAdvance
	for rows.Next() {
		a, err := scanAdvance(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	return items, rows.Err()
}

// UpdateEmailPlanStatus writes the plan's status transitions (pause/resume/
// advance completion). Only non-deleted rows are updated.
func (s *Store) UpdateEmailPlanStatus(ctx context.Context, plan *model.EmailSendingPlan) error {
	_, err := s.pool.Exec(ctx, `UPDATE email_sending_plans SET
		status = $1, advanced_intervals_count = $2, next_interval_at = $3, last_advanced_at = $4,
		paused_at = $5, completed_at = $6, updated_at = $7
		WHERE id = $8 AND deleted_at IS NULL`,
		int(plan.Status), plan.AdvancedIntervalsCount, plan.NextIntervalAt, plan.LastAdvancedAt,
		plan.PausedAt, plan.CompletedAt, plan.UpdatedAt, plan.Id)
	return err
}

// CountPendingRecipients counts pending recipients of a plan.
func (s *Store) CountPendingRecipients(ctx context.Context, planID uuid.UUID) (int, error) {
	var count int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM email_sending_plan_recipients WHERE plan_id = $1 AND status = 0 AND deleted_at IS NULL`, planID).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// ListPendingRecipients loads pending recipients ordered by created_at, id
// (the advance loop's recipient fetch).
func (s *Store) ListPendingRecipients(ctx context.Context, planID uuid.UUID) ([]*model.EmailSendingPlanRecipient, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+recipientColumns+` FROM email_sending_plan_recipients
		 WHERE plan_id = $1 AND status = 0 AND deleted_at IS NULL ORDER BY created_at, id`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []*model.EmailSendingPlanRecipient
	for rows.Next() {
		r, err := scanRecipient(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

// UpdateRecipient writes the advance-loop recipient state.
func (s *Store) UpdateRecipient(ctx context.Context, r *model.EmailSendingPlanRecipient) error {
	_, err := s.pool.Exec(ctx, `UPDATE email_sending_plan_recipients SET
		status = $1, attempt_count = $2, last_interval_number = $3, last_resolved_email = $4,
		last_error = $5, processed_at = $6, updated_at = $7
		WHERE id = $8 AND deleted_at IS NULL`,
		int(r.Status), r.AttemptCount, r.LastIntervalNumber, r.LastResolvedEmail,
		r.LastError, r.ProcessedAt, r.UpdatedAt, r.Id)
	return err
}

// InsertAdvance inserts an email_sending_plan_advances row.
func (s *Store) InsertAdvance(ctx context.Context, a *model.EmailSendingPlanAdvance) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO email_sending_plan_advances
		(id, plan_id, interval_number, is_manual, attempted_count, sent_count, skipped_count, failed_count,
		 pending_count_after, started_at, completed_at, created_at, updated_at, deleted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12, $13)`,
		a.Id, a.PlanId, a.IntervalNumber, a.IsManual, a.AttemptedCount, a.SentCount, a.SkippedCount,
		a.FailedCount, a.PendingCountAfter, a.StartedAt, a.CompletedAt, a.CreatedAt, a.DeletedAt)
	return err
}

// SumAttemptedToday sums attempted_count of advances started in the current
// UTC day (GetRemainingDailyCapacityAsync).
func (s *Store) SumAttemptedToday(ctx context.Context, planID uuid.UUID, startOfDay, endOfDay time.Time) (int, error) {
	var sum int
	if err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(sum(attempted_count), 0) FROM email_sending_plan_advances
		 WHERE plan_id = $1 AND started_at >= $2 AND started_at < $3 AND deleted_at IS NULL`,
		planID, models.Time(startOfDay.UTC()), models.Time(endOfDay.UTC())).Scan(&sum); err != nil {
		return 0, err
	}
	return sum, nil
}

// ListDuePlans loads scheduled plans whose next_interval_at <= now, ordered
// by next_interval_at (AdvanceDuePlansAsync).
func (s *Store) ListDuePlans(ctx context.Context, now time.Time) ([]*model.EmailSendingPlan, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+emailPlanColumns+` FROM email_sending_plans
		 WHERE status = 0 AND next_interval_at IS NOT NULL AND next_interval_at <= $1 AND deleted_at IS NULL
		 ORDER BY next_interval_at`, models.Time(now.UTC()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []*model.EmailSendingPlan
	for rows.Next() {
		plan, err := scanEmailPlan(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, plan)
	}
	return items, rows.Err()
}
