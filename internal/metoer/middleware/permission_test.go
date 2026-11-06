package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/auth"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
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

// allowPermissionClient grants every lookup, like Stargate does for an account
// that really holds the node.
type allowPermissionClient struct{ denyPermissionClient }

func (allowPermissionClient) HasPermission(context.Context, *gen.DyHasPermissionRequest, ...grpc.CallOption) (*gen.DyHasPermissionResponse, error) {
	return &gen.DyHasPermissionResponse{HasPermission: true}, nil
}

// TestAskPermissionScopeIsAFilterNotAGrant pins the rule: "*" removes the
// scope narrowing but never replaces the permission-service decision. An OAuth
// token carrying the wildcard is still denied when the account holds no node
// for the key, and is allowed only when the service grants it.
func TestAskPermissionScopeIsAFilterNotAGrant(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name    string
		session gen.DySessionType
		scopes  []string
		client  gen.DyPermissionServiceClient
		want    int
	}{
		{name: "login session has no scope filter", session: gen.DySessionType_DY_LOGIN, client: denyPermissionClient{}, want: http.StatusForbidden},
		{name: "oidc session has no scope filter", session: gen.DySessionType_DY_OIDC, client: denyPermissionClient{}, want: http.StatusForbidden},
		{name: "api key session has no scope filter", session: gen.DySessionType_DY_API_KEY, client: denyPermissionClient{}, want: http.StatusForbidden},
		{name: "oauth wildcard still needs the permission", session: gen.DySessionType_DY_OAUTH, scopes: []string{"*"}, client: denyPermissionClient{}, want: http.StatusForbidden},
		{name: "oauth wildcard passes a granted permission", session: gen.DySessionType_DY_OAUTH, scopes: []string{"*"}, client: allowPermissionClient{}, want: http.StatusOK},
		{name: "oauth scope omission denies", session: gen.DySessionType_DY_OAUTH, scopes: []string{"chat.read"}, client: allowPermissionClient{}, want: http.StatusForbidden},
		{name: "oauth matching scope passes a granted permission", session: gen.DySessionType_DY_OAUTH, scopes: []string{"notifications.send"}, client: allowPermissionClient{}, want: http.StatusOK},
		{name: "oauth empty scope list denies", session: gen.DySessionType_DY_OAUTH, client: allowPermissionClient{}, want: http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/admin/stats", nil)

			auth.WithAuth(c, &auth.AuthResult{
				Account: &gen.DyAccount{Id: "65732f7f-b74d-4409-9a8e-3b4438b3b014", Name: "tester"},
				Session: &gen.DyAuthSession{Type: tt.session, Scopes: tt.scopes},
			}, auth.TokenInfo{})

			AskPermission(tt.client, "notifications.send", nil)(c)

			if recorder.Code != tt.want {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.want)
			}
		})
	}
}
