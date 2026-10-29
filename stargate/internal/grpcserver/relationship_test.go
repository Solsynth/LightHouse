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

	"src.solsynth.dev/sosys/stargate/internal/relationship"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// relationshipSeed accounts inserted for one test and removed afterwards.
type relationshipSeed struct {
	pool *pgxpool.Pool
	ids  []string
	ctx  context.Context
}

func newRelationshipSeed(t *testing.T, ctx context.Context, pool *pgxpool.Pool, n int) *relationshipSeed {
	t.Helper()
	s := &relationshipSeed{pool: pool, ctx: ctx}
	now := time.Now().UTC()
	for range n {
		id := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
			VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, id, "relgrpc_"+uuid.NewString()[:8], now); err != nil {
			t.Fatalf("seed account: %v", err)
		}
		s.ids = append(s.ids, id)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(s.ctx, `DELETE FROM account_relationships WHERE account_id = ANY($1) OR related_id = ANY($1)`, s.ids); err != nil {
			t.Errorf("cleanup relationships: %v", err)
		}
		if _, err := pool.Exec(s.ctx, `DELETE FROM account_profiles WHERE account_id = ANY($1)`, s.ids); err != nil {
			t.Errorf("cleanup profiles: %v", err)
		}
		if _, err := pool.Exec(s.ctx, `DELETE FROM accounts WHERE id = ANY($1)`, s.ids); err != nil {
			t.Errorf("cleanup accounts: %v", err)
		}
	})
	return s
}

// insert writes one account_relationships row. status mirrors the C# enum:
// Friends=100, Pending=0, Muted=-50, Blocked=-100, CloseFriend=200.
func (s *relationshipSeed) insert(t *testing.T, account, related string, status int16, expiredAt *time.Time) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := s.pool.Exec(s.ctx, `INSERT INTO account_relationships
		(account_id, related_id, status, expired_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $5)
		ON CONFLICT (account_id, related_id) DO UPDATE SET status = $3, expired_at = $4`,
		account, related, status, expiredAt, now); err != nil {
		t.Fatalf("seed relationship: %v", err)
	}
}

// startRelationshipServer serves DyProfileService over a real TCP listener and
// returns a client bound to it.
func startRelationshipServer(t *testing.T, deps Deps) gen.DyProfileServiceClient {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcSrv := grpc.NewServer()
	Register(grpcSrv, deps)
	go grpcSrv.Serve(lis)
	t.Cleanup(grpcSrv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return gen.NewDyProfileServiceClient(conn)
}

func relTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), mlsGrpcDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	return pool
}

// TestRelationshipRPCFriendsFamily pins the strict-vs-loose distinction the
// fleet depends on. ListFriends must include CloseFriend rows (GetCached
// Relationships matches both), while HasRelationship with status=Friends is
// strict and must NOT report a CloseFriend-only row. A regression here makes
// Sphere treat close friends as non-friends, or Meet treat them as friends.
func TestRelationshipRPCFriendsFamily(t *testing.T) {
	pool := relTestPool(t)
	ctx := context.Background()
	seed := newRelationshipSeed(t, ctx, pool, 3)
	viewer, closeFriend, plainFriend := seed.ids[0], seed.ids[1], seed.ids[2]
	seed.insert(t, viewer, closeFriend, 200, nil) // CloseFriend
	seed.insert(t, viewer, plainFriend, 100, nil) // Friends

	client := startRelationshipServer(t, Deps{Store: store.New(pool),
		Relationships: relationship.New(store.New(pool), nil, nil)})

	list, err := client.ListFriends(ctx, &gen.DyListRelationshipSimpleRequest{
		RelationIdentifier: &gen.DyListRelationshipSimpleRequest_AccountId{AccountId: viewer},
	})
	if err != nil {
		t.Fatalf("ListFriends: %v", err)
	}
	got := map[string]bool{}
	for _, id := range list.AccountsId {
		got[id] = true
	}
	if !got[closeFriend] {
		t.Errorf("ListFriends = %v, want CloseFriend row included (friends-family)", list.AccountsId)
	}
	if !got[plainFriend] {
		t.Errorf("ListFriends = %v, want Friends row included", list.AccountsId)
	}

	// Strict status check: CloseFriend is NOT Friends.
	has, err := client.HasRelationship(ctx, &gen.DyGetRelationshipRequest{
		AccountId: viewer, RelatedId: closeFriend, Status: new(int32(100)),
	})
	if err != nil {
		t.Fatalf("HasRelationship: %v", err)
	}
	if has.Value {
		t.Error("HasRelationship(status=Friends) on a CloseFriend row = true, want strict false")
	}

	// CloseFriend status matches itself.
	has, err = client.HasRelationship(ctx, &gen.DyGetRelationshipRequest{
		AccountId: viewer, RelatedId: closeFriend, Status: new(int32(200)),
	})
	if err != nil {
		t.Fatalf("HasRelationship: %v", err)
	}
	if !has.Value {
		t.Error("HasRelationship(status=CloseFriend) on a CloseFriend row = false, want true")
	}
}

// TestRelationshipRPCExpiredExcluded pins that expired rows are invisible to
// the list and check RPCs: a timed mute/block that has lapsed must not still
// report.
func TestRelationshipRPCExpiredExcluded(t *testing.T) {
	pool := relTestPool(t)
	ctx := context.Background()
	seed := newRelationshipSeed(t, ctx, pool, 2)
	muter, muted := seed.ids[0], seed.ids[1]
	past := time.Now().UTC().Add(-time.Hour)
	seed.insert(t, muter, muted, -50, &past) // Muted, expired

	client := startRelationshipServer(t, Deps{Store: store.New(pool),
		Relationships: relationship.New(store.New(pool), nil, nil)})

	list, err := client.ListMuted(ctx, &gen.DyListRelationshipSimpleRequest{
		RelationIdentifier: &gen.DyListRelationshipSimpleRequest_AccountId{AccountId: muter},
	})
	if err != nil {
		t.Fatalf("ListMuted: %v", err)
	}
	if len(list.AccountsId) != 0 {
		t.Errorf("ListMuted returned expired row(s) %v, want none", list.AccountsId)
	}
}

// TestRelationshipRPCBlockedEitherDirection pins the bidirectional block check
// the C# handlers expose through either_direction: a block by the other side
// must also report.
func TestRelationshipRPCBlockedEitherDirection(t *testing.T) {
	pool := relTestPool(t)
	ctx := context.Background()
	seed := newRelationshipSeed(t, ctx, pool, 2)
	blocked, blocker := seed.ids[0], seed.ids[1]
	seed.insert(t, blocker, blocked, -100, nil) // blocker blocks blocked

	client := startRelationshipServer(t, Deps{Store: store.New(pool),
		Relationships: relationship.New(store.New(pool), nil, nil)})

	has, err := client.HasRelationship(ctx, &gen.DyGetRelationshipRequest{
		AccountId: blocked, RelatedId: blocker, Status: new(int32(-100)), EitherDirection: true,
	})
	if err != nil {
		t.Fatalf("HasRelationship: %v", err)
	}
	if !has.Value {
		t.Error("HasRelationship(either_direction, Blocked) missed a block by the other side")
	}

	// Without either_direction the same block is not reported (directional).
	has, err = client.HasRelationship(ctx, &gen.DyGetRelationshipRequest{
		AccountId: blocked, RelatedId: blocker, Status: new(int32(-100)),
	})
	if err != nil {
		t.Fatalf("HasRelationship: %v", err)
	}
	if has.Value {
		t.Error("HasRelationship(directed, Blocked) reported the reverse direction")
	}
}

// TestRelationshipRPCInvalidArguments pins the wire error contract the C#
// handlers shipped with: an unparsable id and a missing identifier are both
// InvalidArgument, and the two list families use their own messages.
func TestRelationshipRPCInvalidArguments(t *testing.T) {
	pool := relTestPool(t)
	client := startRelationshipServer(t, Deps{Store: store.New(pool),
		Relationships: relationship.New(store.New(pool), nil, nil)})
	ctx := context.Background()

	_, err := client.ListFriends(ctx, &gen.DyListRelationshipSimpleRequest{
		RelationIdentifier: &gen.DyListRelationshipSimpleRequest_AccountId{AccountId: "not-a-uuid"},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("ListFriends unparsable id: code = %v, want InvalidArgument", status.Code(err))
	}

	_, err = client.ListFriends(ctx, &gen.DyListRelationshipSimpleRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("ListFriends missing identifier: code = %v, want InvalidArgument", status.Code(err))
	}

	// ListMuted/ListCloseFriends reject related_id (the C# only took account_id).
	_, err = client.ListMuted(ctx, &gen.DyListRelationshipSimpleRequest{
		RelationIdentifier: &gen.DyListRelationshipSimpleRequest_RelatedId{RelatedId: uuid.NewString()},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("ListMuted related_id: code = %v, want InvalidArgument", status.Code(err))
	}
	if status.Convert(err).Message() != "The account_id must be provided." {
		t.Errorf("ListMuted related_id message = %q, want the C# wording", status.Convert(err).Message())
	}
}

// TestRelationshipRPCDisabledModule pins the module-gating contract: with no
// relationship service (relationships module disabled) every relationship RPC
// reports Unimplemented rather than answering or erroring on input.
func TestRelationshipRPCDisabledModule(t *testing.T) {
	client := startRelationshipServer(t, Deps{})
	ctx := context.Background()
	accountID := uuid.NewString()

	_, err := client.ListFriends(ctx, &gen.DyListRelationshipSimpleRequest{
		RelationIdentifier: &gen.DyListRelationshipSimpleRequest_AccountId{AccountId: accountID},
	})
	if status.Code(err) != codes.Unimplemented {
		t.Errorf("ListFriends with module disabled: code = %v, want Unimplemented", status.Code(err))
	}

	_, err = client.HasRelationship(ctx, &gen.DyGetRelationshipRequest{
		AccountId: accountID, RelatedId: uuid.NewString(),
	})
	if status.Code(err) != codes.Unimplemented {
		t.Errorf("HasRelationship with module disabled: code = %v, want Unimplemented", status.Code(err))
	}
}
