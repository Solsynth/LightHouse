package discovery

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

type fakeDiscoveryClient struct {
	gen.DyServiceDiscoveryServiceClient
	response *gen.DyResolveServiceResponse
}

func (f *fakeDiscoveryClient) Resolve(context.Context, *gen.DyResolveServiceRequest, ...grpc.CallOption) (*gen.DyResolveServiceResponse, error) {
	return f.response, nil
}

func TestResolverFallsBackUntilStargateRegisters(t *testing.T) {
	resolver := &Resolver{
		client:   &fakeDiscoveryClient{response: &gen.DyResolveServiceResponse{}},
		fallback: map[string]string{"stargate": "stargate:9090"},
		cache:    make(map[string]cachedResolution),
	}

	resolution, err := resolver.Resolve(context.Background(), "stargate")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(resolution.Endpoints) != 1 || resolution.Endpoints[0] != "stargate:9090" {
		t.Fatalf("endpoints = %v, want static fallback", resolution.Endpoints)
	}
}

func TestResolverUsesHealthyRegisteredGRPCEndpoint(t *testing.T) {
	resolver := &Resolver{
		client: &fakeDiscoveryClient{response: &gen.DyResolveServiceResponse{Instances: []*gen.DyServiceInstance{{
			Service: "stargate", InstanceId: "stargate-1", Healthy: true, GrpcEndpoint: "stargate-1:9090",
		}}}},
		fallback: map[string]string{"stargate": "stargate:9090"},
		cache:    make(map[string]cachedResolution),
	}

	resolution, err := resolver.Resolve(context.Background(), "stargate")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(resolution.Endpoints) != 1 || resolution.Endpoints[0] != "stargate-1:9090" {
		t.Fatalf("endpoints = %v, want the registered endpoint", resolution.Endpoints)
	}
}

func TestResolverDoesNotFallBackToStaticWhenRegisteredUnhealthy(t *testing.T) {
	resolver := &Resolver{
		client: &fakeDiscoveryClient{response: &gen.DyResolveServiceResponse{Instances: []*gen.DyServiceInstance{{
			Service: "stargate", InstanceId: "stargate-1", Healthy: false,
		}}}},
		fallback: map[string]string{"stargate": "stargate:9090"},
		cache:    make(map[string]cachedResolution),
	}

	resolution, _ := resolver.Resolve(context.Background(), "stargate")
	if len(resolution.Endpoints) != 0 {
		t.Fatalf("endpoints = %v, want none while the registered instance is unhealthy", resolution.Endpoints)
	}
}

func TestTarget(t *testing.T) {
	if got := Target("stargate"); got != "blade:///stargate" {
		t.Fatalf("Target() = %q", got)
	}
}
