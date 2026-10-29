package socialctl

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"github.com/golang-jwt/jwt/v5"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"src.solsynth.dev/sosys/stargate/internal/config"
)

func testDeps(cfg *config.Config) Deps {
	if cfg == nil {
		cfg = config.Default()
	}
	return Deps{Cfg: cfg}
}

func writeP8(t *testing.T, key *ecdsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "key.p8")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAppleClientSecret(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Oidc.Apple.ClientId = "dev.solsynth.solian"
	cfg.Oidc.Apple.TeamId = "TEAM123"
	cfg.Oidc.Apple.KeyId = "KEY456"
	cfg.Oidc.Apple.PrivateKeyPath = writeP8(t, key)

	b := newBaseProvider("apple", testDeps(cfg))
	secret, err := b.appleClientSecret()
	if err != nil {
		t.Fatalf("appleClientSecret: %v", err)
	}
	parts := strings.Split(secret, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 JWT parts, got %d", len(parts))
	}
	headerJSON, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var header map[string]any
	_ = json.Unmarshal(headerJSON, &header)
	if header["alg"] != "ES256" || header["kid"] != "KEY456" {
		t.Fatalf("header = %v", header)
	}
	parsed, err := jwt.Parse(secret, func(tok *jwt.Token) (any, error) { return &key.PublicKey, nil },
		jwt.WithValidMethods([]string{"ES256"}), jwt.WithAudience("https://appleid.apple.com"))
	if err != nil {
		t.Fatalf("signature/claims validation failed: %v", err)
	}
	claims := parsed.Claims.(jwt.MapClaims)
	if claims["iss"] != "TEAM123" || claims["sub"] != "dev.solsynth.solian" {
		t.Fatalf("claims = %v", claims)
	}
	if exp, _ := claims["exp"].(float64); int64(exp)-int64(claims["iat"].(float64)) != 300 {
		t.Fatalf("exp-iat not 300s: %v", claims)
	}

	// Missing config must error with the C# literal message.
	b2 := newBaseProvider("apple", testDeps(config.Default()))
	if _, err := b2.appleClientSecret(); err == nil || err.Error() != "Apple OIDC configuration is missing required values (TeamId, ClientId, KeyId, PrivateKeyPath)." {
		t.Fatalf("missing-config error = %v", err)
	}
}

func TestValidateIDTokenApple(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	kid := "apple-kid-1"
	x := base64.RawURLEncoding.EncodeToString(key.PublicKey.X.Bytes())
	y := base64.RawURLEncoding.EncodeToString(key.PublicKey.Y.Bytes())
	jwks := map[string]any{"keys": []map[string]any{
		{"kty": "EC", "kid": kid, "use": "sig", "alg": "ES256", "crv": "P-256", "x": x, "y": y},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jwks)
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.Oidc.Apple.ClientId = "dev.solsynth.solian"
	b := newBaseProvider("apple", testDeps(cfg))

	mint := func(claims jwt.MapClaims) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
		tok.Header["kid"] = kid
		s, err := tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	now := time.Now().UTC()
	good := mint(jwt.MapClaims{
		"iss": "https://appleid.apple.com", "aud": "dev.solsynth.solian",
		"sub": "001234.abc", "email": "u@example.com", "email_verified": "true",
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
	})
	claims, err := b.validateIDToken(context.Background(), good, "https://appleid.apple.com", srv.URL, cfg.Oidc.Apple.ClientId)
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	user := extractUserInfoFromJwt(claims, "apple")
	if user.Email != "u@example.com" || !user.EmailVerified || user.UserId != "001234.abc" {
		t.Fatalf("user = %+v", user)
	}

	badIssuer := mint(jwt.MapClaims{
		"iss": "https://evil.example", "aud": "dev.solsynth.solian", "sub": "x",
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
	})
	if _, err := b.validateIDToken(context.Background(), badIssuer, "https://appleid.apple.com", srv.URL, cfg.Oidc.Apple.ClientId); err == nil {
		t.Fatal("wrong issuer accepted")
	} else if _, ok := err.(*idTokenValidationError); !ok {
		t.Fatalf("error type = %T", err)
	}
}

func TestValidateIDTokenGoogle(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	kid := "google-kid-1"
	n := base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes())
	jwks := map[string]any{"keys": []map[string]any{
		{"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256", "n": n, "e": e},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jwks)
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.Oidc.Google.ClientId = "google-client"
	b := newBaseProvider("google", testDeps(cfg))

	now := time.Now().UTC()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "https://accounts.google.com", "aud": "google-client",
		"sub": "g-user-1", "email": "g@example.com", "email_verified": true,
		"name": "G User", "given_name": "G", "family_name": "User",
		"preferred_username": "guser", "picture": "https://pic/g.png",
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
	})
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := b.validateIDToken(context.Background(), signed, "https://accounts.google.com", srv.URL, "google-client")
	if err != nil {
		t.Fatalf("valid google token rejected: %v", err)
	}
	user := extractUserInfoFromJwt(claims, "google")
	if user.UserId != "g-user-1" || user.DisplayName != "G User" || user.PreferredUsername != "guser" || !user.EmailVerified || user.ProfilePictureUrl != "https://pic/g.png" {
		t.Fatalf("user = %+v", user)
	}
}

func TestParseOidcState(t *testing.T) {
	st, ok := parseOidcState(`{"flow_type":1,"account_id":"11111111-1111-1111-1111-111111111111","provider":"google","nonce":"n","device_id":"d","version":1}`)
	if !ok || st.FlowType != flowConnect || st.AccountId == nil || *st.AccountId != "11111111-1111-1111-1111-111111111111" || *st.Provider != "google" {
		t.Fatalf("snake json: %+v ok=%v", st, ok)
	}
	st, ok = parseOidcState(`{"flowType":"Login","returnUrl":"/home","deviceId":null,"version":1}`)
	if !ok || st.FlowType != flowLogin || st.ReturnUrl == nil || *st.ReturnUrl != "/home" {
		t.Fatalf("camel json: %+v ok=%v", st, ok)
	}
	st, ok = parseOidcState("11111111-1111-1111-1111-111111111111|google|nonce|dev|connect")
	if !ok || st.FlowType != flowConnect || *st.AccountId != "11111111-1111-1111-1111-111111111111" || *st.Provider != "google" || *st.DeviceId != "dev" {
		t.Fatalf("pipe connect: %+v ok=%v", st, ok)
	}
	st, ok = parseOidcState("/feed|dev|login")
	if !ok || st.FlowType != flowLogin || *st.ReturnUrl != "/feed" || *st.DeviceId != "dev" {
		t.Fatalf("pipe login: %+v ok=%v", st, ok)
	}
	st, ok = parseOidcState("/profile")
	if !ok || st.FlowType != flowLogin || *st.ReturnUrl != "/profile" {
		t.Fatalf("single: %+v ok=%v", st, ok)
	}
	if _, ok := parseOidcState(""); ok {
		t.Fatal("empty accepted")
	}
	if _, ok := parseOidcState("garbage|||"); ok {
		t.Fatal("garbage accepted")
	}
}

func TestMetadataShape(t *testing.T) {
	u := &userInfo{UserId: "id1", Email: "a@b.c", EmailVerified: true, DisplayName: "A B", PreferredUsername: "ab", ProfilePictureUrl: "https://x/y.png"}
	meta := u.toMetadata()
	for _, k := range []string{"user_id", "email", "email_verified", "display_name", "preferred_username", "profile_picture_url"} {
		if _, ok := meta[k]; !ok {
			t.Fatalf("missing %s in %v", k, meta)
		}
	}
	if meta["email_verified"] != true {
		t.Fatalf("email_verified = %v", meta["email_verified"])
	}
}

func TestSteamAuthorizationURL(t *testing.T) {
	cfg := config.Default()
	cfg.SiteUrl = "https://example.com"
	b := newBaseProvider("steam", testDeps(cfg))
	p := &steamProvider{base: b}
	u, err := p.authorizationURL(context.Background(), "state123", "nonce")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, "https://steamcommunity.com/openid/login?") {
		t.Fatalf("url = %s", u)
	}
	if !strings.Contains(u, "openid.mode=checkid_setup") || !strings.Contains(u, "openid.ns=http%3A%2F%2Fspecs.openid.net%2Fauth%2F2.0") {
		t.Fatalf("url missing openid params: %s", u)
	}
	if !strings.Contains(u, "openid.return_to=https%3A%2F%2Fexample.com%2Fauth%2Fcallback%2Fsteam%3Fstate%3Dstate123") {
		t.Fatalf("return_to wrong: %s", u)
	}
	if !strings.Contains(u, "openid.realm=https%3A%2F%2Fexample.com") {
		t.Fatalf("realm wrong: %s", u)
	}

	// Callback verification: id_res mode + claimed_id → steam id.
	data := &callbackData{QueryParameters: map[string]string{
		"openid.mode":       "id_res",
		"openid.claimed_id": "https://steamcommunity.com/openid/id/76561198000000000",
		"openid.return_to":  "https://example.com/auth/callback/steam?state=state123",
	}}
	ui, err := p.processCallback(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if ui.UserId != "76561198000000000" || ui.Email != "" {
		t.Fatalf("userInfo = %+v", ui)
	}
	if data.State != "state123" {
		t.Fatalf("state not extracted from return_to: %q", data.State)
	}

	if _, err := p.processCallback(context.Background(), &callbackData{QueryParameters: map[string]string{"openid.mode": "cancel"}}); err == nil {
		t.Fatal("cancel accepted")
	}
	if _, err := p.processCallback(context.Background(), &callbackData{QueryParameters: map[string]string{
		"openid.mode": "id_res", "openid.claimed_id": "https://steamcommunity.com/openid/id/notanumber",
	}}); err == nil || err.Error() != "Invalid Steam ID format" {

		t.Fatalf("bad steam id error = %v", err)
	}
}

func TestTwitterProviderFactoryAndRedirect(t *testing.T) {
	cfg := config.Default()
	cfg.SiteUrl = "https://example.com"
	cfg.Oidc.Twitter.ClientId = "client-id"
	svc, err := newProvider("twitter", testDeps(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if svc.name() != "twitter" {
		t.Fatalf("provider name = %q", svc.name())
	}
	twitter, ok := svc.(*twitterProvider)
	if !ok {
		t.Fatalf("provider type = %T", svc)
	}
	if twitter.base.cfg.RedirectUri != "https://example.com/auth/callback/twitter" {
		t.Fatalf("redirect URI = %q", twitter.base.cfg.RedirectUri)
	}
	if twitter.base.cfg.ClientId != "client-id" {
		t.Fatalf("client ID = %q", twitter.base.cfg.ClientId)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestTwitterProviderOAuthExchangeAndUser(t *testing.T) {
	cfg := config.Default()
	cfg.SiteUrl = "https://example.com"
	cfg.Oidc.Twitter.ClientId = "client-id"
	cfg.Oidc.Twitter.ClientSecret = "client-secret"
	b := newBaseProvider("twitter", testDeps(cfg))
	requests := 0
	b.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		switch requests {
		case 1:
			if r.URL.String() != "https://api.x.com/2/oauth2/token" {
				t.Fatalf("token URL = %s", r.URL)
			}
			if id, secret, ok := r.BasicAuth(); !ok || id != "client-id" || secret != "client-secret" {
				t.Fatalf("unexpected token authorization: %q %q %v", id, secret, ok)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			form, err := url.ParseQuery(string(body))
			if err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]string{
				"code":          "auth-code",
				"code_verifier": "verifier",
				"grant_type":    "authorization_code",
				"redirect_uri":  "https://example.com/auth/callback/twitter",
			} {
				if got := form.Get(key); got != want {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"access","refresh_token":"refresh"}`)),
				Header:     make(http.Header),
			}, nil
		case 2:
			if r.URL.Host != "api.x.com" || r.URL.Path != "/2/users/me" {
				t.Fatalf("user URL = %s", r.URL)
			}
			if got := r.URL.Query().Get("user.fields"); got != "confirmed_email,profile_image_url" {
				t.Fatalf("user.fields = %q", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer access" {
				t.Fatalf("authorization = %q", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"data":{"id":"123","name":"Example","username":"example","confirmed_email":"e@example.com","profile_image_url":"https://img.example/avatar"}}`)),
				Header:     make(http.Header),
			}, nil
		default:
			t.Fatalf("unexpected request %d", requests)
			return nil, nil
		}
	})}
	p := &twitterProvider{base: b}

	tokens, err := p.exchangeCode(context.Background(), "auth-code", "verifier")
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "access" || tokens.RefreshToken != "refresh" {
		t.Fatalf("tokens = %+v", tokens)
	}
	user, err := p.getUserInfo(context.Background(), tokens.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if user.UserId != "123" || user.Email != "e@example.com" || !user.EmailVerified || user.PreferredUsername != "example" {
		t.Fatalf("user = %+v", user)
	}
}
