package notificationctl

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/go/pkg/errs"

	"src.solsynth.dev/sosys/metoer/internal/middleware"
	"src.solsynth.dev/sosys/metoer/internal/model"
)

// countUnread mirrors CountUnreadNotifications.
func countUnread(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		if user == nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		accountID, err := uuid.Parse(user.Id)
		if err != nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		app := c.Query("app")
		resolved := deps.Push.ResolveAppId(app, true)
		defaultApp := deps.Push.GetDefaultAppId()
		count, err := deps.Store.CountUnreadNotifications(c.Request.Context(), accountID, &resolved, new(defaultApp))
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to count notifications.", http.StatusInternalServerError))
			return
		}
		c.JSON(http.StatusOK, count)
	}
}

// list mirrors ListNotifications.
func list(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		if user == nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		accountID, err := uuid.Parse(user.Id)
		if err != nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		offset := queryInt(c, "offset", 0)
		take := queryInt(c, "take", 8)
		unmark := queryBool(c, "unmark")
		app := c.Query("app")

		resolved := deps.Push.ResolveAppId(app, true)
		defaultApp := deps.Push.GetDefaultAppId()
		notifications, totalCount, err := deps.Store.ListNotifications(c.Request.Context(), accountID, &resolved, new(defaultApp), offset, take)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to list notifications.", http.StatusInternalServerError))
			return
		}

		c.Header("X-Total", strconv.Itoa(totalCount))

		// Snapshot the response before marking the page viewed. The list endpoint
		// returns the unread state as it was when the page was selected.
		response := notificationsResponse(notifications)
		if !unmark {
			if err := deps.Push.MarkNotificationsViewed(c.Request.Context(), notifications, time.Now().UTC()); err != nil {
				c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to mark notifications viewed.", http.StatusInternalServerError))
				return
			}
		}
		c.JSON(http.StatusOK, response)
	}
}

// markAllRead mirrors MarkAllNotificationsViewed.
func markAllRead(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		if user == nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		accountID, err := uuid.Parse(user.Id)
		if err != nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		if err := deps.Push.MarkAllNotificationsViewed(c.Request.Context(), accountID, c.Query("app")); err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to mark notifications viewed.", http.StatusInternalServerError))
			return
		}
		c.Status(http.StatusOK)
	}
}

// pushSubscribeRequest mirrors PushNotificationSubscribeRequest.
type pushSubscribeRequest struct {
	DeviceToken *string `json:"device_token"`
	DeviceName  *string `json:"device_name"`
	Provider    *int    `json:"provider"`
	AppId       *string `json:"app_id"`
}

// subscribe mirrors SubscribeToPushNotification.
func subscribe(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		session := middleware.CurrentSession(c)
		if user == nil || session == nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		var request pushSubscribeRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, errs.BadRequest("INVALID_BODY", "The request body is invalid."))
			return
		}
		if request.Provider == nil {
			c.JSON(http.StatusBadRequest, errs.BadRequest("INVALID_BODY", "The request body is invalid."))
			return
		}
		provider := model.PushProvider(*request.Provider)

		if provider == model.PushProviderSop {
			c.JSON(http.StatusBadRequest, errs.BadRequest("RING_NOTIFICATION_SOP_NOT_SUPPORTED", "Use /api/notifications/sop/subscription to register SOP provider."))
			return
		}
		if request.DeviceToken == nil || strings.TrimSpace(*request.DeviceToken) == "" {
			c.JSON(http.StatusBadRequest, errs.BadRequest("RING_NOTIFICATION_DEVICE_TOKEN_REQUIRED", "DeviceToken is required."))
			return
		}
		if provider == model.PushProviderUnifiedPush && !isValidUnifiedPushEndpoint(*request.DeviceToken) {
			c.JSON(http.StatusBadRequest, errs.BadRequest("RING_NOTIFICATION_INVALID_UP_ENDPOINT", "For UnifiedPush, DeviceToken must be a valid absolute HTTP(S) endpoint URL."))
			return
		}

		accountID, err := uuid.Parse(user.Id)
		if err != nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}

		force := queryBool(c, "force")
		if !force && session.ClientId != nil {
			activeSubscription, err := deps.Push.GetCurrentDeviceActiveSubscription(c.Request.Context(), accountID, *session.ClientId)
			if err == nil && activeSubscription != nil && activeSubscription.Provider == model.PushProviderSop {
				c.JSON(http.StatusOK, activeSubscription)
				return
			}
		}

		var appId string
		if request.AppId != nil {
			appId = *request.AppId
		}
		var clientId string
		if session.ClientId != nil {
			clientId = *session.ClientId
		}
		result, err := deps.Push.SubscribeDevice(
			c.Request.Context(),
			clientId,
			*request.DeviceToken,
			request.DeviceName,
			provider,
			user,
			true,
			appId,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to subscribe device.", http.StatusInternalServerError))
			return
		}
		c.JSON(http.StatusOK, result)
	}
}

// isValidUnifiedPushEndpoint mirrors the controller's private helper.
func isValidUnifiedPushEndpoint(endpoint string) bool {
	uri, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	if uri.Scheme != "http" && uri.Scheme != "https" {
		return false
	}
	host := uri.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback()
	}
	return !strings.EqualFold(host, "localhost")
}

// listSubscriptions mirrors ListPushSubscriptions.
func listSubscriptions(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		if user == nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		accountID, err := uuid.Parse(user.Id)
		if err != nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		app := c.Query("app")
		resolved := deps.Push.ResolveAppId(app, true)
		defaultApp := deps.Push.GetDefaultAppId()
		subscriptions, err := deps.Store.ListSubscriptions(c.Request.Context(), accountID, &resolved, new(defaultApp))
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to list subscriptions.", http.StatusInternalServerError))
			return
		}
		c.JSON(http.StatusOK, subscriptions)
	}
}

// currentSubscription mirrors GetCurrentDeviceActiveSubscription.
func currentSubscription(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		session := middleware.CurrentSession(c)
		if user == nil || session == nil || session.ClientId == nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		accountID, err := uuid.Parse(user.Id)
		if err != nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		subscription, err := deps.Push.GetCurrentDeviceActiveSubscription(c.Request.Context(), accountID, *session.ClientId)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load subscription.", http.StatusInternalServerError))
			return
		}
		c.JSON(http.StatusOK, subscription)
	}
}

// unsubscribe mirrors UnsubscribeFromPushNotification.
func unsubscribe(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		if user == nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		accountID, err := uuid.Parse(user.Id)
		if err != nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		subscriptionID, ok := parseUUID(c, c.Param("subscriptionId"))
		if !ok {
			return
		}
		affected, err := deps.Store.DeleteSubscriptionById(c.Request.Context(), accountID, subscriptionID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to delete subscription.", http.StatusInternalServerError))
			return
		}
		c.JSON(http.StatusOK, affected)
	}
}

// notificationRequest mirrors NotificationRequest.
type notificationRequest struct {
	Topic    string         `json:"topic"`
	Title    string         `json:"title"`
	Subtitle *string        `json:"subtitle"`
	Content  string         `json:"content"`
	Meta     map[string]any `json:"meta"`
	Priority *int           `json:"priority"`
	PushType *string        `json:"push_type"`
}

// notificationWithAimRequest mirrors NotificationWithAimRequest.
type notificationWithAimRequest struct {
	notificationRequest
	AccountId []uuid.UUID `json:"account_id"`
}

// send mirrors SendNotification.
func send(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request notificationWithAimRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, errs.BadRequest("INVALID_BODY", "The request body is invalid."))
			return
		}
		if len(request.AccountId) == 0 {
			c.JSON(http.StatusBadRequest, errs.BadRequest("INVALID_BODY", "The request body is invalid."))
			return
		}

		appId := deps.Push.ResolveAppId(c.Query("app"), true)
		priority := 10
		if request.Priority != nil {
			priority = *request.Priority
		}
		now := model.NowTime()
		notification := &model.Notification{
			ModelBase: model.ModelBase{Id: uuid.New(), CreatedAt: now, UpdatedAt: now},
			Topic:     request.Topic,
			Title:     new(request.Title),
			Subtitle:  request.Subtitle,
			Content:   new(request.Content),
			Priority:  priority,
			AppId:     new(appId),
			PushType:  request.PushType,
		}
		if request.Meta != nil {
			notification.Meta = request.Meta
		} else {
			notification.Meta = map[string]any{}
		}

		save := queryBool(c, "save")
		if err := deps.Push.SendNotificationBatch(c.Request.Context(), notification, request.AccountId, save, nil); err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to send notification.", http.StatusInternalServerError))
			return
		}
		c.Status(http.StatusOK)
	}
}

// listPreferences mirrors ListPreferences.
func listPreferences(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		if user == nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		accountID, err := uuid.Parse(user.Id)
		if err != nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		preferences, err := deps.Store.ListPreferences(c.Request.Context(), accountID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to list preferences.", http.StatusInternalServerError))
			return
		}
		c.JSON(http.StatusOK, preferences)
	}
}

// getPreference mirrors GetPreference.
func getPreference(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		if user == nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		accountID, err := uuid.Parse(user.Id)
		if err != nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		level, err := preferenceLevel(deps, c.Request.Context(), accountID, c.Param("topic"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load preference.", http.StatusInternalServerError))
			return
		}
		c.JSON(http.StatusOK, level)
	}
}

func preferenceLevel(deps Deps, ctx context.Context, accountID uuid.UUID, topic string) (model.NotificationPreferenceLevel, error) {
	pref, err := deps.Store.GetPreference(ctx, accountID, topic)
	if err != nil {
		return model.NotificationPreferenceNormal, err
	}
	if pref == nil {
		return model.NotificationPreferenceNormal, nil
	}
	return pref.Preference, nil
}

// setPreferenceRequest mirrors SetPreferenceRequest.
type setPreferenceRequest struct {
	Preference *int `json:"preference"`
}

// setPreference mirrors SetPreference.
func setPreference(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		if user == nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		accountID, err := uuid.Parse(user.Id)
		if err != nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		var request setPreferenceRequest
		if err := c.ShouldBindJSON(&request); err != nil || request.Preference == nil {
			c.JSON(http.StatusBadRequest, errs.BadRequest("INVALID_BODY", "The request body is invalid."))
			return
		}
		if err := deps.Store.SetPreference(c.Request.Context(), accountID, c.Param("topic"), model.NotificationPreferenceLevel(*request.Preference), time.Now().UTC()); err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to set preference.", http.StatusInternalServerError))
			return
		}
		c.Status(http.StatusOK)
	}
}

// deletePreference mirrors DeletePreference.
func deletePreference(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		if user == nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		accountID, err := uuid.Parse(user.Id)
		if err != nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		if err := deps.Store.DeletePreference(c.Request.Context(), accountID, c.Param("topic"), time.Now().UTC()); err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to delete preference.", http.StatusInternalServerError))
			return
		}
		c.Status(http.StatusOK)
	}
}
