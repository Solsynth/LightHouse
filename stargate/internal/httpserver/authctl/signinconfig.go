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
// may be used, and how, so the console never has to guess:
//
//   - `cookie_hosts` share the session cookie, so a plain navigation lands
//     there signed in.
//   - `handoff_hosts` are registered hand-off clients whose hosts the cookie
//     cannot reach; those finish by a single-use code exchange.
//   - `require_https` mirrors [auth] cookieSecure — with a Secure cookie an
//     http target would arrive signed out.
//
// The split matters because the two trips are not interchangeable: asking the
// issuer for a code on a cookie host is refused (no client is registered for
// it), which would report a refused hand-off even though the session had
// already reached the app.
func (h *handler) signInConfig(c *gin.Context) {
	cfg := h.d.Cfg
	if cfg == nil {
		c.JSON(http.StatusOK, gin.H{
			"cookie_hosts":  []string{},
			"handoff_hosts": []string{},
			"require_https": false,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"cookie_hosts":  cfg.SignInCookieHosts(),
		"handoff_hosts": cfg.SignInHandoffHosts(),
		"require_https": cfg.SignInRequireHTTPS(),
	})
}
