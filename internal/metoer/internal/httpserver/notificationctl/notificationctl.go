// Package notificationctl ports NotificationController.cs — the
// /api/notifications/** route tree.
package notificationctl

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/go/pkg/errs"

	"src.solsynth.dev/sosys/metoer/internal/middleware"
	"src.solsynth.dev/sosys/metoer/internal/model"
	"src.solsynth.dev/sosys/metoer/internal/push"
	"src.solsynth.dev/sosys/metoer/internal/store"
)

// Deps carries the controller dependencies.
type Deps struct {
	Push  *push.Service
	Store *store.Store
	Perm  gen.DyPermissionServiceClient
	Log   *slog.Logger
}

// Register wires the notification routes.
func Register(api *gin.RouterGroup, deps Deps) {
	group := api.Group("/notifications")
	group.GET("/count", middleware.RequireAuth(), countUnread(deps))
	group.GET("", middleware.RequireAuth(), list(deps))
	group.POST("/all/read", middleware.RequireAuth(), middleware.AskPermission(deps.Perm, "notifications.read.all", deps.Log), markAllRead(deps))
	group.PUT("/subscription", middleware.RequireAuth(), middleware.AskPermission(deps.Perm, "notifications.subscriptions.manage", deps.Log), subscribe(deps))
	group.GET("/subscription", middleware.RequireAuth(), listSubscriptions(deps))
	group.GET("/subscription/current", middleware.RequireAuth(), currentSubscription(deps))
	group.DELETE("/subscription/:subscriptionId", middleware.RequireAuth(), middleware.AskPermission(deps.Perm, "notifications.subscriptions.manage", deps.Log), unsubscribe(deps))
	group.POST("/send", middleware.RequireAuth(), middleware.AskPermission(deps.Perm, "notifications.send", deps.Log), send(deps))
	group.GET("/preferences", middleware.RequireAuth(), listPreferences(deps))
	group.GET("/preferences/:topic", middleware.RequireAuth(), getPreference(deps))
	group.PUT("/preferences/:topic", middleware.RequireAuth(), middleware.AskPermission(deps.Perm, "notifications.preferences.manage", deps.Log), setPreference(deps))
	group.DELETE("/preferences/:topic", middleware.RequireAuth(), middleware.AskPermission(deps.Perm, "notifications.preferences.manage", deps.Log), deletePreference(deps))
}

// notificationResponse returns the notification with meta keys snake-cased
// (Ring's DictionaryKeyPolicy = SnakeCaseLower on outbound serialization).
func notificationResponse(n *model.Notification) *model.Notification {
	if n == nil {
		return nil
	}
	copy := *n
	if copy.Meta != nil {
		copy.Meta = model.SnakeMapKeys(copy.Meta).(map[string]any)
	}
	return &copy
}

func notificationsResponse(items []*model.Notification) []*model.Notification {
	out := make([]*model.Notification, 0, len(items))
	for _, n := range items {
		out = append(out, notificationResponse(n))
	}
	return out
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

func queryBool(c *gin.Context, key string) bool {
	raw := c.Query(key)
	return strings.EqualFold(raw, "true") || raw == "1"
}

func parseUUID(c *gin.Context, value string) (uuid.UUID, bool) {
	id, err := uuid.Parse(value)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("INVALID_UUID", "The id is not a valid UUID."))
		return uuid.Nil, false
	}
	return id, true
}

var _ = context.Background
