package grpcserver

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
	gen "src.solsynth.dev/sosys/go/proto"
)

// TestRegisterWiring mounts every inbound service through Register() on a
// bufconn server and exercises the DB-free methods over the real wire,
// proving the service registrations and the standard health service work.
func TestRegisterWiring(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	Register(server, Deps{})
	go func() {
		_ = server.Serve(lis)
	}()
	t.Cleanup(func() {
		server.Stop()
	})

	// Every expected service is registered (with its full method set).
	info := server.GetServiceInfo()
	for service, wantMethods := range map[string]int{
		"proto.DyAuthService":               3,
		"proto.DyAccountService":            16,
		"proto.DyProfileService":            25,
		"proto.DyActionLogService":          3,
		"proto.DyPermissionService":         6,
		"proto.DyBotAccountReceiverService": 9,
		"proto.DyAuthorizedAppService":      1,
		"proto.DyCapabilitiesService":       1,
	} {
		svc, ok := info[service]
		if !ok {
			t.Errorf("service %s not registered", service)
			continue
		}
		if len(svc.Methods) != wantMethods {
			t.Errorf("service %s has %d methods, want %d", service, len(svc.Methods), wantMethods)
		}
	}
	if _, ok := info["grpc.health.v1.Health"]; !ok {
		t.Error("grpc.health.v1.Health not registered")
	}

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Capabilities round-trips over the wire (no DB needed).
	capClient := gen.NewDyCapabilitiesServiceClient(conn)
	resp, err := capClient.GetCapabilities(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("GetCapabilities over wire: %v", err)
	}
	if resp.ApiRevision != 1 || len(resp.Capabilities) == 0 {
		t.Errorf("capabilities response wrong: revision=%d count=%d", resp.ApiRevision, len(resp.Capabilities))
	}

	// Standard gRPC health service answers SERVING.
	healthClient := grpc_health_v1.NewHealthClient(conn)
	healthResp, err := healthClient.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("health check over wire: %v", err)
	}
	if healthResp.Status != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Errorf("health status = %v, want SERVING", healthResp.Status)
	}
}
