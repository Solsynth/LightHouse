// Package wellbeingctl ports Passport's account activity statistics and
// night-owl reminder HTTP surface:
//
//	GET  /api/admin/stats
//	GET  /api/admin/accounts/metrics/activity
//	POST /api/admin/accounts/:name/notifications/night-owl
//
// Paths, permission keys, error codes and JSON field names match
// AccountStatsAdminController and AccountAdminController.
package wellbeingctl

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/permission"
	"src.solsynth.dev/sosys/stargate/store"
	"src.solsynth.dev/sosys/stargate/wellbeing"
)

// Deps is the controller dependency bundle.
type Deps struct {
	Service *wellbeing.Service
	Store   *store.Store
	Perm    middleware.PermissionChecker
	Log     *slog.Logger
}

// Register mounts the wellbeing admin routes under /api.
func Register(api *gin.RouterGroup, d Deps) {
	admin := api.Group("/admin", middleware.RequireAuth())
	admin.GET("/stats", requirePermission(d, permission.AccountsView), d.getStats)
	admin.GET("/accounts/metrics/activity", requirePermission(d, permission.AccountsView), d.getActivity)
	admin.POST("/accounts/:name/notifications/night-owl",
		requirePermission(d, permission.AccountsManage), d.sendNightOwl)
}

// requirePermission mirrors adminctl.requirePerm: no authenticated user →
// 401; superuser bypass; otherwise the key must pass.
func requirePermission(d Deps, key string) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := middleware.CurrentUser(c.Request.Context())
		if user == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized,
				errs.Unauthorized("Authentication is required before permission checks can run."))
			return
		}
		if user.IsSuperuser {
			c.Next()
			return
		}
		if d.Perm == nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError,
				errs.New("INTERNAL_ERROR", "Permission check failed.", http.StatusInternalServerError))
			return
		}
		accountID, err := uuid.Parse(user.Id)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized,
				errs.Unauthorized("Authentication is required before permission checks can run."))
			return
		}
		ok, err := d.Perm.HasPermission(c.Request.Context(), accountID, key)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError,
				errs.New("INTERNAL_ERROR", "Permission check failed.", http.StatusInternalServerError))
			return
		}
		if !ok {
			c.AbortWithStatusJSON(http.StatusForbidden,
				errs.New("FORBIDDEN", "Permission "+key+" was required.", http.StatusForbidden))
			return
		}
		c.Next()
	}
}

func (d Deps) logger() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

// getStats answers GET /api/admin/stats.
func (d Deps) getStats(c *gin.Context) {
	if d.Service == nil {
		c.JSON(http.StatusInternalServerError,
			errs.New("INTERNAL_ERROR", "Failed to calculate account stats.", http.StatusInternalServerError))
		return
	}
	stats, err := d.Service.Stats(c.Request.Context(), time.Now().UTC())
	if err != nil {
		d.logger().Warn("account stats calculation failed", "error", err)
		c.JSON(http.StatusInternalServerError,
			errs.New("INTERNAL_ERROR", "Failed to calculate account stats.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, stats)
}

// getActivity answers GET /api/admin/accounts/metrics/activity.
func (d Deps) getActivity(c *gin.Context) {
	if d.Service == nil {
		c.JSON(http.StatusInternalServerError,
			errs.New("INTERNAL_ERROR", "Failed to calculate account activity metrics.", http.StatusInternalServerError))
		return
	}
	activity, err := d.Service.Activity(c.Request.Context(), time.Now().UTC())
	if err != nil {
		d.logger().Warn("account activity metrics calculation failed", "error", err)
		c.JSON(http.StatusInternalServerError,
			errs.New("INTERNAL_ERROR", "Failed to calculate account activity metrics.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, activity)
}

type nightOwlRequest struct {
	Hour *int `json:"hour"`
}

// sendNightOwl answers POST
// /api/admin/accounts/:name/notifications/night-owl.
func (d Deps) sendNightOwl(c *gin.Context) {
	hour := 0
	if c.Request.Body != nil {
		var payload nightOwlRequest
		err := json.NewDecoder(c.Request.Body).Decode(&payload)
		if err != nil && !errors.Is(err, io.EOF) {
			c.JSON(http.StatusBadRequest,
				errs.BadRequest("VALIDATION_ERROR", "The request body is invalid."))
			return
		}
		if payload.Hour != nil {
			hour = *payload.Hour
		}
	}
	if hour < 0 || hour > 2 {
		c.JSON(http.StatusBadRequest, errs.New(
			"PASSPORT_NIGHT_OWL_REMINDER_HOUR_INVALID",
			"Hour must be between 0 and 2.",
			http.StatusBadRequest))
		return
	}

	if d.Store == nil || d.Service == nil {
		c.JSON(http.StatusInternalServerError,
			errs.New("INTERNAL_ERROR", "Night owl reminder cannot be sent.", http.StatusInternalServerError))
		return
	}
	account, err := d.Store.AdminLookupAccount(c.Request.Context(), c.Param("name"))
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, errs.New(
			"PASSPORT_ACCOUNT_NOT_FOUND", "Account not found.", http.StatusNotFound))
		return
	}
	if err != nil {
		d.logger().Warn("night owl: account lookup failed", "error", err)
		c.JSON(http.StatusInternalServerError,
			errs.New("INTERNAL_ERROR", "Failed to load the account.", http.StatusInternalServerError))
		return
	}
	if account == nil {
		c.JSON(http.StatusNotFound, errs.New(
			"PASSPORT_ACCOUNT_NOT_FOUND", "Account not found.", http.StatusNotFound))
		return
	}

	variant := rand.IntN(3) + 1
	err = d.Service.SendNightOwl(c.Request.Context(), account.Id, account.Language, hour, variant)
	if errors.Is(err, wellbeing.ErrPushUnavailable) {
		c.JSON(http.StatusServiceUnavailable,
			errs.New("SERVICE_UNAVAILABLE", "Push notification delivery is not available.", http.StatusServiceUnavailable))
		return
	}
	if err != nil {
		d.logger().Warn("night owl: push failed", "account_id", account.Id, "error", err)
		c.JSON(http.StatusInternalServerError,
			errs.New("INTERNAL_ERROR", "Failed to send the push notification.", http.StatusInternalServerError))
		return
	}
	c.Status(http.StatusNoContent)
}
