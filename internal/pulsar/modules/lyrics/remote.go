package lyrics

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/auth"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
)

const (
	maxPersonaBody = 4 << 20
	// A lyric page is a few dozen short lines; this is orders of magnitude
	// above one and far below a request nobody would send on purpose.
	maxTranslateBytes = 512 * 1024
)

// personaClient is the fleet's route to Persona's OpenAI-compatible API. The
// account's own token travels on every call, so a translation is billed and
// scoped the same way it was when the app asked Persona directly.
type personaClient struct {
	baseURL   string
	userAgent string
	http      *http.Client
}

func newPersonaClient(baseURL, userAgent string) *personaClient {
	ua := strings.TrimSpace(userAgent)
	if ua == "" {
		ua = "Pulsar/1.0 (+https://solsynth.dev)"
	}
	return &personaClient{
		baseURL:   strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		userAgent: ua,
		http:      &http.Client{Timeout: 95 * time.Second},
	}
}

func (p *personaClient) enabled() bool { return p != nil && p.baseURL != "" }

func (p *personaClient) do(ctx context.Context, method, path, token string, payload []byte) (*upstreamReply, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", p.userAgent)
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Authorization", "Bearer "+token)

	response, err := p.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxPersonaBody))
	if err != nil {
		return nil, err
	}
	return &upstreamReply{
		Status:     response.StatusCode,
		Body:       raw,
		RetryAfter: strings.TrimSpace(response.Header.Get("Retry-After")),
	}, nil
}

// lrclibGet proxies `/api/get`: one record by signature, exactly as LRCLIB
// answers it. The app still decides which row is close enough to a recording.
func (i *instance) lrclibGet(c *gin.Context) {
	query := url.Values{}
	track := strings.TrimSpace(c.Query("track_name"))
	artist := strings.TrimSpace(c.Query("artist_name"))
	if track == "" || artist == "" {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "track_name and artist_name are required")
		return
	}
	query.Set("track_name", track)
	query.Set("artist_name", artist)
	if album := strings.TrimSpace(c.Query("album_name")); album != "" {
		query.Set("album_name", album)
	}
	if raw := strings.TrimSpace(c.Query("duration")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 1 || seconds > 3600 {
			httpx.BadRequest(c, "INVALID_ARGUMENT", "duration must be between 1 and 3600 seconds")
			return
		}
		query.Set("duration", strconv.Itoa(seconds))
	}

	reply, err := i.lrclib.get(c.Request.Context(), query)
	writeLRCLIB(c, reply, err, false)
}

// lrclibSearch proxies `/api/search`: either free text in `q`, or the
// track-and-artist pair the automatic lookup uses.
func (i *instance) lrclibSearch(c *gin.Context) {
	query := url.Values{}
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		query.Set("q", q)
	}
	if track := strings.TrimSpace(c.Query("track_name")); track != "" {
		query.Set("track_name", track)
	}
	if artist := strings.TrimSpace(c.Query("artist_name")); artist != "" {
		query.Set("artist_name", artist)
	}
	if len(query) == 0 {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "q, track_name or artist_name is required")
		return
	}

	reply, err := i.lrclib.search(c.Request.Context(), query)
	// A search that found nothing is an empty list, not a missing record: a
	// picker should say "nothing under that search", not draw an error.
	writeLRCLIB(c, reply, err, true)
}

// writeLRCLIB maps an upstream status onto the app's expectations: 404 is
// "nobody has this lyric" (or an empty search), 429 keeps its Retry-After,
// and a dead upstream is a retryable gateway failure.
func writeLRCLIB(c *gin.Context, reply *upstreamReply, err error, emptyOnNotFound bool) {
	if err != nil {
		httpx.Fail(c, errs.New("UPSTREAM_UNAVAILABLE", "The lyrics service could not be reached.", http.StatusBadGateway))
		return
	}
	switch reply.Status {
	case http.StatusOK:
		c.Data(http.StatusOK, "application/json; charset=utf-8", reply.Body)
	case http.StatusNotFound:
		if emptyOnNotFound {
			c.Data(http.StatusOK, "application/json; charset=utf-8", []byte("[]"))
			return
		}
		httpx.NotFound(c, "lyrics not found")
	case http.StatusTooManyRequests:
		if reply.RetryAfter != "" {
			c.Header("Retry-After", reply.RetryAfter)
		}
		httpx.Fail(c, errs.New("UPSTREAM_RATE_LIMITED", "The lyrics service asked us to slow down.", http.StatusTooManyRequests))
	default:
		httpx.Fail(c, errs.New("UPSTREAM_UNAVAILABLE", "The lyrics service could not answer.", http.StatusBadGateway))
	}
}

type translateMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type translateRequest struct {
	Model    string             `json:"model" binding:"required"`
	Stream   bool               `json:"stream,omitempty"`
	Messages []translateMessage `json:"messages" binding:"required"`
}

// translateLyrics forwards one stateless chat completion to Persona. The app
// still owns the prompt, batching and parsing; Pulsar owns the network call
// and the account token that authorises it.
func (i *instance) translateLyrics(c *gin.Context) {
	if !i.persona.enabled() {
		httpx.Fail(c, errs.New("LYRICS_TRANSLATION_UNAVAILABLE", "Lyric translation is not configured on this server.", http.StatusServiceUnavailable))
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxTranslateBytes)
	var req translateRequest
	if !httpx.BindJSON(c, &req) {
		return
	}
	if req.Stream {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "streaming is not supported")
		return
	}
	model := strings.TrimSpace(req.Model)
	if model == "" || len(model) > 200 {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "model is required")
		return
	}
	if len(req.Messages) == 0 || len(req.Messages) > 16 {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "messages are required")
		return
	}
	total := 0
	for _, message := range req.Messages {
		switch message.Role {
		case "system", "user", "assistant":
		default:
			httpx.BadRequest(c, "INVALID_ARGUMENT", "message role is not supported")
			return
		}
		total += len(message.Content)
	}
	if total == 0 || total > maxTranslateBytes {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "the translation request is empty or too large")
		return
	}
	token, ok := callerToken(c)
	if !ok {
		httpx.Unauthorized(c)
		return
	}

	payload, err := json.Marshal(map[string]any{
		"model":    model,
		"stream":   false,
		"messages": req.Messages,
	})
	if err != nil {
		httpx.Internal(c)
		return
	}
	reply, err := i.persona.do(c.Request.Context(), http.MethodPost, "/v1/chat/completions", token, payload)
	writePersona(c, reply, err, false)
}

// listModels proxies Persona's catalogue so the app's model picker does not
// have to know the gateway path or hold a second client.
func (i *instance) listModels(c *gin.Context) {
	if !i.persona.enabled() {
		httpx.Fail(c, errs.New("LYRICS_TRANSLATION_UNAVAILABLE", "Lyric translation is not configured on this server.", http.StatusServiceUnavailable))
		return
	}
	token, ok := callerToken(c)
	if !ok {
		httpx.Unauthorized(c)
		return
	}
	reply, err := i.persona.do(c.Request.Context(), http.MethodGet, "/models", token, nil)
	// An upstream that has no catalogue is an empty list to the picker, not
	// an error card: the reader can still choose Google.
	writePersona(c, reply, err, true)
}

func writePersona(c *gin.Context, reply *upstreamReply, err error, emptyOnNotFound bool) {
	if err != nil {
		httpx.Fail(c, errs.New("UPSTREAM_UNAVAILABLE", "The translation service could not be reached.", http.StatusBadGateway))
		return
	}
	switch reply.Status {
	case http.StatusOK:
		c.Data(http.StatusOK, "application/json; charset=utf-8", reply.Body)
	case http.StatusNotFound:
		if emptyOnNotFound {
			c.Data(http.StatusOK, "application/json; charset=utf-8", []byte("[]"))
			return
		}
		httpx.Fail(c, errs.New("UPSTREAM_UNAVAILABLE", "The translation service has no such model.", http.StatusBadGateway))
	case http.StatusUnauthorized:
		httpx.Unauthorized(c)
	case http.StatusForbidden:
		httpx.Forbidden(c, "The account cannot use this translation model.")
	case http.StatusTooManyRequests:
		if reply.RetryAfter != "" {
			c.Header("Retry-After", reply.RetryAfter)
		}
		httpx.Fail(c, errs.New("UPSTREAM_RATE_LIMITED", "The translation service asked us to slow down.", http.StatusTooManyRequests))
	default:
		httpx.Fail(c, errs.New("UPSTREAM_UNAVAILABLE", "The translation service could not answer.", http.StatusBadGateway))
	}
}

// callerToken reads the same token authn validated, including the header and
// query/cookie fallbacks the fleet accepts. Persona speaks Bearer only.
func callerToken(c *gin.Context) (string, bool) {
	info, ok := auth.ExtractToken(c.Request)
	if !ok || strings.TrimSpace(info.Token) == "" {
		return "", false
	}
	return strings.TrimSpace(info.Token), true
}
