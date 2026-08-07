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
	seed := func(badgeJSON string) string {
		id := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
			VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, id, "profile_batch_"+uuid.NewString()[:8], now); err != nil {
			t.Fatalf("seed account: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO account_profiles (id, account_id, first_name, picture, active_badge, created_at, updated_at, experience, social_credits)
			VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6, $6, 0, 100)`,
			uuid.NewString(), id, "TestName",
			`{"Id": "pic-`+id[:8]+`", "Url": "https://example.com/`+id[:8]+`.png"}`,
			badgeJSON,
			now); err != nil {
			t.Fatalf("seed profile: %v", err)
		}
		return id
	}
	// Legacy C# EF rows store PascalCase partial refs; NATS-synced refs are
	// snake_case (Passport's ProfileFieldUpdatedEvent). Both must map to the
	// same DyBadgeReferenceObject on the wire.
	alice := seed(`{"Id": "badge-` + uuid.NewString()[:8] + `", "Type": "pioneer", "Label": "Pioneer"}`)
	bob := seed(`{"id": "badge-` + uuid.NewString()[:8] + `", "type": "pioneer", "label": "Pioneer", "meta": {}, "activated_at": "2026-08-07T02:33:00Z", "account_id": "00000000-0000-0000-0000-000000000000"}`)
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

// TestGetAccountHydratesProfile guards the single-account read that
// Passport's ticket (and other) hydration uses: DyAccountService.GetAccount
// must carry the profile. Regression for hydrateProfiles mutating a
// dereferenced copy, which serialized a nil profile on this RPC.
func TestGetAccountHydratesProfile(t *testing.T) {
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
	id := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, id, "profile_single_"+uuid.NewString()[:8], now); err != nil {
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
	defer pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, id)

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

	resp, err := client.GetAccount(ctx, &gen.DyGetAccountRequest{Id: id})
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if resp.Profile == nil || resp.Profile.Id == "" {
		t.Fatalf("account %s has no profile: %+v", resp.Id, resp)
	}
	if resp.Profile.FirstName.GetValue() != "TestName" {
		t.Fatalf("account %s profile first_name = %q, want TestName", resp.Id, resp.Profile.FirstName.GetValue())
	}
	if resp.Profile.Picture == nil || resp.Profile.Picture.Id == "" || resp.Profile.Picture.Url == "" {
		t.Fatalf("account %s profile picture missing: %+v", resp.Id, resp.Profile.Picture)
	}
	if resp.Profile.ActiveBadge == nil || resp.Profile.ActiveBadge.Id == "" || resp.Profile.ActiveBadge.Type != "pioneer" {
		t.Fatalf("account %s profile active_badge missing: %+v", resp.Id, resp.Profile.ActiveBadge)
	}
}

// TestGetAccountBatchCreatesMissingProfiles covers the chat member hydration
// path: a batch with accounts that have no profile row yet (or a soft-deleted
// tombstone) must still return every account WITH a profile — Messager
// filters out members whose account failed to load.
func TestGetAccountBatchCreatesMissingProfiles(t *testing.T) {
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
	seed := func(withProfile bool) string {
		id := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
			VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, id, "chat_batch_"+uuid.NewString()[:8], now); err != nil {
			t.Fatalf("seed account: %v", err)
		}
		if withProfile {
			if _, err := pool.Exec(ctx, `INSERT INTO account_profiles (id, account_id, created_at, updated_at, experience, social_credits)
				VALUES ($1, $2, $3, $3, 0, 100)`, uuid.NewString(), id, now); err != nil {
				t.Fatalf("seed profile: %v", err)
			}
		}
		return id
	}
	live := seed(true)
	missing := seed(false)
	bare := uuid.NewString()
	tombstoned := seed(true)
	if _, err := pool.Exec(ctx, `UPDATE account_profiles SET deleted_at = $1 WHERE account_id = $2`, now, tombstoned); err != nil {
		t.Fatalf("tombstone profile: %v", err)
	}
	// Bare row: exists with zero profile data (migrated account that never
	// edited its profile) — must be backfilled with the account name.
	bareName := "chat_bare_" + uuid.NewString()[:8]
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, bare, bareName, now); err != nil {
		t.Fatalf("seed bare account: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE account_profiles SET first_name = '', last_name = '', bio = NULL, picture = NULL WHERE account_id = $1`, bare); err != nil {
		t.Fatalf("blank bare profile: %v", err)
	}
	defer pool.Exec(ctx, `DELETE FROM accounts WHERE id = ANY($1)`, []string{live, missing, bare, tombstoned})

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

	resp, err := client.GetAccountBatch(ctx, &gen.DyGetAccountBatchRequest{Id: []string{live, missing, bare, tombstoned}})
	if err != nil {
		t.Fatalf("GetAccountBatch: %v", err)
	}
	if len(resp.Accounts) != 4 {
		t.Fatalf("got %d accounts, want 4 (chat members with missing profiles must not vanish)", len(resp.Accounts))
	}
	for _, a := range resp.Accounts {
		if a.Profile == nil || a.Profile.Id == "" {
			t.Fatalf("account %s has no profile", a.Id)
		}
	}
	byID := map[string]*gen.DyAccount{}
	for _, a := range resp.Accounts {
		byID[a.Id] = a
	}
	if f := byID[bare].Profile.GetFirstName().GetValue(); f != bareName {
		t.Fatalf("bare profile first_name = %q, want %q (backfill from account name)", f, bareName)
	}
	if f := byID[missing].Profile.GetFirstName().GetValue(); f == "" {
		t.Fatalf("created profile not backfilled with account name")
	}
}
