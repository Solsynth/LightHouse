// Package routeinfo renders the gateway's public route table for operators
// and clients that need to know which service a path is routed to.
package routeinfo

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"srv.solsynth.dev/sosys/blade/config"
	"srv.solsynth.dev/sosys/blade/discovery"
)

// Document is the body of GET /.well-known/routes.
type Document struct {
	Aliases map[string]string `json:"aliases"`
	Routes  []Route           `json:"routes"`
	Counts  Counts            `json:"counts"`
}

// Route is one configured special route, with the service name resolved
// through the discovery alias table.
type Route struct {
	Path            string `json:"path"`
	Target          string `json:"target,omitempty"`
	Prefix          bool   `json:"prefix"`
	Service         string `json:"service"`
	ResolvedService string `json:"resolved_service"`
}

type Counts struct {
	Aliases int `json:"aliases"`
	Routes  int `json:"routes"`
}

// Build assembles the route document. registry may be nil when discovery is
// disabled; configured aliases are still reported and resolved in that case.
func Build(cfg *config.Config, registry *discovery.Registry) Document {
	document := Document{
		Aliases: make(map[string]string),
		Routes:  make([]Route, 0, len(cfg.Routes)),
	}
	if registry != nil {
		for alias, target := range registry.Aliases() {
			document.Aliases[alias] = target
		}
	} else {
		for alias, target := range cfg.Discovery.Aliases {
			document.Aliases[alias] = target
		}
	}

	for _, rule := range cfg.Routes {
		document.Routes = append(document.Routes, Route{
			Path:            rule.Path,
			Target:          rule.Target,
			Prefix:          rule.Prefix,
			Service:         rule.Service,
			ResolvedService: resolve(cfg, registry, rule.Service),
		})
	}

	document.Counts = Counts{
		Aliases: len(document.Aliases),
		Routes:  len(document.Routes),
	}
	return document
}

// Handler serves the route document.
func Handler(cfg *config.Config, registry *discovery.Registry) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, Build(cfg, registry))
	}
}

func resolve(cfg *config.Config, registry *discovery.Registry, service string) string {
	if registry != nil {
		return registry.Canonical(service)
	}
	service = strings.ToLower(strings.TrimSpace(service))
	if target, ok := cfg.Discovery.Aliases[service]; ok {
		return target
	}
	return service
}
