package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAssetLinksUsesSupportedAppLinkPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/.well-known/assetlinks.json", nil)
	(&Server{}).assetLinks(c)

	var document []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &document); err != nil {
		t.Fatalf("assetlinks response is not valid JSON: %v", err)
	}
	if len(document) != 1 {
		t.Fatalf("assetlinks statement count = %d, want 1", len(document))
	}

	relations, ok := document[0]["relation_extensions"].(map[string]any)
	if !ok {
		t.Fatal("assetlinks response is missing relation_extensions")
	}
	common, ok := relations["delegate_permission/common.handle_all_urls"].(map[string]any)
	if !ok {
		t.Fatal("assetlinks response is missing common URL relation")
	}
	components, ok := common["dynamic_app_link_components"].([]any)
	if !ok {
		t.Fatal("assetlinks response is missing dynamic_app_link_components")
	}

	paths := make(map[string]bool, len(components))
	for _, raw := range components {
		component, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("dynamic component has unexpected type: %T", raw)
		}
		path, ok := component["/"].(string)
		if !ok {
			t.Fatalf("dynamic component has no path: %#v", component)
		}
		paths[path] = true
	}

	for _, path := range []string{"/", "/posts/*", "/account/me/stellar-program"} {
		if !paths[path] {
			t.Errorf("supported path %q is missing", path)
		}
	}
	for _, path := range []string{"/pricing", "/nonexistent"} {
		if paths[path] {
			t.Errorf("unsupported path %q is associated with the app", path)
		}
	}
}
