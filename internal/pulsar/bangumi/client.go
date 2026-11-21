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
	defaultAPIBase      = "https://api.bgm.tv"
	defaultAuthorizeURL = "https://bgm.tv/oauth/authorize"
	defaultTokenURL     = "https://bgm.tv/oauth/access_token"
	defaultUserAgent    = "Pulsar/1.0 (+https://solsynth.dev)"
	// The service publishes no hard rate limit. A steady half-second pace
	// keeps a first sync from arriving as a burst.
	defaultInterval = 500 * time.Millisecond
)

var (
	// ErrNotFound says the subject or collection does not exist, or a private
	// collection is invisible without the right token.
	ErrNotFound = errors.New("bangumi: not found")
	// ErrUnauthorized says the token is missing, expired or lacks the write
	// scope.
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

// Config is the deployment's Bangumi app and endpoint settings.
type Config struct {
	APIBaseURL   string
	AuthorizeURL string
	TokenURL     string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	UserAgent    string
	Scope        string
	HTTPClient   *http.Client
	Interval     time.Duration
}

// Client is one Bangumi transport shared by every account operation.
type Client struct {
	apiBase      string
	authorizeURL string
	tokenURL     string
	clientID     string
	clientSecret string
	redirectURL  string
	userAgent    string
	scope        string
	http         *http.Client
	interval     time.Duration
	mu           sync.Mutex
	nextCall     time.Time
}

// NewClient builds a client with public defaults for every empty setting.
func NewClient(cfg Config) *Client {
	base := strings.TrimRight(strings.TrimSpace(cfg.APIBaseURL), "/")
	if base == "" {
		base = defaultAPIBase
	}
	authorize := strings.TrimSpace(cfg.AuthorizeURL)
	if authorize == "" {
		authorize = defaultAuthorizeURL
	}
	tokenURL := strings.TrimSpace(cfg.TokenURL)
	if tokenURL == "" {
		tokenURL = defaultTokenURL
	}
	ua := strings.TrimSpace(cfg.UserAgent)
	if ua == "" {
		ua = defaultUserAgent
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
		apiBase:      base,
		authorizeURL: authorize,
		tokenURL:     tokenURL,
		clientID:     strings.TrimSpace(cfg.ClientID),
		clientSecret: strings.TrimSpace(cfg.ClientSecret),
		redirectURL:  strings.TrimSpace(cfg.RedirectURL),
		userAgent:    ua,
		scope:        strings.TrimSpace(cfg.Scope),
		http:         httpClient,
		interval:     interval,
	}
}

// Configured reports whether the deployment has the credentials an account
// link needs.
func (c *Client) Configured() bool {
	return c != nil && c.clientID != "" && c.clientSecret != "" && c.redirectURL != ""
}

// RedirectURL is the callback registered on the Bangumi app.
func (c *Client) RedirectURL() string { return c.redirectURL }

// AuthorizeURL builds the consent URL for one state value.
func (c *Client) AuthorizeURL(state string) string {
	parsed, err := url.Parse(c.authorizeURL)
	if err != nil {
		return c.authorizeURL
	}
	query := parsed.Query()
	query.Set("client_id", c.clientID)
	query.Set("response_type", "code")
	query.Set("redirect_uri", c.redirectURL)
	query.Set("state", state)
	// Bangumi documents scope as not implemented yet. Sending a configured
	// value is harmless and future-proof; sending nothing is the default.
	if c.scope != "" {
		query.Set("scope", c.scope)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// ExchangeCode turns an authorization code into a token pair.
func (c *Client) ExchangeCode(ctx context.Context, code string) (*Token, error) {
	return c.tokenRequest(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"code":          {code},
		"redirect_uri":  {c.redirectURL},
	})
}

// Refresh exchanges a refresh token for a fresh pair. Bangumi rotates the
// refresh token, so the caller must persist both values.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (*Token, error) {
	return c.tokenRequest(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"refresh_token": {refreshToken},
		"redirect_uri":  {c.redirectURL},
	})
}

func (c *Client) tokenRequest(ctx context.Context, form url.Values) (*Token, error) {
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			return nil, BusyError{Status: resp.StatusCode}
		}
		return nil, fmt.Errorf("bangumi token exchange returned status %d: %s", resp.StatusCode, tokenError(body))
	}
	var token Token
	if err := json.Unmarshal(body, &token); err != nil {
		return nil, fmt.Errorf("decode bangumi token: %w", err)
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return nil, errors.New("bangumi token exchange returned an empty access token")
	}
	return &token, nil
}

func tokenError(body []byte) string {
	var payload struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		Message          string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		for _, part := range []string{payload.Error, payload.ErrorDescription, payload.Message} {
			if strings.TrimSpace(part) != "" {
				return part
			}
		}
	}
	text := strings.TrimSpace(string(body))
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}

// Me returns the token owner's profile.
func (c *Client) Me(ctx context.Context, accessToken string) (*User, error) {
	var user User
	if err := c.do(ctx, http.MethodGet, "/v0/me", accessToken, nil, &user); err != nil {
		return nil, err
	}
	return &user, nil
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
