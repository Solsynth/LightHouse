package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"log/slog"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"src.solsynth.dev/sosys/stargate/internal/config"
)

func testSingleUser(t *testing.T) (*SingleUser, *JWTService) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwt := &JWTService{
		issuer: "solar-network", validIssuers: []string{"solar-network"},
		audience: "test-audience", private: key, public: &key.PublicKey,
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("correct horse"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Auth.Provider = "single-user"
	cfg.Auth.SingleUser.Username = "admin"
	cfg.Auth.SingleUser.PasswordHash = string(hash)
	cfg.Auth.SingleUser.Superuser = true
	cfg.Auth.SingleUser.AllowRefresh = true
	cfg.Auth.AccessTokenLifetime = "5m"
	cfg.Auth.RefreshTokenLifetime = "1h"

	provider, err := NewSingleUser(cfg, jwt)
	if err != nil {
		t.Fatal(err)
	}
	return provider, jwt
}

func TestSingleUserAuthenticate(t *testing.T) {
	provider, _ := testSingleUser(t)
	if _, err := provider.Authenticate(context.Background(), "admin", "correct horse"); err != nil {
		t.Fatalf("valid credentials rejected: %v", err)
	}
	if _, err := provider.Authenticate(context.Background(), "admin", "wrong"); err == nil {
		t.Fatal("invalid password accepted")
	}
	if _, err := provider.Authenticate(context.Background(), "other", "correct horse"); err == nil {
		t.Fatal("invalid username accepted")
	}
}

func TestSingleUserTokensBypassDatabase(t *testing.T) {
	provider, jwt := testSingleUser(t)
	service := NewTokenAuthService(nil, nil, jwt, nil, nil, slog.Default())
	service.SetSingleUser(provider)

	pair, session, err := provider.IssueTokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pair.RefreshToken == "" {
		t.Fatal("expected refresh token")
	}

	valid, gotSession, message, use := service.AuthenticateToken(context.Background(), pair.AccessToken, "")
	if !valid || gotSession == nil {
		t.Fatalf("token rejected: message=%q use=%q", message, use)
	}
	if gotSession.Account == nil || gotSession.Account.Id != provider.Account().Id {
		t.Fatalf("session account = %+v", gotSession.Account)
	}
	if session.Id == "" {
		t.Fatal("synthetic session id missing")
	}

	renewed, renewedSession, err := provider.RefreshSessionAndIssueTokens(context.Background(), pair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.AccessToken == "" || renewedSession == nil {
		t.Fatal("refresh did not issue a pair")
	}
	if renewed.AccessToken == pair.AccessToken {
		t.Fatal("refresh reused the access token")
	}
}

func TestSingleUserRefreshDisabled(t *testing.T) {
	provider, _ := testSingleUser(t)
	provider.allowRefresh = false
	pair, _, err := provider.IssueTokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pair.RefreshToken != "" {
		t.Fatal("refresh token issued while disabled")
	}
	if _, _, err := provider.RefreshSessionAndIssueTokens(context.Background(), "x"); err == nil {
		t.Fatal("refresh accepted while disabled")
	}
}
