// Package httpx wires the gin engine every Lighthouse service uses: the
// middleware chain that mirrors the C# ConfigureAppMiddleware, the canonical
// ApiError responses, and pagination helpers that match the fleet's
// offset/take + X-Total convention.
package httpx

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"src.solsynth.dev/sosys/go/pkg/errs"
)

// Options configures the engine.
type Options struct {
	Log *slog.Logger
	// Auth runs on every request. It mirrors the C# DysonTokenAuthHandler:
	// requests without a token pass through unauthenticated, handlers that
	// require auth call RequireAuth.
	Auth gin.HandlerFunc
	// TrustedProxyHops is how many proxies in front of the service append to
	// X-Forwarded-For. Used by ClientIP.
	TrustedProxyHops int
}

// Server hosts the HTTP API.
type Server struct {
	Engine *gin.Engine
	api    *gin.RouterGroup
	opts   Options
}

// New builds the gin engine with the middleware chain and health endpoints.
func New(opts Options) *Server {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.TrustedProxyHops < 1 {
		opts.TrustedProxyHops = 1
	}
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Use(gin.Recovery())
	engine.Use(requestID())
	engine.Use(accessLog(opts.Log))

	engine.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "Healthy"})
	})
	engine.GET("/alive", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "Healthy"})
	})
	engine.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "Healthy"})
	})

	// The auth middleware must be attached to the engine BEFORE the /api group
	// is created: a gin group snapshots the engine's handler chain when it is
	// built, so middleware added afterwards would not run for its routes.
	if opts.Auth != nil {
		engine.Use(opts.Auth)
	}
	api := engine.Group("/api")
	engine.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, errs.NotFound("The requested resource was not found."))
	})
	return &Server{Engine: engine, api: api, opts: opts}
}

// API returns the shared /api router group. Modules register their routes
// here; the group exists once so middleware order is stable.
func (s *Server) API() *gin.RouterGroup { return s.api }

// requestID attaches an X-Trace-Id header to every response, matching the
// fleet's ApiError.traceId source.
func requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Trace-Id")
		if id == "" {
			id = uuid.NewString()
		}
		c.Set("trace_id", id)
		c.Header("X-Trace-Id", id)
		c.Next()
	}
}

// TraceID returns the request trace id.
func TraceID(c *gin.Context) string {
	if v, ok := c.Get("trace_id"); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func accessLog(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Debug("http request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"duration", time.Since(start),
		)
	}
}

// ClientIP resolves the client IP honoring X-Forwarded-For the way Stargate
// does: only the rightmost entries are trustworthy because they were appended
// by our own proxies.
func ClientIP(r *http.Request, trustedHops int) string {
	if trustedHops < 1 {
		trustedHops = 1
	}
	if r == nil {
		return ""
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		index := len(parts) - trustedHops
		if index < 0 {
			index = 0
		}
		if parts[index] != "" {
			return parts[index]
		}
	}
	host := r.RemoteAddr
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		host = host[:idx]
	}
	return host
}
