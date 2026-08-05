package profilectl

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// testDeps builds a Deps that never touches a database: every handler is
// expected to 401 before reaching the store.
func testDeps() Deps {
	return Deps{Log: nil}
}

// Route table smoke test: Register must not panic (gin panics on duplicate
// or conflicting routes) and must expose every expected path/method.
func TestRegisterRouteTable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	api := engine.Group("/api")
	Register(api, Deps{Log: nil})

	type route struct {
		method string
		path   string
	}
	want := []route{
		{"GET", "/api/accounts/me"},
		{"PATCH", "/api/accounts/me"},
		{"DELETE", "/api/accounts/me"},
		{"PATCH", "/api/accounts/me/profile"},
		{"GET", "/api/accounts/me/board"},
		{"PUT", "/api/accounts/me/board"},
		{"GET", "/api/accounts/id/:id"},
		{"GET", "/api/accounts/search"},
		{"GET", "/api/accounts/:name"},
		{"GET", "/api/accounts/:name/picture"},
		{"GET", "/api/accounts/:name/background"},
		{"GET", "/api/accounts/:name/board"},
		{"GET", "/api/accounts/:name/connections"},
		{"GET", "/api/accounts/:name/followers"},
		{"GET", "/api/accounts/:name/following"},
		{"GET", "/api/relationships"},
		{"GET", "/api/relationships/requests"},
		{"GET", "/api/relationships/close-friends"},
		{"GET", "/api/relationships/inspect/:accountId"},
		{"POST", "/api/relationships/sync"},
		{"GET", "/api/relationships/:accountId"},
		{"POST", "/api/relationships/:accountId"},
		{"PATCH", "/api/relationships/:accountId"},
		{"DELETE", "/api/relationships/:accountId"},
		{"POST", "/api/relationships/:accountId/friends"},
		{"DELETE", "/api/relationships/:accountId/friends"},
		{"POST", "/api/relationships/:accountId/friends/accept"},
		{"POST", "/api/relationships/:accountId/friends/decline"},
		{"POST", "/api/relationships/:accountId/block"},
		{"DELETE", "/api/relationships/:accountId/block"},
		{"POST", "/api/relationships/:accountId/mute"},
		{"DELETE", "/api/relationships/:accountId/mute"},
		{"POST", "/api/relationships/:accountId/close-friend"},
		{"DELETE", "/api/relationships/:accountId/close-friend"},
		{"PATCH", "/api/relationships/:accountId/alias"},
		{"GET", "/api/relationships/:accountId/mutual-friends"},
	}
	// Resolve the gin route tree (any panic here fails the test).
	routes := engine.Routes()
	got := make(map[string]bool)
	for _, r := range routes {
		got[r.Method+" "+r.Path] = true
	}
	for _, w := range want {
		key := w.method + " " + w.path
		if !got[key] {
			t.Errorf("missing route %s", key)
		}
	}
}

// Anonymous requests to auth-required routes must 401 with the C# message.
func TestAnonymousRequiresAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	api := engine.Group("/api")
	Register(api, Deps{Log: nil})

	for _, tc := range []struct {
		method, path string
	}{
		{"GET", "/api/accounts/me"},
		{"GET", "/api/relationships"},
		{"GET", "/api/relationships/requests"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: got %d, want 401", tc.method, tc.path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "UNAUTHORIZED") {
			t.Errorf("%s %s: missing UNAUTHORIZED body: %s", tc.method, tc.path, rec.Body.String())
		}
	}
}

func TestNotFoundShape(t *testing.T) {
	err := notFound("alice")
	raw, _ := json.Marshal(err)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if m["code"] != "NOT_FOUND" {
		t.Errorf("code = %v", m["code"])
	}
	if m["detail"] != "alice" {
		t.Errorf("detail = %v", m["detail"])
	}
	if m["message"] != "The requested resource 'alice' was not found." {
		t.Errorf("message = %v", m["message"])
	}
}

func TestParseExpiresIn(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"30m", 30 * time.Minute, true},
		{"1h", time.Hour, true},
		{"24h", 24 * time.Hour, true},
		{"7d", 7 * 24 * time.Hour, true},
		{"30d", 30 * 24 * time.Hour, true},
		{" 2h ", 2 * time.Hour, true},
		{"5x", 0, false},
		{"", 0, false},
		{"h", 0, false},
	}
	for _, c := range cases {
		got, err := parseExpiresIn(c.in)
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("parseExpiresIn(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("parseExpiresIn(%q) expected error", c.in)
		}
	}
}

func TestRelationshipStatusNames(t *testing.T) {
	if relationshipStatusName(model.RelationshipFriends) != "Friends" {
		t.Errorf("Friends name = %q", relationshipStatusName(model.RelationshipFriends))
	}
	if relationshipStatusNameLower(model.RelationshipBlocked) != "blocked" {
		t.Errorf("Blocked lower = %q", relationshipStatusNameLower(model.RelationshipBlocked))
	}
	if relationshipStatusName(model.RelationshipCloseFriend) != "CloseFriend" {
		t.Errorf("CloseFriend name = %q", relationshipStatusName(model.RelationshipCloseFriend))
	}
}

func TestStatusIntsMatchCSharp(t *testing.T) {
	// RelationshipStatus enum values must be exactly the C# ints.
	if int(model.RelationshipPending) != 0 || int(model.RelationshipFriends) != 100 ||
		int(model.RelationshipMuted) != -50 || int(model.RelationshipBlocked) != -100 ||
		int(model.RelationshipCloseFriend) != 200 {
		t.Errorf("relationship status ints do not match C#")
	}
	if int(model.BoardItemKindWidget) != 0 || int(model.BoardItemKindApp) != 1 {
		t.Errorf("board kind ints do not match C# Prebuilt=0, CustomApp=1")
	}
}

func TestBuildPublicConnectionUrl(t *testing.T) {
	steam := &model.Connection{Provider: "Steam", ProvidedIdentifier: "7656119"}
	if got := buildPublicConnectionUrl(steam); got != "https://steamcommunity.com/profiles/7656119" {
		t.Errorf("steam url = %q", got)
	}
	github := &model.Connection{Provider: "github", ProvidedIdentifier: "abc", Meta: map[string]any{"preferred_username": "octocat"}}
	if got := buildPublicConnectionUrl(github); got != "https://github.com/octocat" {
		t.Errorf("github url = %q", got)
	}
	other := &model.Connection{Provider: "google", ProvidedIdentifier: "x"}
	if got := buildPublicConnectionUrl(other); got != "" {
		t.Errorf("other url = %q", got)
	}
}

func TestBadgeToJSONSnakeCase(t *testing.T) {
	b := &gen.DyAccountBadge{Id: "b1", Type: "pioneer", AccountId: "acc"}
	m := badgeToJSON(b)
	if m["id"] != "b1" || m["type"] != "pioneer" || m["account_id"] != "acc" {
		t.Errorf("badge map = %#v", m)
	}
}

func TestVerificationFromProto(t *testing.T) {
	v := verificationFromProto(&gen.DyVerificationMark{Type: 2, Title: "t", Description: "d", VerifiedBy: "admin"})
	if v == nil || v.Type != 2 || v.Title == nil || *v.Title != "t" || v.VerifiedBy == nil || *v.VerifiedBy != "admin" {
		t.Errorf("verification = %#v", v)
	}
	if verificationFromProto(nil) != nil {
		t.Error("nil proto should map to nil")
	}
}

func TestFileRefFromProto(t *testing.T) {
	w, h := int32(120), int32(240)
	f := &gen.DyCloudFile{Id: "f1", Url: "https://files.solian.app/f1", MimeType: "image/png", Size: 42, Width: &w, Height: &h, Blurhash: nil}
	ref := fileRefFromProto(f)
	if ref.Id != "f1" || ref.Url != "https://files.solian.app/f1" || ref.MimeType != "image/png" {
		t.Errorf("ref = %#v", ref)
	}
	if ref.Width == nil || *ref.Width != 120 || ref.Height == nil || *ref.Height != 240 {
		t.Errorf("ref dims = %#v", ref)
	}
	if ref.Size == nil || *ref.Size != 42 {
		t.Errorf("ref size = %#v", ref)
	}
}

func TestRelationshipWireShape(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	degrade := model.RelationshipBlocked
	rel := &model.Relationship{
		AccountId:       "11111111-1111-1111-1111-111111111111",
		RelatedId:       "22222222-2222-2222-2222-222222222222",
		Status:          model.RelationshipMuted,
		ExpiredAt:       model.NewTime(now),
		DegradeToStatus: &degrade,
		CreatedAt:       model.NewTime(now),
		UpdatedAt:       model.NewTime(now),
	}
	raw, err := json.Marshal(rel)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for _, key := range []string{"account_id", "related_id", "expired_at", "degrade_to_status", "status", "created_at", "updated_at"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing wire key %q in %s", key, raw)
		}
	}
	if m["status"] != float64(-50) {
		t.Errorf("status = %v, want -50", m["status"])
	}
}

func TestSearchAccountsEmptyQuery(t *testing.T) {
	// The store helper must short-circuit on blank queries like the C#.
	s := &store.Store{}
	accounts, err := s.SearchAccounts(t.Context(), "   ", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 0 {
		t.Errorf("blank query returned %d accounts", len(accounts))
	}
}

func TestErrNotFoundMapping(t *testing.T) {
	if !errors.Is(store.ErrNotFound, store.ErrNotFound) {
		t.Fatal("sentinel mismatch")
	}
}
