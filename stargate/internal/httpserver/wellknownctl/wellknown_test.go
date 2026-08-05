package wellknownctl

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/gin-gonic/gin"
)

func newTestEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	RegisterTop(e, Deps{})
	return e
}

func get(t *testing.T, e *gin.Engine, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("%s: status = %d, body = %s", path, w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: invalid JSON: %v", path, err)
	}
	return w, body
}

func TestPermissionsManifest(t *testing.T) {
	_, body := get(t, newTestEngine(), "/.well-known/permissions")

	if got := body["count"]; got != float64(len(permissionEntries)) {
		t.Fatalf("count = %v, want %d", got, len(permissionEntries))
	}
	perms, ok := body["permissions"].([]any)
	if !ok {
		t.Fatalf("permissions is %T, want array", body["permissions"])
	}
	if len(perms) != len(permissionEntries) {
		t.Fatalf("permissions length = %d, want %d", len(perms), len(permissionEntries))
	}
	prev := ""
	for i, raw := range perms {
		item := raw.(map[string]any)
		// exact keys {key, name} only
		if len(item) != 2 || item["key"] == nil || item["name"] == nil {
			t.Fatalf("item %d has unexpected keys: %v", i, item)
		}
		key := item["key"].(string)
		if i > 0 && key < prev {
			t.Fatalf("permissions not sorted by key at %d: %q before %q", i, prev, key)
		}
		prev = key
	}
}

func TestErrorCodesManifest(t *testing.T) {
	_, body := get(t, newTestEngine(), "/.well-known/error-codes")

	ec, ok := body["error_codes"].(map[string]any)
	if !ok {
		t.Fatalf("error_codes is %T, want object", body["error_codes"])
	}
	// count = 3 general + 8 categories, all non-empty
	wantCount := len(topLevelErrorCodes)
	for _, cat := range errorCodeCategories {
		wantCount += len(cat.Codes)
	}
	if got := body["count"]; got != float64(wantCount) {
		t.Fatalf("count = %v, want %d", got, wantCount)
	}

	general, ok := ec["general"].([]any)
	if !ok || len(general) != len(topLevelErrorCodes) {
		t.Fatalf("general = %#v, want %d items", ec["general"], len(topLevelErrorCodes))
	}
	prev := ""
	for i, raw := range general {
		item := raw.(map[string]any)
		if len(item) != 2 || item["code"] == nil || item["name"] == nil {
			t.Fatalf("general item %d has unexpected keys: %v", i, item)
		}
		code := item["code"].(string)
		if i > 0 && code < prev {
			t.Fatalf("general not sorted by code at %d: %q before %q", i, prev, code)
		}
		prev = code
	}

	categories, ok := ec["categories"].([]any)
	if !ok || len(categories) != len(errorCodeCategories) {
		t.Fatalf("categories = %#v, want %d items", ec["categories"], len(errorCodeCategories))
	}
	prevCat := ""
	for i, raw := range categories {
		cat := raw.(map[string]any)
		name := cat["category"].(string)
		if i > 0 && name < prevCat {
			t.Fatalf("categories not sorted by name at %d: %q before %q", i, prevCat, name)
		}
		prevCat = name
		codes, ok := cat["codes"].([]any)
		if !ok || len(codes) == 0 {
			t.Fatalf("category %s has no codes", name)
		}
		prevCode := ""
		for j, rawCode := range codes {
			item := rawCode.(map[string]any)
			if len(item) != 2 || item["code"] == nil || item["name"] == nil {
				t.Fatalf("category %s code %d has unexpected keys: %v", name, j, item)
			}
			code := item["code"].(string)
			if j > 0 && code < prevCode {
				t.Fatalf("category %s codes not sorted at %d: %q before %q", name, j, prevCode, code)
			}
			prevCode = code
		}
	}
}

func TestErrorCodesVerbatim(t *testing.T) {
	// Every C# constant value must appear exactly once in the registry.
	seen := map[string]string{}
	for _, it := range topLevelErrorCodes {
		if prev, dup := seen[it.Code]; dup {
			t.Fatalf("duplicate code %q (names %s, %s)", it.Code, prev, it.Name)
		}
		seen[it.Code] = it.Name
	}
	for _, cat := range errorCodeCategories {
		for _, it := range cat.Codes {
			if prev, dup := seen[it.Code]; dup {
				t.Fatalf("duplicate code %q (names %s, %s)", it.Code, prev, it.Name)
			}
			seen[it.Code] = it.Name
		}
	}
	if len(seen) != 383 {
		t.Fatalf("registry has %d distinct codes, want 383", len(seen))
	}
	for _, want := range []string{"UNKNOWN_ERROR", "VALIDATION_ERROR", "SERVER_ERROR", "AUTH_ACCOUNT_NOT_FOUND", "QR_CHALLENGE_NOT_FOUND", "SESSION_INTERACTIVE_REQUIRED", "PAGINATION_TAKE_EXCEEDED"} {
		if _, ok := seen[want]; !ok {
			t.Fatalf("missing code %q", want)
		}
	}
}

func TestPermissionEntriesVerbatim(t *testing.T) {
	keys := make([]string, len(permissionEntries))
	for i, it := range permissionEntries {
		keys[i] = it.Key
	}
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	// source order is irrelevant; the endpoint sorts. Just assert no dupes.
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			t.Fatalf("duplicate permission key %q", k)
		}
		seen[k] = true
	}
	if len(seen) != 298 {
		t.Fatalf("permission registry has %d keys, want 298", len(seen))
	}
}

func TestSwaggerManifests(t *testing.T) {
	e := newTestEngine()
	for _, path := range []string{"/swagger/padlock/v1/swagger.json", "/swagger/passport/v1/swagger.json"} {
		_, body := get(t, e, path)
		if body["openapi"] != "3.0.1" {
			t.Fatalf("%s: openapi = %v, want 3.0.1", path, body["openapi"])
		}
		info, ok := body["info"].(map[string]any)
		if !ok || info["version"] != "v1" {
			t.Fatalf("%s: info = %v, want version v1", path, body["info"])
		}
		paths, ok := body["paths"].(map[string]any)
		if !ok || len(paths) == 0 {
			t.Fatalf("%s: no paths", path)
		}
		for p := range paths {
			if p[0] != '/' || p[:5] != "/api/" {
				t.Fatalf("%s: path %q is not keyed /api/...", path, p)
			}
		}
	}
}
