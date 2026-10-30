package e2eectl

import (
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRegisterRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	api := engine.Group("/api")
	Register(api, Deps{})

	want := []string{
		"PUT /api/e2ee/mls/devices/me/kps",
		"GET /api/e2ee/mls/kp/status",
		"GET /api/e2ee/mls/keys/:accountId/devices",
		"POST /api/e2ee/mls/users/ready/batch",
		"GET /api/e2ee/mls/users/:accountId/ready",
		"GET /api/e2ee/mls/groups/:groupId/devices/capable",
		"POST /api/e2ee/mls/groups/:groupId/bootstrap",
		"POST /api/e2ee/mls/groups/:groupId/commit",
		"POST /api/e2ee/mls/groups/:groupId/welcome/fanout",
		"POST /api/e2ee/mls/groups/:groupId/reshare-required",
		"GET /api/e2ee/mls/devices/me/reshare-required",
		"POST /api/e2ee/mls/devices/me/reshare-required/:groupId/complete",
		"PUT /api/e2ee/mls/groups/:groupId/groupinfo",
		"GET /api/e2ee/mls/groups/:groupId/groupinfo",
		"POST /api/e2ee/mls/messages/fanout",
		"POST /api/e2ee/mls/groups/:groupId/commit/fanout",
		"POST /api/e2ee/mls/groups/:groupId/messages/fanout",
		"GET /api/e2ee/mls/envelopes/pending",
		"POST /api/e2ee/mls/envelopes/:envelopeId/ack",
		"POST /api/e2ee/mls/devices/:deviceId/revoke",
		"POST /api/e2ee/mls/devices/:deviceId/membership",
		"POST /api/e2ee/mls/groups/:groupId/reset",
	}
	routes := engine.Routes()
	got := map[string]bool{}
	for _, r := range routes {
		if len(r.Path) >= len("/api/e2ee") && r.Path[:len("/api/e2ee")] == "/api/e2ee" {
			got[r.Method+" "+r.Path] = true
		}
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing route %s", w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d e2ee routes, want %d", len(got), len(want))
	}
}
