package grpcserver

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/httpserver/e2eectl"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// TestGetAccountBatchHydratesProfiles guards the fleet's profile reads:
// Passport's friend-overview (and RemoteAccountService batch hydration)
// relies on DyAccountService.GetAccountBatch carrying the profile.
func TestGetAccountBatchHydratesProfiles(t *testing.T) {
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
	now := time.Now().UTC()
	seed := func() string {
		id := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
			VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, id, "profile_batch_"+uuid.NewString()[:8], now); err != nil {
			t.Fatalf("seed account: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO account_profiles (id, account_id, first_name, picture, active_badge, created_at, updated_at, experience, social_credits)
			VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6, $6, 0, 100)`,
			uuid.NewString(), id, "TestName",
			`{"Id": "pic-`+id[:8]+`", "Url": "https://example.com/`+id[:8]+`.png"}`,
			`{"Id": "badge-`+id[:8]+`", "Type": "pioneer", "Label": "Pioneer"}`,
			now); err != nil {
			t.Fatalf("seed profile: %v", err)
		}
		return id
	}
	alice := seed()
	bob := seed()
	defer pool.Exec(ctx, `DELETE FROM accounts WHERE id = ANY($1)`, []string{alice, bob})

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcSrv := grpc.NewServer()
	Register(grpcSrv, Deps{Store: st, E2ee: e2eectl.NewService(st, recordingBus{}, nil, nil)})
	go grpcSrv.Serve(lis)
	defer grpcSrv.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	client := gen.NewDyAccountServiceClient(conn)

	resp, err := client.GetAccountBatch(ctx, &gen.DyGetAccountBatchRequest{Id: []string{alice, bob}})
	if err != nil {
		t.Fatalf("GetAccountBatch: %v", err)
	}
	if len(resp.Accounts) != 2 {
		t.Fatalf("got %d accounts, want 2", len(resp.Accounts))
	}
	for _, a := range resp.Accounts {
		if a.Profile == nil || a.Profile.Id == "" {
			t.Fatalf("account %s has no profile: %+v", a.Id, a)
		}
		if a.Profile.FirstName.GetValue() != "TestName" {
			t.Fatalf("account %s profile first_name = %q, want TestName", a.Id, a.Profile.FirstName.GetValue())
		}
		if a.Profile.Picture == nil || a.Profile.Picture.Id == "" || a.Profile.Picture.Url == "" {
			t.Fatalf("account %s profile picture missing: %+v", a.Id, a.Profile.Picture)
		}
		if a.Profile.ActiveBadge == nil || a.Profile.ActiveBadge.Id == "" || a.Profile.ActiveBadge.Type != "pioneer" {
			t.Fatalf("account %s profile active_badge missing: %+v", a.Id, a.Profile.ActiveBadge)
		}
	}
}
