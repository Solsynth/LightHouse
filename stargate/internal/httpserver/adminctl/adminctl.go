// Package adminctl ports Padlock's admin HTTP surface (Phase 10):
// AccountAdminController, PermissionAdminController, CacheAdminController,
// AccountGeographyStatsAdminController, AccountActionLogController and
// AccountPunishmentController. Every admin route is permission-gated with
// the exact C# [AskPermission] keys; the two user-facing controllers
// (action log + punishments) keep their own auth requirements.
package adminctl

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/internal/actionlog"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/errs"
	"src.solsynth.dev/sosys/stargate/internal/grpcclient"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/permission"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/spell"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// Deps is the admin controller dependency bundle.
type Deps struct {
	Store   *store.Store
	Redis   *redis.Client
	Cfg     *config.Config
	Perm    *permission.Service
	Logs    *actionlog.Service
	Clients *grpcclient.Clients
	Spells  *spell.Service
	Log     *slog.Logger
}

// Register mounts every admin + related user-facing route on the api group.
func Register(api *gin.RouterGroup, d Deps) {
	admin := api.Group("/admin", middleware.RequireAuth())
	registerAccountAdmin(admin.Group("/accounts"), d)
	registerPermissionAdmin(admin.Group("/permissions"), d)
	registerCacheAdmin(admin.Group("/cache"), d)
	registerGeography(admin.Group("/stats/users/geography"), d)

	// User-facing routes from the ported controllers (Padlock served these
	// under /api as well; Stargate keeps the same paths).
	api.Group("/actions", middleware.RequireAuth(), middleware.RequireInteractive()).
		GET("", getActionLogs(d))
	punishments := api.Group("/accounts")
	punishments.GET("/me/punishments", middleware.RequireAuth(), getMyPunishments(d))
	punishments.GET("/:name/punishments", getAccountPunishments(d))
	punishments.GET("/:name/punishments/overview", getPunishmentOverview(d))
}

// requirePerm mirrors Padlock's LocalPermissionMiddleware: no authenticated
// user → 401; superuser bypass; otherwise every required key must pass
// (multiple [AskPermission] attributes are ANDed).
func requirePerm(d Deps, keys ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := middleware.CurrentUser(c.Request.Context())
		if user == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required before permission checks can run."))
			return
		}
		if user.IsSuperuser {
			c.Next()
			return
		}
		accountID, err := uuid.Parse(user.Id)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required before permission checks can run."))
			return
		}
		for _, key := range keys {
			ok, err := d.Perm.HasPermission(c.Request.Context(), accountID, key)
			if err != nil || !ok {
				c.AbortWithStatusJSON(http.StatusForbidden, errs.New("FORBIDDEN", "Permission "+key+" was required.", http.StatusForbidden))
				return
			}
		}
		c.Next()
	}
}

// currentUserID returns the authenticated account id or aborts with 401.
func currentUserID(c *gin.Context) (uuid.UUID, bool) {
	user := middleware.CurrentUser(c.Request.Context())
	if user == nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return uuid.Nil, false
	}
	id, err := uuid.Parse(user.Id)
	if err != nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return uuid.Nil, false
	}
	return id, true
}

// middlewareCurrentUser returns the authenticated account (nil when absent).
func middlewareCurrentUser(c *gin.Context) *model.Account {
	return middleware.CurrentUser(c.Request.Context())
}

// accountNotFound writes the canonical admin account-not-found error.
func accountNotFound(c *gin.Context) {
	c.JSON(http.StatusNotFound, errs.New("PADLOCK_ACCOUNT_NOT_FOUND", "Account not found.", http.StatusNotFound))
}

// lookupAccount resolves a name-or-GUID route identifier to an account,
// aborting with 404 when unknown.
func lookupAccount(c *gin.Context, d Deps, identifier string) *model.Account {
	account, err := d.Store.AdminLookupAccount(c.Request.Context(), identifier)
	if err != nil {
		if err == store.ErrNotFound {
			accountNotFound(c)
			return nil
		}
		c.JSON(http.StatusInternalServerError, errs.New("SERVER_ERROR", "An internal server error occurred.", http.StatusInternalServerError))
		return nil
	}
	return account
}

// queryTake clamps the take query parameter like the C# Math.Clamp(take, 1, 200).
func queryTake(c *gin.Context, def int) int {
	raw := c.Query("take")
	if raw == "" {
		return def
	}
	take, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if take < 1 {
		return 1
	}
	if take > 200 {
		return 200
	}
	return take
}

// queryOffset clamps the offset query parameter like Math.Max(0, offset).
func queryOffset(c *gin.Context) int {
	raw := c.Query("offset")
	if raw == "" {
		return 0
	}
	offset, err := strconv.Atoi(raw)
	if err != nil || offset < 0 {
		return 0
	}
	return offset
}

// setTotal writes the X-Total pagination header.
func setTotal(c *gin.Context, total int) {
	c.Header("X-Total", strconv.Itoa(total))
}

// parseTimeParam parses an optional ISO-8601 (or NodaTime fractional) query
// parameter.
func parseTimeParam(c *gin.Context, name string) (*time.Time, bool) {
	raw := c.Query(name)
	if raw == "" {
		return nil, true
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		parsed, err = time.Parse("2006-01-02T15:04:05.9999999Z07:00", raw)
	}
	if err != nil {
		return nil, false
	}
	t := parsed.UTC()
	return &t, true
}

// logAction writes an action log attributed to the given account (the C#
// admin flows log against the target account), best-effort.
func logAction(d Deps, ctx *gin.Context, accountID uuid.UUID, action model.ActionLogType, meta map[string]any) {
	if d.Logs == nil {
		return
	}
	if meta == nil {
		meta = map[string]any{}
	}
	ip := middleware.ClientIP(ctx.Request)
	_ = d.Logs.Create(ctx.Request.Context(), accountID.String(), action, meta, ctx.Request.UserAgent(), ip, nil, nil)
}

// clearActorPermissionCache removes the C#-side permission cache entries for
// an actor. The Go permission service is DB-backed, but the C# fleet still
// reads the perm:* keys, so the clear is kept for interop (best-effort).
func clearActorPermissionCache(d Deps, c *gin.Context, actor string) {
	d.Redis.ClearActorPermissionCache(c.Request.Context(), actor)
}
