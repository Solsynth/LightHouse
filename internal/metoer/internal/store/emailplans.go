package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/metoer/internal/model"
)

func (s *Store) CreateEmailPlan(ctx context.Context, plan *model.EmailSendingPlan, recipients []*model.EmailSendingPlanRecipient) error {
	return s.db(ctx).Transaction(func(tx *gorm.DB) error {
		entity := planEntityFromModel(plan)
		if err := tx.Create(&entity).Error; err != nil {
			return err
		}
		for _, recipient := range recipients {
			item := recipientEntityFromModel(recipient)
			if err := tx.Create(&item).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) GetEmailPlan(ctx context.Context, planID uuid.UUID) (*model.EmailSendingPlan, error) {
	var entity EmailSendingPlanEntity
	err := s.db(ctx).Where("id = ?", planID).First(&entity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return planFromEntity(&entity), nil
}

func (s *Store) ListEmailPlans(ctx context.Context, offset, take int, status *model.EmailSendingPlanStatus) ([]*model.EmailSendingPlan, int, error) {
	query := s.db(ctx).Model(&EmailSendingPlanEntity{})
	if status != nil {
		query = query.Where("status = ?", int(*status))
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var entities []EmailSendingPlanEntity
	if err := query.Order("created_at DESC").Offset(offset).Limit(take).Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	items := make([]*model.EmailSendingPlan, 0, len(entities))
	for i := range entities {
		items = append(items, planFromEntity(&entities[i]))
	}
	return items, int(total), nil
}

type recipientStatusCount struct {
	PlanID uuid.UUID
	Status int
	Count  int64
}

func (s *Store) CountRecipientsByStatus(ctx context.Context, planIDs []uuid.UUID) (map[uuid.UUID]model.EmailSendingPlanCounts, error) {
	counts := map[uuid.UUID]model.EmailSendingPlanCounts{}
	if len(planIDs) == 0 {
		return counts, nil
	}
	var rows []recipientStatusCount
	err := s.db(ctx).Model(&EmailSendingPlanRecipientEntity{}).Select("plan_id, status, count(*) AS count").Where("plan_id IN ?", planIDs).Group("plan_id, status").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		value := counts[row.PlanID]
		value.Total += int(row.Count)
		switch model.EmailSendingPlanRecipientStatus(row.Status) {
		case model.EmailSendingPlanRecipientPending:
			value.Pending += int(row.Count)
		case model.EmailSendingPlanRecipientSent:
			value.Sent += int(row.Count)
		case model.EmailSendingPlanRecipientSkipped:
			value.Skipped += int(row.Count)
		case model.EmailSendingPlanRecipientFailed:
			value.Failed += int(row.Count)
		}
		counts[row.PlanID] = value
	}
	return counts, nil
}

func (s *Store) ListAdvancesByPlans(ctx context.Context, planIDs []uuid.UUID) ([]*model.EmailSendingPlanAdvance, error) {
	if len(planIDs) == 0 {
		return nil, nil
	}
	var entities []EmailSendingPlanAdvanceEntity
	if err := s.db(ctx).Where("plan_id IN ?", planIDs).Order("interval_number DESC").Find(&entities).Error; err != nil {
		return nil, err
	}
	items := make([]*model.EmailSendingPlanAdvance, 0, len(entities))
	for i := range entities {
		items = append(items, advanceFromEntity(&entities[i]))
	}
	return items, nil
}

func (s *Store) UpdateEmailPlanStatus(ctx context.Context, plan *model.EmailSendingPlan) error {
	return s.db(ctx).Model(&EmailSendingPlanEntity{}).Where("id = ?", plan.Id).Updates(map[string]any{
		"status": int(plan.Status), "advanced_intervals_count": plan.AdvancedIntervalsCount,
		"next_interval_at": timePtr(plan.NextIntervalAt), "last_advanced_at": timePtr(plan.LastAdvancedAt),
		"paused_at": timePtr(plan.PausedAt), "completed_at": timePtr(plan.CompletedAt),
		"updated_at": time.Time(plan.UpdatedAt),
	}).Error
}

func (s *Store) CountPendingRecipients(ctx context.Context, planID uuid.UUID) (int, error) {
	var count int64
	err := s.db(ctx).Model(&EmailSendingPlanRecipientEntity{}).Where("plan_id = ? AND status = ?", planID, int(model.EmailSendingPlanRecipientPending)).Count(&count).Error
	return int(count), err
}

func (s *Store) ListPendingRecipients(ctx context.Context, planID uuid.UUID) ([]*model.EmailSendingPlanRecipient, error) {
	var entities []EmailSendingPlanRecipientEntity
	if err := s.db(ctx).Where("plan_id = ? AND status = ?", planID, int(model.EmailSendingPlanRecipientPending)).Order("created_at, id").Find(&entities).Error; err != nil {
		return nil, err
	}
	items := make([]*model.EmailSendingPlanRecipient, 0, len(entities))
	for i := range entities {
		items = append(items, recipientFromEntity(&entities[i]))
	}
	return items, nil
}

func (s *Store) UpdateRecipient(ctx context.Context, recipient *model.EmailSendingPlanRecipient) error {
	return s.db(ctx).Model(&EmailSendingPlanRecipientEntity{}).Where("id = ?", recipient.Id).Updates(map[string]any{
		"status": int(recipient.Status), "attempt_count": recipient.AttemptCount, "last_interval_number": recipient.LastIntervalNumber,
		"last_resolved_email": recipient.LastResolvedEmail, "last_error": recipient.LastError, "processed_at": timePtr(recipient.ProcessedAt),
		"updated_at": time.Time(recipient.UpdatedAt),
	}).Error
}

func (s *Store) InsertAdvance(ctx context.Context, advance *model.EmailSendingPlanAdvance) error {
	entity := advanceEntityFromModel(advance)
	return s.db(ctx).Create(&entity).Error
}

func (s *Store) SumAttemptedToday(ctx context.Context, planID uuid.UUID, startOfDay, endOfDay time.Time) (int, error) {
	var sum int64
	err := s.db(ctx).Model(&EmailSendingPlanAdvanceEntity{}).Select("COALESCE(SUM(attempted_count), 0)").Where("plan_id = ? AND started_at >= ? AND started_at < ?", planID, startOfDay.UTC(), endOfDay.UTC()).Scan(&sum).Error
	return int(sum), err
}

func (s *Store) ListDuePlans(ctx context.Context, now time.Time) ([]*model.EmailSendingPlan, error) {
	var entities []EmailSendingPlanEntity
	if err := s.db(ctx).Where("status = ? AND next_interval_at IS NOT NULL AND next_interval_at <= ?", int(model.EmailSendingPlanScheduled), now.UTC()).Order("next_interval_at").Find(&entities).Error; err != nil {
		return nil, err
	}
	items := make([]*model.EmailSendingPlan, 0, len(entities))
	for i := range entities {
		items = append(items, planFromEntity(&entities[i]))
	}
	return items, nil
}
