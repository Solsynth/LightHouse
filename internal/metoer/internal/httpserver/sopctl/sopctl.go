// Package sopctl ports SopNotificationController.cs — the
// /api/notifications/sop/** route tree (SOP token registration, list, SSE
// stream).
package sopctl

import (
	"encoding/json"
	"io"
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
)

// Deps carries the controller dependencies.
type Deps struct {
	Push *push.Service
	Perm gen.DyPermissionServiceClient
	Log  *slog.Logger
}

// Register wires the SOP routes.
func Register(api *gin.RouterGroup, deps Deps) {
	group := api.Group("/notifications/sop")
	group.POST("/subscription", middleware.RequireAuth(), middleware.AskPermission(deps.Perm, "notifications.sop.subscribe", deps.Log), registerSopToken(deps))
	group.GET("", listBySopToken(deps))
	group.GET("/stream", streamBySopToken(deps))
}

// sopRegistrationRequest mirrors SopRegistrationRequest.
type sopRegistrationRequest struct {
	DeviceName *string `json:"device_name"`
	AppId      *string `json:"app_id"`
}

// sopRegistrationResponse mirrors SopRegistrationResponse.
type sopRegistrationResponse struct {
	Token        string                     `json:"token"`
	Subscription *model.PushSubscription    `json:"subscription"`
}

// registerSopToken mirrors RegisterSopToken.
func registerSopToken(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		session := middleware.CurrentSession(c)
		if user == nil || session == nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		var request sopRegistrationRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, errs.BadRequest("INVALID_BODY", "The request body is invalid."))
			return
		}
		if session.ClientId == nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		sopDeviceId := *session.ClientId + ":sop"
		var appId string
		if request.AppId != nil {
			appId = *request.AppId
		}
		token, subscription, err := deps.Push.RegisterSopToken(c.Request.Context(), sopDeviceId, request.DeviceName, user, appId)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to register SOP token.", http.StatusInternalServerError))
			return
		}
		c.JSON(http.StatusOK, sopRegistrationResponse{Token: token, Subscription: subscription})
	}
}

// extractSopToken mirrors the controller's private ExtractSopToken: query
// `token`, then `X-SOP-Token`, then `Authorization: SOP <t>`.
func extractSopToken(c *gin.Context) string {
	if token := strings.TrimSpace(c.Query("token")); token != "" {
		return token
	}
	if token := strings.TrimSpace(c.GetHeader("X-SOP-Token")); token != "" {
		return token
	}
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		return ""
	}
	const sopPrefix = "SOP "
	if len(authHeader) >= len(sopPrefix) && strings.EqualFold(authHeader[:len(sopPrefix)], sopPrefix) {
		return strings.TrimSpace(authHeader[len(sopPrefix):])
	}
	return ""
}

// listBySopToken mirrors ListNotificationsBySopToken.
func listBySopToken(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractSopToken(c)
		if token == "" {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		sopSub, err := deps.Push.GetSopSubscriptionByToken(c.Request.Context(), token)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load SOP subscription.", http.StatusInternalServerError))
			return
		}
		if sopSub == nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}

		offset := queryInt(c, "offset", 0)
		take := queryInt(c, "take", 8)
		app := c.Query("app")
		notifications, totalCount, err := deps.Push.ListSopNotifications(c.Request.Context(), sopSub.AccountId, offset, take, app)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to list notifications.", http.StatusInternalServerError))
			return
		}

		c.Header("X-Total", strconv.Itoa(totalCount))
		var ids []uuid.UUID
		for _, n := range notifications {
			ids = append(ids, n.Id)
		}
		deps.Push.MarkSopDeliveryReadAsync(c.Request.Context(), sopSub.Id, ids)
		_ = deps.Push.RemoveSopReplayNotifications(c.Request.Context(), sopSub.AccountId, ids)

		c.JSON(http.StatusOK, notificationList(notifications))
	}
}

// streamBySopToken mirrors StreamNotificationsBySopToken (SSE).
func streamBySopToken(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractSopToken(c)
		var sopSub *model.PushSubscription
		if token != "" {
			sub, err := deps.Push.GetSopSubscriptionByToken(c.Request.Context(), token)
			if err == nil {
				sopSub = sub
			}
		}

		var accountID uuid.UUID
		var deviceId string
		if sopSub != nil {
			accountID = sopSub.AccountId
			deviceId = sopSub.DeviceId
		} else {
			user := middleware.CurrentUser(c)
			session := middleware.CurrentSession(c)
			if user == nil || session == nil || session.ClientId == nil {
				c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
				return
			}
			id, err := uuid.Parse(user.Id)
			if err != nil {
				c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
				return
			}
			accountID = id
			deviceId = *session.ClientId + ":sop"
		}

		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")

		streamID, stream := deps.Push.SubscribeSopStream(accountID, deviceId)
		defer deps.Push.UnsubscribeSopStream(accountID, streamID)

		flusher, ok := c.Writer.(http.Flusher)
		if !ok {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Streaming is not supported.", http.StatusInternalServerError))
			return
		}

		write := func(s string) {
			_, _ = io.WriteString(c.Writer, s)
		}
		write("event: ready\n")
		write("data: {\"status\":\"connected\"}\n\n")
		flusher.Flush()

		var deliveredIds []uuid.UUID
		for stream.WaitToRead(c.Request.Context()) {
			for {
				notification, ok := stream.TryRead()
				if !ok {
					break
				}
				payload, _ := json.Marshal(notificationResponse(notification))
				write("event: notification\n")
				write("data: " + string(payload) + "\n\n")
				flusher.Flush()
				if sopSub != nil {
					deliveredIds = append(deliveredIds, notification.Id)
				}
			}

			// Read receipts are observability-only; batch them outside the
			// per-message hot path.
			if sopSub != nil && len(deliveredIds) > 0 {
				deps.Push.MarkSopDeliveryReadAsync(c.Request.Context(), sopSub.Id, deliveredIds)
				deliveredIds = nil
			}
		}
	}
}

// notificationResponse snake-cases meta keys (DictionaryKeyPolicy) exactly
// like the API responses.
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

func notificationList(items []*model.Notification) []*model.Notification {
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
