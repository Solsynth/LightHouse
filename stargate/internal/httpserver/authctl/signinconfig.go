package authctl

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// GET /api/auth/sign-in/config
//
// The sign-in page is shared: another Solar Network app can open
// `{siteUrl}/sign-in?redirectUri={its callback}` and be handed the person back
// once they are signed in. This answers the policy that decides which targets
// may be used, so the console can refuse a target the server would refuse too
// instead of navigating away and losing the session on arrival.
//
// The set is derived, not configured: the session lives in the HttpOnly cookie
// scoped by [auth] cookieDomain, and a browser sends that cookie to every
// origin under the domain, so exactly the cookie's own hosts can complete a
// shared sign-in. `require_https` mirrors [auth] cookieSecure — with a Secure
// cookie an http hand-off would arrive signed out.
func (h *handler) signInConfig(c *gin.Context) {
	cfg := h.d.Cfg
	if cfg == nil {
		c.JSON(http.StatusOK, gin.H{"redirect_hosts": []string{}, "require_https": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"redirect_hosts": cfg.SignInRedirectHosts(),
		"require_https":  cfg.SignInRequireHTTPS(),
	})
}
