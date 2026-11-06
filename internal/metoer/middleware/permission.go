package middleware

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

// AskPermission returns a middleware enforcing a permission key against the
// current user, mirroring the C# [AskPermission(...)] attribute +
// RemotePermissionMiddleware: scope filter for OAuth sessions, superuser
// bypass, then a gRPC DyPermissionService.HasPermission call (Stargate).
//
// The token scope is a filter, never a grant. A "*" scope only means the token
// asked for no narrowing; the permission service still decides. Skipping that
// check for scopes containing "*" let any app holding the wildcard exercise
// every permission of the signed-in account, including ones the account never
// held.
func AskPermission(permission gen.DyPermissionServiceClient, key string, log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := CurrentUser(c)
		if user == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required before permission checks can run."))
			return
		}
		session := CurrentSession(c)

		if session != nil && session.Type == gen.DySessionType_DY_OAUTH {
			if !ScopeAllows(session.Scopes, key) {
				matched, ok := MatchedScope(session.Scopes, key)
				if !ok {
					matched = "<none>"
				}
				if log != nil {
					log.Warn("permission omitted by token scope",
						"actor", user.Id, "required_key", key, "matched_scope", matched)
				}
				c.AbortWithStatusJSON(http.StatusForbidden, errs.Unauthorized("Permission "+key+" was omitted by token scope."))
				return
			}
		}

		if user.IsSuperuser {
			c.Next()
			return
		}

		if permission == nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Permission service is not configured.", http.StatusInternalServerError))
			return
		}

		resp, err := permission.HasPermission(c.Request.Context(), &gen.DyHasPermissionRequest{
			Actor: user.Id,
			Key:   key,
		})
		if err != nil {
			if log != nil {
				log.Error("gRPC call to PermissionService failed",
					"actor", user.Id, "required_key", key, "error", err)
			}
			c.AbortWithStatusJSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Error checking permissions.", http.StatusInternalServerError))
			return
		}
		if resp == nil || !resp.HasPermission {
			if log != nil {
				log.Warn("permission denied by permission service",
					"actor", user.Id, "required_key", key)
			}
			c.AbortWithStatusJSON(http.StatusForbidden, errs.Unauthorized("Permission "+key+" was required."))
			return
		}
		c.Next()
	}
}
