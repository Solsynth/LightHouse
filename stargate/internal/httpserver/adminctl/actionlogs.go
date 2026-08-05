package adminctl

// Port of Padlock's AccountActionLogController (GET /api/actions): the
// authenticated user's own action logs. The admin-side log search lives on
// the gRPC surface (ActionLogServiceGrpc.SearchActionLogs, Phase 9) — there
// is no admin HTTP log-search route in the C# fleet.

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

func registerActionLogs(g *gin.RouterGroup, d Deps) {
	g.GET("", getActionLogs(d))
}

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
