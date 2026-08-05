package socialctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// GitHub + Afdian providers, ported from GitHubOidcService.cs and
// AfdianOidcService.cs. Neither implements standard OIDC discovery; the C#
// overrides the endpoints directly.

// --- GitHub ---

type githubProvider struct{ base *baseProvider }

func (p *githubProvider) name() string { return p.base.name }

func (p *githubProvider) authorizationURL(ctx context.Context, state, nonce string) (string, error) {
	q := url.Values{}
	q.Set("client_id", p.base.cfg.ClientId)
	q.Set("redirect_uri", p.base.cfg.RedirectUri)
	q.Set("scope", "user:email")
	q.Set("state", state)
	// The C# omits response_type (GitHub OAuth implies code).
	return "https://github.com/login/oauth/authorize?" + q.Encode(), nil
}

func (p *githubProvider) processCallback(ctx context.Context, data *callbackData) (*userInfo, error) {
	tokenResponse, err := p.exchangeCode(ctx, data.Code)
	if err != nil {
		return nil, err
	}
	if tokenResponse == nil || tokenResponse.AccessToken == "" {
		return nil, errors.New("Failed to obtain access token from GitHub")
	}
	userInfo, err := p.getUserInfo(ctx, tokenResponse.AccessToken)
	if err != nil {
		return nil, err
	}
	userInfo.AccessToken = tokenResponse.AccessToken
	userInfo.RefreshToken = tokenResponse.RefreshToken
	return userInfo, nil
}

// exchangeCode mirrors GitHubOidcService.ExchangeCodeForTokensAsync.
func (p *githubProvider) exchangeCode(ctx context.Context, code string) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("client_id", p.base.cfg.ClientId)
	form.Set("client_secret", p.base.cfg.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", p.base.cfg.RedirectUri)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://github.com/login/oauth/access_token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := p.base.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("github token endpoint returned %s", resp.Status)
	}
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, err
	}
	return &tr, nil
}

// getUserInfo mirrors GitHubOidcService.GetUserInfoAsync + GetPrimaryEmailAsync.
func (p *githubProvider) getUserInfo(ctx context.Context, accessToken string) (*userInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "DysonNetwork.Pass")

	resp, err := p.base.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("github userinfo endpoint returned %s", resp.Status)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	str := func(k string) string {
		if v, ok := body[k].(string); ok {
			return v
		}
		return ""
	}

	email := str("email")
	if email == "" {
		email = p.getPrimaryEmail(ctx, accessToken)
	}
	return &userInfo{
		UserId:            strconv.FormatInt(int64(jsonNumber(body["id"])), 10),
		Email:             email,
		DisplayName:       str("name"),
		PreferredUsername: str("login"),
		ProfilePictureUrl: str("avatar_url"),
		Provider:          p.base.name,
	}, nil
}

func (p *githubProvider) getPrimaryEmail(ctx context.Context, accessToken string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user/emails", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "DysonNetwork.Pass")

	resp, err := p.base.http.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return ""
	}
	var emails []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&emails); err != nil {
		return ""
	}
	for _, e := range emails {
		if primary, _ := e["primary"].(bool); primary {
			if email, ok := e["email"].(string); ok {
				return email
			}
		}
	}
	return ""
}

// --- Afdian (ifdian.net) ---

type afdianProvider struct{ base *baseProvider }

func (p *afdianProvider) name() string { return p.base.name }

func (p *afdianProvider) authorizationURL(ctx context.Context, state, nonce string) (string, error) {
	q := url.Values{}
	q.Set("client_id", p.base.cfg.ClientId)
	q.Set("redirect_uri", p.base.cfg.RedirectUri)
	q.Set("response_type", "code")
	q.Set("scope", "basic")
	q.Set("state", state)
	return "https://ifdian.net/oauth2/authorize?" + q.Encode(), nil
}

func (p *afdianProvider) processCallback(ctx context.Context, data *callbackData) (*userInfo, error) {
	// Afdian's API is not OAuth2-compliant: the token exchange response IS the
	// userinfo ({"data": {"user_id", "name", "avatar"}}).
	form := url.Values{}
	form.Set("client_id", p.base.cfg.ClientId)
	form.Set("client_secret", p.base.cfg.ClientSecret)
	form.Set("grant_type", "authorization_code")
	form.Set("code", data.Code)
	form.Set("redirect_uri", p.base.cfg.RedirectUri)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://ifdian.net/api/oauth2/access_token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.base.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("afdian token endpoint returned %s", resp.Status)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	dataEl, _ := body["data"].(map[string]any)
	str := func(k string) string {
		if v, ok := dataEl[k].(string); ok {
			return v
		}
		return ""
	}
	return &userInfo{
		UserId:            str("user_id"),
		DisplayName:       str("name"),
		ProfilePictureUrl: str("avatar"),
		Provider:          p.base.name,
	}, nil
}

// jsonNumber coerces a JSON number (float64 or json.Number) to int64.
func jsonNumber(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}
