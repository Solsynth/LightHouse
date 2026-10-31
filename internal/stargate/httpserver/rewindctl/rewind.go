// Package rewindctl ports Passport's AccountRewindController: the public
// sharable-code read, the authenticated current-year read (create on demand)
// and the public/private sharing toggles.
package rewindctl

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"src.solsynth.dev/sosys/go/pkg/errs"

	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/permission"
	"src.solsynth.dev/sosys/stargate/rewind"
)

// Deps is the dependency set the rewind controller needs.
type Deps struct {
	Service *rewind.Service
	Perm    middleware.PermissionChecker
	Log     *slog.Logger
}

// Register mounts /api/rewind.
func Register(api *gin.RouterGroup, d Deps) {
	group := api.Group("/rewind")
	group.GET("/me", middleware.RequireAuth(), d.getMine)
	group.GET("/:code", d.getPublic)
	group.POST("/me/:year/public", middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.RewindCreate), d.setPublic)
	group.POST("/me/:year/private", middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.RewindCreate), d.setPrivate)
}

func (d Deps) getPublic(c *gin.Context) {
	point, err := d.Service.GetPublic(c.Request.Context(), c.Param("code"))
	if err != nil {
		d.internal(c, err)
		return
	}
	if point == nil {
		c.JSON(http.StatusNotFound, errs.New(
			"PASSPORT_REWIND_POINT_NOT_FOUND", "Rewind point not found.", http.StatusNotFound))
		return
	}
	c.JSON(http.StatusOK, point)
}

func (d Deps) getMine(c *gin.Context) {
	accountID, ok := currentAccountID(c)
	if !ok {
		return
	}
	point, err := d.Service.GetOrCreate(c.Request.Context(), accountID)
	if err != nil {
		d.internal(c, err)
		return
	}
	c.JSON(http.StatusOK, point)
}

func (d Deps) setPublic(c *gin.Context) {
	accountID, year, ok := d.accountAndYear(c)
	if !ok {
		return
	}
	point, err := d.Service.SetPublic(c.Request.Context(), accountID, year)
	if err != nil {
		d.handleSetError(c, err, "PASSPORT_REWIND_SET_PUBLIC_FAILED")
		return
	}
	c.JSON(http.StatusOK, point)
}

func (d Deps) setPrivate(c *gin.Context) {
	accountID, year, ok := d.accountAndYear(c)
	if !ok {
		return
	}
	point, err := d.Service.SetPrivate(c.Request.Context(), accountID, year)
	if err != nil {
		d.handleSetError(c, err, "PASSPORT_REWIND_SET_PRIVATE_FAILED")
		return
	}
	c.JSON(http.StatusOK, point)
}

func (d Deps) accountAndYear(c *gin.Context) (uuid.UUID, int, bool) {
	accountID, ok := currentAccountID(c)
	if !ok {
		return uuid.Nil, 0, false
	}
	year, err := strconv.Atoi(c.Param("year"))
	if err != nil || year <= 0 {
		c.JSON(http.StatusBadRequest, errs.BadRequest("PASSPORT_REWIND_INVALID_YEAR", "Invalid year."))
		return uuid.Nil, 0, false
	}
	return accountID, year, true
}

func (d Deps) handleSetError(c *gin.Context, err error, code string) {
	if errors.Is(err, rewind.ErrPointNotFound) {
		c.JSON(http.StatusBadRequest, errs.BadRequest(code, err.Error()))
		return
	}
	d.internal(c, err)
}

func currentAccountID(c *gin.Context) (uuid.UUID, bool) {
	account := middleware.CurrentUser(c.Request.Context())
	if account == nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return uuid.Nil, false
	}
	id, err := uuid.Parse(account.Id)
	if err != nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return uuid.Nil, false
	}
	return id, true
}

func (d Deps) internal(c *gin.Context, err error) {
	if d.Log != nil {
		d.Log.Error("rewind request failed", "error", err)
	}
	c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Internal server error.", http.StatusInternalServerError))
}
