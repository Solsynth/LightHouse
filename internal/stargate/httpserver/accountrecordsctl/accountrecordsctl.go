// Package accountrecordsctl mounts the account-record HTTP surface that used
// to live in Passport: the account's own actions/badges/leveling/credits/
// connections/board, the public badge/board/credit routes, and the admin
// badge/board/credit/verification routes.
package accountrecordsctl

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"src.solsynth.dev/sosys/go/pkg/errs"

	"src.solsynth.dev/sosys/stargate/accountrecords"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/permission"
	"src.solsynth.dev/sosys/stargate/store"
)

// Deps is the dependency set the controllers need.
type Deps struct {
	Store    *accountrecords.Store
	Accounts *store.Store
	Perm     middleware.PermissionChecker
	Log      *slog.Logger
}

// Register mounts the self and public routes on the /api group.
func Register(api *gin.RouterGroup, d Deps) {
	me := api.Group("/accounts/me")
	me.Use(middleware.RequireAuth())
	me.GET("/actions", d.getActions)
	me.GET("/badges", d.getBadges)
	me.POST("/badges/:id/active", d.requirePerm(permission.ProgressionBadgesManage), d.activateBadge)
	me.GET("/leveling", d.getLeveling)
	me.GET("/credits", d.getCredits)
	me.GET("/credits/history", d.getCreditsHistory)
	me.GET("/connections", d.getConnections)
	me.GET("/board", d.requirePerm(permission.AccountsProfileBoardManage), d.getBoard)
	me.PUT("/board", d.requirePerm(permission.AccountsProfileBoardManage), d.replaceBoard)

	accounts := api.Group("/accounts")
	accounts.GET("/:name/badges", d.getPublicBadges)
	accounts.GET("/:name/board", d.getPublicBoard)
	accounts.GET("/:name/credits", d.getPublicCredits)
}

// RegisterAdmin mounts the console routes on the /api group.
func RegisterAdmin(api *gin.RouterGroup, d Deps) {
	admin := api.Group("/admin/accounts", middleware.RequireAuth())
	admin.GET("/:name/badges", d.requirePerm(permission.AccountsView), d.adminGetBadges)
	admin.POST("/:name/badges", d.requirePerm(permission.ProgressionBadgesManage), d.adminGrantBadge)
	admin.POST("/:name/badges/:badgeId/active", d.requirePerm(permission.ProgressionBadgesManage), d.adminActivateBadge)
	admin.DELETE("/:name/badges/:badgeId", d.requirePerm(permission.ProgressionBadgesManage), d.adminRevokeBadge)

	admin.GET("/:name/board", d.requirePerm(permission.AccountsView), d.adminGetBoard)
	admin.PUT("/:name/board", d.requirePerm(permission.AccountsBoardManage), d.adminReplaceBoard)
	admin.POST("/:name/board/items/:itemId/payload", d.requirePerm(permission.AccountsBoardManage), d.adminUpdateBoardPayload)
	admin.DELETE("/:name/board/items/:itemId", d.requirePerm(permission.AccountsBoardManage), d.adminDeleteBoardItem)

	admin.POST("/:name/verification", d.requirePerm(permission.AccountsManage), d.adminSetVerification)
	admin.DELETE("/:name/verification", d.requirePerm(permission.AccountsManage), d.adminClearVerification)

	// Passport's route used `{name}`; gin needs one parameter name for the
	// whole tree, so every admin account route shares `:name`.
	admin.POST("/:name/credits", d.requirePerm(permission.CreditsValidatePerform), d.adminInvalidateCredits)
}

// requirePerm guards a route, returning 403 instead of panicking when the
// permission service is unavailable.
func (d Deps) requirePerm(key string) gin.HandlerFunc {
	if d.Perm == nil {
		return func(c *gin.Context) {
			c.AbortWithStatusJSON(http.StatusForbidden, errs.Forbidden("You do not have permission to perform this action."))
		}
	}
	return middleware.AskPermission(d.Perm, key)
}

func (d Deps) internalError(c *gin.Context, err error) {
	if d.Log != nil {
		d.Log.Error("account records request failed", "error", err)
	}
	c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Internal server error.", http.StatusInternalServerError))
}

// notFound mirrors the C# ApiError.NotFound(resource) shape for public routes.
func notFound(resource string) *errs.ApiError {
	msg := "The requested resource '" + resource + "' was not found."
	return &errs.ApiError{Code: "NOT_FOUND", Message: msg, Status: http.StatusNotFound, Detail: &resource}
}

// accountNotFound is the admin route's canonical missing-account error.
func accountNotFound(c *gin.Context) {
	c.JSON(http.StatusNotFound, errs.New("PASSPORT_ACCOUNT_NOT_FOUND", "Account not found.", http.StatusNotFound))
}

func (d Deps) lookupAdminAccount(c *gin.Context) (*model.Account, bool) {
	if d.Accounts == nil {
		d.internalError(c, errors.New("account store unavailable"))
		return nil, false
	}
	account, err := d.Accounts.AdminLookupAccount(c.Request.Context(), c.Param("name"))
	if err != nil {
		if err == store.ErrNotFound {
			accountNotFound(c)
			return nil, false
		}
		d.internalError(c, err)
		return nil, false
	}
	return account, true
}
