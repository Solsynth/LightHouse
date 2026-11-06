package middleware

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
)

// PermissionChecker is the local permission evaluation contract (implemented
// by internal/permission.Service).
type PermissionChecker interface {
	HasPermission(ctx context.Context, accountID uuid.UUID, key string) (bool, error)
}

// AskPermission returns a middleware that enforces a permission key against
// the current user, mirroring Padlock's [AskPermission(...)] attribute.
//
// The token scope is a filter, never a grant: an OAuth session must carry a
// scope covering the key ("*" removes the narrowing), while login, OIDC and
// API-key sessions are not filtered. Superusers then bypass the permission
// lookup, like the C# attribute and the fleet's authn.RequirePermission: an
// operator with is_superuser set must not need every permission node granted
// to the `moderator` group before the admin routes work.
func AskPermission(perm PermissionChecker, key string) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := CurrentUser(c.Request.Context())
		if user == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		if !ScopeAllows(c, key) {
			c.AbortWithStatusJSON(http.StatusForbidden, errs.New("FORBIDDEN", "Permission "+key+" was omitted by the token scope.", http.StatusForbidden))
			return
		}
		if user.IsSuperuser {
			c.Next()
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
