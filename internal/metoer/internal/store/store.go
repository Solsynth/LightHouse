// Package store holds the Postgres access layer for the dyson_ring schema.
// Every query mirrors the EF Core behavior of DysonNetwork.Ring: the global
// soft-delete filter (deleted_at IS NULL) applies to all reads and to
// ExecuteUpdate/ExecuteDelete statements, exactly like AppDatabase's
// ApplySoftDeleteFilters.
package store

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/metoer/internal/model"
	"src.solsynth.dev/sosys/go/pkg/models"
)

// Store wraps the shared connection pool.
type Store struct {
	pool *pgxpool.Pool
}

// ErrNotFound is returned when a row lookup misses (pgx.ErrNoRows).
var ErrNotFound = pgx.ErrNoRows

// New creates a Store over the pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Pool exposes the underlying pool (used by jobs and scoped services).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

const notificationColumns = `id, account_id, app_id, content, created_at, deleted_at, meta, priority, push_type, subtitle, title, topic, updated_at, viewed_at`

const subscriptionColumns = `id, account_id, app_id, count_delivered, created_at, deleted_at, device_id, device_name, device_token, is_activated, last_used_at, provider, updated_at`

const preferenceColumns = `id, account_id, created_at, deleted_at, preference, topic, updated_at`

const emailPlanColumns = `id, sending_plan_key, created_by_account_id, subject, html_body, broadcast_to_all, recipient_count, max_emails_per_interval, interval_minutes, max_emails_per_day, status, advanced_intervals_count, planned_start_at, next_interval_at, last_advanced_at, paused_at, completed_at, created_at, updated_at, deleted_at`

const recipientColumns = `id, plan_id, account_id, recipient_name_snapshot, status, attempt_count, last_interval_number, last_resolved_email, last_error, processed_at, created_at, updated_at, deleted_at`

const advanceColumns = `id, plan_id, interval_number, is_manual, attempted_count, sent_count, skipped_count, failed_count, pending_count_after, started_at, completed_at, created_at, updated_at, deleted_at`

// scanNotification scans a notifications row (notificationColumns order).
func scanNotification(row pgx.Row) (*model.Notification, error) {
	var n model.Notification
	var id, accountId uuid.UUID
	var appId, pushType, title, subtitle, content *string
	var deletedAt, viewedAt *models.Time
	var createdAt, updatedAt models.Time
	var metaRaw []byte
	var priority int
	var topic string
	if err := row.Scan(&id, &accountId, &appId, &content, &createdAt, &deletedAt, &metaRaw, &priority, &pushType, &subtitle, &title, &topic, &updatedAt, &viewedAt); err != nil {
		return nil, err
	}
	meta := map[string]any{}
	if len(metaRaw) > 0 {
		if err := json.Unmarshal(metaRaw, &meta); err != nil {
			return nil, fmt.Errorf("parse notification meta: %w", err)
		}
	}
	n = model.Notification{
		ModelBase: model.ModelBase{Id: id, CreatedAt: createdAt, UpdatedAt: updatedAt, DeletedAt: deletedAt},
		Topic:     topic,
		Title:     title,
		Subtitle:  subtitle,
		Content:   content,
		Meta:      meta,
		Priority:  priority,
		ViewedAt:  viewedAt,
		AppId:     appId,
		PushType:  pushType,
		AccountId: accountId,
	}
	return &n, nil
}

func scanSubscription(row pgx.Row) (*model.PushSubscription, error) {
	var s model.PushSubscription
	var id, accountId uuid.UUID
	var appId, deviceName *string
	var createdAt, updatedAt, lastUsedAt models.Time
	var deletedAt *models.Time
	var deviceId, deviceToken string
	var isActivated bool
	var countDelivered int
	var provider int
	if err := row.Scan(&id, &accountId, &appId, &countDelivered, &createdAt, &deletedAt, &deviceId, &deviceName, &deviceToken, &isActivated, &lastUsedAt, &provider, &updatedAt); err != nil {
		return nil, err
	}
	s = model.PushSubscription{
		ModelBase:      model.ModelBase{Id: id, CreatedAt: createdAt, UpdatedAt: updatedAt, DeletedAt: deletedAt},
		AccountId:      accountId,
		DeviceId:       deviceId,
		DeviceToken:    deviceToken,
		DeviceName:     deviceName,
		Provider:       model.PushProvider(provider),
		IsActivated:    isActivated,
		AppId:          appId,
		CountDelivered: countDelivered,
		LastUsedAt:     &lastUsedAt,
	}
	return &s, nil
}

func scanPreference(row pgx.Row) (*model.NotificationPreference, error) {
	var p model.NotificationPreference
	var id, accountId uuid.UUID
	var createdAt, updatedAt models.Time
	var deletedAt *models.Time
	var preference int
	var topic string
	if err := row.Scan(&id, &accountId, &createdAt, &deletedAt, &preference, &topic, &updatedAt); err != nil {
		return nil, err
	}
	p = model.NotificationPreference{
		ModelBase:  model.ModelBase{Id: id, CreatedAt: createdAt, UpdatedAt: updatedAt, DeletedAt: deletedAt},
		AccountId:  accountId,
		Topic:      topic,
		Preference: model.NotificationPreferenceLevel(preference),
	}
	return &p, nil
}

func scanEmailPlan(row pgx.Row) (*model.EmailSendingPlan, error) {
	var p model.EmailSendingPlan
	var id, createdBy uuid.UUID
	var sendingPlanKey, subject, htmlBody *string
	var maxEmailsPerDay *int
	var plannedStartAt models.Time
	var nextIntervalAt, lastAdvancedAt, pausedAt, completedAt *models.Time
	var createdAt, updatedAt models.Time
	var deletedAt *models.Time
	var broadcastToAll bool
	var recipientCount, maxPerInterval, intervalMinutes, status, advancedIntervals int
	if err := row.Scan(&id, &sendingPlanKey, &createdBy, &subject, &htmlBody, &broadcastToAll, &recipientCount, &maxPerInterval, &intervalMinutes, &maxEmailsPerDay, &status, &advancedIntervals, &plannedStartAt, &nextIntervalAt, &lastAdvancedAt, &pausedAt, &completedAt, &createdAt, &updatedAt, &deletedAt); err != nil {
		return nil, err
	}
	subj, body := "", ""
	if subject != nil {
		subj = *subject
	}
	if htmlBody != nil {
		body = *htmlBody
	}
	p = model.EmailSendingPlan{
		ModelBase:             model.ModelBase{Id: id, CreatedAt: createdAt, UpdatedAt: updatedAt, DeletedAt: deletedAt},
		SendingPlanKey:        sendingPlanKey,
		CreatedByAccountId:    createdBy,
		Subject:               subj,
		HtmlBody:              body,
		BroadcastToAll:        broadcastToAll,
		RecipientCount:        recipientCount,
		MaxEmailsPerInterval:  maxPerInterval,
		IntervalMinutes:       intervalMinutes,
		MaxEmailsPerDay:       maxEmailsPerDay,
		Status:                model.EmailSendingPlanStatus(status),
		AdvancedIntervalsCount: advancedIntervals,
		PlannedStartAt:        plannedStartAt,
		NextIntervalAt:        nextIntervalAt,
		LastAdvancedAt:        lastAdvancedAt,
		PausedAt:              pausedAt,
		CompletedAt:           completedAt,
	}
	return &p, nil
}

func scanRecipient(row pgx.Row) (*model.EmailSendingPlanRecipient, error) {
	var r model.EmailSendingPlanRecipient
	var id, planId, accountId uuid.UUID
	var nameSnapshot, lastResolvedEmail, lastError *string
	var status, attemptCount int
	var lastIntervalNumber *int
	var processedAt *models.Time
	var createdAt, updatedAt models.Time
	var deletedAt *models.Time
	if err := row.Scan(&id, &planId, &accountId, &nameSnapshot, &status, &attemptCount, &lastIntervalNumber, &lastResolvedEmail, &lastError, &processedAt, &createdAt, &updatedAt, &deletedAt); err != nil {
		return nil, err
	}
	r = model.EmailSendingPlanRecipient{
		ModelBase:            model.ModelBase{Id: id, CreatedAt: createdAt, UpdatedAt: updatedAt, DeletedAt: deletedAt},
		PlanId:               planId,
		AccountId:            accountId,
		RecipientNameSnapshot: nameSnapshot,
		Status:               model.EmailSendingPlanRecipientStatus(status),
		AttemptCount:         attemptCount,
		LastIntervalNumber:   lastIntervalNumber,
		LastResolvedEmail:    lastResolvedEmail,
		LastError:            lastError,
		ProcessedAt:          processedAt,
	}
	return &r, nil
}

func scanAdvance(row pgx.Row) (*model.EmailSendingPlanAdvance, error) {
	var a model.EmailSendingPlanAdvance
	var id, planId uuid.UUID
	var intervalNumber, attempted, sent, skipped, failed, pending int
	var isManual bool
	var startedAt, completedAt, createdAt, updatedAt models.Time
	var deletedAt *models.Time
	if err := row.Scan(&id, &planId, &intervalNumber, &isManual, &attempted, &sent, &skipped, &failed, &pending, &startedAt, &completedAt, &createdAt, &updatedAt, &deletedAt); err != nil {
		return nil, err
	}
	a = model.EmailSendingPlanAdvance{
		ModelBase:         model.ModelBase{Id: id, CreatedAt: createdAt, UpdatedAt: updatedAt, DeletedAt: deletedAt},
		PlanId:            planId,
		IntervalNumber:    intervalNumber,
		IsManual:          isManual,
		AttemptedCount:    attempted,
		SentCount:         sent,
		SkippedCount:      skipped,
		FailedCount:       failed,
		PendingCountAfter: pending,
		StartedAt:         startedAt,
		CompletedAt:       completedAt,
	}
	return &a, nil
}

// buildWhere joins non-empty clauses with AND and appends args.
func buildWhere(clauses []string, args []any) string {
	var parts []string
	for _, c := range clauses {
		if c != "" {
			parts = append(parts, c)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(parts, " AND ")
}
