// Package oauthconnections keeps stored provider credentials usable. Stargate
// owns the account link; services such as Pulsar ask for the connection and
// receive a token that is refreshed here, so provider client secrets never
// leave the auth service.
package oauthconnections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/config"
	"src.solsynth.dev/sosys/stargate/model"
)

const (
	providerBangumi = "bangumi"

	defaultBangumiTokenUrl  = "https://bgm.tv/oauth/access_token"
	defaultBangumiUserAgent = "Stargate/1.0 (+https://solsynth.dev)"
	refreshWindow           = 5 * time.Minute
	expiryMetaKey           = "token_expires_at"
)

// Store is the persistence slice a refresh needs. The concrete store.Store
// satisfies it; tests use a small fake.
type Store interface {
	UpdateConnectionTokensAndMeta(ctx context.Context, id uuid.UUID, accessToken, refreshToken string, meta map[string]any) error
}

// Service refreshes expired provider tokens on read.
type Service struct {
	cfg   *config.Config
	store Store
	http  *http.Client
	log   *slog.Logger
}

// New builds the refresher. A nil logger falls back to slog.Default.
func New(cfg *config.Config, store Store, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		cfg:   cfg,
		store: store,
		http:  &http.Client{Timeout: 15 * time.Second},
		log:   log,
	}
}

// EnsureFresh returns the connection with a usable access token. Connections
// with no recorded expiry are returned untouched: the provider did not supply
// one, and guessing would rotate tokens needlessly.
func (s *Service) EnsureFresh(ctx context.Context, conn *model.Connection) (*model.Connection, error) {
	if conn == nil || !strings.EqualFold(conn.Provider, providerBangumi) {
		return conn, nil
	}
	expires, ok := tokenExpiry(conn.Meta)
	if !ok || time.Until(expires) > refreshWindow {
		return conn, nil
	}
	if strings.TrimSpace(conn.RefreshToken) == "" {
		return conn, errors.New("bangumi connection has no refresh token")
	}
	cfg := s.cfg.Oidc.Bangumi
	if strings.TrimSpace(cfg.ClientId) == "" || strings.TrimSpace(cfg.ClientSecret) == "" {
		return conn, errors.New("bangumi OAuth client is not configured")
	}

	tokens, err := s.refresh(ctx, cfg, conn.RefreshToken)
	if err != nil {
		return conn, err
	}
	access := tokens.AccessToken
	refresh := conn.RefreshToken
	if strings.TrimSpace(tokens.RefreshToken) != "" {
		refresh = tokens.RefreshToken
	}
	meta := make(map[string]any, len(conn.Meta)+1)
	for key, value := range conn.Meta {
		meta[key] = value
	}
	if tokens.ExpiresIn > 0 {
		meta[expiryMetaKey] = time.Now().UTC().Add(time.Duration(tokens.ExpiresIn) * time.Second).Format(time.RFC3339)
	} else {
		delete(meta, expiryMetaKey)
	}
	connectionID, err := uuid.Parse(conn.Id)
	if err != nil {
		return conn, err
	}
	if err := s.store.UpdateConnectionTokensAndMeta(ctx, connectionID, access, refresh, meta); err != nil {
		return conn, err
	}
	conn.AccessToken = access
	conn.RefreshToken = refresh
	conn.Meta = meta
	return conn, nil
}

type refreshTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

func (s *Service) refresh(ctx context.Context, cfg config.BangumiClient, refreshToken string) (*refreshTokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", cfg.ClientId)
	form.Set("client_secret", cfg.ClientSecret)
	form.Set("refresh_token", refreshToken)
	form.Set("redirect_uri", strings.TrimRight(s.cfg.SiteUrl, "/")+"/auth/callback/"+providerBangumi)

	endpoint := strings.TrimSpace(cfg.TokenUrl)
	if endpoint == "" {
		endpoint = defaultBangumiTokenUrl
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	agent := strings.TrimSpace(cfg.UserAgent)
	if agent == "" {
		agent = defaultBangumiUserAgent
	}
	req.Header.Set("User-Agent", agent)

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("bangumi token refresh returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var tokens refreshTokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tokens); err != nil {
		return nil, err
	}
	if strings.TrimSpace(tokens.AccessToken) == "" {
		return nil, errors.New("bangumi token refresh returned an empty access token")
	}
	return &tokens, nil
}

// tokenExpiry reads the RFC3339 expiry stored in connection meta. JSON may
// decode it as a string; accepting time.Time too keeps direct callers honest.
func tokenExpiry(meta map[string]any) (time.Time, bool) {
	raw, ok := meta[expiryMetaKey]
	if !ok {
		return time.Time{}, false
	}
	switch value := raw.(type) {
	case string:
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return time.Time{}, false
		}
		return parsed, true
	case time.Time:
		return value, true
	default:
		return time.Time{}, false
	}
}
