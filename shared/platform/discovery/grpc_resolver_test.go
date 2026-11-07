package discovery

import (
	"context"
	"net"
	"reflect"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/resolver"
)

func TestGRPCResolverConnectsToProviderEndpoints(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	healthpb.RegisterHealthServer(server, &healthServer{})
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	provider := &fakeProvider{resolution: Resolution{
		Registered: true,
		Endpoints:  []string{listener.Addr().String()},
	}}
	RegisterResolver(NewResolver(provider, nil))

	conn, err := grpc.NewClient(Target("stargate"), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("health check through discovery target: %v", err)
	}
	if provider.calls == 0 {
		t.Fatal("resolver never queried the provider")
	}
}

type healthServer struct {
	healthpb.UnimplementedHealthServer
}

func (h *healthServer) Check(context.Context, *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

func TestEndpointAddressesCarryServerName(t *testing.T) {
	got := endpointAddresses([]string{"drive:5001", "https://stargate:5001", "", "10.0.0.5:7001"})
	want := []resolver.Address{
		{Addr: "drive:5001", ServerName: "drive"},
		{Addr: "stargate:5001", ServerName: "stargate"},
		{Addr: "10.0.0.5:7001", ServerName: "10.0.0.5"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("endpointAddresses() = %#v, want %#v", got, want)
	}
}
