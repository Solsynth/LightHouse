package middleware

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/internal/errs"
)

// PermissionChecker is the local permission evaluation contract (implemented
// by internal/permission.Service).
type PermissionChecker interface {
	HasPermission(ctx context.Context, accountID uuid.UUID, key string) (bool, error)
}

// AskPermission returns a middleware that enforces a permission key against
// the current user, mirroring Padlock's [AskPermission(...)] attribute.
func AskPermission(perm PermissionChecker, key string) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := CurrentUser(c.Request.Context())
		if user == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		accountID, err := uuid.Parse(user.Id)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		ok, err := perm.HasPermission(c.Request.Context(), accountID, key)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Permission check failed.", http.StatusInternalServerError))
			return
		}
		if !ok {
			c.AbortWithStatusJSON(http.StatusForbidden, errs.Forbidden("You do not have permission to perform this action."))
			return
		}
		c.Next()
	}
}
