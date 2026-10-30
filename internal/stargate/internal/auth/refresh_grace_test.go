package auth

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/geo"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// TestRefreshRotationGrace pins the rotation grace window: after a refresh
// rotation, a duplicate presentation of the immediately previous refresh token
// (the caller never received the replacement — dropped/timed-out response, or a
// concurrent duplicate) must still yield a working pair inside the window, and
// must be rejected outside it or when further than one rotation behind.
func TestRefreshRotationGrace(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), apiKeysDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	ctx := context.Background()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file path")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	priv := filepath.Join(root, "Keys", "PrivateKey.pem")
	pub := filepath.Join(root, "Keys", "PublicKey.pem")
	if _, err := os.Stat(priv); err != nil {
		t.Skipf("dev signing keys missing: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	newService := func(grace string) *AuthService {
		t.Helper()
		cfg := &config.Config{}
		cfg.Auth.Issuer = "solar-network"
		cfg.Auth.Audiences = []string{"solar-network"}
		cfg.Auth.PublicKeyPath = pub
		cfg.Auth.PrivateKeyPath = priv
		cfg.Auth.AccessTokenLifetime = "5m"
		cfg.Auth.RefreshTokenLifetime = "720h"
		cfg.Auth.RefreshGracePeriod = grace
		jwtSvc, err := NewJWTService(cfg)
		if err != nil {
			t.Fatalf("jwt service: %v", err)
		}
		st := store.New(pool)
		tokenSvc := NewTokenAuthService(st, nil, jwtSvc, nil, nil, log)
		return NewAuthService(st, nil, cfg, geo.NewService(""), jwtSvc, tokenSvc, nil, nil, log)
	}

	seedSession := func(t *testing.T) (string, *AuthService) {
		t.Helper()
		svc := newService("60s")
		accountID := uuid.NewString()
		now := time.Now().UTC()
		if _, err := pool.Exec(ctx, `INSERT INTO accounts
			(id, name, nick, language, region, is_superuser, created_at, updated_at)
			VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`,
			accountID, "grace_"+uuid.NewString()[:8], now); err != nil {
			t.Fatalf("seed account: %v", err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM auth_sessions WHERE account_id = $1`, accountID)
			_, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = $1`, accountID)
		})
		sessionID := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions
			(id, account_id, audiences, scopes, epoch, type, expired_at, created_at, updated_at)
			VALUES ($1, $2, '[]', '["*"]', 0, 0, $3, $4, $4)`,
			sessionID, accountID, now.Add(720*time.Hour), now); err != nil {
			t.Fatalf("seed session: %v", err)
		}
		return sessionID, svc
	}

	refresh := func(t *testing.T, svc *AuthService, token string) (*TokenPair, int) {
		t.Helper()
		pair, session, err := svc.RefreshSessionAndIssueTokens(ctx, token)
		if err != nil {
			t.Fatalf("refresh failed: %v", err)
		}
		return pair, session.Epoch
	}

	t.Run("duplicate of the previous rotation is served inside the window", func(t *testing.T) {
		sessionID, svc := seedSession(t)

		pair1, err := svc.CreateTokenPair(ctx, mustSession(t, svc, sessionID))
		if err != nil {
			t.Fatalf("pair1: %v", err)
		}
		_, epoch := refresh(t, svc, pair1.RefreshToken)
		if epoch != 1 {
			t.Fatalf("first rotation epoch = %d, want 1", epoch)
		}

		// The caller lost the response to the first rotation and retries with
		// the same (now previous) token.
		replayed, epoch := refresh(t, svc, pair1.RefreshToken)
		if epoch != 1 {
			t.Fatalf("grace replay bumped epoch to %d, want 1 (no further rotation)", epoch)
		}
		ok, _, msg, _ := svc.token.AuthenticateToken(ctx, replayed.AccessToken, "")
		if !ok {
			t.Fatalf("grace pair access token rejected: %q", msg)
		}
		var storedEpoch int
		if err := pool.QueryRow(ctx, `SELECT epoch FROM auth_sessions WHERE id = $1`, sessionID).Scan(&storedEpoch); err != nil {
			t.Fatalf("read epoch: %v", err)
		}
		if storedEpoch != 1 {
			t.Fatalf("stored epoch = %d, want 1", storedEpoch)
		}
	})

	t.Run("duplicate outside the window is rejected", func(t *testing.T) {
		sessionID, svc := seedSession(t)

		pair1, err := svc.CreateTokenPair(ctx, mustSession(t, svc, sessionID))
		if err != nil {
			t.Fatalf("pair1: %v", err)
		}
		refresh(t, svc, pair1.RefreshToken)

		if _, err := pool.Exec(ctx, `UPDATE auth_sessions SET refreshed_at = now() - interval '5 minutes' WHERE id = $1`, sessionID); err != nil {
			t.Fatalf("age rotation: %v", err)
		}
		_, _, err = svc.RefreshSessionAndIssueTokens(ctx, pair1.RefreshToken)
		if err == nil || !strings.Contains(err.Error(), "revoked") {
			t.Fatalf("replay outside window: err = %v, want revoked", err)
		}
	})

	t.Run("more than one rotation behind is rejected", func(t *testing.T) {
		sessionID, svc := seedSession(t)

		pair1, err := svc.CreateTokenPair(ctx, mustSession(t, svc, sessionID))
		if err != nil {
			t.Fatalf("pair1: %v", err)
		}
		pair2, _ := refresh(t, svc, pair1.RefreshToken)
		refresh(t, svc, pair2.RefreshToken) // epoch 2

		_, _, err = svc.RefreshSessionAndIssueTokens(ctx, pair1.RefreshToken)
		if err == nil || !strings.Contains(err.Error(), "revoked") {
			t.Fatalf("two-rotations-behind replay: err = %v, want revoked", err)
		}
	})

	t.Run("a zero window disables the grace", func(t *testing.T) {
		sessionID, svc := seedSession(t)
		svc.cfg.Auth.RefreshGracePeriod = "0s"

		pair1, err := svc.CreateTokenPair(ctx, mustSession(t, svc, sessionID))
		if err != nil {
			t.Fatalf("pair1: %v", err)
		}
		refresh(t, svc, pair1.RefreshToken)
		_, _, err = svc.RefreshSessionAndIssueTokens(ctx, pair1.RefreshToken)
		if err == nil || !strings.Contains(err.Error(), "revoked") {
			t.Fatalf("disabled grace replay: err = %v, want revoked", err)
		}
	})

	t.Run("a revoked session still rejects the previous token", func(t *testing.T) {
		sessionID, svc := seedSession(t)

		pair1, err := svc.CreateTokenPair(ctx, mustSession(t, svc, sessionID))
		if err != nil {
			t.Fatalf("pair1: %v", err)
		}
		refresh(t, svc, pair1.RefreshToken)
		if _, err := pool.Exec(ctx, `UPDATE auth_sessions SET expired_at = now() WHERE id = $1`, sessionID); err != nil {
			t.Fatalf("revoke session: %v", err)
		}
		_, _, err = svc.RefreshSessionAndIssueTokens(ctx, pair1.RefreshToken)
		if err == nil || !strings.Contains(err.Error(), "expired") {
			t.Fatalf("revoked session: err = %v, want expired", err)
		}
	})
}

func mustSession(t *testing.T, svc *AuthService, sessionID string) *model.AuthSession {
	t.Helper()
	session, err := svc.store.GetSessionWithAccount(context.Background(), uuid.MustParse(sessionID))
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	return session
}
