package socialctl

// Regression tests for the social-login account-link boundary: a provider
// identity may only attach to an existing local account when both sides proved
// the email address. An unverified provider email (or an unverified local
// contact row) is an account-takeover primitive, because the resolver used to
// link by any email match and then mint that account's tokens.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"src.solsynth.dev/sosys/stargate/model"
)

type linkStubProvider struct {
	info *userInfo
}

func (p linkStubProvider) name() string { return "github" }

func (p linkStubProvider) authorizationURL(context.Context, string, string) (string, error) {
	return "", nil
}

func (p linkStubProvider) processCallback(context.Context, *callbackData) (*userInfo, error) {
	return p.info, nil
}

func createUnverifiedSocialAccount(t *testing.T, env socialEnv, nick, email string) *model.Account {
	t.Helper()
	account, err := env.Store.CreateAccountFromSocial(context.Background(),
		strings.SplitN(email, "@", 2)[0], nick, email, false, time.Now().UTC())
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	return account
}

func TestSocialLoginRefusesUnverifiedEmailLink(t *testing.T) {
	env := newSocialDeps(t)
	createUnverifiedSocialAccount(t, env, "Unverified Owner", "owner@example.com")

	c, recorder := callbackContext(t)
	env.Deps.handleLoginOrRegistration(c, linkStubProvider{info: &userInfo{
		UserId: "provider-attacker",
		Email:  "owner@example.com",
	}}, &callbackData{}, nil, "state-token")

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "OIDC_EMAIL_NOT_VERIFIED") {
		t.Fatalf("body = %s", recorder.Body.String())
	}
	var connections int64
	if err := env.Store.DB.WithContext(context.Background()).
		Raw(`SELECT count(*) FROM account_connections WHERE provided_identifier = 'provider-attacker'`).
		Row().Scan(&connections); err != nil {
		t.Fatalf("count connections: %v", err)
	}
	if connections != 0 {
		t.Fatalf("connections = %d, want 0", connections)
	}
}

func TestSocialLoginRefusesVerifiedProviderEmailWhenLocalContactUnverified(t *testing.T) {
	env := newSocialDeps(t)
	createUnverifiedSocialAccount(t, env, "Unverified Owner", "owner@example.com")

	c, recorder := callbackContext(t)
	env.Deps.handleLoginOrRegistration(c, linkStubProvider{info: &userInfo{
		UserId:        "provider-verified",
		Email:         "owner@example.com",
		EmailVerified: true,
	}}, &callbackData{}, nil, "state-token")

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "OIDC_EMAIL_NOT_VERIFIED") {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}

func TestSocialLoginLinksVerifiedEmail(t *testing.T) {
	env := newSocialDeps(t)
	account := createSocialAccount(t, env.Store, "Verified Owner", "owner@example.com")

	c, recorder := callbackContext(t)
	env.Deps.handleLoginOrRegistration(c, linkStubProvider{info: &userInfo{
		UserId:        "provider-verified",
		Email:         "owner@example.com",
		EmailVerified: true,
	}}, &callbackData{}, nil, "state-token")

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (body %s)", recorder.Code, recorder.Body.String())
	}
	var linkedTo string
	if err := env.Store.DB.WithContext(context.Background()).
		Raw(`SELECT account_id::text FROM account_connections WHERE provider = 'github' AND provided_identifier = 'provider-verified'`).
		Row().Scan(&linkedTo); err != nil {
		t.Fatalf("load connection: %v", err)
	}
	if linkedTo != account.Id {
		t.Fatalf("linked account = %s, want %s", linkedTo, account.Id)
	}
}
