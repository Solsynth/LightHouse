package middleware

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/model"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testAuthConfig mirrors config.Config.Auth (an anonymous struct) with a
// throwaway keypair so tokens pass signature validation without a store.
func testAuthConfig(privPath, pubPath string) *config.Config {
	return &config.Config{Auth: struct {
		Issuer               string   `toml:"issuer"`
		Audiences            []string `toml:"audiences"`
		PublicKeyPath        string   `toml:"publicKeyPath"`
		PrivateKeyPath       string   `toml:"privateKeyPath"`
		AccessTokenLifetime  string   `toml:"accessTokenLifetime"`
		RefreshTokenLifetime string   `toml:"refreshTokenLifetime"`
		CookieDomain         string   `toml:"cookieDomain"`
		CookieSecure         bool     `toml:"cookieSecure"`
	}{
		Issuer:         "test-issuer",
		Audiences:      []string{"test-aud"},
		PublicKeyPath:  pubPath,
		PrivateKeyPath: privPath,
	}}
}

// newSignedJWTService builds a JWTService over a throwaway RSA keypair.
func newSignedJWTService(t *testing.T) *auth.JWTService {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	dir := t.TempDir()
	privPath := filepath.Join(dir, "private.pem")
	pubPath := filepath.Join(dir, "public.pem")
	privDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	if err := os.WriteFile(privPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}), 0o600); err != nil {
		t.Fatalf("write private key: %v", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	if err := os.WriteFile(pubPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}), 0o600); err != nil {
		t.Fatalf("write public key: %v", err)
	}
	svc, err := auth.NewJWTService(testAuthConfig(privPath, pubPath))
	if err != nil {
		t.Fatalf("build jwt service: %v", err)
	}
	return svc
}

// fakeRenewer is a TokenRenewer stub that records whether it was called.
type fakeRenewer struct {
	pair    *auth.TokenPair
	session *model.AuthSession
	err     error
	calls   int
}

func (f *fakeRenewer) RefreshSessionAndIssueTokens(ctx context.Context, refreshToken string) (*auth.TokenPair, *model.AuthSession, error) {
	f.calls++
	return f.pair, f.session, f.err
}

// TestAuthAutoRenewsExpiredToken pins the auto-renew contract: an expired
// access token with a matching RefreshToken cookie is rotated transparently
// — the request succeeds, fresh cookies are set, and the context carries the
// renewed session instead of a TOKEN_EXPIRED 401.
func TestAuthAutoRenewsExpiredToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	jwtSvc := newSignedJWTService(t)
	tokenSvc := auth.NewTokenAuthService(nil, nil, jwtSvc, nil, nil, discardLogger())

	session := &model.AuthSession{Id: uuid.NewString(), AccountId: uuid.NewString(), Epoch: 0, Scopes: []string{"*"}}
	account := &model.Account{Id: session.AccountId, Name: "Test"}
	expired, err := jwtSvc.CreateUserToken(session, account, 0, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("mint expired token: %v", err)
	}
	refreshToken, err := jwtSvc.CreateRefreshToken(session, 0, time.Now().Add(30*24*time.Hour))
	if err != nil {
		t.Fatalf("mint refresh token: %v", err)
	}

	renewed := &model.AuthSession{Id: session.Id, AccountId: session.AccountId, Epoch: 1, Scopes: []string{"*"}, Account: account}
	renewer := &fakeRenewer{
		pair: &auth.TokenPair{
			AccessToken: "new-access", RefreshToken: "new-refresh",
			AccessTokenExpiresAt: time.Now().Add(time.Hour), RefreshTokenExpiresAt: time.Now().Add(30 * 24 * time.Hour),
		},
		session: renewed,
	}

	e := gin.New()
	e.Use(Auth(AuthDeps{Token: tokenSvc, Renewer: renewer, CookieDomain: "localhost", Log: discardLogger()}))
	e.GET("/protected", RequireAuth(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"user": CurrentUser(c.Request.Context()).Id, "epoch": CurrentSession(c.Request.Context()).Epoch})
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.AddCookie(&http.Cookie{Name: "AuthToken", Value: expired})
	req.AddCookie(&http.Cookie{Name: "RefreshToken", Value: refreshToken})
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("auto-renewed request: got %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if renewer.calls != 1 {
		t.Fatalf("renewer calls = %d, want 1", renewer.calls)
	}
	var got struct {
		User  string `json:"user"`
		Epoch int    `json:"epoch"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got.User != session.AccountId || got.Epoch != 1 {
		t.Fatalf("context session = (%s, epoch %d), want (%s, 1)", got.User, got.Epoch, session.AccountId)
	}
	var authCookie, refreshCookie string
	for _, ck := range w.Result().Cookies() {
		switch ck.Name {
		case "AuthToken":
			authCookie = ck.Value
		case "RefreshToken":
			refreshCookie = ck.Value
		}
	}
	if authCookie != "new-access" || refreshCookie != "new-refresh" {
		t.Fatalf("cookies = (AuthToken %q, RefreshToken %q), want (new-access, new-refresh)", authCookie, refreshCookie)
	}
}

// TestAuthAutoRenewFailureFallsBackToTokenExpired pins the fallback: when
// renewal fails, RequireAuth still emits the TOKEN_EXPIRED code so clients
// can drive the explicit refresh flow.
func TestAuthAutoRenewFailureFallsBackToTokenExpired(t *testing.T) {
	gin.SetMode(gin.TestMode)
	jwtSvc := newSignedJWTService(t)
	tokenSvc := auth.NewTokenAuthService(nil, nil, jwtSvc, nil, nil, discardLogger())

	session := &model.AuthSession{Id: uuid.NewString(), AccountId: uuid.NewString(), Epoch: 0}
	account := &model.Account{Id: session.AccountId, Name: "Test"}
	expired, err := jwtSvc.CreateUserToken(session, account, 0, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("mint expired token: %v", err)
	}
	refreshToken, err := jwtSvc.CreateRefreshToken(session, 0, time.Now().Add(30*24*time.Hour))
	if err != nil {
		t.Fatalf("mint refresh token: %v", err)
	}

	renewer := &fakeRenewer{err: context.DeadlineExceeded}

	e := gin.New()
	e.Use(Auth(AuthDeps{Token: tokenSvc, Renewer: renewer, CookieDomain: "localhost", Log: discardLogger()}))
	e.GET("/protected", RequireAuth(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.AddCookie(&http.Cookie{Name: "AuthToken", Value: expired})
	req.AddCookie(&http.Cookie{Name: "RefreshToken", Value: refreshToken})
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("failed renewal: got %d, want 401", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"code":"TOKEN_EXPIRED"`) {
		t.Fatalf("failed renewal body: got %s, want TOKEN_EXPIRED code", w.Body.String())
	}
}

// TestAuthDoesNotAutoRenewOidcToken pins the gate: an expired OIDC-issued
// token is never rotated with a Padlock refresh cookie (its session epoch
// would be bumped and the OAuth client's refresh tokens revoked); the client
// must use the OAuth refresh grant instead.
func TestAuthDoesNotAutoRenewOidcToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	jwtSvc := newSignedJWTService(t)
	tokenSvc := auth.NewTokenAuthService(nil, nil, jwtSvc, nil, nil, discardLogger())

	session := &model.AuthSession{Id: uuid.NewString(), AccountId: uuid.NewString(), Epoch: 0}
	account := &model.Account{Id: session.AccountId, Name: "Test"}
	oidcToken, err := jwtSvc.CreateOidcUserToken(session, account, 0, time.Now().Add(-time.Hour),
		"https://oidc.example", "client-slug", nil, map[string]any{"azp": "client-slug"})
	if err != nil {
		t.Fatalf("mint oidc token: %v", err)
	}
	refreshToken, err := jwtSvc.CreateRefreshToken(session, 0, time.Now().Add(30*24*time.Hour))
	if err != nil {
		t.Fatalf("mint refresh token: %v", err)
	}

	renewer := &fakeRenewer{}

	e := gin.New()
	e.Use(Auth(AuthDeps{Token: tokenSvc, Renewer: renewer, CookieDomain: "localhost", Log: discardLogger()}))
	e.GET("/protected", RequireAuth(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.AddCookie(&http.Cookie{Name: "AuthToken", Value: oidcToken})
	req.AddCookie(&http.Cookie{Name: "RefreshToken", Value: refreshToken})
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	if renewer.calls != 0 {
		t.Fatalf("renewer calls = %d, want 0 for OIDC token", renewer.calls)
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("oidc token: got %d, want 401", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"code":"TOKEN_EXPIRED"`) {
		t.Fatalf("oidc token body: got %s, want TOKEN_EXPIRED code", w.Body.String())
	}
}
