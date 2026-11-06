package adminctl

// Port of Padlock's AccountActionLogController (GET /api/actions): the
// authenticated user's own action logs. The admin-side log search lives on
// the gRPC surface (ActionLogServiceGrpc.SearchActionLogs, Phase 9) — there
// is no admin HTTP log-search route in the C# fleet.
//
// The summary route is the console's default view: grouped counts instead of
// the timeline. The paged list stays for clients that explicitly want rows.

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/model"
)

func getActionLogs(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := currentUserID(c)
		if !ok {
			return
		}
		action := c.Query("action")
		take := 50
		if raw := c.Query("take"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
				take = parsed
			}
		}
		if take > 1000 {
			take = 1000
		}
		offset := queryOffset(c)

		logs, total, err := d.Store.AdminListOwnActionLogs(c.Request.Context(), userID, action, take, offset)
		if err != nil {
			serverError(c, err, d)
			return
		}
		setTotal(c, total)
		c.JSON(http.StatusOK, logs)
	}
}

func getActionLogSummary(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := currentUserID(c)
		if !ok {
			return
		}
		writeActionLogSummary(c, d, userID)
	}
}

func getAccountActionLogSummary(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, err := uuid.Parse(account.Id)
		if err != nil {
			accountNotFound(c)
			return
		}
		writeActionLogSummary(c, d, accountID)
	}
}

type actionLogSummaryResponse struct {
	Days    int                      `json:"days"`
	Since   model.Time               `json:"since"`
	Until   model.Time               `json:"until"`
	Total   int64                    `json:"total"`
	Last24h int64                    `json:"last_24h"`
	Last7d  int64                    `json:"last_7d"`
	Last30d int64                    `json:"last_30d"`
	Groups  []actionLogSummaryGroup  `json:"groups"`
	Buckets []actionLogSummaryBucket `json:"buckets"`
}

type actionLogSummaryGroup struct {
	Action string     `json:"action"`
	Count  int64      `json:"count"`
	LastAt model.Time `json:"last_at"`
}

type actionLogSummaryBucket struct {
	Start model.Time `json:"start"`
	Count int64      `json:"count"`
}

// writeActionLogSummary resolves the window, runs the database rollup and
// writes the grouped response shared by the user and admin routes.
func writeActionLogSummary(c *gin.Context, d Deps, accountID uuid.UUID) {
	since, days := actionLogSummaryWindow(c)
	summary, err := d.Store.SummarizeActionLogs(c.Request.Context(), accountID, since)
	if err != nil {
		serverError(c, err, d)
		return
	}

	response := actionLogSummaryResponse{
		Days:    days,
		Since:   model.Time(summary.Since),
		Until:   model.Time(summary.Until),
		Total:   summary.Total,
		Last24h: summary.Last24h,
		Last7d:  summary.Last7d,
		Last30d: summary.Last30d,
		Groups:  make([]actionLogSummaryGroup, 0, len(summary.Groups)),
		Buckets: make([]actionLogSummaryBucket, 0, len(summary.Buckets)),
	}
	for _, group := range summary.Groups {
		response.Groups = append(response.Groups, actionLogSummaryGroup{
			Action: group.Action,
			Count:  group.Count,
			LastAt: model.Time(group.LastAt),
		})
	}
	for _, bucket := range summary.Buckets {
		response.Buckets = append(response.Buckets, actionLogSummaryBucket{
			Start: model.Time(bucket.Start),
			Count: bucket.Count,
		})
	}
	c.JSON(http.StatusOK, response)
}

// actionLogSummaryWindow reads ?days= (1–365, default 30). Invalid values
// fall back to the default rather than failing the dashboard.
func actionLogSummaryWindow(c *gin.Context) (time.Time, int) {
	days := 30
	if raw := c.Query("days"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			days = parsed
		}
	}
	if days > 365 {
		days = 365
	}
	now := time.Now().UTC()
	return now.Add(-time.Duration(days) * 24 * time.Hour), days
}
