// Package httpserver wires the gin engine and hosts the /api route tree,
// mirroring Stargate's server.go (Ring serves no universal-link endpoints).
package httpserver

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/metoer/internal/config"
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
	engine.Use(authMiddleware)

	s := &Server{Engine: engine, cfg: cfg}

	engine.GET("/health", func(c *gin.Context) { c.Status(http.StatusOK) })
	engine.GET("/alive", func(c *gin.Context) { c.Status(http.StatusOK) })

	return s
}

// Register adds route registrars to the /api group.
func (s *Server) Register(registrars ...RouteRegistrar) {
	api := s.Engine.Group("/api")
	for _, r := range registrars {
		r(api)
	}
}
