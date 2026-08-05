package securityctl

import (
	"log/slog"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestRegisterRoutes verifies the route table registers without conflicts.
func TestRegisterRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	api := engine.Group("/api")
	d := Deps{Log: slog.Default()}
	Register(api, d)
	for _, route := range engine.Routes() {
		t.Logf("%-7s %s", route.Method, route.Path)
	}
	if len(engine.Routes()) == 0 {
		t.Fatal("no routes registered")
	}
}
