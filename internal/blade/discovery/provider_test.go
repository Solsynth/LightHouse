package discovery

import (
	"context"
	"testing"
	"time"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

func TestRegistryProviderReportsRegisteredButUnhealthyInstances(t *testing.T) {
	registry := NewRegistry(nil, "test:discovery", time.Minute, nil)
	ctx := context.Background()
	_, _, err := registry.Register(ctx, &gen.DyServiceInstance{
		Service: "stargate", InstanceId: "stargate-1", GrpcEndpoint: "stargate:9090",
	}, time.Minute)
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	provider := NewRegistryProvider(registry)
	resolution, err := provider.Resolve(ctx, "stargate")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !resolution.Registered {
		t.Fatal("provider did not report the registered instance")
	}
	if len(resolution.Endpoints) != 0 {
		t.Fatalf("endpoints = %v, want none while the instance is unhealthy", resolution.Endpoints)
	}

	if err := registry.SetHealth(ctx, "stargate", "stargate-1", true); err != nil {
		t.Fatalf("SetHealth() error = %v", err)
	}
	resolution, err = provider.Resolve(ctx, "stargate")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(resolution.Endpoints) != 1 || resolution.Endpoints[0] != "stargate:9090" {
		t.Fatalf("endpoints = %v, want the healthy grpc endpoint", resolution.Endpoints)
	}
}
