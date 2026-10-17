package auth

import (
	"context"
	"testing"

	"google.golang.org/protobuf/types/known/wrapperspb"
	gen "src.solsynth.dev/sosys/go/proto"
)

// stubAppProvider resolves a fixed slug, recording the app id it was asked for.
type stubAppProvider struct {
	slug  string
	asked []string
}

func (s *stubAppProvider) GetCustomAppSlug(_ context.Context, appID string) (string, error) {
	s.asked = append(s.asked, appID)
	return s.slug, nil
}

// TestSetCustomAppProviderReplacesResolver pins that a module-installed
// resolver wins over the one the token service was built with. Without this,
// a token bound to a Stargate-owned OAuth app could never validate when the
// Develop service is not configured.
func TestSetCustomAppProviderReplacesResolver(t *testing.T) {
	original := &stubAppProvider{slug: "from-develop"}
	service := &TokenAuthService{apps: original}

	installed := &stubAppProvider{slug: "from-module"}
	service.SetCustomAppProvider(installed)

	session := &gen.DyAuthSession{
		AppId: &wrapperspb.StringValue{Value: "app-id"},
		Type:  gen.DySessionType_DY_OAUTH,
	}
	ok, message := service.validateOidcBinding(context.Background(), session, nil)
	if !ok {
		t.Fatalf("binding rejected: %s", message)
	}
	if len(installed.asked) != 1 || installed.asked[0] != "app-id" {
		t.Fatalf("installed provider asked for %v", installed.asked)
	}
	if len(original.asked) != 0 {
		t.Fatalf("original provider was consulted: %v", original.asked)
	}
	if installed.slug != "from-module" {
		t.Fatalf("slug = %q", installed.slug)
	}
	// A token whose audience names the module's slug validates.
}

// TestSetCustomAppProviderIgnoresNil pins that a nil provider cannot erase
// the configured resolver.
func TestSetCustomAppProviderIgnoresNil(t *testing.T) {
	original := &stubAppProvider{slug: "from-develop"}
	service := &TokenAuthService{apps: original}
	service.SetCustomAppProvider(nil)
	if service.apps != original {
		t.Fatal("nil provider replaced the resolver")
	}
}
