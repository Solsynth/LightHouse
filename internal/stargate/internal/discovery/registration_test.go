package discovery

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
	gen "src.solsynth.dev/sosys/go/proto"
)

var notFoundErr = status.Error(codes.NotFound, "lease not found")

// fakeBladeDiscovery is an in-process DyServiceDiscoveryService server that
// records Register/Renew/Deregister calls and can simulate a lost lease.
type fakeBladeDiscovery struct {
	gen.UnimplementedDyServiceDiscoveryServiceServer

	mu          sync.Mutex
	registers   int
	renews      int
	deregisters int
	lastAuth    string
	lease       time.Duration
	notFoundOn  int // fail the Nth renew with NotFound to force re-register
}

func (f *fakeBladeDiscovery) Register(ctx context.Context, req *gen.DyRegisterServiceInstanceRequest) (*gen.DyRegisterServiceInstanceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registers++
	f.lastAuth = authHeader(ctx)
	return &gen.DyRegisterServiceInstanceResponse{
		Instance:             req.GetInstance(),
		LeaseExpiresAtUnixMs: time.Now().Add(f.lease).UnixMilli(),
	}, nil
}

func (f *fakeBladeDiscovery) Renew(ctx context.Context, req *gen.DyRenewServiceLeaseRequest) (*gen.DyRenewServiceLeaseResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renews++
	f.lastAuth = authHeader(ctx)
	if f.notFoundOn > 0 && f.renews == f.notFoundOn {
		return nil, notFoundErr
	}
	return &gen.DyRenewServiceLeaseResponse{
		LeaseExpiresAtUnixMs: time.Now().Add(f.lease).UnixMilli(),
	}, nil
}

func (f *fakeBladeDiscovery) Deregister(ctx context.Context, req *gen.DyDeregisterServiceInstanceRequest) (*emptypb.Empty, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deregisters++
	f.lastAuth = authHeader(ctx)
	return &emptypb.Empty{}, nil
}

func (f *fakeBladeDiscovery) snapshot() (registers, renews, deregisters int, auth string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.registers, f.renews, f.deregisters, f.lastAuth
}

func authHeader(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if values := md.Get("authorization"); len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func newTestClient(t *testing.T, fake *fakeBladeDiscovery) gen.DyServiceDiscoveryServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	gen.RegisterDyServiceDiscoveryServiceServer(server, fake)
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return gen.NewDyServiceDiscoveryServiceClient(conn)
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(discard{}, nil))
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// TestRegistrationLifecycle pins the full register -> renew -> deregister
// loop with a short lease so the renewal fires within the test.
func TestRegistrationLifecycle(t *testing.T) {
	fake := &fakeBladeDiscovery{lease: 3 * time.Second}
	client := newTestClient(t, fake)

	reg := New(client, Options{
		Service: "stargate", InstanceID: "inst-1",
		HttpEndpoint: "http://stargate:8080", GrpcEndpoint: "stargate:9090",
		RegistrationToken: "tok", LeaseSeconds: 30, Weight: 1,
	}, discardLog())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		reg.Run(ctx)
		close(done)
	}()

	waitFor(t, func() bool {
		r, _, _, _ := fake.snapshot()
		return r >= 1
	}, "register")

	// Lease is 3s; renewal interval is ~1s, so a renew must fire.
	waitFor(t, func() bool {
		_, n, _, _ := fake.snapshot()
		return n >= 1
	}, "renew")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	reg.Deregister(context.Background())
	_, _, d, _ := fake.snapshot()
	if d != 1 {
		t.Fatalf("deregisters = %d, want 1", d)
	}
}

// TestRegistrationAuthorization pins the Bearer token on every call.
func TestRegistrationAuthorization(t *testing.T) {
	fake := &fakeBladeDiscovery{lease: time.Hour}
	client := newTestClient(t, fake)
	reg := New(client, Options{
		Service: "stargate", InstanceID: "inst-2",
		GrpcEndpoint:      "stargate:9090",
		RegistrationToken: "secret-token", LeaseSeconds: 30,
	}, discardLog())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		reg.Run(ctx)
		close(done)
	}()
	waitFor(t, func() bool {
		_, _, _, auth := fake.snapshot()
		return auth == "Bearer secret-token"
	}, "authorized register")
	cancel()
	<-done
}

// TestRenewalInterval pins the C# GetRenewalInterval math.
func TestRenewalInterval(t *testing.T) {
	cases := []struct {
		expiresAtUnixMs int64
		leaseSeconds    int
		wantMin         time.Duration
		wantMax         time.Duration
	}{
		{time.Now().Add(9 * time.Second).UnixMilli(), 30, 2*time.Second + 500*time.Millisecond, 4 * time.Second},
		{0, 30, 9*time.Second - time.Millisecond, 11 * time.Second},
		{0, 2, time.Second, time.Second}, // lease < 3s clamps to 1s
	}
	for _, tc := range cases {
		got := renewalInterval(tc.expiresAtUnixMs, tc.leaseSeconds)
		if got < tc.wantMin || got > tc.wantMax {
			t.Errorf("renewalInterval(%d, %d) = %v, want in [%v, %v]",
				tc.expiresAtUnixMs, tc.leaseSeconds, got, tc.wantMin, tc.wantMax)
		}
	}
}

// TestValidate pins the configuration guards.
func TestValidate(t *testing.T) {
	base := Options{Service: "stargate", InstanceID: "i", HttpEndpoint: "http://x", LeaseSeconds: 30, Weight: 1}
	if err := Validate(base); err != nil {
		t.Fatalf("valid options rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Options){
		"no service":  func(o *Options) { o.Service = "" },
		"no instance": func(o *Options) { o.InstanceID = "" },
		"no endpoint": func(o *Options) { o.HttpEndpoint = ""; o.GrpcEndpoint = "" },
		"short lease": func(o *Options) { o.LeaseSeconds = 2 },
		"zero weight": func(o *Options) { o.Weight = 0 },
	} {
		opts := base
		mutate(&opts)
		if err := Validate(opts); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
