package discovery

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
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
