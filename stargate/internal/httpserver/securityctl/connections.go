package securityctl

// Connection routes (ConnectionController port — the /api/connections surface
// only; the OIDC login/callback/connect routes live in socialctl).
//
// C# source:
//	../DysonNetwork/DysonNetwork.Padlock/Auth/OpenId/ConnectionController.cs

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

func connectionNotFound() *errs.ApiError {
	return errs.New("CONNECTION_NOT_FOUND", "Account connection was not found.", http.StatusNotFound)
}

// GET /api/connections
func (c *controller) getConnections(ctx *gin.Context) {
	user := middleware.CurrentUser(ctx.Request.Context())
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, authUnauthorized401())
		return
	}
	connections, err := c.d.Store.ListConnections(ctx.Request.Context(), user.Id)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load connections.", http.StatusInternalServerError))
		return
	}
	ctx.JSON(http.StatusOK, connections)
}

// DELETE /api/connections/{id}
func (c *controller) removeConnection(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, authUnauthorized401())
		return
	}
	id, ok := parseIDParam(ctx)
	if !ok {
		ctx.JSON(http.StatusNotFound, connectionNotFound())
		return
	}
	if _, err := c.d.Store.GetConnectionByID(reqCtx, user.Id, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			ctx.JSON(http.StatusNotFound, connectionNotFound())
			return
		}
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load connection.", http.StatusInternalServerError))
		return
	}
	if err := c.d.Store.DeleteConnectionRow(reqCtx, id); err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to delete connection.", http.StatusInternalServerError))
		return
	}
	ctx.Status(http.StatusOK)
}

type setConnectionVisibilityRequest struct {
	IsPublic bool `json:"is_public"`
}

// POST /api/connections/{id}/visibility
func (c *controller) setConnectionVisibility(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, authUnauthorized401())
		return
	}
	id, ok := parseIDParam(ctx)
	if !ok {
		ctx.JSON(http.StatusNotFound, connectionNotFound())
		return
	}
	var request setConnectionVisibilityRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, validationError(map[string][]string{
			"is_public": {"is_public is required."},
		}))
		return
	}
	connection, err := c.d.Store.GetConnectionByID(reqCtx, user.Id, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			ctx.JSON(http.StatusNotFound, connectionNotFound())
			return
		}
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load connection.", http.StatusInternalServerError))
		return
	}
	connection.IsPublic = request.IsPublic
	if err := c.d.Store.UpdateConnection(ctx, connection); err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to update connection.", http.StatusInternalServerError))
		return
	}
	ctx.JSON(http.StatusOK, connection)
}
