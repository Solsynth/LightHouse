// Package httpserver wires the gin engine with the middleware chain mirroring
// Padlock's ConfigureAppMiddleware and hosts the /api route tree.
package httpserver

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/stargate/internal/config"
)

// Server hosts the HTTP API.
type Server struct {
	Engine *gin.Engine
	cfg    *config.Config
}

// RouteRegistrar registers routes on the /api group.
type RouteRegistrar func(api *gin.RouterGroup)

// New builds the gin engine with the middleware chain and health endpoints.
func New(cfg *config.Config, authMiddleware gin.HandlerFunc) *Server {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Use(gin.Recovery())
	engine.Use(corsMiddleware())
	engine.Use(authMiddleware)

	s := &Server{Engine: engine, cfg: cfg}

	engine.GET("/health", func(c *gin.Context) { c.Status(http.StatusOK) })
	engine.GET("/alive", func(c *gin.Context) { c.Status(http.StatusOK) })

	// Apple/Android universal links (ported from Padlock's
	// ConfigureAppMiddleware).
	engine.GET("/.well-known/apple-app-site-association", s.appleAppSiteAssociation)
	engine.GET("/.well-known/assetlinks.json", s.assetLinks)

	return s
}

// Register adds route registrars to the /api group.
func (s *Server) Register(registrars ...RouteRegistrar) {
	api := s.Engine.Group("/api")
	for _, r := range registrars {
		r(api)
	}
}

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if origin := c.GetHeader("Origin"); origin != "" {
			// Echo the origin and allow credentials so cookie-based auth works
			// (browsers reject `*` with credentials). Mirror Blade's gateway
			// CORS (AllowOriginFunc → true); Blade's reverse proxy copies these
			// response headers through verbatim, so the values must match.
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Credentials", "true")
		} else {
			c.Header("Access-Control-Allow-Origin", "*")
		}
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Device-Id, X-Auth-Session, tk")
		c.Header("Access-Control-Expose-Headers", "X-Total, X-Auth-Session")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
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
	}})
}

// appLinkComponents mirrors Padlock's AppLinkComponents route table (kept in
// sync with the Island client route table).
func appLinkComponents() []gin.H {
	patterns := []string{
		"/", "/explore", "/chat", "/chat/search", "/chat/*/detail", "/chat/*/search",
		"/chat/*", "/realms", "/account", "/account/stickers", "/account/stickers/*",
		"/account/relationships", "/account/me/update", "/account/me/activation",
		"/account/me/board", "/account/me/leveling", "/account/me/settings", "/account/me/qr",
		"/account/me/badges", "/account/me/progress", "/account/me/meet", "/account/me/meet/*",
		"/account/me/action-logs", "/account/me/physical-passports", "/account/tickets",
		"/account/tickets/*", "/account/me/punishments", "/account/me/affiliations",
		"/account/me/affiliations/*", "/files", "/files/*", "/creators", "/creators/*/posts",
		"/creators/*/collections", "/creators/*/surveys", "/creators/*/stickers",
		"/creators/*/stickers/*", "/creators/*/domains", "/creators/*/tags", "/wallet",
		"/wallet/transactions/*", "/articles/compose", "/articles/*/edit", "/blogs/compose",
		"/blogs/*/edit", "/auth/login", "/auth/create-account", "/auth/authorize", "/settings",
		"/settings/chat-room-storage", "/plugins", "/plugins/editor", "/about", "/cf-ip-speed-test",
		"/posts/shuffle", "/posts/bookmarks", "/posts/categories", "/posts/categories/*",
		"/posts/*", "/publishers/*", "/fediverse/actors/*", "/accounts/*", "/search",
		"/calendar/*/events/*", "/calendar/*", "/realms/*", "/surveys/*", "/orders/*",
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
