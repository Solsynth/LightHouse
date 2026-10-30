package model

import (
	"strings"
	"unicode"

	"github.com/google/uuid"
	"src.solsynth.dev/sosys/go/pkg/models"
)

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// EmailSendingPlanStatus mirrors SnEmailSendingPlan.Status.
type EmailSendingPlanStatus int

const (
	EmailSendingPlanScheduled EmailSendingPlanStatus = iota // 0
	EmailSendingPlanPaused                                  // 1
	EmailSendingPlanCompleted                               // 2
)

// EmailSendingPlanRecipientStatus mirrors SnEmailSendingPlanRecipient.Status.
type EmailSendingPlanRecipientStatus int

const (
	EmailSendingPlanRecipientPending EmailSendingPlanRecipientStatus = iota // 0
	EmailSendingPlanRecipientSent                                          // 1
	EmailSendingPlanRecipientSkipped                                       // 2
	EmailSendingPlanRecipientFailed                                        // 3
)

// EmailSendingPlan mirrors SnEmailSendingPlan.
type EmailSendingPlan struct {
	ModelBase
	SendingPlanKey       *string               `json:"sending_plan_key"`
	CreatedByAccountId   uuid.UUID             `json:"created_by_account_id"`
	Subject              string                `json:"subject"`
	HtmlBody             string                `json:"html_body"`
	BroadcastToAll       bool                  `json:"broadcast_to_all"`
	RecipientCount       int                   `json:"recipient_count"`
	MaxEmailsPerInterval int                   `json:"max_emails_per_interval"`
	IntervalMinutes      int                   `json:"interval_minutes"`
	MaxEmailsPerDay      *int                  `json:"max_emails_per_day"`
	Status               EmailSendingPlanStatus `json:"status"`
	AdvancedIntervalsCount int                 `json:"advanced_intervals_count"`
	PlannedStartAt       models.Time           `json:"planned_start_at"`
	NextIntervalAt       *models.Time          `json:"next_interval_at"`
	LastAdvancedAt       *models.Time          `json:"last_advanced_at"`
	PausedAt             *models.Time          `json:"paused_at"`
	CompletedAt          *models.Time          `json:"completed_at"`
}

// EmailSendingPlanRecipient mirrors SnEmailSendingPlanRecipient.
type EmailSendingPlanRecipient struct {
	ModelBase
	PlanId               uuid.UUID                       `json:"plan_id"`
	AccountId            uuid.UUID                       `json:"account_id"`
	RecipientNameSnapshot *string                        `json:"recipient_name_snapshot"`
	Status               EmailSendingPlanRecipientStatus `json:"status"`
	AttemptCount         int                             `json:"attempt_count"`
	LastIntervalNumber   *int                            `json:"last_interval_number"`
	LastResolvedEmail    *string                         `json:"last_resolved_email"`
	LastError            *string                         `json:"last_error"`
	ProcessedAt          *models.Time                    `json:"processed_at"`
}

// EmailSendingPlanAdvance mirrors SnEmailSendingPlanAdvance.
type EmailSendingPlanAdvance struct {
	ModelBase
	PlanId            uuid.UUID `json:"plan_id"`
	IntervalNumber    int       `json:"interval_number"`
	IsManual          bool      `json:"is_manual"`
	AttemptedCount    int       `json:"attempted_count"`
	SentCount         int       `json:"sent_count"`
	SkippedCount      int       `json:"skipped_count"`
	FailedCount       int       `json:"failed_count"`
	PendingCountAfter int       `json:"pending_count_after"`
	StartedAt         models.Time `json:"started_at"`
	CompletedAt       models.Time `json:"completed_at"`
}

// EmailSendingPlanCounts mirrors EmailSendingPlanService.EmailSendingPlanCounts.
type EmailSendingPlanCounts struct {
	Total   int `json:"total"`
	Pending int `json:"pending"`
	Sent    int `json:"sent"`
	Skipped int `json:"skipped"`
	Failed  int `json:"failed"`
}

// EmailSendingPlanAdvanceView mirrors EmailSendingPlanService.EmailSendingPlanAdvanceView.
type EmailSendingPlanAdvanceView struct {
	Id                uuid.UUID   `json:"id"`
	IntervalNumber    int         `json:"interval_number"`
	IsManual          bool        `json:"is_manual"`
	AttemptedCount    int         `json:"attempted_count"`
	SentCount         int         `json:"sent_count"`
	SkippedCount      int         `json:"skipped_count"`
	FailedCount       int         `json:"failed_count"`
	PendingCountAfter int         `json:"pending_count_after"`
	StartedAt         models.Time `json:"started_at"`
	CompletedAt       models.Time `json:"completed_at"`
}

// EmailSendingPlanView mirrors EmailSendingPlanService.EmailSendingPlanView.
type EmailSendingPlanView struct {
	Id                    uuid.UUID                `json:"id"`
	SendingPlanKey        *string                  `json:"sending_plan_key"`
	CreatedByAccountId    uuid.UUID                `json:"created_by_account_id"`
	Subject               string                   `json:"subject"`
	BroadcastToAll        bool                     `json:"broadcast_to_all"`
	RecipientCount        int                      `json:"recipient_count"`
	MaxEmailsPerInterval  int                      `json:"max_emails_per_interval"`
	IntervalMinutes       int                      `json:"interval_minutes"`
	MaxEmailsPerDay       *int                     `json:"max_emails_per_day"`
	Status                EmailSendingPlanStatus   `json:"status"`
	AdvancedIntervalsCount int                     `json:"advanced_intervals_count"`
	PlannedStartAt        models.Time              `json:"planned_start_at"`
	NextIntervalAt        *models.Time             `json:"next_interval_at"`
	LastAdvancedAt        *models.Time             `json:"last_advanced_at"`
	PausedAt              *models.Time             `json:"paused_at"`
	CompletedAt           *models.Time             `json:"completed_at"`
	Counts                EmailSendingPlanCounts   `json:"counts"`
	Advances              []EmailSendingPlanAdvanceView `json:"advances"`
}

// IsValidTopicRune keeps unicode import alive for future validation helpers.
var _ = unicode.IsLetter
