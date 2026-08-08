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
	"time"
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

// --- X (Twitter) ---

type twitterProvider struct{ base *baseProvider }

func (p *twitterProvider) name() string { return p.base.name }

func (p *twitterProvider) authorizationURL(ctx context.Context, state, nonce string) (string, error) {
	codeVerifier := generateCodeVerifier()
	codeChallenge := generateCodeChallenge(codeVerifier)
	if err := p.base.d.cacheSet(ctx, "pkce:"+state, codeVerifier, 15*time.Minute); err != nil {
		return "", err
	}

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", p.base.cfg.ClientId)
	q.Set("redirect_uri", p.base.cfg.RedirectUri)
	q.Set("scope", "users.read users.email tweet.write offline.access")
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	return "https://x.com/i/oauth2/authorize?" + q.Encode(), nil
}

func (p *twitterProvider) processCallback(ctx context.Context, data *callbackData) (*userInfo, error) {
	var codeVerifier string
	found, err := p.base.d.cacheGet(ctx, "pkce:"+data.State, &codeVerifier)
	if err != nil || !found || codeVerifier == "" {
		return nil, errors.New("PKCE code verifier not found or expired")
	}
	p.base.d.cacheRemove(ctx, "pkce:"+data.State)

	tokens, err := p.exchangeCode(ctx, data.Code, codeVerifier)
	if err != nil {
		return nil, err
	}
	if tokens == nil || tokens.AccessToken == "" {
		return nil, errors.New("Failed to obtain access token from X")
	}
	user, err := p.getUserInfo(ctx, tokens.AccessToken)
	if err != nil {
		return nil, err
	}
	user.AccessToken = tokens.AccessToken
	user.RefreshToken = tokens.RefreshToken
	return user, nil
}

func (p *twitterProvider) exchangeCode(ctx context.Context, code, codeVerifier string) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("code", code)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", p.base.cfg.RedirectUri)
	form.Set("code_verifier", codeVerifier)
	if p.base.cfg.ClientSecret == "" {
		form.Set("client_id", p.base.cfg.ClientId)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.x.com/2/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if p.base.cfg.ClientSecret != "" {
		req.SetBasicAuth(p.base.cfg.ClientId, p.base.cfg.ClientSecret)
	}

	resp, err := p.base.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("X token endpoint returned %s", resp.Status)
	}
	var tokens tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokens); err != nil {
		return nil, err
	}
	return &tokens, nil
}

func (p *twitterProvider) getUserInfo(ctx context.Context, accessToken string) (*userInfo, error) {
	q := url.Values{}
	q.Set("user.fields", "confirmed_email,profile_image_url")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.x.com/2/users/me?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := p.base.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("X user endpoint returned %s", resp.Status)
	}

	var body struct {
		Data struct {
			Id              string `json:"id"`
			Name            string `json:"name"`
			Username        string `json:"username"`
			ConfirmedEmail  string `json:"confirmed_email"`
			ProfileImageURL string `json:"profile_image_url"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Data.Id == "" {
		return nil, errors.New("X user endpoint returned no user id")
	}
	return &userInfo{
		UserId:            body.Data.Id,
		Email:             body.Data.ConfirmedEmail,
		EmailVerified:     body.Data.ConfirmedEmail != "",
		DisplayName:       body.Data.Name,
		PreferredUsername: body.Data.Username,
		ProfilePictureUrl: body.Data.ProfileImageURL,
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
