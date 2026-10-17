package socialctl

import (
	"reflect"
	"testing"

	"src.solsynth.dev/sosys/stargate/internal/config"
)

func TestAvailableProvidersOnlyListsConfigured(t *testing.T) {
	cfg := config.Default()
	cfg.Oidc.GitHub.ClientId = "client-id"
	cfg.Oidc.GitHub.ClientSecret = "client-secret"
	cfg.Oidc.Google.ClientId = "google-client-id"

	got := availableProviders(testDeps(cfg))
	want := []string{"github", "google"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("availableProviders() = %v, want %v", got, want)
	}

	if got := availableProviders(testDeps(config.Default())); len(got) != 0 {
		t.Fatalf("unconfigured deployment lists %v", got)
	}
}

func TestAvailableProvidersRequiresCompleteCredentials(t *testing.T) {
	cfg := config.Default()
	cfg.Oidc.Microsoft.ClientId = "client-id"
	cfg.Oidc.Microsoft.ClientSecret = "client-secret"
	// No discovery endpoint: the provider cannot start a flow.
	if got := availableProviders(testDeps(cfg)); len(got) != 0 {
		t.Fatalf("incomplete Microsoft config was offered: %v", got)
	}

	cfg.Oidc.Microsoft.DiscoveryEndpoint = "https://login.microsoftonline.com/common/v2.0/.well-known/openid-configuration"
	if got := availableProviders(testDeps(cfg)); !reflect.DeepEqual(got, []string{"microsoft"}) {
		t.Fatalf("complete Microsoft config not offered: %v", got)
	}
}
