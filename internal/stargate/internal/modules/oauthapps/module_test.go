package oauthapps

import (
	"testing"

	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/core"
	"src.solsynth.dev/sosys/stargate/internal/module"
	"src.solsynth.dev/sosys/stargate/internal/ports"
)

// TestFactoryDeclaresSchemaAndPort pins the module contract: the two tables
// it migrates and the port it provides. Disabling the module must therefore
// leave no oauth table behind and no client provider of its own.
func TestFactoryDeclaresSchemaAndPort(t *testing.T) {
	factory := Factory()
	if factory.ID != "app.solian.id.oauthapps" {
		t.Fatalf("module id = %q", factory.ID)
	}
	if factory.Kind != "feature" {
		t.Fatalf("module kind = %q; want feature", factory.Kind)
	}
	if len(factory.Models) != 2 {
		t.Fatalf("module models = %d; want 2", len(factory.Models))
	}
	if len(factory.SQL) != 1 {
		t.Fatalf("module schema hooks = %d; want 1", len(factory.SQL))
	}
	if len(factory.Provides) != 1 || factory.Provides[0] != ports.PortOAuthClients {
		t.Fatalf("module provides = %v", factory.Provides)
	}
}

// TestConfiguredClientWinsOverEverything pins the documented precedence:
// a deployment-configured client is returned without consulting the table
// or Develop.
func TestConfiguredClientWinsOverEverything(t *testing.T) {
	cfg := config.Default()
	cfg.OidcProvider.Clients = []config.OAuthClient{{
		Id:             "11111111-1111-1111-1111-111111111111",
		Slug:           "configured",
		Name:           "Configured Client",
		ClientSecret:   "configured-secret",
		RedirectUris:   []string{"https://client.example/callback"},
		AllowedScopes:  []string{"openid"},
		IsPublicClient: false,
	}}
	src := &source{cfg: cfg}

	app, err := src.Get(t.Context(), "11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if app == nil || app.Slug != "configured" || app.Name != "Configured Client" {
		t.Fatalf("client = %+v", app)
	}
	// An omitted status is treated as production, matching the C# enum
	// default handling in the previous provider path.
	if app.Status != 2 {
		t.Fatalf("status = %d; want 2 (production)", app.Status)
	}

	bySlug, err := src.GetBySlug(t.Context(), "configured")
	if err != nil {
		t.Fatalf("GetBySlug: %v", err)
	}
	if bySlug == nil || bySlug.ID != app.ID {
		t.Fatalf("client by slug = %+v", bySlug)
	}

	valid, err := src.CheckSecret(t.Context(), "11111111-1111-1111-1111-111111111111", "configured-secret")
	if err != nil || !valid {
		t.Fatalf("CheckSecret(valid) = %v, %v", valid, err)
	}
	valid, err = src.CheckSecret(t.Context(), "11111111-1111-1111-1111-111111111111", "wrong")
	if err != nil {
		t.Fatalf("CheckSecret(wrong): %v", err)
	}
	if valid {
		t.Fatal("wrong configured secret was accepted")
	}
}

// TestPublicClientSecretIsNeverConfigured pins that a configured public
// client cannot authenticate with a secret.
func TestPublicClientWithoutSecretNeverValidates(t *testing.T) {
	cfg := config.Default()
	cfg.OidcProvider.Clients = []config.OAuthClient{{
		Id:             "22222222-2222-2222-2222-222222222222",
		Slug:           "public-client",
		IsPublicClient: true,
		AllowedScopes:  []string{"openid"},
	}}
	src := &source{cfg: cfg}

	valid, err := src.CheckSecret(t.Context(), "22222222-2222-2222-2222-222222222222", "anything")
	if err != nil {
		t.Fatalf("CheckSecret: %v", err)
	}
	if valid {
		t.Fatal("a client with no configured secret accepted a secret")
	}
}

// TestProvidePortsPublishesResolvableProvider pins the boot-time wiring:
// after ProvidePorts, the oauthClients port resolves to this module and the
// OIDC provider can use it. No database is involved: the configured client
// is served from configuration.
func TestProvidePortsPublishesResolvableProvider(t *testing.T) {
	cfg := config.Default()
	cfg.OidcProvider.Clients = []config.OAuthClient{{
		Id:            "44444444-4444-4444-4444-444444444444",
		Slug:          "wired-client",
		Name:          "Wired Client",
		AllowedScopes: []string{"openid"},
	}}
	registry := module.NewPortRegistry()
	host := &module.Host{
		Core:  &core.Services{Config: cfg},
		Ports: registry,
	}

	instance := &instance{}
	if err := instance.ProvidePorts(t.Context(), host); err != nil {
		t.Fatalf("ProvidePorts: %v", err)
	}

	resolved, err := registry.Resolve(ports.PortOAuthClients)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.ModuleID != "app.solian.id.oauthapps" {
		t.Fatalf("provider module = %q", resolved.ModuleID)
	}
	client, ok := module.Get[ports.OAuthClients](registry, ports.PortOAuthClients)
	if !ok || client == nil || !client.Available() {
		t.Fatalf("port client = %v, %v", client, ok)
	}
	app, err := client.Get(t.Context(), "44444444-4444-4444-4444-444444444444")
	if err != nil || app == nil || app.Slug != "wired-client" {
		t.Fatalf("Get = %+v, %v", app, err)
	}
	slugged, ok := client.(ports.OAuthClientsSlugged)
	if !ok {
		t.Fatal("provider does not implement OAuthClientsSlugged")
	}
	bySlug, err := slugged.GetBySlug(t.Context(), "wired-client")
	if err != nil || bySlug == nil || bySlug.ID != app.ID {
		t.Fatalf("GetBySlug = %+v, %v", bySlug, err)
	}
}

// TestMissingProviderReturnsNothing pins the degraded mode: no config, no
// store, no Develop client means an unknown client, not a panic.
func TestMissingProviderReturnsNothing(t *testing.T) {
	src := &source{}
	app, err := src.Get(t.Context(), "33333333-3333-3333-3333-333333333333")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if app != nil {
		t.Fatalf("client = %+v; want nil", app)
	}
	valid, err := src.CheckSecret(t.Context(), "33333333-3333-3333-3333-333333333333", "secret")
	if err != nil || valid {
		t.Fatalf("CheckSecret = %v, %v; want false, nil", valid, err)
	}
}

func TestAdapterUnavailableWithoutSource(t *testing.T) {
	var adapter *adapter
	if adapter.Available() {
		t.Fatal("nil adapter reported available")
	}
	if _, err := adapter.Get(t.Context(), "id"); err != ports.ErrUnavailable {
		t.Fatalf("Get error = %v; want ErrUnavailable", err)
	}
}
