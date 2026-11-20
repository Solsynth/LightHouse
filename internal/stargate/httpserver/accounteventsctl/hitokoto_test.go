package accounteventsctl

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func hitokotoTestServer(t *testing.T, body string, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if got := r.URL.Query().Get("encode"); got != "json" {
			t.Errorf("encode = %q, want json", got)
		}
		if got := r.URL.Query()["c"]; len(got) != 3 {
			t.Errorf("categories = %v, want three", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

// TestHitokotoClientServesAndCachesSource checks the upstream sentence is
// mapped onto the fortune shape and reused instead of refetched.
func TestHitokotoClientServesAndCachesSource(t *testing.T) {
	server, hits := hitokotoTestServer(t, `{"uuid":"u-1","hitokoto":"路漫漫其修远兮，吾将上下而求索。","type":"i","from":"离骚","from_who":"屈原"}`, http.StatusOK)
	client := NewHitokotoClient(server.URL, slog.Default())
	if client == nil {
		t.Fatal("client is nil")
	}

	saying, ok := client.Random(t.Context())
	if !ok {
		t.Fatal("Random reported no sentence")
	}
	if saying.Content != "路漫漫其修远兮，吾将上下而求索。" {
		t.Errorf("content = %q", saying.Content)
	}
	if saying.Source != "离骚" {
		t.Errorf("source = %q, want 离骚", saying.Source)
	}
	if saying.Language != "zh" {
		t.Errorf("language = %q, want zh", saying.Language)
	}

	if _, ok := client.Random(t.Context()); !ok {
		t.Fatal("cached Random reported no sentence")
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("upstream hits = %d, want 1 (cache reuse)", got)
	}
}

// TestHitokotoClientFallsBackOnUpstreamFailure checks a failing upstream
// reports no sentence and backs off instead of retrying on every request.
func TestHitokotoClientFallsBackOnUpstreamFailure(t *testing.T) {
	server, hits := hitokotoTestServer(t, `{}`, http.StatusBadGateway)
	client := NewHitokotoClient(server.URL, slog.Default())
	now := time.Unix(1_700_000_000, 0)
	client.now = func() time.Time { return now }

	if _, ok := client.Random(t.Context()); ok {
		t.Fatal("Random reported a sentence from a failing upstream")
	}
	now = now.Add(5 * time.Second)
	if _, ok := client.Random(t.Context()); ok {
		t.Fatal("Random reported a sentence during cooldown")
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("upstream hits = %d, want 1 (cooldown suppresses retry)", got)
	}
	now = now.Add(hitokotoFailureCooldown)
	if _, ok := client.Random(t.Context()); ok {
		t.Fatal("Random reported a sentence from a failing upstream after cooldown")
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("upstream hits = %d, want 2 (retry after cooldown)", got)
	}
}

// TestRandomFortunePrefersHitokoto pins the handler contract: a Chinese
// request is served from the remote sentence, an English one from the
// embedded catalogue, and a dead upstream degrades to the catalogue.
func TestRandomFortunePrefersHitokoto(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	server, _ := hitokotoTestServer(t, `{"uuid":"u-2","hitokoto":"纸上得来终觉浅，绝知此事要躬行。","type":"i","from":"冬夜读书示子聿"}`, http.StatusOK)
	client := NewHitokotoClient(server.URL, slog.Default())
	router := gin.New()
	router.GET("/fortune/random", Deps{Hitokoto: client}.randomFortune)

	call := func(query string) []fortuneSaying {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/fortune/random"+query, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", recorder.Code)
		}
		var sayings []fortuneSaying
		if err := json.Unmarshal(recorder.Body.Bytes(), &sayings); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(sayings) != 1 {
			t.Fatalf("sayings = %d, want 1", len(sayings))
		}
		return sayings
	}

	if got := call("?language=zh")[0]; got.Content != "纸上得来终觉浅，绝知此事要躬行。" || got.Language != "zh" {
		t.Errorf("zh fortune = %+v, want the hitokoto sentence", got)
	}
	if got := call("?language=en")[0]; got.Language != "en" {
		t.Errorf("en fortune language = %q, want en", got.Language)
	}

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(dead.Close)
	router = gin.New()
	router.GET("/fortune/random", Deps{Hitokoto: NewHitokotoClient(dead.URL, slog.Default())}.randomFortune)
	if got := call("")[0]; !isCatalogueSaying(got) {
		t.Errorf("fallback fortune = %+v, want an embedded catalogue entry", got)
	}
}

// isCatalogueSaying reports whether the saying is one of the embedded
// Passport entries, i.e. the handler fell back instead of using 一言.
func isCatalogueSaying(saying fortuneSaying) bool {
	for _, entry := range fortuneSayings {
		if entry.Content == saying.Content {
			return entry.Language == saying.Language
		}
	}
	return false
}

// TestHitokotoEndpointFromSettings pins the settings contract: absent means
// the public default, an explicit empty string disables the client.
func TestHitokotoEndpointFromSettings(t *testing.T) {
	if got := HitokotoEndpointFromSettings(nil); got != DefaultHitokotoEndpoint {
		t.Errorf("nil settings endpoint = %q, want %q", got, DefaultHitokotoEndpoint)
	}
	if got := HitokotoEndpointFromSettings(map[string]any{"hitokoto_endpoint": "https://example.test/"}); got != "https://example.test/" {
		t.Errorf("endpoint = %q", got)
	}
	if got := HitokotoEndpointFromSettings(map[string]any{"hitokotoEndpoint": ""}); got != "" {
		t.Errorf("endpoint = %q, want empty", got)
	}
	if client := NewHitokotoClient("", slog.Default()); client != nil {
		t.Error("NewHitokotoClient with an empty endpoint must disable the client")
	}
}
