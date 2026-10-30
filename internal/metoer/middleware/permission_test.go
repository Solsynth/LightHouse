package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"src.solsynth.dev/sosys/go/pkg/auth"
	gen "src.solsynth.dev/sosys/go/proto"
)

// denyPermissionClient denies every lookup, like Stargate does for an account
// holding no node for the key.
type denyPermissionClient struct{}

func (denyPermissionClient) HasPermission(context.Context, *gen.DyHasPermissionRequest, ...grpc.CallOption) (*gen.DyHasPermissionResponse, error) {
	return &gen.DyHasPermissionResponse{HasPermission: false}, nil
}

func (denyPermissionClient) GetPermission(context.Context, *gen.DyGetPermissionRequest, ...grpc.CallOption) (*gen.DyGetPermissionResponse, error) {
	return nil, nil
}

func (denyPermissionClient) AddPermissionNode(context.Context, *gen.DyAddPermissionNodeRequest, ...grpc.CallOption) (*gen.DyAddPermissionNodeResponse, error) {
	return nil, nil
}

func (denyPermissionClient) AddPermissionNodeToGroup(context.Context, *gen.DyAddPermissionNodeToGroupRequest, ...grpc.CallOption) (*gen.DyAddPermissionNodeToGroupResponse, error) {
	return nil, nil
}

func (denyPermissionClient) RemovePermissionNode(context.Context, *gen.DyRemovePermissionNodeRequest, ...grpc.CallOption) (*gen.DyRemovePermissionNodeResponse, error) {
	return nil, nil
}

func (denyPermissionClient) RemovePermissionNodeFromGroup(context.Context, *gen.DyRemovePermissionNodeFromGroupRequest, ...grpc.CallOption) (*gen.DyRemovePermissionNodeFromGroupResponse, error) {
	return nil, nil
}

// Login sessions are issued carrying the full-grant wildcard scope (Stargate
// scopesWithFullScope). The wildcard only bypasses the permission chain for
// OAuth sessions: a login session has to reach the permission service, so a
// denied lookup still answers 403.
func TestAskPermission_FullScopeOnlyBypassesOAuthSessions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name    string
		session gen.DySessionType
		want    int
	}{
		{name: "login session", session: gen.DySessionType_DY_LOGIN, want: http.StatusForbidden},
		{name: "oidc session", session: gen.DySessionType_DY_OIDC, want: http.StatusForbidden},
		{name: "api key session", session: gen.DySessionType_DY_API_KEY, want: http.StatusForbidden},
		{name: "oauth session", session: gen.DySessionType_DY_OAUTH, want: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/admin/stats", nil)

			auth.WithAuth(c, &auth.AuthResult{
				Account: &gen.DyAccount{Id: "65732f7f-b74d-4409-9a8e-3b4438b3b014", Name: "tester"},
				Session: &gen.DyAuthSession{Type: tt.session, Scopes: []string{"*"}},
			}, auth.TokenInfo{})

			AskPermission(denyPermissionClient{}, "notifications.send", nil)(c)

			if recorder.Code != tt.want {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.want)
			}
		})
	}
}
