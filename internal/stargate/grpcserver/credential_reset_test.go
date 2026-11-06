package grpcserver

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/stargate/auth"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/store"
)

func resetGRPCHarness(t *testing.T) (gen.DyAccountServiceClient, *store.Store, *pgxpool.Pool) {
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

	st := store.New(pool)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	RegisterAccounts(srv, Deps{Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return gen.NewDyAccountServiceClient(conn), st, pool
}

func seedPasswordResetAccount(t *testing.T, pool *pgxpool.Pool, ctx context.Context) string {
	t.Helper()
	accountID := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, "grpc_reset_"+uuid.NewString()[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM account_auth_factors WHERE account_id = $1`, accountID)
		pool.Exec(context.Background(), `DELETE FROM magic_spells WHERE account_id = $1`, accountID)
		pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = $1`, accountID)
	})
	return accountID
}

func seedResetSpell(t *testing.T, pool *pgxpool.Pool, ctx context.Context, accountID string, expiresAt time.Time) string {
	t.Helper()
	id := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO magic_spells
		(id, spell, type, expires_at, affected_at, meta, account_id, created_at, updated_at, deleted_at)
		VALUES ($1, $2, $3, $4, NULL, '{}'::jsonb, $5, $6, $6, NULL)`,
		id, "reset-"+uuid.NewString(), int(model.MagicSpellTypeAuthPasswordReset), expiresAt, accountID, now); err != nil {
		t.Fatalf("seed reset spell: %v", err)
	}
	return id
}

// TestResetPasswordFactorRequiresTheSpell pins the gRPC contract: the RPC has
// no per-caller authentication, so the redeemed magic spell is the only thing
// that authorizes rewriting a password. Before the fix the SpellId field was
// parsed away and any in-fleet caller could reset any account.
func TestResetPasswordFactorRequiresTheSpell(t *testing.T) {
	ctx := context.Background()
	client, st, pool := resetGRPCHarness(t)
	accountID := seedPasswordResetAccount(t, pool, ctx)
	otherAccountID := seedPasswordResetAccount(t, pool, ctx)

	// 1. No spell id: refused, and nothing was written.
	_, err := client.ResetPasswordFactor(ctx, &gen.DyResetPasswordFactorRequest{
		AccountId: accountID, NewPassword: "spellless-password",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("spellless reset code = %v (%v), want InvalidArgument", status.Code(err), err)
	}
	assertNoPasswordFactor(t, st, ctx, accountID)

	// 2. A spell that belongs to another account: refused.
	foreign := seedResetSpell(t, pool, ctx, otherAccountID, time.Now().UTC().Add(time.Hour))
	_, err = client.ResetPasswordFactor(ctx, &gen.DyResetPasswordFactorRequest{
		AccountId: accountID, NewPassword: "spellless-password",
		SpellId: wrapperspb.String(foreign),
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("foreign spell code = %v (%v), want PermissionDenied", status.Code(err), err)
	}
	assertNoPasswordFactor(t, st, ctx, accountID)

	// 3. An expired spell: refused.
	expired := seedResetSpell(t, pool, ctx, accountID, time.Now().UTC().Add(-time.Minute))
	_, err = client.ResetPasswordFactor(ctx, &gen.DyResetPasswordFactorRequest{
		AccountId: accountID, NewPassword: "spellless-password",
		SpellId: wrapperspb.String(expired),
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expired spell code = %v (%v), want PermissionDenied", status.Code(err), err)
	}
	assertNoPasswordFactor(t, st, ctx, accountID)

	// 4. A live spell for this account: the password is installed and the
	// spell is consumed.
	live := seedResetSpell(t, pool, ctx, accountID, time.Now().UTC().Add(time.Hour))
	if _, err := client.ResetPasswordFactor(ctx, &gen.DyResetPasswordFactorRequest{
		AccountId: accountID, NewPassword: "authorized-password",
		SpellId: wrapperspb.String(live),
	}); err != nil {
		t.Fatalf("authorized reset: %v", err)
	}
	factor, err := st.GetEnabledPasswordFactor(ctx, accountID)
	if err != nil {
		t.Fatalf("load password factor: %v", err)
	}
	if ok, _ := auth.VerifyFactorPassword(factor, "authorized-password"); !ok {
		t.Fatal("installed password does not verify")
	}
	var spells int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM magic_spells WHERE id = $1`, live).Scan(&spells); err != nil {
		t.Fatalf("count spell: %v", err)
	}
	if spells != 0 {
		t.Fatal("reset spell was not consumed")
	}

	// 5. Replaying the consumed spell: refused.
	_, err = client.ResetPasswordFactor(ctx, &gen.DyResetPasswordFactorRequest{
		AccountId: accountID, NewPassword: "replay-password",
		SpellId: wrapperspb.String(live),
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("replayed spell code = %v (%v), want PermissionDenied", status.Code(err), err)
	}
	if ok, _ := auth.VerifyFactorPassword(factor, "replay-password"); ok {
		t.Fatal("consumed spell reset the password again")
	}
}

func assertNoPasswordFactor(t *testing.T, st *store.Store, ctx context.Context, accountID string) {
	t.Helper()
	if _, err := st.GetEnabledPasswordFactor(ctx, accountID); err == nil {
		t.Fatal("password factor was written without a valid spell")
	}
}
