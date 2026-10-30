package socialctl

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
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

// --- Last.fm ---

// LastFm is not OAuth2/OIDC: the user is sent to last.fm/api/auth with the API
// key and a callback URL, Last.fm redirects back to that URL with an
// authentication token appended, and the token is exchanged for an (infinite
// lifetime) session key through a shared-secret-signed auth.getSession call.

type lastfmProvider struct{ base *baseProvider }

func (p *lastfmProvider) name() string { return p.base.name }

// lastFmAPIBase is the Last.fm web service root.
const lastFmAPIBase = "https://ws.audioscrobbler.com/2.0/"

func (p *lastfmProvider) authorizationURL(ctx context.Context, state, nonce string) (string, error) {
	// Last.fm has no `state` parameter (and no nonce): the callback URL is
	// echoed verbatim with `&token=` appended when it already carries a query
	// string, so the state travels inside `cb` exactly like Steam's
	// openid.return_to.
	q := url.Values{}
	q.Set("api_key", p.base.cfg.ClientId)
	q.Set("cb", p.base.cfg.RedirectUri+"?state="+url.QueryEscape(state))
	return "https://www.last.fm/api/auth/?" + q.Encode(), nil
}

func (p *lastfmProvider) processCallback(ctx context.Context, data *callbackData) (*userInfo, error) {
	token := strings.TrimSpace(data.QueryParameters["token"])
	if token == "" {
		return nil, errors.New("No authentication token in Last.fm response")
	}
	session, err := p.getSession(ctx, token)
	if err != nil {
		return nil, err
	}
	user := &userInfo{
		UserId:            session.Name,
		DisplayName:       session.Name,
		PreferredUsername: session.Name,
		Provider:          p.base.name,
		AccessToken:       session.Key,
	}
	// Profile enrichment is best-effort: user.getInfo only decorates the
	// connection and must never fail an otherwise valid link. Last.fm does not
	// expose the account email, so Email/EmailVerified stay empty.
	if info, err := p.getUserInfo(ctx, session.Name); err == nil {
		if info.RealName != "" {
			user.DisplayName = info.RealName
		}
		user.ProfilePictureUrl = lastFmLargestImage(info.Image)
	}
	return user, nil
}

// lastFmSession is the session object returned by auth.getSession.
type lastFmSession struct {
	Name string
	Key  string
}

// getSession mirrors step 3/4 of the Last.fm web auth flow: the callback token
// is consumed (single use, 60-minute lifetime) for a web service session key.
func (p *lastfmProvider) getSession(ctx context.Context, token string) (*lastFmSession, error) {
	apiKey := p.base.cfg.ClientId
	apiSecret := p.base.cfg.ClientSecret
	if apiKey == "" || apiSecret == "" {
		return nil, errors.New("Last.fm API key or shared secret is not configured")
	}
	params := map[string]string{
		"api_key": apiKey,
		"method":  "auth.getSession",
		"token":   token,
	}
	q := url.Values{}
	for name, value := range params {
		q.Set(name, value)
	}
	q.Set("api_sig", lastFmSignature(apiSecret, params))
	q.Set("format", "json")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lastFmAPIBase+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.base.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		// Last.fm answers parameter errors with a plain-text body and a non-2xx
		// status, so surface it instead of pretending the body is JSON.
		return nil, fmt.Errorf("lastfm auth.getSession returned %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var body struct {
		Session struct {
			Name string `json:"name"`
			Key  string `json:"key"`
		} `json:"session"`
		Error   int    `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	if body.Error != 0 {
		return nil, fmt.Errorf("lastfm auth.getSession error %d: %s", body.Error, body.Message)
	}
	if body.Session.Name == "" || body.Session.Key == "" {
		return nil, errors.New("Last.fm did not return a session key")
	}
	return &lastFmSession{Name: body.Session.Name, Key: body.Session.Key}, nil
}

type lastFmImage struct {
	Size string `json:"size"`
	Text string `json:"#text"`
}

type lastFmUser struct {
	Name     string        `json:"name"`
	RealName string        `json:"realname"`
	URL      string        `json:"url"`
	Image    []lastFmImage `json:"image"`
}

// getUserInfo loads the public profile (real name + avatar) through
// user.getInfo. The call needs only the API key, no signature.
func (p *lastfmProvider) getUserInfo(ctx context.Context, username string) (*lastFmUser, error) {
	q := url.Values{}
	q.Set("method", "user.getInfo")
	q.Set("api_key", p.base.cfg.ClientId)
	q.Set("user", username)
	q.Set("format", "json")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lastFmAPIBase+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.base.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("lastfm user.getInfo returned %s", resp.Status)
	}
	var body struct {
		User    lastFmUser `json:"user"`
		Error   int        `json:"error"`
		Message string     `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Error != 0 {
		return nil, fmt.Errorf("lastfm user.getInfo error %d: %s", body.Error, body.Message)
	}
	return &body.User, nil
}

// lastFmLargestImage returns the largest avatar URL of a user.getInfo image
// list (Last.fm orders the entries small → extralarge, leaving #text empty
// when the account has no avatar).
func lastFmLargestImage(images []lastFmImage) string {
	picture := ""
	for _, image := range images {
		if image.Text != "" {
			picture = image.Text
		}
	}
	return picture
}

// lastFmSignature builds a Last.fm api_sig: md5 over the alphabetically ordered
// <name><value> pairs with the shared secret appended. The `format` and
// `callback` parameters are never part of the signed string.
func lastFmSignature(secret string, params map[string]string) string {
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)
	var signed strings.Builder
	for _, name := range names {
		signed.WriteString(name)
		signed.WriteString(params[name])
	}
	signed.WriteString(secret)
	sum := md5.Sum([]byte(signed.String()))
	return hex.EncodeToString(sum[:])
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
