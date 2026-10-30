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
	"google.golang.org/grpc/test/bufconn"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// sudoGrpcTestDSN mirrors config.example.toml (same convention as the other
// DB-backed smoke tests).
const sudoGrpcTestDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

func sudoGrpcLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// startSudoGrpcServer mounts the inbound services through Register on a
// bufconn server and returns a DyAuthService client bound to it.
func startSudoGrpcServer(t *testing.T, deps Deps) gen.DyAuthServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	Register(server, deps)
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return gen.NewDyAuthServiceClient(conn)
}

// TestValidateSudoRPC pins the ValidateSudo contract over the wire: an
// elevated session is valid with its factor hint, an unelevated session is
// valid=false without an error, and an unresolvable elevation store (Redis
// down) is a gRPC error so callers fail closed.
func TestValidateSudoRPC(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, sudoGrpcTestDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}

	rc, err := redis.Connect(ctx, "localhost:6379", "", 0)
	if err != nil {
		t.Skipf("redis unavailable: %v", err)
	}
	t.Cleanup(func() { _ = rc.Raw.Close() })

	st := store.New(pool)
	svc := auth.NewAuthService(st, rc, config.Default(), nil, nil, nil, nil, nil, sudoGrpcLogger())
	client := startSudoGrpcServer(t, Deps{Store: st, Auth: svc})

	// A session whose account has a pickable password factor, so the
	// hint is non-empty.
	now := time.Now().UTC()
	accountID := uuid.New()
	sessionID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, "sudo_grpc_"+uuid.NewString()[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions (id, account_id, audiences, scopes, type, epoch, created_at, updated_at)
		VALUES ($1, $2, '[]', '[]', $3, 0, $4, $4)`, sessionID, accountID, int(model.SessionTypeLogin), now); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO account_auth_factors (id, account_id, type, secret, config, trustworthy, enabled_at, created_at, updated_at)
		VALUES ($1, $2, $3, '', '{}', 1, $4, $4, $4)`, uuid.New(), accountID, int(model.AuthFactorTypePassword), now); err != nil {
		t.Fatalf("seed factor: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, accountID) })
	t.Cleanup(func() { _ = rc.Cache.Remove(ctx, "accounts:"+sessionID.String()+":sudo") })

	t.Run("not elevated", func(t *testing.T) {
		resp, err := client.ValidateSudo(ctx, &gen.DyValidateSudoRequest{SessionId: sessionID.String()})
		if err != nil {
			t.Fatalf("ValidateSudo (unelevated): %v", err)
		}
		if resp.GetValid() {
			t.Fatal("valid = true for an unelevated session")
		}
		if got := resp.GetFactorTypes(); len(got) != 1 || got[0] != "password" {
			t.Fatalf("factor_types = %v, want [password]", got)
		}
	})

	t.Run("elevated", func(t *testing.T) {
		if _, err := svc.GrantSudo(ctx, sessionID.String()); err != nil {
			t.Fatalf("GrantSudo: %v", err)
		}
		resp, err := client.ValidateSudo(ctx, &gen.DyValidateSudoRequest{SessionId: sessionID.String()})
		if err != nil {
			t.Fatalf("ValidateSudo (elevated): %v", err)
		}
		if !resp.GetValid() {
			t.Fatal("valid = false for an elevated session")
		}
		if got := resp.GetFactorTypes(); len(got) != 1 || got[0] != "password" {
			t.Fatalf("factor_types = %v, want [password]", got)
		}
		svc.ClearSudo(ctx, sessionID.String())
	})

	t.Run("unknown session is not elevated", func(t *testing.T) {
		resp, err := client.ValidateSudo(ctx, &gen.DyValidateSudoRequest{SessionId: uuid.NewString()})
		if err != nil {
			t.Fatalf("ValidateSudo (unknown session): %v", err)
		}
		if resp.GetValid() {
			t.Fatal("valid = true for an unknown session")
		}
	})

	t.Run("elevation store unavailable fails closed", func(t *testing.T) {
		broken := auth.NewAuthService(st, nil, config.Default(), nil, nil, nil, nil, nil, sudoGrpcLogger())
		brokenClient := startSudoGrpcServer(t, Deps{Store: st, Auth: broken})
		_, err := brokenClient.ValidateSudo(ctx, &gen.DyValidateSudoRequest{SessionId: sessionID.String()})
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("ValidateSudo without redis = %v, want codes.Unavailable", err)
		}
	})
}
