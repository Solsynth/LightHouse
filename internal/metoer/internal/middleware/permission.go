package middleware

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/go/pkg/errs"
)

// presetScopes mirrors PermissionScopeGate.PresetScopes (case-insensitive).
var presetScopes = map[string]struct{}{
	"openid": {},
	"profile": {},
	"email":  {},
	"*":      {},
}

// AskPermission returns a middleware enforcing a permission key against the
// current user, mirroring the C# [AskPermission(...)] attribute +
// RemotePermissionMiddleware: scope gate for OAuth sessions, superuser
// bypass, then a gRPC DyPermissionService.HasPermission call (Stargate).
func AskPermission(permission gen.DyPermissionServiceClient, key string, log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := CurrentUser(c)
		if user == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required before permission checks can run."))
			return
		}
		session := CurrentSession(c)

		if session != nil && hasFullScope(session.Scopes) {
			c.Next()
			return
		}

		if session != nil && session.Type == gen.DySessionType_DY_OAUTH {
			if !isPermissionEnabled(session.Scopes, key) {
				matched := getMatchedPermissionScope(session.Scopes, key)
				if matched == nil {
					matched = new("<none>")
				}
				if log != nil {
					log.Warn("permission omitted by token scope",
						"actor", user.Id, "required_key", key, "matched_scope", *matched)
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

// hasFullScope mirrors PermissionScopeGate.HasFullScope.
func hasFullScope(scopes []string) bool {
	for _, scope := range scopes {
		if scope == "*" {
			return true
		}
	}
	return false
}

// isPermissionEnabled mirrors PermissionScopeGate.IsPermissionEnabled.
func isPermissionEnabled(scopes []string, permissionKey string) bool {
	return getMatchedPermissionScope(scopes, permissionKey) != nil
}

// getMatchedPermissionScope mirrors PermissionScopeGate.GetMatchedPermissionScope.
func getMatchedPermissionScope(scopes []string, permissionKey string) *string {
	if strings.TrimSpace(permissionKey) == "" {
		return nil
	}
	for _, rawScope := range scopes {
		scope := strings.TrimSpace(rawScope)
		if scope == "" {
			continue
		}
		if strings.EqualFold(scope, "*") {
			return new("*")
		}
		if _, ok := presetScopes[strings.ToLower(scope)]; ok {
			continue
		}
		if strings.EqualFold(scope, permissionKey) {
			return new(scope)
		}
		if strings.HasSuffix(scope, ".*") {
			prefix := scope[:len(scope)-1]
			if strings.HasPrefix(strings.ToLower(permissionKey), strings.ToLower(prefix)) {
				return new(scope)
			}
		}
	}
	return nil
}
