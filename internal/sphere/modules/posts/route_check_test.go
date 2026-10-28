package posts

import (
	"testing"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/app"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
)

func TestRouteRegistration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	group := engine.Group("/api")
	svc := &core.Services{Env: &app.Env{}}
	host := &module.Host[core.Services]{API: group, Core: svc, Ports: module.NewPortRegistry(), Engine: engine}
	h := &handler{core: svc, ports: host.Ports}
	h.registerRoutes(host)
	seen := map[string]bool{}
	for _, r := range engine.Routes() {
		key := r.Method + " " + r.Path
		if seen[key] {
			t.Fatalf("duplicate route %s", key)
		}
		seen[key] = true
	}
	for _, r := range engine.Routes() {
		t.Logf("%s %s", r.Method, r.Path)
	}
}
