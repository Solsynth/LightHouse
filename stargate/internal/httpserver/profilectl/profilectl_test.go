package profilectl

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/structpb"
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
		{"GET", "/api/accounts/id/:id"},
		{"GET", "/api/accounts/search"},
		{"GET", "/api/accounts/:name"},
		{"GET", "/api/accounts/:name/picture"},
		{"GET", "/api/accounts/:name/background"},
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
// /api/accounts/search is deliberately absent: it is a public product surface
// (FloatLand's public search page, Sokai's signed-out account picker) and is
// pinned by TestAnonymousSearchServesAccountsWithoutSuperuser below.
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

// searchDSN mirrors config.example.toml and the other DB-backed smoke tests.
const searchDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

// TestAnonymousSearchServesAccountsWithoutSuperuser pins the restored public
// read: an unauthenticated GET /api/accounts/search serves the matching
// accounts (FloatLand's public search page and Sokai's signed-out account
// picker need them) and still applies the public projection, so a platform
// admin is reported with is_superuser false.
func TestAnonymousSearchServesAccountsWithoutSuperuser(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), searchDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}

	accountID := uuid.New()
	name := "anon_search_" + uuid.NewString()[:8]
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts
		(id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', true, $3, $3)`, accountID, name, now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	defer func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM account_profiles WHERE account_id = $1`, accountID)
		_, _ = pool.Exec(bg, `DELETE FROM accounts WHERE id = $1`, accountID)
	}()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	api := engine.Group("/api")
	Register(api, Deps{
		Store: store.New(pool),
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/accounts/search?query="+name, nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("anonymous search: got %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var results []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil {
		t.Fatalf("decode search response: %v", err)
	}
	found := false
	for _, result := range results {
		if result["is_superuser"] != false {
			t.Errorf("anonymous search result %v leaks is_superuser = %v", result["name"], result["is_superuser"])
		}
		if result["id"] == accountID.String() {
			found = true
		}
	}
	if !found {
		t.Fatalf("seeded account %q missing from anonymous search results: %s", name, rec.Body.String())
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
	lastfm := &model.Connection{Provider: "lastfm", ProvidedIdentifier: "some user"}
	if got := buildPublicConnectionUrl(lastfm); got != "https://www.last.fm/user/some%20user" {
		t.Errorf("lastfm url = %q", got)
	}
	lastfmNoName := &model.Connection{Provider: "lastfm"}
	if got := buildPublicConnectionUrl(lastfmNoName); got != "" {
		t.Errorf("lastfm url without identifier = %q", got)
	}
}

func TestBadgeToJSONSnakeCase(t *testing.T) {
	b := &gen.DyAccountBadge{Id: "b1", Type: "pioneer", AccountId: "acc"}
	m := badgeToJSON(b)
	if m["id"] != "b1" || m["type"] != "pioneer" || m["account_id"] != "acc" {
		t.Errorf("badge map = %#v", m)
	}
	// The Dart SDK strict-casts badge.meta as Map<String, dynamic>; a badge
	// without meta must still serialize an empty object, never omit the key.
	meta, ok := m["meta"].(map[string]any)
	if !ok {
		t.Fatalf("badge meta = %#v (%T), want map[string]any{}", m["meta"], m["meta"])
	}
	if len(meta) != 0 {
		t.Errorf("badge meta = %#v, want empty", meta)
	}
}

func TestBadgeToJSONKeepsMeta(t *testing.T) {
	b := &gen.DyAccountBadge{
		Id:   "b2",
		Type: "custom",
		Meta: map[string]*structpb.Value{"color": structpb.NewStringValue("gold")},
	}
	m := badgeToJSON(b)
	meta, ok := m["meta"].(map[string]any)
	if !ok {
		t.Fatalf("badge meta = %#v (%T), want map[string]any", m["meta"], m["meta"])
	}
	if meta["color"] != "gold" {
		t.Errorf("badge meta = %#v, want color=gold", meta)
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
	// Full CloudFileReferenceObject shape: size is a plain int, meta maps are
	// always present, and missing proto timestamps fall back to now.
	if ref.Size != 42 {
		t.Errorf("ref size = %d, want 42", ref.Size)
	}
	if ref.FileMeta == nil || ref.UserMeta == nil {
		t.Errorf("file_meta/user_meta must be non-nil: %#v", ref)
	}
	if ref.CreatedAt == nil || ref.UpdatedAt == nil {
		t.Error("created_at/updated_at must fall back to now")
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

// TestApplyPublicProjectionHidesSuperuser pins the public projection: another
// account's payload keeps the model.Account wire shape (so clients parsing it
// do not break) but never carries is_superuser=true.
func TestApplyPublicProjectionHidesSuperuser(t *testing.T) {
	account := &model.Account{
		Id:          "11111111-1111-1111-1111-111111111111",
		Name:        "alice",
		Nick:        "Alice",
		Region:      "US",
		IsSuperuser: true,
		PerkLevel:   3,
		CreatedAt:   model.NewTime(time.Now().UTC()),
	}
	before := marshalToMap(t, account)

	applyPublicProjection(account)
	if account.IsSuperuser {
		t.Fatal("applyPublicProjection must clear IsSuperuser")
	}
	after := marshalToMap(t, account)

	if after["is_superuser"] != false {
		t.Errorf("is_superuser = %v, want false", after["is_superuser"])
	}
	delete(before, "is_superuser")
	delete(after, "is_superuser")
	if len(before) != len(after) {
		t.Fatalf("public projection changed the wire shape: %d keys -> %d keys", len(before), len(after))
	}
	for key := range before {
		if _, ok := after[key]; !ok {
			t.Errorf("public projection dropped wire key %q", key)
		}
	}
}

func marshalToMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
