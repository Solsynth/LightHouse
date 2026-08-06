package grpcserver

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/e2eectl"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// recordingBus satisfies auth.EventBus without a live NATS broker.
type recordingBus struct{}

func (recordingBus) PublishSessionRevoked(context.Context, []auth.SessionRevokedEvent) error { return nil }
func (recordingBus) PublishWS(context.Context, string, string, any) error                     { return nil }

const mlsGrpcDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

// TestDyMlsServiceSmoke exercises the RPCs Messager relies on
// (GetGroupState, AddMlsDeviceMembership) plus the group-info and delete
// surface, over a real in-process gRPC server.
func TestDyMlsServiceSmoke(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), mlsGrpcDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	ctx = context.Background()

	st := store.New(pool)
	e2ee := e2eectl.NewService(st, recordingBus{}, nil, nil)

	seed := func(name string) string {
		id := uuid.NewString()
		now := time.Now().UTC()
		if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
			VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, id, name, now); err != nil {
			t.Fatalf("seed account: %v", err)
		}
		return id
	}
	bob := seed("mls_grpc_smoke_" + uuid.NewString()[:8])
	defer pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, bob)

	groupID := "grp-" + uuid.NewString()[:8]
	deviceB := "dev-" + uuid.NewString()[:8]
	if _, err := e2ee.BootstrapMlsGroup(ctx, bob, groupID, 0, 1, nil); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if _, err := e2ee.AddMlsDeviceMembership(ctx, bob, deviceB, groupID, 0); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	if _, err := e2ee.UploadGroupInfo(ctx, groupID, []byte("gi"), []byte("rt"), 0); err != nil {
		t.Fatalf("seed group info: %v", err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcSrv := grpc.NewServer()
	Register(grpcSrv, Deps{Store: st, E2ee: e2ee})
	go grpcSrv.Serve(lis)
	defer grpcSrv.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	client := gen.NewDyMlsServiceClient(conn)

	// GetGroupState returns the bootstrapped epoch.
	state, err := client.GetGroupState(ctx, &gen.GetMlsGroupStateRequest{GroupId: groupID})
	if err != nil {
		t.Fatalf("GetGroupState: %v", err)
	}
	if state.GroupId != groupID || state.Epoch != 0 || state.StateVersion != 1 {
		t.Fatalf("GetGroupState mismatch: %+v", state)
	}

	// AddMlsDeviceMembership registers a new device; response epoch falls
	// back to the joined epoch when no last-seen is recorded.
	deviceC := "dev-" + uuid.NewString()[:8]
	membership, err := client.AddMlsDeviceMembership(ctx, &gen.AddMlsDeviceMembershipRequest{
		AccountId: bob, DeviceId: deviceC, GroupId: groupID, Epoch: 2,
	})
	if err != nil {
		t.Fatalf("AddMlsDeviceMembership: %v", err)
	}
	if !membership.Success || membership.GroupId != groupID || membership.DeviceId != deviceC || membership.Epoch != 2 {
		t.Fatalf("AddMlsDeviceMembership mismatch: %+v", membership)
	}

	// GetGroupInfo exposes the uploaded blobs.
	info, err := client.GetGroupInfo(ctx, &gen.GetMlsGroupInfoRequest{GroupId: groupID})
	if err != nil {
		t.Fatalf("GetGroupInfo: %v", err)
	}
	if string(info.GroupInfo) != "gi" || string(info.RatchetTree) != "rt" {
		t.Fatalf("GetGroupInfo mismatch: %+v", info)
	}

	// Unknown group surfaces NotFound (the C# threw RpcException NotFound).
	if _, err := client.GetGroupState(ctx, &gen.GetMlsGroupStateRequest{GroupId: "grp-missing"}); status.Code(err) != codes.NotFound {
		t.Fatalf("GetGroupState missing = %v, want NotFound", err)
	}

	// DeleteGroup soft-deletes the state; subsequent reads are NotFound.
	del, err := client.DeleteGroup(ctx, &gen.DeleteMlsGroupRequest{GroupId: groupID})
	if err != nil || !del.Success || del.DeletedStateCount != 1 {
		t.Fatalf("DeleteGroup: %v %+v", err, del)
	}
	if _, err := client.GetGroupInfo(ctx, &gen.GetMlsGroupInfoRequest{GroupId: groupID}); status.Code(err) != codes.NotFound {
		t.Fatalf("GetGroupInfo after delete = %v, want NotFound", err)
	}
}
