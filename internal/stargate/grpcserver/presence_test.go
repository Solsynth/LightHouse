package grpcserver

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/stargate/store"
)

// TestListFriendsActivitiesCapsPerType pins the contract Sphere depends on:
// the per-type cap keeps each activity type visible instead of letting one
// noisy type crowd out the rest, only active leases are reported, and an
// account with nothing recent yields an empty page rather than an error.
//
// Skips when no Postgres is reachable, like the other db-backed gRPC tests.
func TestListFriendsActivitiesCapsPerType(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://postgres@127.0.0.1:5432/dyson_stargate?sslmode=disable")
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}

	account := "11111111-1111-1111-1111-111111111111"
	now := time.Now().UTC()
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM presence_activities WHERE account_id = $1`, account); err != nil {
		t.Fatalf("clean: %v", err)
	}

	// Two active "gaming" rows and one "listening" row: the per-type cap must
	// keep 2 + 1, not the newest 3 overall.
	seed := func(id, typ string, minutesAgo int) {
		started := now.Add(-time.Duration(minutesAgo) * time.Minute)
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO presence_activities
			 (id, account_id, type, title, visibility, lease_minutes, lease_expires_at,
			  queryable_terms, created_at, updated_at, started_at)
			 VALUES ($1,$2,$3,$4,1,10,$5,'[]',$6,$6,$6)`,
			id, account, typ, "t-"+id, now.Add(10*time.Minute), started); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("aaaaaaaa-0000-0000-0000-000000000001", "gaming", 5)
	seed("aaaaaaaa-0000-0000-0000-000000000002", "gaming", 10)
	seed("aaaaaaaa-0000-0000-0000-000000000003", "listening", 15)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM presence_activities WHERE account_id = $1`, account)
	})

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcSrv := grpc.NewServer()
	Register(grpcSrv, Deps{Store: store.New(pool)})
	go grpcSrv.Serve(lis)
	t.Cleanup(grpcSrv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := gen.NewDyPresenceServiceClient(conn)

	req := &gen.DyListFriendsActivitiesRequest{MaxPerType: 1, Take: 10}
	req.AccountIds = append(req.AccountIds, account)

	resp, err := client.ListFriendsActivities(context.Background(), req)
	if err != nil {
		t.Fatalf("ListFriendsActivities: %v", err)
	}
	if len(resp.Activities) != 2 {
		t.Fatalf("activities = %d, want 2 (one per type)", len(resp.Activities))
	}
	for _, a := range resp.Activities {
		if a.GetAccountId() != account {
			t.Fatalf("account id = %q", a.GetAccountId())
		}
		if a.GetLeaseExpiresAt() == nil {
			t.Fatalf("lease_expires_at missing on %s", a.GetId())
		}
		if !a.GetIsActive() {
			t.Fatalf("expected active activity, got %+v", a.GetId())
		}
	}

	// An unknown account yields an empty page rather than an error.
	empty := &gen.DyListFriendsActivitiesRequest{}
	empty.AccountIds = append(empty.AccountIds, "22222222-2222-2222-2222-222222222222")
	emptyResp, err := client.ListFriendsActivities(context.Background(), empty)
	if err != nil {
		t.Fatalf("empty ListFriendsActivities: %v", err)
	}
	if len(emptyResp.Activities) != 0 {
		t.Fatalf("expected no activities, got %d", len(emptyResp.Activities))
	}
}
