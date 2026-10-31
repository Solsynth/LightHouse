package discovery

import (
	"context"
	"errors"
	"testing"
)

type fakeProvider struct {
	resolution Resolution
	err        error
	calls      int
}

func (f *fakeProvider) Resolve(context.Context, string) (Resolution, error) {
	f.calls++
	return f.resolution, f.err
}

func TestResolverFallsBackOnlyWhenServiceIsUnregistered(t *testing.T) {
	t.Run("unregistered service uses the static address", func(t *testing.T) {
		provider := &fakeProvider{}
		r := NewResolver(provider, map[string]string{"stargate": "http://static:9090"})

		resolution, err := r.Resolve(context.Background(), "stargate")
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if len(resolution.Endpoints) != 1 || resolution.Endpoints[0] != "static:9090" {
			t.Fatalf("endpoints = %v, want [static:9090]", resolution.Endpoints)
		}
	})

	t.Run("registered but unhealthy service does not fall back", func(t *testing.T) {
		provider := &fakeProvider{resolution: Resolution{Registered: true}}
		r := NewResolver(provider, map[string]string{"stargate": "static:9090"})

		resolution, err := r.Resolve(context.Background(), "stargate")
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if len(resolution.Endpoints) != 0 {
			t.Fatalf("endpoints = %v, want none while registered instances are unhealthy", resolution.Endpoints)
		}
	})

	t.Run("registry failure while unregistered falls back", func(t *testing.T) {
		provider := &fakeProvider{err: errors.New("blade down")}
		r := NewResolver(provider, map[string]string{"stargate": "static:9090"})

		resolution, _ := r.Resolve(context.Background(), "stargate")
		if len(resolution.Endpoints) != 1 || resolution.Endpoints[0] != "static:9090" {
			t.Fatalf("endpoints = %v, want static fallback", resolution.Endpoints)
		}
	})
}

func TestResolverUsesHealthyRegisteredEndpoints(t *testing.T) {
	provider := &fakeProvider{resolution: Resolution{
		Registered: true,
		Endpoints:  []string{"http://stargate-1:9090", "stargate-1:9090", "https://stargate-2:9090/"},
	}}
	r := NewResolver(provider, map[string]string{"stargate": "static:9090"})

	resolution, err := r.Resolve(context.Background(), "stargate")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(resolution.Endpoints) != 2 {
		t.Fatalf("endpoints = %v, want two deduplicated addresses", resolution.Endpoints)
	}
	if resolution.Endpoints[0] != "stargate-1:9090" || resolution.Endpoints[1] != "stargate-2:9090" {
		t.Fatalf("endpoints = %v, want host:port values", resolution.Endpoints)
	}
}

func TestResolverCachesBriefly(t *testing.T) {
	provider := &fakeProvider{resolution: Resolution{Registered: true, Endpoints: []string{"stargate:9090"}}}
	r := NewResolver(provider, nil)

	for i := 0; i < 3; i++ {
		if _, err := r.Resolve(context.Background(), "stargate"); err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want a single cached lookup", provider.calls)
	}
}

func TestTargetAndIsTarget(t *testing.T) {
	target := Target("stargate")
	if target != "blade:///stargate" {
		t.Fatalf("Target() = %q", target)
	}
	if !IsTarget(target) || IsTarget("stargate:9090") {
		t.Fatal("IsTarget did not distinguish discovery targets")
	}
}

func TestEndpointAddress(t *testing.T) {
	cases := map[string]string{
		"http://stargate:9090":    "stargate:9090",
		"https://stargate:9090/":  "stargate:9090",
		"grpc://stargate:9090":    "stargate:9090",
		"stargate:9090":           "stargate:9090",
		"tcp://relay.example:443": "relay.example:443",
	}
	for raw, want := range cases {
		if got := EndpointAddress(raw); got != want {
			t.Fatalf("EndpointAddress(%q) = %q, want %q", raw, got, want)
		}
	}
}
