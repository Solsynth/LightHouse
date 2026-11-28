package lyrics

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/config"
)

func TestLRCLIBGetProxiesTheRecord(t *testing.T) {
	var path string
	var query url.Values
	var agent string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		query = r.URL.Query()
		agent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":7,"trackName":"Yellow","syncedLyrics":"[00:01.00] stars"}`)
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	cfg.Lyrics.LRCLIBBaseURL = upstream.URL
	cfg.Lyrics.UserAgent = "Pulsar-Test/1.0"
	h := newHarnessWithConfig(t, cfg)

	recorder := h.call(t, http.MethodGet,
		"/api/lyrics/lrclib/get?track_name=Yellow&artist_name=Coldplay&album_name=Parachutes&duration=266", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if path != "/api/get" {
		t.Fatalf("upstream path = %q", path)
	}
	if agent != "Pulsar-Test/1.0" {
		t.Fatalf("upstream agent = %q", agent)
	}
	if query.Get("track_name") != "Yellow" || query.Get("artist_name") != "Coldplay" ||
		query.Get("album_name") != "Parachutes" || query.Get("duration") != "266" {
		t.Fatalf("upstream query = %v", query)
	}
	var record map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &record); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if record["id"] != float64(7) {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}

func TestLRCLIBSearchNotFoundIsAnEmptyList(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"statusCode":404}`, http.StatusNotFound)
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	cfg.Lyrics.LRCLIBBaseURL = upstream.URL
	h := newHarnessWithConfig(t, cfg)

	recorder := h.call(t, http.MethodGet, "/api/lyrics/lrclib/search?q=nothing", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if strings.TrimSpace(recorder.Body.String()) != "[]" {
		t.Fatalf("body = %q", recorder.Body.String())
	}
}

func TestLRCLIBRateLimitIsShared(t *testing.T) {
	var requests int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Retry-After", "12")
		http.Error(w, `{"statusCode":429}`, http.StatusTooManyRequests)
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	cfg.Lyrics.LRCLIBBaseURL = upstream.URL
	h := newHarnessWithConfig(t, cfg)

	first := h.call(t, http.MethodGet, "/api/lyrics/lrclib/get?track_name=Yellow&artist_name=Coldplay", nil)
	if first.Code != http.StatusTooManyRequests {
		t.Fatalf("first status = %d: %s", first.Code, first.Body.String())
	}
	if first.Header().Get("Retry-After") != "12" {
		t.Fatalf("retry-after = %q", first.Header().Get("Retry-After"))
	}

	// The exact lookup opened the window; the search behind it must not be
	// sent, and the caller gets the same Retry-After.
	second := h.call(t, http.MethodGet, "/api/lyrics/lrclib/search?q=Yellow", nil)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d: %s", second.Code, second.Body.String())
	}
	if requests != 1 {
		t.Fatalf("upstream requests = %d, want 1", requests)
	}
}

func TestTranslateForwardsTheAccountGrant(t *testing.T) {
	var path string
	var authorization string
	var body map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		authorization = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"1|quietly"}}]}`)
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	cfg.Lyrics.PersonaBaseURL = upstream.URL + "/persona"
	h := newHarnessWithConfig(t, cfg)

	recorder := h.call(t, http.MethodPost, "/api/lyrics/translate", map[string]any{
		"model":  "raw/openai/gpt-4.1-mini",
		"stream": false,
		"messages": []map[string]string{
			{"role": "system", "content": "translate"},
			{"role": "user", "content": "1|静かに"},
		},
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if path != "/persona/v1/chat/completions" {
		t.Fatalf("upstream path = %q", path)
	}
	if authorization != "Bearer test-token" {
		t.Fatalf("authorization = %q", authorization)
	}
	if body["model"] != "raw/openai/gpt-4.1-mini" || body["stream"] != false {
		t.Fatalf("upstream body = %v", body)
	}
	if !strings.Contains(recorder.Body.String(), "quietly") {
		t.Fatalf("response = %s", recorder.Body.String())
	}
}

func TestModelsAreProxiedWithTheSameGrant(t *testing.T) {
	var path string
	var authorization string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"id":"openai/gpt-4.1-mini","provider":"openai","name":"gpt-4.1-mini"}]`)
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	cfg.Lyrics.PersonaBaseURL = upstream.URL + "/persona"
	h := newHarnessWithConfig(t, cfg)

	recorder := h.call(t, http.MethodGet, "/api/lyrics/models", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if path != "/persona/models" {
		t.Fatalf("upstream path = %q", path)
	}
	if authorization != "Bearer test-token" {
		t.Fatalf("authorization = %q", authorization)
	}
}

func TestTranslationWithoutPersonaIsUnavailable(t *testing.T) {
	// An unconfigured deployment must decline clearly, so the client can fall
	// back to its own engine instead of retrying a request that cannot work.
	h := newHarness(t)

	recorder := h.call(t, http.MethodPost, "/api/lyrics/translate", map[string]any{
		"model":    "raw/openai/gpt-4.1-mini",
		"messages": []map[string]string{{"role": "user", "content": "1|a"}},
	})
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
}
