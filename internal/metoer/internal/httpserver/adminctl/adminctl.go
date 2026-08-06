// Package adminctl ports the Ring admin controllers:
// EmailSendingPlanAdminController.cs (/api/admin/email-plans),
// DeliveryObservabilityAdminController.cs and
// NotificationStatsAdminController.cs.
package adminctl

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gen "src.solsynth.dev/sosys/go/proto"
	"github.com/jackc/pgx/v5/pgconn"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/go/pkg/models"

	"src.solsynth.dev/sosys/metoer/internal/email"
	"src.solsynth.dev/sosys/metoer/internal/middleware"
	"src.solsynth.dev/sosys/metoer/internal/model"
	"src.solsynth.dev/sosys/metoer/internal/store"
)

// Deps carries the admin controller dependencies.
type Deps struct {
	Plans *email.PlanService
	Store *store.Store
	Perm  gen.DyPermissionServiceClient
	Log   *slog.Logger
}

// Register wires the admin routes.
func Register(api *gin.RouterGroup, deps Deps) {
	plans := api.Group("/admin/email-plans", middleware.RequireAuth(), middleware.AskPermission(deps.Perm, "emails.send", deps.Log))
	plans.POST("", createPlan(deps))
	plans.GET("", listPlans(deps))
	plans.GET("/:planId", getPlan(deps))
	plans.POST("/:planId/pause", pausePlan(deps))
	plans.POST("/:planId/resume", resumePlan(deps))
	plans.POST("/:planId/advance", advancePlan(deps))

	obs := api.Group("/admin/delivery-observability", middleware.RequireAuth())
	obs.GET("/emails", middleware.AskPermission(deps.Perm, "emails.send", deps.Log), emailDeliveryOverviewHandler(deps))
	obs.GET("/notifications", middleware.AskPermission(deps.Perm, "notifications.send", deps.Log), notificationDeliveryOverviewHandler(deps))

	stats := api.Group("/admin/stats", middleware.RequireAuth(), middleware.AskPermission(deps.Perm, "notifications.send", deps.Log))
	stats.GET("", statsHandler(deps))
}

// createEmailSendingPlanRequest mirrors CreateEmailSendingPlanRequest.
type createEmailSendingPlanRequest struct {
	AccountId            *uuid.UUID `json:"account_id"`
	AccountIds           []uuid.UUID `json:"account_ids"`
	BroadcastToAll       bool        `json:"broadcast_to_all"`
	SendingPlanKey       *string     `json:"sending_plan_key"`
	Subject              string      `json:"subject"`
	HtmlBody             string      `json:"html_body"`
	PlannedStartAt       *time.Time  `json:"planned_start_at"`
	MaxEmailsPerInterval int         `json:"max_emails_per_interval"`
	IntervalMinutes      int         `json:"interval_minutes"`
	MaxEmailsPerDay      *int        `json:"max_emails_per_day"`
}

// createPlan mirrors CreatePlan.
func createPlan(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request createEmailSendingPlanRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, errs.BadRequest("INVALID_BODY", "The request body is invalid."))
			return
		}
		user := middleware.CurrentUser(c)
		if user == nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}

		if !request.BroadcastToAll && request.AccountId == nil && len(request.AccountIds) == 0 {
			c.JSON(http.StatusBadRequest, errs.BadRequest("EMAIL_PLAN_TARGETING_REQUIRED", "Provide account_id, account_ids, or set broadcast_to_all=true."))
			return
		}
		if strings.TrimSpace(request.Subject) == "" {
			c.JSON(http.StatusBadRequest, errs.BadRequest("EMAIL_PLAN_SUBJECT_REQUIRED", "Subject is required."))
			return
		}
		if strings.TrimSpace(request.HtmlBody) == "" {
			c.JSON(http.StatusBadRequest, errs.BadRequest("EMAIL_PLAN_HTML_BODY_REQUIRED", "Html body is required."))
			return
		}
		if request.MaxEmailsPerDay != nil && *request.MaxEmailsPerDay < request.MaxEmailsPerInterval {
			c.JSON(http.StatusBadRequest, errs.BadRequest("EMAIL_PLAN_MAX_EMAILS_PER_DAY_INVALID", "max_emails_per_day must be greater than or equal to max_emails_per_interval."))
			return
		}

		createdBy, err := uuid.Parse(user.Id)
		if err != nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}

		plan, err := deps.Plans.CreatePlanAsync(c.Request.Context(), email.CreateEmailSendingPlanCommand{
			AccountId:            request.AccountId,
			AccountIds:           request.AccountIds,
			BroadcastToAll:       request.BroadcastToAll,
			Subject:              request.Subject,
			HtmlBody:             request.HtmlBody,
			SendingPlanKey:       request.SendingPlanKey,
			PlannedStartAt:       request.PlannedStartAt,
			MaxEmailsPerInterval: request.MaxEmailsPerInterval,
			IntervalMinutes:      request.IntervalMinutes,
			MaxEmailsPerDay:      request.MaxEmailsPerDay,
		}, createdBy)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				c.JSON(http.StatusConflict, errs.New("EMAIL_PLAN_KEY_CONFLICT", "The sending plan key already exists.", http.StatusConflict))
				return
			}
			if errors.Is(err, errInvalidOperation) || isInvalidOperation(err) {
				c.JSON(http.StatusBadRequest, errs.New("EMAIL_PLAN_VALIDATION_ERROR", err.Error(), http.StatusBadRequest))
				return
			}
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to create email sending plan.", http.StatusInternalServerError))
			return
		}
		c.JSON(http.StatusOK, plan)
	}
}

// errInvalidOperation is the marker for the C# InvalidOperationException
// mapping (CreatePlanAsync's targeting error).
var errInvalidOperation = errors.New("invalid operation")

func isInvalidOperation(err error) bool {
	// Advance/pause/resume throw plain errors with the C# messages; the
	// create path's InvalidOperationException ("No valid target accounts
	// were resolved.") maps to 400 EMAIL_PLAN_VALIDATION_ERROR.
	return strings.HasPrefix(err.Error(), "No valid target accounts were resolved.")
}

// listPlans mirrors ListPlans.
func listPlans(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		take := queryInt(c, "take", 20)
		if take < 1 {
			take = 1
		}
		if take > 100 {
			take = 100
		}
		offset := queryInt(c, "offset", 0)
		if offset < 0 {
			offset = 0
		}
		var status *model.EmailSendingPlanStatus
		if raw := c.Query("status"); raw != "" {
			if v, err := strconv.Atoi(raw); err == nil {
				s := model.EmailSendingPlanStatus(v)
				status = &s
			}
		}
		items, totalCount, err := deps.Plans.ListPlansAsync(c.Request.Context(), offset, take, status)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to list email sending plans.", http.StatusInternalServerError))
			return
		}
		c.Header("X-Total", strconv.Itoa(totalCount))
		c.JSON(http.StatusOK, items)
	}
}

// getPlan mirrors GetPlan.
func getPlan(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		planID, ok := parseUUIDParam(c)
		if !ok {
			return
		}
		plan, err := deps.Plans.GetPlanAsync(c.Request.Context(), planID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load email sending plan.", http.StatusInternalServerError))
			return
		}
		if plan == nil {
			c.JSON(http.StatusNotFound, errs.New("EMAIL_PLAN_NOT_FOUND", "The requested email sending plan was not found.", http.StatusNotFound))
			return
		}
		c.JSON(http.StatusOK, plan)
	}
}

// pausePlan mirrors PausePlan.
func pausePlan(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		planID, ok := parseUUIDParam(c)
		if !ok {
			return
		}
		plan, err := deps.Plans.PausePlanAsync(c.Request.Context(), planID)
		if err != nil {
			if planNotFound(err) {
				c.JSON(http.StatusNotFound, errs.New("EMAIL_PLAN_NOT_FOUND", "The requested email sending plan was not found.", http.StatusNotFound))
				return
			}
			c.JSON(http.StatusConflict, errs.New("EMAIL_PLAN_PAUSE_CONFLICT", err.Error(), http.StatusConflict))
			return
		}
		c.JSON(http.StatusOK, plan)
	}
}

// resumePlan mirrors ResumePlan.
func resumePlan(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		planID, ok := parseUUIDParam(c)
		if !ok {
			return
		}
		plan, err := deps.Plans.ResumePlanAsync(c.Request.Context(), planID)
		if err != nil {
			if planNotFound(err) {
				c.JSON(http.StatusNotFound, errs.New("EMAIL_PLAN_NOT_FOUND", "The requested email sending plan was not found.", http.StatusNotFound))
				return
			}
			c.JSON(http.StatusConflict, errs.New("EMAIL_PLAN_RESUME_CONFLICT", err.Error(), http.StatusConflict))
			return
		}
		c.JSON(http.StatusOK, plan)
	}
}

// advancePlan mirrors AdvancePlan.
func advancePlan(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		planID, ok := parseUUIDParam(c)
		if !ok {
			return
		}
		plan, err := deps.Plans.AdvancePlanIntervalAsync(c.Request.Context(), planID, true)
		if err != nil {
			if planNotFound(err) {
				c.JSON(http.StatusNotFound, errs.New("EMAIL_PLAN_NOT_FOUND", "The requested email sending plan was not found.", http.StatusNotFound))
				return
			}
			c.JSON(http.StatusConflict, errs.New("EMAIL_PLAN_ADVANCE_CONFLICT", err.Error(), http.StatusConflict))
			return
		}
		c.JSON(http.StatusOK, plan)
	}
}

func planNotFound(err error) bool {
	return strings.Contains(err.Error(), "was not found")
}

func parseUUIDParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("planId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("INVALID_UUID", "The id is not a valid UUID."))
		return uuid.Nil, false
	}
	return id, true
}

func queryInt(c *gin.Context, key string, def int) int {
	raw := c.Query(key)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

// ---- delivery observability ----

// deliverySummary mirrors DeliverySummary.
type deliverySummary struct {
	Total        int64    `json:"total"`
	Successful   int64    `json:"successful"`
	Failed       int64    `json:"failed"`
	InvalidToken int64    `json:"invalid_token"`
	Skipped      int64    `json:"skipped"`
	Held         int64    `json:"held"`
	SuccessRate  *float64 `json:"success_rate"`
}

// deliveryBreakdown mirrors DeliveryBreakdown.
type deliveryBreakdown struct {
	deliverySummary
	Key string `json:"key"`
}

// emailDeliveryOverview mirrors EmailDeliveryOverview.
type emailDeliveryOverview struct {
	Summary  deliverySummary     `json:"summary"`
	BySource []deliveryBreakdown `json:"by_source"`
}

// notificationDeliveryOverview mirrors NotificationDeliveryOverview.
type notificationDeliveryOverview struct {
	SendRequests        int64              `json:"send_requests"`
	SendRequestsByTopic []deliveryBreakdown `json:"send_requests_by_topic"`
	Summary             deliverySummary    `json:"summary"`
	ByProvider          []deliveryBreakdown `json:"by_provider"`
	ByTopic             []deliveryBreakdown `json:"by_topic"`
}

// toSummary mirrors ToSummary.
func toSummary(outcomes []store.OutcomeCount) deliverySummary {
	values := map[model.DeliveryOutcome]int64{}
	for _, o := range outcomes {
		values[o.Outcome] += o.Count
	}
	successful := values[model.DeliveryOutcomeSuccess]
	failed := values[model.DeliveryOutcomeFailure]
	invalidToken := values[model.DeliveryOutcomeInvalidToken]
	skipped := values[model.DeliveryOutcomeSkipped]
	held := values[model.DeliveryOutcomeHeld]
	var total int64
	for _, count := range values {
		total += count
	}
	attempts := successful + failed + invalidToken
	var successRate *float64
	if attempts != 0 {
		rate := float64(successful) / float64(attempts)
		successRate = &rate
	}
	return deliverySummary{
		Total:        total,
		Successful:   successful,
		Failed:       failed,
		InvalidToken: invalidToken,
		Skipped:      skipped,
		Held:         held,
		SuccessRate:  successRate,
	}
}

// resolveRange mirrors ResolveRange: to ?? now, from ?? to-30d, from <= to.
func resolveRange(c *gin.Context, now time.Time) (time.Time, time.Time, bool) {
	to := now
	if raw := c.Query("to"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			to = t.UTC()
		}
	}
	from := to.Add(-30 * 24 * time.Hour)
	if raw := c.Query("from"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			from = t.UTC()
		}
	}
	if from.After(to) {
		return from, to, false
	}
	return from, to, true
}

// emailDeliveryOverviewHandler mirrors GetEmailDeliveryOverview.
func emailDeliveryOverviewHandler(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		from, to, ok := resolveRange(c, time.Now().UTC())
		if !ok {
			c.JSON(http.StatusBadRequest, errs.BadRequest("INVALID_RANGE", "from must be earlier than to."))
			return
		}
		outcomes, err := deps.Store.CountDeliveryOutcomes(c.Request.Context(), "email_delivery_records", from, to)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load delivery observability.", http.StatusInternalServerError))
			return
		}
		bySourceRows, err := deps.Store.CountDeliveryByKeyOutcome(c.Request.Context(), "email_delivery_records", "source", from, to)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load delivery observability.", http.StatusInternalServerError))
			return
		}
		c.JSON(http.StatusOK, emailDeliveryOverview{
			Summary:  toSummary(outcomes),
			BySource: buildBreakdowns(bySourceRows),
		})
	}
}

// notificationDeliveryOverviewHandler mirrors GetNotificationDeliveryOverview.
func notificationDeliveryOverviewHandler(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		from, to, ok := resolveRange(c, time.Now().UTC())
		if !ok {
			c.JSON(http.StatusBadRequest, errs.BadRequest("INVALID_RANGE", "from must be earlier than to."))
			return
		}
		sendRequests, err := deps.Store.CountSendRecords(c.Request.Context(), from, to)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load delivery observability.", http.StatusInternalServerError))
			return
		}
		sendByTopic, err := deps.Store.CountSendByTopic(c.Request.Context(), from, to)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load delivery observability.", http.StatusInternalServerError))
			return
		}
		outcomes, err := deps.Store.CountDeliveryOutcomes(c.Request.Context(), "notification_delivery_records", from, to)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load delivery observability.", http.StatusInternalServerError))
			return
		}
		byProvider, err := deps.Store.CountDeliveryByKeyOutcome(c.Request.Context(), "notification_delivery_records", "provider", from, to)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load delivery observability.", http.StatusInternalServerError))
			return
		}
		byTopic, err := deps.Store.CountDeliveryByKeyOutcome(c.Request.Context(), "notification_delivery_records", "topic", from, to)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load delivery observability.", http.StatusInternalServerError))
			return
		}

		var sendRequestsByTopic []deliveryBreakdown
		for _, row := range sendByTopic {
			sendRequestsByTopic = append(sendRequestsByTopic, deliveryBreakdown{
				deliverySummary: deliverySummary{Total: row.Count},
				Key:             row.Key,
			})
		}
		sortBreakdowns(sendRequestsByTopic)

		c.JSON(http.StatusOK, notificationDeliveryOverview{
			SendRequests:        sendRequests,
			SendRequestsByTopic: sendRequestsByTopic,
			Summary:             toSummary(outcomes),
			ByProvider:          buildBreakdowns(byProvider),
			ByTopic:             buildBreakdowns(byTopic),
		})
	}
}

// buildBreakdowns mirrors BuildBreakdownAsync: group rows by key, fold
// outcome counts into a summary per key, order by total desc.
func buildBreakdowns(rows []store.KeyOutcomeCount) []deliveryBreakdown {
	grouped := map[string][]store.OutcomeCount{}
	var keys []string
	for _, row := range rows {
		if _, ok := grouped[row.Key]; !ok {
			keys = append(keys, row.Key)
		}
		grouped[row.Key] = append(grouped[row.Key], store.OutcomeCount{Outcome: row.Outcome, Count: row.Count})
	}
	out := make([]deliveryBreakdown, 0, len(keys))
	for _, key := range keys {
		summary := toSummary(grouped[key])
		out = append(out, deliveryBreakdown{deliverySummary: summary, Key: key})
	}
	sortBreakdowns(out)
	return out
}

func sortBreakdowns(items []deliveryBreakdown) {
	// Order by total desc (stable insertion sort for small N).
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j-1].Total < items[j].Total; j-- {
			items[j-1], items[j] = items[j], items[j-1]
		}
	}
}

// ---- stats ----

// notificationStatsResponse mirrors NotificationStatsResponse.
type notificationStatsResponse struct {
	CalculatedAt            models.Time `json:"calculated_at"`
	TotalNotifications      int64       `json:"total_notifications"`
	UnreadNotifications     int64       `json:"unread_notifications"`
	NotificationsLastDay    int64       `json:"notifications_last_day"`
	NotificationsLastWeek   int64       `json:"notifications_last_week"`
	NotificationsLastMonth  int64       `json:"notifications_last_month"`
	TotalPushSubscriptions  int64       `json:"total_push_subscriptions"`
	ActivePushSubscriptions int64       `json:"active_push_subscriptions"`
	TotalSendRequests       int64       `json:"total_send_requests"`
	TotalDeliveryAttempts   int64       `json:"total_delivery_attempts"`
}

// statsHandler mirrors GetStats.
func statsHandler(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		now := time.Now().UTC()
		stats, err := deps.Store.GetNotificationStats(c.Request.Context(), now)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load stats.", http.StatusInternalServerError))
			return
		}
		c.JSON(http.StatusOK, notificationStatsResponse{
			CalculatedAt:            models.Time(now),
			TotalNotifications:      stats.TotalNotifications,
			UnreadNotifications:     stats.UnreadNotifications,
			NotificationsLastDay:    stats.NotificationsLastDay,
			NotificationsLastWeek:   stats.NotificationsLastWeek,
			NotificationsLastMonth:  stats.NotificationsLastMonth,
			TotalPushSubscriptions:  stats.TotalPushSubscriptions,
			ActivePushSubscriptions: stats.ActivePushSubscriptions,
			TotalSendRequests:       stats.TotalSendRequests,
			TotalDeliveryAttempts:   stats.TotalDeliveryAttempts,
		})
	}
}
