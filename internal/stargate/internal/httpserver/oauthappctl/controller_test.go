package oauthappctl

import (
	"log/slog"
	"testing"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// TestRegisterRouteTable is the gin smoke test: Register must not panic
// (gin panics on duplicate or conflicting routes) and must expose the
// documented surface.
func TestRegisterRouteTable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	api := engine.Group("/api")
	Register(api, Deps{Log: slog.Default()})

	want := map[string]bool{
		"GET /api/oauth-apps":                                  false,
		"POST /api/oauth-apps":                                 false,
		"GET /api/oauth-apps/:appId":                           false,
		"PATCH /api/oauth-apps/:appId":                         false,
		"DELETE /api/oauth-apps/:appId":                        false,
		"GET /api/oauth-apps/:appId/secrets":                   false,
		"POST /api/oauth-apps/:appId/secrets":                  false,
		"GET /api/oauth-apps/:appId/secrets/:secretId":         false,
		"DELETE /api/oauth-apps/:appId/secrets/:secretId":      false,
		"POST /api/oauth-apps/:appId/secrets/:secretId/rotate": false,
	}
	for _, route := range engine.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := want[key]; ok {
			want[key] = true
			continue
		}
		t.Errorf("unexpected route %s", key)
	}
	for key, seen := range want {
		if !seen {
			t.Errorf("missing route %s", key)
		}
	}
}

func TestGenerateSecretShape(t *testing.T) {
	first := GenerateSecret()
	second := GenerateSecret()
	if len(first) != 64 {
		t.Fatalf("secret length = %d; want 64", len(first))
	}
	if first == second {
		t.Fatal("two generated secrets are identical")
	}
	for _, char := range first {
		if !isSecretAlphabetRune(char) {
			t.Fatalf("secret contains %q, outside the OAuth-safe alphabet", char)
		}
	}
}

func isSecretAlphabetRune(char rune) bool {
	for _, allowed := range secretAlphabet {
		if char == allowed {
			return true
		}
	}
	return false
}

func newValidTestApp() *model.OAuthApp {
	return &model.OAuthApp{
		Slug: "test-app",
		Name: "Test App",
		OauthConfig: model.OAuthAppOauthConfig{
			AllowedScopes: []string{"openid", "profile"},
			RedirectUris:  []string{"https://client.example/callback"},
			RequirePkce:   true,
		},
	}
}

func TestValidateAppRequiresOwnerScopesAndRedirects(t *testing.T) {
	app := newValidTestApp()
	if err := validateApp(app); err != nil {
		t.Fatalf("valid app rejected: %v", err)
	}

	noScopes := newValidTestApp()
	noScopes.OauthConfig.AllowedScopes = nil
	if err := validateApp(noScopes); err != errScopesRequired {
		t.Fatalf("missing scopes error = %v; want %v", err, errScopesRequired)
	}

	productionNoRedirect := newValidTestApp()
	productionNoRedirect.Status = customAppStatusProduction
	productionNoRedirect.OauthConfig.RedirectUris = nil
	if err := validateApp(productionNoRedirect); err != errRedirectMissing {
		t.Fatalf("production without redirects error = %v; want %v", err, errRedirectMissing)
	}

	// A non-production client may omit redirect URIs: the provider accepts
	// any redirect for it.
	developing := newValidTestApp()
	developing.Status = 0
	developing.OauthConfig.RedirectUris = nil
	if err := validateApp(developing); err != nil {
		t.Fatalf("developing app without redirects rejected: %v", err)
	}
}
