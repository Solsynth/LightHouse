package socialctl

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// This file ports the five social-login providers of DysonNetwork.Padlock
// Auth/OpenId (OidcService.cs + GoogleOidcService/AppleOidcService/
// MicrosoftOidcService/SteamOidcService/DiscordOidcService.cs). Token
// exchanges use raw form posts exactly like the C# HttpClient calls.

// callbackData mirrors OidcCallbackData.
type callbackData struct {
	Code            string
	IdToken         string
	State           string
	RawData         string
	QueryParameters map[string]string
}

// userInfo mirrors OidcUserInfo.
type userInfo struct {
	UserId            string
	Email             string
	EmailVerified     bool
	FirstName         string
	LastName          string
	DisplayName       string
	PreferredUsername string
	ProfilePictureUrl string
	Provider          string
	RefreshToken      string
	AccessToken       string
}

// toMetadata mirrors OidcUserInfo.ToMetadata: snake_case keys, only
// non-blank values.
func (u *userInfo) toMetadata() map[string]any {
	meta := map[string]any{}
	if u.UserId != "" {
		meta["user_id"] = u.UserId
	}
	if u.Email != "" {
		meta["email"] = u.Email
	}
	meta["email_verified"] = u.EmailVerified
	if u.FirstName != "" {
		meta["first_name"] = u.FirstName
	}
	if u.LastName != "" {
		meta["last_name"] = u.LastName
	}
	if u.DisplayName != "" {
		meta["display_name"] = u.DisplayName
	}
	if u.PreferredUsername != "" {
		meta["preferred_username"] = u.PreferredUsername
	}
	if u.ProfilePictureUrl != "" {
		meta["profile_picture_url"] = u.ProfilePictureUrl
	}
	return meta
}

// provider is the ported OidcService contract.
type provider interface {
	// name is the canonical lowercase provider key used in URLs and rows.
	name() string
	authorizationURL(ctx context.Context, state, nonce string) (string, error)
	processCallback(ctx context.Context, data *callbackData) (*userInfo, error)
}

// newProvider mirrors OidcService.GetOidcService (apple/google/microsoft/
// discord/steam/github/afdian/twitter).
func newProvider(name string, d Deps) (provider, error) {
	switch strings.ToLower(name) {
	case "apple":
		return &appleProvider{base: newBaseProvider("apple", d)}, nil
	case "google":
		return &googleProvider{base: newBaseProvider("google", d)}, nil
	case "microsoft":
		return &microsoftProvider{base: newBaseProvider("microsoft", d)}, nil
	case "discord":
		return &discordProvider{base: newBaseProvider("discord", d)}, nil
	case "steam":
		return &steamProvider{base: newBaseProvider("steam", d)}, nil
	case "github":
		return &githubProvider{base: newBaseProvider("github", d)}, nil
	case "afdian":
		return &afdianProvider{base: newBaseProvider("afdian", d)}, nil
	case "twitter":
		return &twitterProvider{base: newBaseProvider("twitter", d)}, nil
	default:
		return nil, fmt.Errorf("Unsupported provider: %s", name)
	}
}

// providerConfig mirrors ProviderConfiguration; RedirectUri follows the C#
// GetProviderConfig: SiteUrl + "/auth/callback/{provider}".
type providerConfig struct {
	ClientId     string
	ClientSecret string
	RedirectUri  string
}

// baseProvider carries the shared OidcService machinery.
type baseProvider struct {
	d    Deps
	name string
	http *http.Client
	cfg  providerConfig
}

func newBaseProvider(name string, d Deps) *baseProvider {
	cfg := providerConfig{
		RedirectUri: strings.TrimRight(d.Cfg.SiteUrl, "/") + "/auth/callback/" + name,
	}
	switch name {
	case "google":
		cfg.ClientId = d.Cfg.Oidc.Google.ClientId
		cfg.ClientSecret = d.Cfg.Oidc.Google.ClientSecret
	case "apple":
		cfg.ClientId = d.Cfg.Oidc.Apple.ClientId
	case "microsoft":
		cfg.ClientId = d.Cfg.Oidc.Microsoft.ClientId
		cfg.ClientSecret = d.Cfg.Oidc.Microsoft.ClientSecret
	case "discord":
		cfg.ClientId = d.Cfg.Oidc.Discord.ClientId
		cfg.ClientSecret = d.Cfg.Oidc.Discord.ClientSecret
	case "github":
		cfg.ClientId = d.Cfg.Oidc.GitHub.ClientId
		cfg.ClientSecret = d.Cfg.Oidc.GitHub.ClientSecret
	case "afdian":
		cfg.ClientId = d.Cfg.Oidc.Afdian.ClientId
		cfg.ClientSecret = d.Cfg.Oidc.Afdian.ClientSecret
	case "twitter":
		cfg.ClientId = d.Cfg.Oidc.Twitter.ClientId
		cfg.ClientSecret = d.Cfg.Oidc.Twitter.ClientSecret
	}
	return &baseProvider{d: d, name: name, http: &http.Client{Timeout: 20 * time.Second}, cfg: cfg}
}

// discoveryDocument mirrors OidcDiscoveryDocument.
type discoveryDocument struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
	JwksUri               string `json:"jwks_uri"`
}

// getDiscoveryDocument mirrors OidcService.GetDiscoveryDocumentAsync with a
// 15-minute "oidc-discovery:{provider}" cache.
func (b *baseProvider) getDiscoveryDocument(ctx context.Context) (*discoveryDocument, error) {
	endpoint := b.discoveryEndpoint()
	if endpoint == "" {
		return nil, nil
	}
	cacheKey := "oidc-discovery:" + b.name
	var doc discoveryDocument
	if found, err := b.d.cacheGet(ctx, cacheKey, &doc); err == nil && found && doc.AuthorizationEndpoint != "" {
		return &doc, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("discovery endpoint returned %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, err
	}
	_ = b.d.cacheSet(ctx, cacheKey, doc, 15*time.Minute)
	return &doc, nil
}

// discoveryEndpoint returns the provider's OIDC discovery URL ("" = none,
// like Steam/Discord in the C#).
func (b *baseProvider) discoveryEndpoint() string {
	switch b.name {
	case "google":
		return "https://accounts.google.com/.well-known/openid-configuration"
	case "apple":
		return "https://appleid.apple.com/.well-known/openid-configuration"
	case "microsoft":
		return b.d.Cfg.Oidc.Microsoft.DiscoveryEndpoint
	default:
		return ""
	}
}

// tokenResponse mirrors OidcTokenResponse.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	IdToken      string `json:"id_token"`
}

// exchangeCodeForTokens mirrors OidcService.ExchangeCodeForTokensAsync with
// the per-provider parameter sets from the C# services.
func (b *baseProvider) exchangeCodeForTokens(ctx context.Context, code, codeVerifier string) (*tokenResponse, error) {
	var tokenEndpoint string
	switch b.name {
	case "discord":
		tokenEndpoint = "https://discord.com/api/oauth2/token"
	default:
		doc, err := b.getDiscoveryDocument(ctx)
		if err != nil {
			return nil, err
		}
		if doc == nil || doc.TokenEndpoint == "" {
			return nil, errors.New("Token endpoint not found in discovery document")
		}
		tokenEndpoint = doc.TokenEndpoint
	}

	form := url.Values{}
	switch b.name {
	case "apple":
		secret, err := b.appleClientSecret()
		if err != nil {
			return nil, err
		}
		form.Set("client_id", b.cfg.ClientId)
		form.Set("client_secret", secret)
		form.Set("code", code)
		form.Set("grant_type", "authorization_code")
		form.Set("redirect_uri", b.cfg.RedirectUri)
	case "google":
		form.Set("client_id", b.cfg.ClientId)
		form.Set("code", code)
		form.Set("grant_type", "authorization_code")
		form.Set("redirect_uri", b.cfg.RedirectUri)
		if b.cfg.ClientSecret != "" {
			form.Set("client_secret", b.cfg.ClientSecret)
		}
		if codeVerifier != "" {
			form.Set("code_verifier", codeVerifier)
		}
	case "microsoft":
		form.Set("client_id", b.cfg.ClientId)
		form.Set("scope", "openid profile email")
		form.Set("code", code)
		form.Set("redirect_uri", b.cfg.RedirectUri)
		form.Set("grant_type", "authorization_code")
		form.Set("client_secret", b.cfg.ClientSecret)
	case "discord":
		form.Set("client_id", b.cfg.ClientId)
		form.Set("client_secret", b.cfg.ClientSecret)
		form.Set("grant_type", "authorization_code")
		form.Set("code", code)
		form.Set("redirect_uri", b.cfg.RedirectUri)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("token endpoint returned %s: %s", resp.Status, string(body))
	}
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, err
	}
	return &tr, nil
}

// idTokenValidationError maps to the C# SecurityTokenValidationException
// (401 OIDC_INVALID_IDENTITY_TOKEN in AppleMobileLogin).
type idTokenValidationError struct{ msg string }

func (e *idTokenValidationError) Error() string { return e.msg }

// jwk mirrors an Apple/Google JWKS key.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// validateIDToken validates an id_token against the provider's JWKS
// (issuer + audience + lifetime) and returns the raw claims. This ports the
// IdTokenValidationStrategy for Google and AppleOidcService.ValidateTokenAsync.
// NOTE: the C# AppleKey.ToSecurityKey only supports RSA keys, which fails
// against Apple's real EC P-256 keys; this port also handles EC keys so Apple
// sign-in actually works.
func (b *baseProvider) validateIDToken(ctx context.Context, idToken, issuer, jwksURI, clientID string) (jwt.MapClaims, error) {
	invalid := func(err error) (jwt.MapClaims, error) {
		return nil, &idTokenValidationError{msg: err.Error()}
	}
	p := jwt.NewParser(jwt.WithoutClaimsValidation())
	tok, _, err := p.ParseUnverified(idToken, jwt.MapClaims{})
	if err != nil {
		return invalid(err)
	}
	kid, _ := tok.Header["kid"].(string)

	keys, err := b.fetchJWKS(ctx, jwksURI)
	if err != nil {
		return invalid(err)
	}
	var match *jwk
	for i := range keys {
		if keys[i].Kid == kid {
			match = &keys[i]
			break
		}
	}
	if match == nil {
		return invalid(errors.New("Unable to find matching key in JWKS"))
	}
	key, err := buildJWKSKey(match)
	if err != nil {
		return invalid(err)
	}

	claims := jwt.MapClaims{}
	_, err = jwt.ParseWithClaims(idToken, claims, func(t *jwt.Token) (any, error) { return key, nil },
		jwt.WithValidMethods([]string{"RS256", "ES256"}),
		jwt.WithIssuer(issuer),
		jwt.WithAudience(clientID),
		jwt.WithExpirationRequired())
	if err != nil {
		return invalid(err)
	}
	return claims, nil
}

func (b *baseProvider) fetchJWKS(ctx context.Context, uri string) ([]jwk, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("JWKS endpoint returned %s", resp.Status)
	}
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, err
	}
	return doc.Keys, nil
}

func buildJWKSKey(k *jwk) (any, error) {
	switch k.Kty {
	case "RSA":
		if k.N == "" || k.E == "" {
			return nil, errors.New("Invalid key data")
		}
		n, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			return nil, errors.New("Invalid key data")
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			return nil, errors.New("Invalid key data")
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(eBytes).Int64())}, nil
	case "EC":
		if k.X == "" || k.Y == "" {
			return nil, errors.New("Invalid key data")
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil {
			return nil, errors.New("Invalid key data")
		}
		y, err := base64.RawURLEncoding.DecodeString(k.Y)
		if err != nil {
			return nil, errors.New("Invalid key data")
		}
		return &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}, nil
	default:
		return nil, fmt.Errorf("Unsupported key type: %s", k.Kty)
	}
}

// extractUserInfoFromJwt mirrors ValidateAndExtractIdToken/ExtractUserInfoFromJwt.
func extractUserInfoFromJwt(claims jwt.MapClaims, providerName string) *userInfo {
	str := func(k string) string {
		if v, ok := claims[k].(string); ok {
			return v
		}
		return ""
	}
	emailVerified := false
	switch v := claims["email_verified"].(type) {
	case bool:
		emailVerified = v
	case string:
		emailVerified = v == "true" || v == "True"
	}
	givenName := str("given_name")
	familyName := str("family_name")
	name := str("name")
	username := str("preferred_username")
	email := str("email")
	if username == "" && email != "" {
		username = strings.SplitN(email, "@", 2)[0]
	}
	displayName := name
	if displayName == "" {
		displayName = strings.TrimSpace(givenName + " " + familyName)
	}
	return &userInfo{
		UserId:            str("sub"),
		Email:             email,
		EmailVerified:     emailVerified,
		FirstName:         givenName,
		LastName:          familyName,
		DisplayName:       displayName,
		PreferredUsername: username,
		ProfilePictureUrl: str("picture"),
		Provider:          providerName,
	}
}

// generateCodeVerifier mirrors OidcService.GenerateCodeVerifier (32 random
// bytes, base64url without padding).
func generateCodeVerifier() string {
	randomBytes := make([]byte, 32)
	_, _ = rand.Read(randomBytes)
	return base64.RawURLEncoding.EncodeToString(randomBytes)
}

// generateCodeChallenge mirrors OidcService.GenerateCodeChallenge (SHA-256,
// base64url without padding).
func generateCodeChallenge(codeVerifier string) string {
	sum := sha256.Sum256([]byte(codeVerifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// appleClientSecret mirrors AppleOidcService.GenerateClientSecret: an ES256
// JWT signed with the .p8 key (alg/kid header, iss=TeamId, sub=ClientId,
// aud=appleid, iat/exp).
func (b *baseProvider) appleClientSecret() (string, error) {
	teamID := b.d.Cfg.Oidc.Apple.TeamId
	clientID := b.d.Cfg.Oidc.Apple.ClientId
	keyID := b.d.Cfg.Oidc.Apple.KeyId
	keyPath := b.d.Cfg.Oidc.Apple.PrivateKeyPath
	if teamID == "" || clientID == "" || keyID == "" || keyPath == "" {
		return "", errors.New("Apple OIDC configuration is missing required values (TeamId, ClientId, KeyId, PrivateKeyPath).")
	}

	pemData, err := os.ReadFile(keyPath)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(pemData)
	if block == nil {
		return "", errors.New("no PEM block found in Apple private key")
	}
	var key *ecdsa.PrivateKey
	if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		var ok bool
		key, ok = parsed.(*ecdsa.PrivateKey)
		if !ok {
			return "", errors.New("Apple private key is not an EC key")
		}
	} else if parsed, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		key = parsed
	} else {
		return "", errors.New("failed to parse Apple private key")
	}

	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"iss": teamID,
		"iat": now.Unix(),
		"exp": now.Add(5 * time.Minute).Unix(),
		"aud": "https://appleid.apple.com",
		"sub": clientID,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tok.Header["kid"] = keyID
	return tok.SignedString(key)
}

// ─────────────────────────── Google ───────────────────────────

type googleProvider struct{ base *baseProvider }

func (p *googleProvider) name() string { return p.base.name }

func (p *googleProvider) authorizationURL(ctx context.Context, state, nonce string) (string, error) {
	doc, err := p.base.getDiscoveryDocument(ctx)
	if err != nil {
		return "", err
	}
	if doc == nil || doc.AuthorizationEndpoint == "" {
		return "", errors.New("Authorization endpoint not found in discovery document")
	}
	codeVerifier := generateCodeVerifier()
	codeChallenge := generateCodeChallenge(codeVerifier)

	q := url.Values{}
	q.Set("client_id", p.base.cfg.ClientId)
	q.Set("redirect_uri", p.base.cfg.RedirectUri)
	q.Set("response_type", "code")
	q.Set("scope", "openid email profile")
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")

	if err := p.base.d.cacheSet(ctx, "pkce:"+state, codeVerifier, 15*time.Minute); err != nil {
		return "", err
	}
	return doc.AuthorizationEndpoint + "?" + q.Encode(), nil
}

func (p *googleProvider) processCallback(ctx context.Context, data *callbackData) (*userInfo, error) {
	state := data.State
	codeVerifierKey := "pkce:" + state
	var codeVerifier string
	found, err := p.base.d.cacheGet(ctx, codeVerifierKey, &codeVerifier)
	if err != nil || !found || codeVerifier == "" {
		return nil, errors.New("PKCE code verifier not found or expired")
	}
	p.base.d.cacheRemove(ctx, codeVerifierKey)

	tokenResponse, err := p.base.exchangeCodeForTokens(ctx, data.Code, codeVerifier)
	if err != nil {
		return nil, err
	}
	if tokenResponse == nil {
		return nil, errors.New("Failed to exchange code for tokens")
	}
	if tokenResponse.IdToken == "" {
		return nil, errors.New("ID token not found in response")
	}

	doc, err := p.base.getDiscoveryDocument(ctx)
	if err != nil {
		return nil, err
	}
	jwksURI := "https://www.googleapis.com/oauth2/v3/certs"
	if doc != nil && doc.JwksUri != "" {
		jwksURI = doc.JwksUri
	}
	claims, err := p.base.validateIDToken(ctx, tokenResponse.IdToken,
		"https://accounts.google.com", jwksURI, p.base.cfg.ClientId)
	if err != nil {
		return nil, err
	}
	user := extractUserInfoFromJwt(claims, p.base.name)
	user.AccessToken = tokenResponse.AccessToken
	user.RefreshToken = tokenResponse.RefreshToken

	// IdTokenValidationStrategy additionally pulls the profile picture from
	// the userinfo endpoint when an access token is present.
	if doc != nil && doc.UserinfoEndpoint != "" && tokenResponse.AccessToken != "" {
		if picture, err := p.base.fetchPicture(ctx, doc.UserinfoEndpoint, tokenResponse.AccessToken); err == nil && picture != "" {
			user.ProfilePictureUrl = picture
		}
	}
	return user, nil
}

func (b *baseProvider) fetchPicture(ctx context.Context, userinfoEndpoint, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userinfoEndpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := b.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("userinfo endpoint returned %s", resp.Status)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if picture, ok := body["picture"].(string); ok {
		return picture, nil
	}
	return "", nil
}

// ─────────────────────────── Apple ───────────────────────────

type appleProvider struct{ base *baseProvider }

func (p *appleProvider) name() string { return p.base.name }

func (p *appleProvider) authorizationURL(ctx context.Context, state, nonce string) (string, error) {
	doc, err := p.base.getDiscoveryDocument(ctx)
	if err != nil {
		return "", err
	}
	if doc == nil || doc.AuthorizationEndpoint == "" {
		return "", errors.New("Authorization endpoint not found in discovery document")
	}
	q := url.Values{}
	q.Set("client_id", p.base.cfg.ClientId)
	q.Set("redirect_uri", p.base.cfg.RedirectUri)
	q.Set("response_type", "code id_token")
	q.Set("scope", "name email")
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("response_mode", "form_post")
	return doc.AuthorizationEndpoint + "?" + q.Encode(), nil
}

// appleUserData mirrors AppleUserData (the `user` form field on first login).
type appleUserData struct {
	Name  *appleNameData `json:"name"`
	Email string         `json:"email"`
}

type appleNameData struct {
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
}

func (p *appleProvider) processCallback(ctx context.Context, data *callbackData) (*userInfo, error) {
	claims, err := p.base.validateIDToken(ctx, data.IdToken,
		"https://appleid.apple.com", "https://appleid.apple.com/auth/keys", p.base.cfg.ClientId)
	if err != nil {
		return nil, err
	}
	user := extractUserInfoFromJwt(claims, p.base.name)

	if data.RawData != "" {
		var userData appleUserData
		if err := json.Unmarshal([]byte(data.RawData), &userData); err == nil && userData.Name != nil {
			user.FirstName = userData.Name.FirstName
			user.LastName = userData.Name.LastName
			user.DisplayName = strings.TrimSpace(user.FirstName + " " + user.LastName)
		}
	}

	// Exchange the authorization code (optional for Apple: the id_token alone
	// identifies the user; the code yields the access/refresh tokens).
	if data.Code == "" {
		return user, nil
	}
	tokenResponse, err := p.base.exchangeCodeForTokens(ctx, data.Code, "")
	if err != nil {
		return nil, err
	}
	if tokenResponse == nil {
		return user, nil
	}
	user.AccessToken = tokenResponse.AccessToken
	user.RefreshToken = tokenResponse.RefreshToken
	return user, nil
}

// ─────────────────────────── Microsoft ───────────────────────────

type microsoftProvider struct{ base *baseProvider }

func (p *microsoftProvider) name() string { return p.base.name }

func (p *microsoftProvider) authorizationURL(ctx context.Context, state, nonce string) (string, error) {
	doc, err := p.base.getDiscoveryDocument(ctx)
	if err != nil {
		return "", err
	}
	if doc == nil || doc.AuthorizationEndpoint == "" {
		return "", errors.New("Authorization endpoint not found in discovery document.")
	}
	q := url.Values{}
	q.Set("client_id", p.base.cfg.ClientId)
	q.Set("redirect_uri", p.base.cfg.RedirectUri)
	q.Set("response_type", "code")
	q.Set("scope", "openid profile email")
	q.Set("state", state)
	q.Set("nonce", nonce)
	return doc.AuthorizationEndpoint + "?" + q.Encode(), nil
}

func (p *microsoftProvider) processCallback(ctx context.Context, data *callbackData) (*userInfo, error) {
	tokenResponse, err := p.base.exchangeCodeForTokens(ctx, data.Code, "")
	if err != nil {
		return nil, err
	}
	if tokenResponse == nil || tokenResponse.AccessToken == "" {
		return nil, errors.New("Failed to obtain access token from Microsoft")
	}

	doc, err := p.base.getDiscoveryDocument(ctx)
	if err != nil {
		return nil, err
	}
	if doc == nil || doc.UserinfoEndpoint == "" {
		return nil, errors.New("Userinfo endpoint not found in discovery document.")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, doc.UserinfoEndpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tokenResponse.AccessToken)
	resp, err := p.base.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("userinfo endpoint returned %s", resp.Status)
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
	return &userInfo{
		UserId:            str("sub"),
		Email:             str("email"),
		DisplayName:       str("name"),
		PreferredUsername: str("preferred_username"),
		ProfilePictureUrl: str("picture"),
		Provider:          p.base.name,
		AccessToken:       tokenResponse.AccessToken,
		RefreshToken:      tokenResponse.RefreshToken,
	}, nil
}

// ─────────────────────────── Discord ───────────────────────────

type discordProvider struct{ base *baseProvider }

func (p *discordProvider) name() string { return p.base.name }

func (p *discordProvider) authorizationURL(ctx context.Context, state, nonce string) (string, error) {
	q := url.Values{}
	q.Set("client_id", p.base.cfg.ClientId)
	q.Set("redirect_uri", p.base.cfg.RedirectUri)
	q.Set("response_type", "code")
	q.Set("scope", "identify email")
	q.Set("state", state)
	return "https://discord.com/api/oauth2/authorize?" + q.Encode(), nil
}

func (p *discordProvider) processCallback(ctx context.Context, data *callbackData) (*userInfo, error) {
	tokenResponse, err := p.base.exchangeCodeForTokens(ctx, data.Code, "")
	if err != nil {
		return nil, err
	}
	if tokenResponse == nil || tokenResponse.AccessToken == "" {
		return nil, errors.New("Failed to obtain access token from Discord")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://discord.com/api/users/@me", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tokenResponse.AccessToken)
	resp, err := p.base.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("discord userinfo endpoint returned %s", resp.Status)
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
	userID := str("id")
	avatar := str("avatar")
	picture := ""
	if avatar != "" {
		picture = "https://cdn.discordapp.com/avatars/" + userID + "/" + avatar + ".png"
	}
	return &userInfo{
		UserId:            userID,
		Email:             str("email"),
		EmailVerified:     boolOf(body["verified"]),
		DisplayName:       str("global_name"),
		PreferredUsername: str("username"),
		ProfilePictureUrl: picture,
		Provider:          p.base.name,
		AccessToken:       tokenResponse.AccessToken,
		RefreshToken:      tokenResponse.RefreshToken,
	}, nil
}

func boolOf(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

// ─────────────────────────── Steam ───────────────────────────

type steamProvider struct{ base *baseProvider }

func (p *steamProvider) name() string { return p.base.name }

func (p *steamProvider) authorizationURL(ctx context.Context, state, nonce string) (string, error) {
	returnURL := p.base.cfg.RedirectUri
	sep := "?"
	if strings.Contains(returnURL, "?") {
		sep = "&"
	}
	returnTo := returnURL + sep + "state=" + url.QueryEscape(state)

	realm := returnURL
	if u, err := url.Parse(returnURL); err == nil {
		realm = u.Scheme + "://" + u.Host
	}

	q := url.Values{}
	q.Set("openid.ns", "http://specs.openid.net/auth/2.0")
	q.Set("openid.mode", "checkid_setup")
	q.Set("openid.return_to", returnTo)
	q.Set("openid.realm", realm)
	q.Set("openid.identity", "http://specs.openid.net/auth/2.0/identifier_select")
	q.Set("openid.claimed_id", "http://specs.openid.net/auth/2.0/identifier_select")
	return "https://steamcommunity.com/openid/login?" + q.Encode(), nil
}

func (p *steamProvider) processCallback(ctx context.Context, data *callbackData) (*userInfo, error) {
	if data.QueryParameters["openid.mode"] != "id_res" {
		return nil, errors.New("Invalid OpenID response mode")
	}

	// Steam OpenID 2.0 does not echo the state parameter at the top level;
	// it travels inside openid.return_to (the provider echoes it back).
	if returnTo := data.QueryParameters["openid.return_to"]; returnTo != "" {
		if u, err := url.Parse(returnTo); err == nil {
			if state := u.Query().Get("state"); state != "" {
				data.State = state
			}
		}
	}

	claimedID := data.QueryParameters["openid.claimed_id"]
	if claimedID == "" {
		return nil, errors.New("No claimed_id in OpenID response")
	}
	parts := strings.Split(claimedID, "/")
	steamID := parts[len(parts)-1]
	if _, err := strconv.ParseUint(steamID, 10, 64); err != nil {
		return nil, errors.New("Invalid Steam ID format")
	}

	return &userInfo{UserId: steamID, Provider: p.base.name}, nil
}
