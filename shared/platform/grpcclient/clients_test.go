package grpcclient

import (
	"context"
	"testing"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/discovery"
)

type stubProvider struct {
	endpoints map[string][]string
}

func (s stubProvider) Resolve(_ context.Context, service string) (discovery.Resolution, error) {
	endpoints := s.endpoints[service]
	return discovery.Resolution{Registered: len(endpoints) > 0, Endpoints: endpoints}, nil
}

func TestNewWithResolverDiscoversEmptyTargets(t *testing.T) {
	resolver := discovery.NewResolver(stubProvider{endpoints: map[string][]string{
		"stargate": {"stargate:9090"},
	}}, map[string]string{"stargate": "static:9090"})
	discovery.RegisterResolver(resolver)

	clients, err := NewWithResolver(Targets{}, resolver)
	if err != nil {
		t.Fatalf("NewWithResolver() error = %v", err)
	}
	defer clients.Close()

	if clients.Account == nil || clients.Profile == nil || clients.Blade == nil || clients.Posts == nil {
		t.Fatal("resolver-backed clients were not created for empty [services] targets")
	}
	if len(clients.conns) == 0 {
		t.Fatal("resolver-backed connections were not created")
	}
	if got := clients.conns[0].Target(); got != discovery.Target("stargate") {
		t.Fatalf("first target = %q, want %q", got, discovery.Target("stargate"))
	}
}

func TestNewLeavesEmptyTargetsDisabledWithoutResolver(t *testing.T) {
	clients, err := New(Targets{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer clients.Close()

	if clients.Account != nil || clients.Blade != nil || clients.Posts != nil {
		t.Fatal("empty targets must stay disabled without a discovery resolver")
	}
}
