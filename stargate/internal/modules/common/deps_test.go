package common

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

	"src.solsynth.dev/sosys/stargate/internal/core"
	"src.solsynth.dev/sosys/stargate/internal/grpcserver"
	"src.solsynth.dev/sosys/stargate/internal/relationship"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// commonGrpcDSN mirrors the grpcserver relationship tests: the shared local
// database, skipped when it is not reachable.
const commonGrpcDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

// TestGRPCDepsCarryRelationshipsService pins the bootstrap contract behind the
// six DyProfileService relationship RPCs: the relationships module owns the
// social-graph read service but does NOT register DyProfileService itself (the
// profiles module does), so the service must travel through the shared GRPC
// dependency bundle. When GRPC() dropped the field the profiles-registered
// service saw a nil Relationships and answered Unimplemented for every call,
// which is exactly what the C# fleet hit at runtime.
//
// The assertion is the RPC result, not the struct field: a bundle that stops
// carrying the service fails here as Unimplemented.
func TestGRPCDepsCarryRelationshipsService(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), commonGrpcDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}

	seedCtx := context.Background()
	viewer, friend := seedAccount(t, pool, seedCtx), seedAccount(t, pool, seedCtx)
	now := time.Now().UTC()
	if _, err := pool.Exec(seedCtx, `INSERT INTO account_relationships
		(account_id, related_id, status, created_at, updated_at)
		VALUES ($1, $2, 100, $3, $3)
		ON CONFLICT (account_id, related_id) DO UPDATE SET status = 100, expired_at = NULL`,
		viewer, friend, now); err != nil {
		t.Fatalf("seed relationship: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(seedCtx, `DELETE FROM account_relationships WHERE account_id = ANY($1) OR related_id = ANY($1)`, []string{viewer, friend}); err != nil {
			t.Errorf("cleanup relationships: %v", err)
		}
		if _, err := pool.Exec(seedCtx, `DELETE FROM accounts WHERE id = ANY($1)`, []string{viewer, friend}); err != nil {
			t.Errorf("cleanup accounts: %v", err)
		}
	})

	// Build the graph exactly as app.Run does for an enabled relationships
	// module, then mount DyProfileService through the shared bundle.
	st := store.New(pool)
	services := &core.Services{Store: st, Relationships: relationship.New(st, nil, nil)}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	grpcserver.RegisterProfiles(srv, GRPC(services))
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := gen.NewDyProfileServiceClient(conn)

	list, err := client.ListFriends(ctx, &gen.DyListRelationshipSimpleRequest{
		RelationIdentifier: &gen.DyListRelationshipSimpleRequest_AccountId{AccountId: viewer},
	})
	if err != nil {
		t.Fatalf("ListFriends = %v, want the friends list (Unimplemented means the bundle dropped Relationships)", err)
	}
	found := false
	for _, id := range list.AccountsId {
		if id == friend {
			found = true
		}
	}
	if !found {
		t.Errorf("ListFriends = %v, want %s", list.AccountsId, friend)
	}

	has, err := client.HasRelationship(ctx, &gen.DyGetRelationshipRequest{
		AccountId: viewer, RelatedId: friend, Status: new(int32(100)),
	})
	if err != nil {
		t.Fatalf("HasRelationship: %v", err)
	}
	if !has.Value {
		t.Error("HasRelationship = false, want true for a seeded Friends row")
	}
}

// TestGRPCDepsRelationshipsNilStaysUnimplemented is the counterpart: an
// absent service (relationships module disabled) must keep reporting
// Unimplemented rather than panicking.
func TestGRPCDepsRelationshipsNilStaysUnimplemented(t *testing.T) {
	srv := grpc.NewServer()
	// No Store either: the nil service is the gate, and available()
	// short-circuits before any store access.
	grpcserver.RegisterProfiles(srv, GRPC(&core.Services{Relationships: nil}))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	_, err = gen.NewDyProfileServiceClient(conn).ListFriends(context.Background(), &gen.DyListRelationshipSimpleRequest{
		RelationIdentifier: &gen.DyListRelationshipSimpleRequest_AccountId{AccountId: uuid.NewString()},
	})
	if status.Code(err) != codes.Unimplemented {
		t.Errorf("ListFriends without a service: code = %v, want Unimplemented", status.Code(err))
	}
}

func seedAccount(t *testing.T, pool *pgxpool.Pool, ctx context.Context) string {
	t.Helper()
	id := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, id, "depsgrpc_"+uuid.NewString()[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return id
}
