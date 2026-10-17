// Package httpserver wires the gin engine with the middleware chain mirroring
// Padlock's ConfigureAppMiddleware and hosts the /api route tree.
package httpserver

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/webui"
)

// Server hosts the HTTP API.
type Server struct {
	Engine *gin.Engine
	cfg    *config.Config
	api    *gin.RouterGroup
	// WebError reports why the SPA was not mounted (missing dist directory or
	// no index.html). The server stays API-only in that case.
	WebError error
}

// RouteRegistrar registers routes on the /api group.
type RouteRegistrar func(api *gin.RouterGroup)

// New builds the gin engine with the middleware chain and health endpoints.
// When [web] distPath is set, the built SPA is mounted here: existing dist
// files are served before the auth middleware (a fingerprinted asset must not
// cost a session lookup) and the NoRoute handler returns index.html for
// client-side routes.
func New(cfg *config.Config, authMiddleware gin.HandlerFunc) *Server {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Use(gin.Recovery())

	s := &Server{Engine: engine, cfg: cfg}

	if cfg != nil && strings.TrimSpace(cfg.Web.DistPath) != "" {
		web, err := webui.New(cfg.Web.DistPath)
		if err != nil {
			s.WebError = err
		} else {
			engine.Use(web.ServeStatic)
			engine.NoRoute(web.Serve)
		}
	}

	engine.Use(authMiddleware)

	// The client IP is derived from X-Forwarded-For by the configured trusted
	// proxy depth, so the edge proxy must append to (not pass through) the
	// header. Default 1 = Blade.
	if cfg != nil {
		middleware.SetTrustedProxyHops(cfg.Security.TrustedProxyHopCount())
	}

	engine.GET("/health", func(c *gin.Context) { c.Status(http.StatusOK) })
	engine.GET("/alive", func(c *gin.Context) { c.Status(http.StatusOK) })

	// Apple/Android universal links (ported from Padlock's
	// ConfigureAppMiddleware).
	engine.GET("/.well-known/apple-app-site-association", s.appleAppSiteAssociation)
	engine.GET("/.well-known/assetlinks.json", s.assetLinks)
	engine.GET("/.well-known/security.txt", s.securityTxt)

	s.api = engine.Group("/api")

	return s
}

// API returns the shared /api router group. Modules register their routes
// here; the group exists once so middleware order is stable.
func (s *Server) API() *gin.RouterGroup {
	return s.api
}

// securityTxt serves the RFC 9116 vulnerability-disclosure document. The
// contact comes from [securityTxt] contact with a built-in default.
func (s *Server) securityTxt(c *gin.Context) {
	expires := time.Now().UTC().AddDate(1, 0, 0).Format(time.RFC3339)
	contact := config.DefaultSecurityContact
	var canonical string
	if s.cfg != nil {
		contact = s.cfg.SecurityContact()
		canonical = strings.TrimSuffix(s.cfg.BaseUrl, "/") + "/.well-known/security.txt"
	}
	lines := []string{
		"Contact: " + contact,
		"Expires: " + expires,
		"Preferred-Languages: en, zh",
	}
	if canonical != "" {
		lines = append(lines, "Canonical: "+canonical)
	}
	lines = append(lines, "")
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(strings.Join(lines, "\n")))
}

// Register adds route registrars to the /api group.
func (s *Server) Register(registrars ...RouteRegistrar) {
	for _, r := range registrars {
		r(s.api)
	}
}

func (s *Server) appleAppSiteAssociation(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"applinks": gin.H{
			"details": []gin.H{{
				"appIDs":     []string{"W7HPZ53V6B.dev.solsynth.solian"},
				"components": appLinkComponents(),
			}},
		},
		"webcredentials": gin.H{"apps": []string{"W7HPZ53V6B.dev.solsynth.solian"}},
		"appclips":       gin.H{"apps": []string{}},
	})
}

func (s *Server) assetLinks(c *gin.Context) {
	c.JSON(http.StatusOK, []gin.H{{
		"relation": []string{"delegate_permission/common.handle_all_urls"},
		"target": gin.H{
			"namespace":                "android_app",
			"package_name":             "dev.solsynth.solian",
			"sha256_cert_fingerprints": []string{},
		},
		"relation_extensions": gin.H{
			"delegate_permission/common.handle_all_urls": gin.H{
				"dynamic_app_link_components": appLinkComponents(),
			},
		},
	}})
}

// appLinkComponents mirrors Padlock's AppLinkComponents route table (kept in
// sync with the Island client route table). The same path rules are emitted
// for Apple's AASA format and Android 15+ dynamic App Links.
func appLinkComponents() []gin.H {
	patterns := []string{
		"/", "/explore", "/chat", "/chat/search", "/chat/*/detail", "/chat/*/search",
		"/chat/*", "/realms", "/account", "/account/stickers", "/account/stickers/*",
		"/account/relationships", "/account/me/update", "/account/me/activation",
		"/account/me/board", "/account/me/leveling", "/account/me/stellar-program",
		"/account/me/settings", "/account/me/qr", "/account/me/badges", "/account/me/progress",
		"/account/me/meet", "/account/me/meet/*", "/account/me/action-logs",
		"/account/me/physical-passports", "/account/tickets", "/account/tickets/*",
		"/account/me/punishments", "/account/me/affiliations", "/account/me/affiliations/*",
		"/files", "/files/*", "/creators", "/creators/*/posts", "/creators/*/collections",
		"/creators/*/surveys", "/creators/*/stickers", "/creators/*/stickers/*",
		"/creators/*/domains", "/creators/*/tags", "/wallet", "/wallet/transactions/*",
		"/articles/compose", "/articles/*/edit", "/blogs/compose", "/blogs/*/edit",
		"/auth/login", "/auth/create-account", "/auth/authorize", "/settings",
		"/settings/chat-room-storage", "/plugins", "/plugins/editor", "/about",
		"/cf-ip-speed-test", "/posts/shuffle", "/posts/bookmarks", "/posts/categories",
		"/posts/categories/*", "/posts/*", "/publishers/*", "/fediverse/actors/*",
		"/accounts/*", "/search", "/calendar/*/events/*", "/calendar/*", "/realms/*",
		"/surveys/*", "/orders/*",
	}
	components := make([]gin.H, 0, len(patterns))
	for _, pattern := range patterns {
		components = append(components, gin.H{"/": pattern})
	}
	return components
}

// CaptchaRateLimiter is a fixed-window limiter for the captcha endpoints
// (5 req/min per IP, mirroring AddFixedWindowLimiter("captcha")).
type CaptchaRateLimiter struct {
	mu     sync.Mutex
	window time.Duration
	limit  int
	hits   map[string][]time.Time
}

// NewCaptchaRateLimiter creates a 5/min fixed-window limiter.
func NewCaptchaRateLimiter() *CaptchaRateLimiter {
	return &CaptchaRateLimiter{window: time.Minute, limit: 5, hits: map[string][]time.Time{}}
}

// Allow reports whether the client is within the rate limit.
func (l *CaptchaRateLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-l.window)
	hits := l.hits[key]
	kept := hits[:0]
	for _, t := range hits {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.limit {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}
