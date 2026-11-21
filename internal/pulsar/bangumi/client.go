package bangumi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultAPIBase = "https://api.bgm.tv"
	// The service publishes no hard rate limit. A steady half-second pace
	// keeps a first sync from arriving as a burst.
	defaultInterval = 500 * time.Millisecond
)

var (
	// ErrNotFound says the subject or collection does not exist, or a private
	// collection is invisible without the right token.
	ErrNotFound = errors.New("bangumi: not found")
	// ErrUnauthorized says the token is missing, expired or lacks the write
	// scope. The TTL is owned by Stargate, which refreshes it before handing
	// the token here.
	ErrUnauthorized = errors.New("bangumi: unauthorized")
)

// BusyError is the service asking for room. Callers should retry later rather
// than treating it as a permanent failure.
type BusyError struct {
	Status int
}

func (e BusyError) Error() string {
	return fmt.Sprintf("bangumi is busy (status %d)", e.Status)
}

// Config is the collection API transport. OAuth credentials intentionally do
// not appear here: Stargate owns the grant and gives Pulsar only the token.
type Config struct {
	APIBaseURL string
	UserAgent  string
	HTTPClient *http.Client
	Interval   time.Duration
}

// Client is one Bangumi transport shared by every account operation.
type Client struct {
	apiBase   string
	userAgent string
	http      *http.Client
	interval  time.Duration
	mu        sync.Mutex
	nextCall  time.Time
}

// NewClient builds a client with public defaults for every empty setting.
func NewClient(cfg Config) *Client {
	base := strings.TrimRight(strings.TrimSpace(cfg.APIBaseURL), "/")
	if base == "" {
		base = defaultAPIBase
	}
	ua := strings.TrimSpace(cfg.UserAgent)
	if ua == "" {
		ua = "Pulsar/1.0 (+https://solsynth.dev)"
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	return &Client{
		apiBase:   base,
		userAgent: ua,
		http:      httpClient,
		interval:  interval,
	}
}

// ListCollections pages through one subject type of a user's collection. The
// access token is optional for public lists but required to see private rows.
func (c *Client) ListCollections(ctx context.Context, accessToken, username string, subjectType, offset, limit int) (*CollectionPage, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	query := url.Values{}
	if subjectType > 0 {
		query.Set("subject_type", strconv.Itoa(subjectType))
	}
	query.Set("limit", strconv.Itoa(limit))
	query.Set("offset", strconv.Itoa(offset))
	path := "/v0/users/" + url.PathEscape(username) + "/collections?" + query.Encode()

	var page CollectionPage
	if err := c.do(ctx, http.MethodGet, path, accessToken, nil, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

// GetCollection returns one subject's collection row.
func (c *Client) GetCollection(ctx context.Context, accessToken, username string, subjectID int) (*SubjectCollection, error) {
	var collection SubjectCollection
	path := fmt.Sprintf("/v0/users/%s/collections/%d", url.PathEscape(username), subjectID)
	if err := c.do(ctx, http.MethodGet, path, accessToken, nil, &collection); err != nil {
		return nil, err
	}
	return &collection, nil
}

// UpsertCollection sets a subject's collection. POST creates when absent and
// updates when present; PATCH is used when the caller knows the row exists.
func (c *Client) UpsertCollection(ctx context.Context, accessToken string, subjectID int, payload CollectionPayload, create bool) error {
	method := http.MethodPatch
	if create {
		method = http.MethodPost
	}
	path := fmt.Sprintf("/v0/users/-/collections/%d", subjectID)
	return c.do(ctx, method, path, accessToken, payload, nil)
}

// do sends one API request. A 404 is ErrNotFound; 401/403 is ErrUnauthorized;
// 429/503 is BusyError.
func (c *Client) do(ctx context.Context, method, path, accessToken string, payload, dst any) error {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	if err := c.wait(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.apiBase+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(accessToken) != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ErrUnauthorized
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable:
		return BusyError{Status: resp.StatusCode}
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		text, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("bangumi returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(text)))
	}
	if dst == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(dst)
}

// wait spaces calls across every operation on this client.
func (c *Client) wait(ctx context.Context) error {
	c.mu.Lock()
	now := time.Now()
	slot := c.nextCall
	if slot.Before(now) {
		slot = now
	}
	c.nextCall = slot.Add(c.interval)
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
