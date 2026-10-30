package socialctl

// Last.fm provider coverage: the authorization redirect, the api_sig-signed
// session exchange (pinned against Last.fm's documented example), and the two
// callback flows against a real store — the connection keeps the session key
// for later API calls, and an account linked through a provider that never
// exposes an email can still sign in.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/db"
	"src.solsynth.dev/sosys/stargate/internal/dbtest"
	"src.solsynth.dev/sosys/stargate/internal/geo"
	"src.solsynth.dev/sosys/stargate/internal/migrate"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

const (
	testLastFmToken = "token123"
	// md5("api_keytest-api-keymethodauth.getSessiontoken" + testLastFmToken + "test-secret")
	testLastFmSignature = "9d622a571c6efb520e440f567268d8af"
)

func TestLastFmSignatureMatchesDocumentedExample(t *testing.T) {
	// Last.fm's spec example: an account with the secret "mysecret" signing
	// api_key=xxxxxxxx, method=auth.getSession, token=xxxxxxx.
	got := lastFmSignature("mysecret", map[string]string{
		"api_key": "xxxxxxxx",
		"method":  "auth.getSession",
		"token":   "xxxxxxx",
	})
	if got != "68afb32bee072407a63b6c41f3e1e2b4" {
		t.Fatalf("api_sig = %q", got)
	}
}

func TestLastFmAuthorizationURL(t *testing.T) {
	cfg := config.Default()
	cfg.SiteUrl = "https://example.com"
	cfg.Oidc.LastFm.ApiKey = "test-api-key"
	cfg.Oidc.LastFm.ApiSecret = "test-secret"

	svc, err := newProvider("lastfm", testDeps(cfg))
	if err != nil {
		t.Fatal(err)
	}
	lastfm, ok := svc.(*lastfmProvider)
	if !ok {
		t.Fatalf("provider type = %T", svc)
	}
	if lastfm.base.cfg.RedirectUri != "https://example.com/auth/callback/lastfm" {
		t.Fatalf("redirect URI = %q", lastfm.base.cfg.RedirectUri)
	}
	if lastfm.base.cfg.ClientId != "test-api-key" || lastfm.base.cfg.ClientSecret != "test-secret" {
		t.Fatalf("credentials = %+v", lastfm.base.cfg)
	}
	if !providerAvailable("lastfm", testDeps(cfg)) {
		t.Fatal("configured provider reported unavailable")
	}
	if providerAvailable("lastfm", testDeps(config.Default())) {
		t.Fatal("unconfigured provider reported available")
	}

	raw, err := lastfm.authorizationURL(context.Background(), "state-123", "nonce")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme+"://"+parsed.Host+parsed.Path != "https://www.last.fm/api/auth/" {
		t.Fatalf("auth URL = %s", raw)
	}
	if got := parsed.Query().Get("api_key"); got != "test-api-key" {
		t.Fatalf("api_key = %q", got)
	}
	// The state rides inside cb; Last.fm appends "&token=..." to it.
	if got := parsed.Query().Get("cb"); got != "https://example.com/auth/callback/lastfm?state=state-123" {
		t.Fatalf("cb = %q", got)
	}
}

func TestLastFmProcessCallbackErrorPaths(t *testing.T) {
	cfg := config.Default()
	cfg.SiteUrl = "https://example.com"
	cfg.Oidc.LastFm.ApiKey = "test-api-key"
	cfg.Oidc.LastFm.ApiSecret = "test-secret"
	b := newBaseProvider("lastfm", testDeps(cfg))
	p := &lastfmProvider{base: b}

	if _, err := p.processCallback(context.Background(), &callbackData{}); err == nil ||
		err.Error() != "No authentication token in Last.fm response" {
		t.Fatalf("missing token error = %v", err)
	}

	// An API-level error is reported from the JSON body, not swallowed.
	b.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(`{"error":4,"message":"Invalid authentication token supplied"}`), nil
	})}
	_, err := p.processCallback(context.Background(), &callbackData{QueryParameters: map[string]string{"token": testLastFmToken}})
	if err == nil || !strings.Contains(err.Error(), "error 4") || !strings.Contains(err.Error(), "Invalid authentication token supplied") {
		t.Fatalf("api error = %v", err)
	}

	// An unconfigured provider must not build a signature from an empty secret.
	unconfigured := &lastfmProvider{base: newBaseProvider("lastfm", testDeps(config.Default()))}
	if _, err := unconfigured.getSession(context.Background(), testLastFmToken); err == nil ||
		err.Error() != "Last.fm API key or shared secret is not configured" {
		t.Fatalf("unconfigured error = %v", err)
	}
}

func TestLastFmLargestImage(t *testing.T) {
	if got := lastFmLargestImage([]lastFmImage{{Size: "small"}, {Size: "large", Text: "https://img/large.png"}}); got != "https://img/large.png" {
		t.Fatalf("image = %q", got)
	}
	if got := lastFmLargestImage([]lastFmImage{{Size: "large"}}); got != "" {
		t.Fatalf("empty image = %q", got)
	}
}

// ─────────────────────────── callback flows (Postgres) ───────────────────────────

func TestLastFmConnectStoresSessionKey(t *testing.T) {
	env := newSocialDeps(t)
	account := createSocialAccount(t, env.Store, "Connect User", "connect@example.com")
	svc := stubLastFmProvider(t, env.Deps, "connect-user", "session-key-connect")
	c, recorder := callbackContext(t)

	env.Deps.handleManualConnection(c, svc, lastFmCallbackData(), account.Id, "state-token")

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d", recorder.Code)
	}
	if loc := c.Writer.Header().Get("Location"); loc != "https://example.com/auth/success" {
		t.Fatalf("location = %q", loc)
	}

	var accessToken, refreshToken *string
	if err := env.Store.DB.WithContext(context.Background()).
		Raw(`SELECT access_token, refresh_token FROM account_connections
			WHERE provider = 'lastfm' AND provided_identifier = 'connect-user'`).
		Row().Scan(&accessToken, &refreshToken); err != nil {
		t.Fatalf("load connection: %v", err)
	}
	if accessToken == nil || *accessToken != "session-key-connect" {
		t.Fatalf("stored access token = %v", accessToken)
	}
	// Last.fm issues no refresh token: the session key never expires and is
	// only invalidated when the user revokes the app.
	if refreshToken != nil && *refreshToken != "" {
		t.Fatalf("stored refresh token = %v", refreshToken)
	}
}

func TestLastFmLoginWithoutEmailUsesLinkedConnection(t *testing.T) {
	env := newSocialDeps(t)
	ctx := context.Background()
	account := createSocialAccount(t, env.Store, "Linked User", "linked@example.com")
	if err := env.Store.InsertConnection(ctx, account.Id, "lastfm", "linked-user", "stored-session-key", "",
		map[string]any{"user_id": "linked-user"}, nil, time.Now().UTC()); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	svc := stubLastFmProvider(t, env.Deps, "linked-user", "fresh-session-key")
	c, recorder := callbackContext(t)
	deviceID := "test-device"

	env.Deps.handleLoginOrRegistration(c, svc, lastFmCallbackData(), &deviceID, "state-token")

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d", recorder.Code)
	}
	location, err := url.Parse(c.Writer.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Scheme+"://"+location.Host+location.Path != "https://example.com/auth/success" {
		t.Fatalf("location = %q", location)
	}
	if location.Query().Get("token") == "" || location.Query().Get("refreshToken") == "" {
		t.Fatalf("location is missing the token pair: %s", location)
	}

	// The callback's fresh session key replaces the stored one: the connection
	// always carries the pair downstream services consume.
	var accessToken *string
	if err := env.Store.DB.WithContext(ctx).
		Raw(`SELECT access_token FROM account_connections
			WHERE provider = 'lastfm' AND provided_identifier = 'linked-user'`).
		Row().Scan(&accessToken); err != nil {
		t.Fatalf("load connection: %v", err)
	}
	if accessToken == nil || *accessToken != "fresh-session-key" {
		t.Fatalf("stored access token = %v", accessToken)
	}
}

func TestLastFmLoginWithoutEmailNeedsRegistrationEmail(t *testing.T) {
	env := newSocialDeps(t)
	ctx := context.Background()
	svc := stubLastFmProvider(t, env.Deps, "brand-new-user", "session-key-new")
	c, recorder := callbackContext(t)

	env.Deps.handleLoginOrRegistration(c, svc, lastFmCallbackData(), nil, "state-token")

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "OIDC_MISSING_EMAIL_OR_USER_ID") {
		t.Fatalf("body = %s", body)
	}
	// Nothing may be linked or provisioned for the unlinked, email-less user.
	var connections int64
	if err := env.Store.DB.WithContext(ctx).Raw(`SELECT count(*) FROM account_connections WHERE provider = 'lastfm'`).Row().Scan(&connections); err != nil {
		t.Fatalf("count connections: %v", err)
	}
	if connections != 0 {
		t.Fatalf("connections = %d, want 0", connections)
	}
}

// ─────────────────────────── helpers ───────────────────────────

type socialEnv struct {
	Deps  Deps
	Store *store.Store
}

func newSocialDeps(t *testing.T) socialEnv {
	t.Helper()
	baseDSN := os.Getenv("STARGATE_TEST_DSN")
	if baseDSN == "" {
		t.Skip("STARGATE_TEST_DSN is not configured")
	}
	ctx := context.Background()
	dsn, cleanup, err := dbtest.NewDatabase(ctx, baseDSN)
	if err != nil {
		t.Skipf("cannot create dedicated database: %v", err)
	}
	t.Cleanup(cleanup)
	database, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = db.Close(database) })
	if err := migrate.Run(ctx, database); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	cfg := config.Default()
	cfg.SiteUrl = "https://example.com"
	cfg.Auth.Issuer = "solar-network"
	cfg.Auth.Audiences = []string{"solar-network"}
	cfg.Auth.PublicKeyPath = filepath.Join(repoRootOf(t), "Keys", "PublicKey.pem")
	cfg.Auth.PrivateKeyPath = filepath.Join(repoRootOf(t), "Keys", "PrivateKey.pem")
	cfg.Oidc.LastFm.ApiKey = "test-api-key"
	cfg.Oidc.LastFm.ApiSecret = "test-secret"

	if _, err := os.Stat(cfg.Auth.PrivateKeyPath); err != nil {
		t.Skipf("dev signing keys missing: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	jwtService, err := auth.NewJWTService(cfg)
	if err != nil {
		t.Fatalf("jwt service: %v", err)
	}
	st := store.New(database)
	tokenService := auth.NewTokenAuthService(st, nil, jwtService, nil, nil, log)
	authService := auth.NewAuthService(st, nil, cfg, geo.NewService(""), jwtService, tokenService, nil, nil, log)
	return socialEnv{Deps: Deps{Store: st, Cfg: cfg, Auth: authService, Log: log}, Store: st}
}

func repoRootOf(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file path")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file))))
}

func createSocialAccount(t *testing.T, st *store.Store, nick, email string) *model.Account {
	t.Helper()
	account, err := st.CreateAccountFromSocial(context.Background(),
		strings.SplitN(email, "@", 2)[0], nick, email, true, time.Now().UTC())
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	return account
}

// stubLastFmProvider points a real lastfmProvider at a canned Last.fm web
// service and asserts the request shape (endpoint, credentials, api_sig).
func stubLastFmProvider(t *testing.T, d Deps, username, sessionKey string) provider {
	t.Helper()
	svc, err := newProvider("lastfm", d)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := svc.(*lastfmProvider)
	if !ok {
		t.Fatalf("provider type = %T", svc)
	}
	p.base.http = &http.Client{Timeout: 5 * time.Second, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "ws.audioscrobbler.com" || r.URL.Path != "/2.0/" {
			t.Errorf("unexpected Last.fm endpoint: %s", r.URL)
		}
		query := r.URL.Query()
		if got := query.Get("api_key"); got != "test-api-key" {
			t.Errorf("api_key = %q", got)
		}
		switch query.Get("method") {
		case "auth.getSession":
			if got := query.Get("token"); got != testLastFmToken {
				t.Errorf("token = %q", got)
			}
			if got := query.Get("api_sig"); got != testLastFmSignature {
				t.Errorf("api_sig = %q", got)
			}
			if got := query.Get("format"); got != "json" {
				t.Errorf("format = %q", got)
			}
			return jsonResponse(fmt.Sprintf(`{"session":{"name":%q,"key":%q,"subscriber":0}}`, username, sessionKey)), nil
		case "user.getInfo":
			return jsonResponse(fmt.Sprintf(`{"user":{"name":%q,"realname":"Richard Jones","url":"https://www.last.fm/user/%s","image":[{"size":"small","#text":""},{"size":"large","#text":"https://img.example/avatar.png"}]}}`, username, username)), nil
		default:
			t.Errorf("unexpected Last.fm method %q", query.Get("method"))
			return jsonResponse(`{"error":3,"message":"Invalid Method"}`), nil
		}
	})}
	return svc
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func lastFmCallbackData() *callbackData {
	return &callbackData{QueryParameters: map[string]string{"token": testLastFmToken}}
}

func callbackContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet,
		"/api/auth/callback/lastfm?state=state-token&token="+testLastFmToken, nil)
	return c, recorder
}
