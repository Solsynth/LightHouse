package grpcclient

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// plaintextServer starts a gRPC server without TLS credentials, mirroring
// Blade's server (grpc.NewServer() with no options).
func plaintextServer(t *testing.T) (addr string, stop func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	healthpb.RegisterHealthServer(srv, health.NewServer())
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), srv.Stop
}

func TestDialPlaintextTalksToPlaintextServer(t *testing.T) {
	addr, stop := plaintextServer(t)
	t.Cleanup(stop)

	conn, err := DialPlaintext(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// grpc.NewClient dials lazily, so the RPC is what proves the transport.
	if _, err := healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("rpc over plaintext dial: %v", err)
	}
}

func TestDialTLSFailsAgainstPlaintextServer(t *testing.T) {
	addr, stop := plaintextServer(t)
	t.Cleanup(stop)

	conn, err := Dial(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if _, err := healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{}); err == nil {
		t.Fatal("expected TLS dial against a plaintext server to fail; blade serves plaintext")
	}
}
