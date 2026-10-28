package realtime

import (
	"context"
	"testing"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/config"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/core"
	platformapp "src.solsynth.dev/sosys/lighthouse/internal/platform/app"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
)

// TestRouteRegistration registers the realtime routes on top of the chat
// module's route shapes to prove the static `realtime` segment does not
// conflict with the `:id` parameter siblings.
func TestRouteRegistration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	api := engine.Group("/api")
	noop := func(c *gin.Context) {}

	for _, shape := range [][2]string{
		{"GET", "/chat/:id"},
		{"GET", "/chat/:id/messages"},
		{"POST", "/chat/:id/messages"},
		{"GET", "/chat/:id/messages/:messageId"},
		{"PATCH", "/chat/:id/messages/:messageId"},
		{"DELETE", "/chat/:id/messages/:messageId"},
		{"POST", "/chat/:id/messages/:messageId/reactions"},
		{"GET", "/chat/:id/pins"},
		{"POST", "/chat/sync"},
		{"GET", "/chat"},
	} {
		api.Handle(shape[0], shape[1], noop)
	}

	svc := &core.Services{Env: &platformapp.Env{}, Config: &config.Config{}}
	host := &module.Host[core.Services]{
		Core:   svc,
		Engine: engine,
		API:    api,
		Ports:  module.NewPortRegistry(),
	}
	if err := Factory().New().Configure(context.Background(), host); err != nil {
		t.Fatalf("configure: %v", err)
	}
	for _, route := range engine.Routes() {
		t.Log(route.Method, route.Path)
	}
}
