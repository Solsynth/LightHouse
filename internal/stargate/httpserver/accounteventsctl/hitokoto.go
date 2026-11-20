package accounteventsctl

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
)

// DefaultHitokotoEndpoint is the public 一言 sentence API (v1). See
// https://developer.hitokoto.cn/sentence/.
const DefaultHitokotoEndpoint = "https://v1.hitokoto.cn/"

const (
	// A primed pool refreshes at most this often. Together with the pool's
	// single-flight claim this stays well inside the endpoint's 2 QPS limit.
	hitokotoRefreshEvery = 5 * time.Minute
	// A failed upstream backs off so a check-in burst does not pay the
	// timeout on every request.
	hitokotoFailureCooldown = 30 * time.Second
	hitokotoTimeout         = 3 * time.Second
	// hitokotoPoolSize bounds the sentence cache; entries rotate out oldest
	// first, so a busy day still shows fresh text.
	hitokotoPoolSize = 32
	// hitokotoCategories favour literature, poetry and philosophy so a
	// fortune reads like a proverb instead of an anime punchline.
	hitokotoCategories = "d,i,k"
)

// HitokotoClient fetches 一言 sentences and caches them as fortunes. It is
// best-effort: callers fall back to the embedded Passport catalogue whenever
// Random reports no sentence.
type HitokotoClient struct {
	endpoint string
	client   *http.Client
	log      *slog.Logger
	now      func() time.Time

	mu         sync.Mutex
	pool       []fortuneSaying
	lastFetch  time.Time
	retryAfter time.Time
}

// HitokotoEndpointFromSettings resolves the module setting: absent means the
// default public endpoint, an explicit empty string disables the client.
func HitokotoEndpointFromSettings(raw map[string]any) string {
	if raw == nil {
		return DefaultHitokotoEndpoint
	}
	for _, key := range []string{"hitokotoEndpoint", "hitokoto_endpoint"} {
		if value, ok := raw[key]; ok {
			if typed, ok := value.(string); ok {
				return strings.TrimSpace(typed)
			}
		}
	}
	return DefaultHitokotoEndpoint
}

// NewHitokotoClient builds the client. An empty endpoint disables it and
// returns nil, which callers treat as "local catalogue only".
func NewHitokotoClient(endpoint string, log *slog.Logger) *HitokotoClient {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	if _, err := url.Parse(endpoint); err != nil {
		log.Warn("fortune: invalid hitokoto endpoint; using local catalogue", "endpoint", endpoint, "error", err)
		return nil
	}
	return &HitokotoClient{
		endpoint: endpoint,
		client:   &http.Client{Timeout: hitokotoTimeout},
		log:      log,
		now:      func() time.Time { return time.Now() },
	}
}

// hitokotoSentence is the v1 JSON body (only the fields a fortune needs).
type hitokotoSentence struct {
	UUID     string `json:"uuid"`
	Hitokoto string `json:"hitokoto"`
	Type     string `json:"type"`
	From     string `json:"from"`
	FromWho  string `json:"from_who"`
}

// Random returns a cached sentence. An empty pool is primed synchronously; a
// primed pool refreshes in the background at most once per
// hitokotoRefreshEvery, so a check-in never pays upstream latency. It reports
// false when no sentence is available, so the caller uses its fallback.
func (h *HitokotoClient) Random(ctx context.Context) (fortuneSaying, bool) {
	if h == nil {
		return fortuneSaying{}, false
	}

	h.mu.Lock()
	now := h.now()
	prime := len(h.pool) == 0
	cooldown := now.Before(h.retryAfter)
	claim := !cooldown && (prime || now.Sub(h.lastFetch) >= hitokotoRefreshEvery)
	if claim {
		// Claiming the slot under the lock coalesces concurrent requests
		// into one upstream call.
		h.lastFetch = now
	}
	saying, hasSaying := h.pickLocked()
	h.mu.Unlock()

	if !claim || cooldown {
		return saying, hasSaying
	}
	if !prime {
		go h.fetchAndStore(context.Background())
		return saying, hasSaying
	}
	h.fetchAndStore(ctx)
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pickLocked()
}

// pickLocked returns a random pooled sentence. Call with h.mu held.
func (h *HitokotoClient) pickLocked() (fortuneSaying, bool) {
	if len(h.pool) == 0 {
		return fortuneSaying{}, false
	}
	return h.pool[rand.IntN(len(h.pool))], true
}

// fetchAndStore performs one upstream call and folds the result into the pool.
// A failure starts a cooldown so a burst does not pay the timeout per request.
func (h *HitokotoClient) fetchAndStore(ctx context.Context) {
	saying, ok := h.fetch(ctx)
	h.mu.Lock()
	defer h.mu.Unlock()
	if !ok {
		h.retryAfter = h.now().Add(hitokotoFailureCooldown)
		return
	}
	h.pool = append(h.pool, saying)
	if len(h.pool) > hitokotoPoolSize {
		h.pool = h.pool[len(h.pool)-hitokotoPoolSize:]
	}
}

// fetch performs one upstream call; fetchLocked serializes it under h.mu.
func (h *HitokotoClient) fetch(ctx context.Context) (fortuneSaying, bool) {
	endpoint, err := url.Parse(h.endpoint)
	if err != nil {
		h.log.Warn("fortune: parse hitokoto endpoint", "error", err)
		return fortuneSaying{}, false
	}
	query := endpoint.Query()
	query.Set("encode", "json")
	query.Set("charset", "utf-8")
	for _, category := range strings.Split(hitokotoCategories, ",") {
		query.Add("c", category)
	}
	endpoint.RawQuery = query.Encode()

	callCtx, cancel := context.WithTimeout(ctx, hitokotoTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(callCtx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		h.log.Warn("fortune: build hitokoto request", "error", err)
		return fortuneSaying{}, false
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "SolarNetwork-Stargate/1.0 (+https://solian.app)")

	response, err := h.client.Do(request)
	if err != nil {
		h.log.Warn("fortune: hitokoto request failed", "error", err)
		return fortuneSaying{}, false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		h.log.Warn("fortune: hitokoto returned status", "status", response.StatusCode)
		return fortuneSaying{}, false
	}
	var sentence hitokotoSentence
	if err := json.NewDecoder(response.Body).Decode(&sentence); err != nil {
		h.log.Warn("fortune: decode hitokoto response", "error", err)
		return fortuneSaying{}, false
	}
	content := strings.TrimSpace(sentence.Hitokoto)
	if content == "" {
		h.log.Warn("fortune: hitokoto response carried no sentence", "uuid", sentence.UUID)
		return fortuneSaying{}, false
	}
	source := strings.TrimSpace(sentence.From)
	if source == "" || source == "未知" {
		source = strings.TrimSpace(sentence.FromWho)
	}
	if source == "" {
		source = "一言"
	}
	return fortuneSaying{
		Content:  content,
		Source:   source,
		Language: fortuneLanguageFor(content),
	}, true
}

// fortuneLanguageFor tags the sentence for the API's language filter. 一言 is
// a Chinese corpus but occasionally carries foreign lines.
func fortuneLanguageFor(text string) string {
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			return "zh"
		}
	}
	return "en"
}

// randomFortuneSaying prefers the remote 一言 sentence; an English request is
// served from the embedded catalogue instead of mislabelling Chinese text.
func (d Deps) randomFortuneSaying(ctx context.Context, language string) (fortuneSaying, bool) {
	if d.Hitokoto == nil {
		return fortuneSaying{}, false
	}
	if language != "" && !strings.HasPrefix(strings.ToLower(language), "zh") {
		return fortuneSaying{}, false
	}
	saying, ok := d.Hitokoto.Random(ctx)
	if !ok || saying.Language != "zh" {
		return fortuneSaying{}, false
	}
	return saying, true
}
