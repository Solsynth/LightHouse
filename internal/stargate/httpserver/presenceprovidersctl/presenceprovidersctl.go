// Package presenceprovidersctl ports the admin half of Passport's
// AccountAdminController presence routes. They scan Steam and Last.fm
// connections on demand; the scheduled stages live in the
// presenceproviders package and in the module's Configure.
package presenceprovidersctl

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/permission"
	"src.solsynth.dev/sosys/stargate/presenceproviders"
	"src.solsynth.dev/sosys/stargate/store"
)

// Deps is the controller dependency bundle.
type Deps struct {
	Service *presenceproviders.Service
	Perm    middleware.PermissionChecker
	Log     *slog.Logger
}

// Register mounts the admin presence-scan routes on the /api group:
//
//	POST /api/admin/accounts/presences/steam/scan
//	POST /api/admin/accounts/presences/steam/scan/:name
//	POST /api/admin/accounts/presences/steam/scan-by-steam-id/:steamId
//	POST /api/admin/accounts/presences/steam/scan/stages/:stage
//
// The identifier segment is :name (not :identifier) because the admin account
// tree already owns a :name wildcard; the static paths are registered before
// it so gin's router resolves them unambiguously.
func Register(api *gin.RouterGroup, d Deps) {
	admin := api.Group("/admin", middleware.RequireAuth())
	steam := admin.Group("/accounts/presences/steam")
	gate := middleware.AskPermission(d.Perm, permission.AccountsStatusesUpdate)

	steam.POST("/scan", gate, d.scanAllSteam)
	steam.POST("/scan/stages/:stage", gate, d.scanSteamStage)
	steam.POST("/scan/:name", gate, d.scanSteamAccount)
	steam.POST("/scan-by-steam-id/:steamId", gate, d.scanSteamByID)
}

func (d Deps) scanAllSteam(c *gin.Context) {
	result, err := d.Service.ScanAllSteam(c.Request.Context())
	if err != nil {
		internalError(c, d, "scan all steam presences", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (d Deps) scanSteamAccount(c *gin.Context) {
	ctx := c.Request.Context()
	account, err := d.Service.LookupAccount(ctx, c.Param("name"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			accountNotFound(c)
			return
		}
		internalError(c, d, "lookup account for steam scan", err)
		return
	}
	accountID, err := uuid.Parse(account.Id)
	if err != nil {
		accountNotFound(c)
		return
	}
	result, err := d.Service.ScanSteamUsers(ctx, []uuid.UUID{accountID})
	if err != nil {
		internalError(c, d, "scan account steam presence", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (d Deps) scanSteamByID(c *gin.Context) {
	result, err := d.Service.ScanSteamBySteamID(c.Request.Context(), c.Param("steamId"))
	if err != nil {
		if errors.Is(err, presenceproviders.ErrAccountNotFound) {
			accountNotFound(c)
			return
		}
		internalError(c, d, "scan steam presence by steam id", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (d Deps) scanSteamStage(c *gin.Context) {
	stage, ok := presenceproviders.ParseStage(c.Param("stage"))
	if !ok {
		c.JSON(http.StatusBadRequest, errs.New("PASSPORT_INVALID_STAGE",
			"Invalid stage. Expected one of: active, maybe, cold.", http.StatusBadRequest))
		return
	}
	result, err := d.Service.ScanSteamStage(c.Request.Context(), stage)
	if err != nil {
		internalError(c, d, "scan steam presence stage", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func accountNotFound(c *gin.Context) {
	c.JSON(http.StatusNotFound, errs.New("PASSPORT_ACCOUNT_NOT_FOUND", "Account not found.", http.StatusNotFound))
}

func internalError(c *gin.Context, d Deps, action string, err error) {
	if d.Log != nil {
		d.Log.Error(action, "error", err)
	}
	c.JSON(http.StatusInternalServerError, errs.New("SERVER_ERROR", "An internal server error occurred.", http.StatusInternalServerError))
}
