// Package accounteventsctl mounts the account-events HTTP surface: the daily
// check-in, the personal calendar (events, aggregation, search, countdown,
// subscriptions), public calendars and timelines, notable days and fortunes.
package accounteventsctl

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	"src.solsynth.dev/sosys/stargate/accountevents"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/permission"
)

// AccountLookup is the account resolution surface the public routes need.
type AccountLookup interface {
	GetAccountByName(ctx context.Context, name string) (*model.Account, error)
	GetAccountByID(ctx context.Context, id uuid.UUID) (*model.Account, error)
}

// Deps is the dependency set for the account-events controllers.
type Deps struct {
	Service  *accountevents.Service
	Notable  *accountevents.NotableService
	Store    *accountevents.Store
	Accounts AccountLookup
	Perm     middleware.PermissionChecker
	Log      *slog.Logger
}

// Register mounts the authenticated /accounts/me routes.
func Register(api *gin.RouterGroup, d Deps) {
	me := api.Group("/accounts/me")
	me.GET("/check-in", middleware.RequireAuth(), d.getCheckIn)
	me.POST("/check-in", middleware.RequireAuth(), d.doCheckIn)

	calendar := me.Group("/calendar", middleware.RequireAuth())
	calendar.GET("", d.getCalendar)
	calendar.GET("/merged", d.getMergedCalendar)
	calendar.GET("/tags", d.getCalendarTags)
	calendar.GET("/events", d.getCalendarEvents)
	calendar.POST("/events", d.createCalendarEvent)
	calendar.GET("/events/:id", d.getCalendarEvent)
	calendar.PUT("/events/:id", d.updateCalendarEvent)
	calendar.DELETE("/events/:id", d.deleteCalendarEvent)
	calendar.GET("/notable-days/:occurrenceKey", d.getGeneratedNotableDay)
	calendar.GET("/search", d.searchCalendar)
	calendar.GET("/countdown", d.getCountdown)
	calendar.GET("/subscriptions", d.getSubscriptions)
	calendar.POST("/subscriptions/:accountId", d.subscribe)
	calendar.DELETE("/subscriptions/:accountId", d.unsubscribe)
	calendar.GET("/subscriptions/subscribers", d.getSubscribers)
}

// RegisterPublic mounts the anonymous notable-day, fortune and public
// account-calendar routes.
func RegisterPublic(api *gin.RouterGroup, d Deps) {
	api.GET("/notable-days", d.listNotableDays)
	api.GET("/notable-days/:id", d.getNotableDay)
	api.POST("/notable-days", middleware.AskPermission(d.Perm, permission.NotableDaysCreate), d.createNotableDay)
	api.PUT("/notable-days/:id", middleware.AskPermission(d.Perm, permission.NotableDaysUpdate), d.updateNotableDay)
	api.DELETE("/notable-days/:id", middleware.AskPermission(d.Perm, permission.NotableDaysDelete), d.deleteNotableDay)

	api.GET("/fortune", d.listFortunes)
	api.GET("/fortune/random", d.randomFortune)

	accounts := api.Group("/accounts")
	accounts.GET("/:name/calendar", d.getAccountCalendar)
	accounts.GET("/:name/calendar/merged", d.getAccountMergedCalendar)
	accounts.GET("/:name/calendar/countdown", d.getAccountCountdown)
	accounts.GET("/:name/timeline", d.getTimeline)
	accounts.GET("/:name/calendar/events", d.getAccountCalendarEvents)
	accounts.GET("/:name/calendar/events/:id", d.getAccountCalendarEvent)
	accounts.GET("/unknown/calendar/events/:id", d.getCalendarEventByID)
}

// ─────────────────────────── shared helpers ───────────────────────────

func notFound(resource string) *errs.ApiError {
	message := "The requested resource '" + resource + "' was not found."
	return &errs.ApiError{Code: "NOT_FOUND", Message: message, Status: http.StatusNotFound, Detail: &resource}
}

func internalError(c *gin.Context, err error) {
	c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Internal server error.", http.StatusInternalServerError))
}

func validationError(c *gin.Context, field, message string) {
	c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{field: {message}}))
}

func requireCurrentUser(c *gin.Context) *model.Account {
	user := middleware.CurrentUser(c.Request.Context())
	if user == nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return nil
	}
	return user
}

func currentViewer(c *gin.Context) *uuid.UUID {
	user := middleware.CurrentUser(c.Request.Context())
	if user == nil {
		return nil
	}
	id, err := uuid.Parse(user.Id)
	if err != nil {
		return nil
	}
	return &id
}

func accountIDOf(account *model.Account) (uuid.UUID, bool) {
	id, err := uuid.Parse(account.Id)
	return id, err == nil
}

func pagination(c *gin.Context, defaultTake int) (offset, take int) {
	offset, _ = strconv.Atoi(c.DefaultQuery("offset", "0"))
	if offset < 0 {
		offset = 0
	}
	take, _ = strconv.Atoi(c.DefaultQuery("take", strconv.Itoa(defaultTake)))
	if take <= 0 {
		take = defaultTake
	}
	return offset, take
}

// queryInstant parses an RFC3339 instant; present=false for an empty value.
func queryInstant(c *gin.Context, name string) (*time.Time, bool, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return nil, false, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil, true, err
	}
	utc := parsed.UTC()
	return &utc, true, nil
}

func queryUUID(c *gin.Context, name string) (*uuid.UUID, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return nil, nil
	}
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func queryInt(c *gin.Context, name string) (*int, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return nil, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

// calendarPeriod applies the C# month/year defaults and validation order.
func calendarPeriod(c *gin.Context) (month, year int, ok bool) {
	now := time.Now().UTC()
	month = int(now.Month())
	year = now.Year()
	if parsed, err := queryInt(c, "month"); err != nil {
		validationError(c, "month", "Month must be between 1 and 12.")
		return 0, 0, false
	} else if parsed != nil {
		month = *parsed
	}
	if parsed, err := queryInt(c, "year"); err != nil {
		validationError(c, "year", "Year must be a positive integer.")
		return 0, 0, false
	} else if parsed != nil {
		year = *parsed
	}
	if month < 1 || month > 12 {
		validationError(c, "month", "Month must be between 1 and 12.")
		return 0, 0, false
	}
	if year < 1 {
		validationError(c, "year", "Year must be a positive integer.")
		return 0, 0, false
	}
	return month, year, true
}

func parseTag(value string) (*accountevents.NotableDayTag, bool) {
	if strings.TrimSpace(value) == "" {
		return nil, true
	}
	tag, ok := accountevents.ParseNotableDayTag(value)
	if !ok {
		return nil, false
	}
	return &tag, true
}

func regionOf(account *model.Account) string {
	if account != nil && strings.TrimSpace(account.Region) != "" {
		return account.Region
	}
	return "cn"
}

func boolQuery(c *gin.Context, name string, fallback bool) bool {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return parsed
}

// ─────────────────────────── check-in ───────────────────────────

func (d Deps) getCheckIn(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := accountIDOf(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	version := 1
	if parsed, err := queryInt(c, "version"); err == nil && parsed != nil {
		version = *parsed
	}

	result, err := d.Service.TodayCheckIn(c.Request.Context(), accountID)
	if err != nil {
		if errors.Is(err, accountevents.ErrNotFound) {
			c.JSON(http.StatusNotFound, notFound("check-in"))
			return
		}
		internalError(c, err)
		return
	}
	d.Service.PrepareCheckInResult(user, result, version)
	c.JSON(http.StatusOK, result)
}

func (d Deps) doCheckIn(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := accountIDOf(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	version := 1
	if parsed, err := queryInt(c, "version"); err == nil && parsed != nil {
		version = *parsed
	}

	var captchaToken *string
	if raw, err := c.GetRawData(); err == nil && len(raw) > 0 {
		var token *string
		if err := json.Unmarshal(raw, &token); err != nil {
			validationError(c, "captchaToken", "Invalid captcha token.")
			return
		}
		captchaToken = token
	}

	backdated, present, err := queryInstant(c, "backdated")
	if err != nil {
		validationError(c, "backdated", "The backdated value is not a valid instant.")
		return
	}
	ctx := c.Request.Context()

	if !present {
		available, err := d.Service.CheckInAvailable(ctx, accountID)
		if err != nil {
			internalError(c, err)
			return
		}
		if !available {
			c.JSON(http.StatusBadRequest, errs.New("PASSPORT_CHECK_IN_NOT_AVAILABLE", "Check-in is not available for today.", http.StatusBadRequest))
			return
		}
	} else {
		if !d.Service.HasPerkSubscription(ctx, user.Id) {
			c.JSON(http.StatusForbidden, errs.New("UNAUTHORIZED", "You need to have a subscription to check-in backdated.", http.StatusForbidden))
			return
		}
		available, err := d.Service.BackdatedAvailable(ctx, accountID, *backdated)
		if err != nil {
			internalError(c, err)
			return
		}
		if !available {
			c.JSON(http.StatusBadRequest, errs.New("PASSPORT_CHECK_IN_BACKDATED_NOT_AVAILABLE", "Check-in is not available for this date.", http.StatusBadRequest))
			return
		}
	}

	if d.Service.NeedsCaptcha(ctx, user.Id) {
		if captchaToken == nil || strings.TrimSpace(*captchaToken) == "" {
			c.JSON(http.StatusLocked, errs.New("PASSPORT_CAPTCHA_REQUIRED", "Captcha is required for this check-in.", http.StatusLocked))
			return
		}
		valid, err := d.Service.ValidateCaptcha(ctx, *captchaToken)
		if err != nil {
			d.logWarn("validate captcha", "account", user.Id, "error", err)
		}
		if !valid {
			validationError(c, "captchaToken", "Invalid captcha token.")
			return
		}
	}

	result, err := d.Service.DoCheckIn(ctx, user, backdated, version)
	switch {
	case errors.Is(err, accountevents.ErrCheckInNotAvailable):
		c.JSON(http.StatusBadRequest, errs.New("PASSPORT_CHECK_IN_NOT_AVAILABLE", "Check-in is not available for today.", http.StatusBadRequest))
	case errors.Is(err, accountevents.ErrCheckInBackdatedNotAvailable):
		c.JSON(http.StatusBadRequest, errs.New("PASSPORT_CHECK_IN_BACKDATED_NOT_AVAILABLE", "Check-in is not available for this date.", http.StatusBadRequest))
	case err != nil:
		c.JSON(http.StatusBadRequest, &errs.ApiError{
			Code:    "PASSPORT_CHECK_IN_FAILED",
			Message: "Check-in failed.",
			Detail:  new(err.Error()),
			Status:  http.StatusBadRequest,
		})
		return
	}
	c.JSON(http.StatusOK, result)
}

func (d Deps) logWarn(message string, args ...any) {
	if d.Log != nil {
		d.Log.Warn(message, args...)
	}
}
