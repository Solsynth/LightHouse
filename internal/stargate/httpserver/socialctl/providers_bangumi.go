package socialctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Bangumi uses a standard OAuth2 authorization-code grant. Its OAuth host and
// API host are different (bgm.tv vs api.bgm.tv), which is why the endpoints
// live in config with public defaults.
const (
	defaultBangumiAuthorizeUrl = "https://bgm.tv/oauth/authorize"
	defaultBangumiTokenUrl     = "https://bgm.tv/oauth/access_token"
	defaultBangumiApiBaseUrl   = "https://api.bgm.tv"
	defaultBangumiUserAgent    = "Stargate/1.0 (+https://solsynth.dev)"
)

type bangumiProvider struct{ base *baseProvider }

func (p *bangumiProvider) name() string { return p.base.name }

func (p *bangumiProvider) authorizationURL(_ context.Context, state, _ string) (string, error) {
	cfg := p.base.d.Cfg.Oidc.Bangumi
	endpoint := firstNonEmpty(cfg.AuthorizeUrl, defaultBangumiAuthorizeUrl)
	q := url.Values{}
	q.Set("client_id", cfg.ClientId)
	q.Set("response_type", "code")
	q.Set("redirect_uri", p.base.cfg.RedirectUri)
	q.Set("state", state)
	return endpoint + "?" + q.Encode(), nil
}

func (p *bangumiProvider) processCallback(ctx context.Context, data *callbackData) (*userInfo, error) {
	if strings.TrimSpace(data.Code) == "" {
		return nil, errors.New("bangumi callback has no authorization code")
	}
	tokens, err := p.exchangeCode(ctx, data.Code)
	if err != nil {
		return nil, err
	}
	if tokens == nil || strings.TrimSpace(tokens.AccessToken) == "" {
		return nil, errors.New("bangumi did not return an access token")
	}
	user, err := p.getUserInfo(ctx, tokens.AccessToken)
	if err != nil {
		return nil, err
	}
	user.Provider = "bangumi"
	user.AccessToken = tokens.AccessToken
	user.RefreshToken = tokens.RefreshToken
	if tokens.ExpiresIn > 0 {
		user.TokenExpiresAt = time.Now().UTC().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	}
	return user, nil
}

func (p *bangumiProvider) exchangeCode(ctx context.Context, code string) (*tokenResponse, error) {
	cfg := p.base.d.Cfg.Oidc.Bangumi
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", cfg.ClientId)
	form.Set("client_secret", cfg.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", p.base.cfg.RedirectUri)

	endpoint := firstNonEmpty(cfg.TokenUrl, defaultBangumiTokenUrl)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", firstNonEmpty(cfg.UserAgent, defaultBangumiUserAgent))

	resp, err := p.base.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("bangumi token endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var tokens tokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tokens); err != nil {
		return nil, err
	}
	return &tokens, nil
}

func (p *bangumiProvider) getUserInfo(ctx context.Context, accessToken string) (*userInfo, error) {
	cfg := p.base.d.Cfg.Oidc.Bangumi
	endpoint := strings.TrimRight(firstNonEmpty(cfg.ApiBaseUrl, defaultBangumiApiBaseUrl), "/") + "/v0/me"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", firstNonEmpty(cfg.UserAgent, defaultBangumiUserAgent))

	resp, err := p.base.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, errors.New("bangumi rejected the access token")
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("bangumi profile endpoint returned %s", resp.Status)
	}
	var me struct {
		ID       int    `json:"id"`
		Username string `json:"username"`
		Nickname string `json:"nickname"`
		Avatar   struct {
			Large  string `json:"large"`
			Medium string `json:"medium"`
			Small  string `json:"small"`
		} `json:"avatar"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&me); err != nil {
		return nil, err
	}
	if me.ID == 0 || strings.TrimSpace(me.Username) == "" {
		return nil, errors.New("bangumi returned a profile without an id or username")
	}
	return &userInfo{
		UserId:            strconv.Itoa(me.ID),
		PreferredUsername: me.Username,
		DisplayName:       me.Nickname,
		ProfilePictureUrl: firstNonEmpty(me.Avatar.Large, me.Avatar.Medium, me.Avatar.Small),
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
