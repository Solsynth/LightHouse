package oidcctl

import (
	"context"
	"testing"

	"src.solsynth.dev/sosys/stargate/internal/config"
)

func TestLocalOAuthClientFallback(t *testing.T) {
	const (
		clientID = "local-client-id"
		slug     = "local-client"
	)
	cfg := config.Default()
	cfg.OidcProvider.Clients = []config.OAuthClient{{
		Id:             clientID,
		Slug:           slug,
		Name:           "Local Client",
		ClientSecret:   "local-secret",
		Status:         customAppStatusProduction,
		RedirectUris:   []string{"https://client.example/callback"},
		AllowedScopes:  []string{"openid", "profile"},
		IsPublicClient: false,
	}}
	s := &service{cfg: cfg}

	byID, err := s.findClientByID(context.Background(), clientID)
	if err != nil {
		t.Fatalf("findClientByID returned error: %v", err)
	}
	if byID == nil || byID.Slug != slug || byID.Name != "Local Client" {
		t.Fatalf("client by id = %+v", byID)
	}
	bySlug, err := s.findClientBySlug(context.Background(), slug)
	if err != nil {
		t.Fatalf("findClientBySlug returned error: %v", err)
	}
	if bySlug == nil || bySlug.Id != clientID {
		t.Fatalf("client by slug = %+v", bySlug)
	}

	valid, err := s.validateClientCredentials(context.Background(), clientID, "local-secret")
	if err != nil || !valid {
		t.Fatalf("valid local secret = %v, %v", valid, err)
	}
	valid, err = s.validateClientCredentials(context.Background(), slug, "wrong-secret")
	if err != nil {
		t.Fatalf("invalid local secret returned error: %v", err)
	}
	if valid {
		t.Fatal("invalid local secret was accepted")
	}

	redirectOK, err := s.validateRedirectURI(context.Background(), clientID, "https://client.example/callback")
	if err != nil || !redirectOK {
		t.Fatalf("valid redirect = %v, %v", redirectOK, err)
	}
	redirectOK, err = s.validateRedirectURI(context.Background(), clientID, "https://attacker.example/callback")
	if err != nil {
		t.Fatalf("invalid redirect returned error: %v", err)
	}
	if redirectOK {
		t.Fatal("invalid redirect was accepted")
	}
}
