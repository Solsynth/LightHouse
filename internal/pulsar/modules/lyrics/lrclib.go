package lyrics

import (
	"context"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultLRCLIBBaseURL = "https://lrclib.net"
	// LRCLIB asks every client to leave a small gap between requests. The
	// limit is fleet-wide because every GroovyBox device now goes through
	// this one client.
	lrclibRequestGap = 250 * time.Millisecond
	// A lyric record is a few kilobytes; a response past this is not one.
	maxLRCLIBBody = 4 << 20
)

// lrclibClient is the one LRCLIB transport Pulsar shares between requests.
// It keeps the documented gap and remembers a rate-limit window so a burst
// from many devices cannot turn one 429 into a stream of them.
type lrclibClient struct {
	baseURL   string
	userAgent string
	http      *http.Client

	gap time.Duration

	mu           sync.Mutex
	nextCall     time.Time
	blockedUntil time.Time
}

func newLRCLIBClient(baseURL, userAgent string) *lrclibClient {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = defaultLRCLIBBaseURL
	}
	ua := strings.TrimSpace(userAgent)
	if ua == "" {
		ua = "Pulsar/1.0 (+https://solsynth.dev)"
	}
	return &lrclibClient{
		baseURL:   base,
		userAgent: ua,
		http:      &http.Client{Timeout: 15 * time.Second},
		gap:       lrclibRequestGap,
	}
}

// upstreamReply is one upstream answer, status and body kept as LRCLIB sent
// them so the app's existing selection and parsing still decide what a row
// means.
type upstreamReply struct {
	Status     int
	Body       []byte
	RetryAfter string
}

func (c *lrclibClient) get(ctx context.Context, query url.Values) (*upstreamReply, error) {
	return c.do(ctx, "/api/get", query)
}

func (c *lrclibClient) search(ctx context.Context, query url.Values) (*upstreamReply, error) {
	return c.do(ctx, "/api/search", query)
}

func (c *lrclibClient) do(ctx context.Context, path string, query url.Values) (*upstreamReply, error) {
	// A rate limit on the exact lookup must stop the search behind it: that
	// is exactly what Retry-After exists to prevent.
	if reply := c.blocked(); reply != nil {
		return reply, nil
	}
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	if reply := c.blocked(); reply != nil {
		return reply, nil
	}

	endpoint := c.baseURL + path
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", c.userAgent)
	request.Header.Set("Accept", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxLRCLIBBody))
	if err != nil {
		return nil, err
	}
	reply := &upstreamReply{Status: response.StatusCode, Body: body}
	if response.StatusCode == http.StatusTooManyRequests {
		reply.RetryAfter = c.rememberRetryAfter(response.Header.Get("Retry-After"))
	}
	return reply, nil
}

// wait reserves a send slot and sleeps until it. Slots are reserved under the
// lock, so concurrent callers queue instead of all waking on the same instant.
func (c *lrclibClient) wait(ctx context.Context) error {
	c.mu.Lock()
	now := time.Now()
	slot := c.nextCall
	if slot.Before(now) {
		slot = now
	}
	c.nextCall = slot.Add(c.gap)
	c.mu.Unlock()

	if delay := time.Until(slot); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

// blocked answers with a 429 while a Retry-After window is open, without
// sending anything upstream.
func (c *lrclibClient) blocked() *upstreamReply {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.blockedUntil.After(time.Now()) {
		return nil
	}
	seconds := int(time.Until(c.blockedUntil).Seconds()) + 1
	return &upstreamReply{
		Status:     http.StatusTooManyRequests,
		RetryAfter: strconv.Itoa(seconds),
	}
}

// rememberRetryAfter opens the fleet's rate-limit window and returns the
// header the client should put on its own answer.
func (c *lrclibClient) rememberRetryAfter(raw string) string {
	seconds, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || seconds <= 0 {
		seconds = 30
	}
	// Up to 30% jitter, as LRCLIB's documentation asks for, so a fleet that
	// was paused together does not return together.
	jitter := rand.Float64() * float64(seconds) * 0.3
	window := time.Duration(float64(seconds)+jitter) * time.Second

	c.mu.Lock()
	c.blockedUntil = time.Now().Add(window)
	c.mu.Unlock()
	return strconv.Itoa(seconds)
}
