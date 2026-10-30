package store

// Regression tests for binding OAuth/OIDC sessions to a device.
//
// An OAuth app can be authorized from another device, so session reuse must be
// scoped to the authorizing client (auth_clients.id): authorizing app X from
// device B must not extend device A's session. Binding a device also makes
// OAuth sessions eligible for the trust predicate, which unlocks interactive
// challenge approval and QR scanning — those must stay excluded.
//
// Mirrors the trusted_test.go convention: skip when Postgres is unavailable.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

const oidcDeviceDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

func oidcDeviceStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), oidcDeviceDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	return New(pool), pool
}

func seedAccount(t *testing.T, pool *pgxpool.Pool, name string, now time.Time) string {
	t.Helper()
	accountID := uuid.NewString()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		 VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, name, now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return accountID
}

func seedDevice(t *testing.T, pool *pgxpool.Pool, accountID, deviceID string, platform model.ClientPlatform, now time.Time) string {
	t.Helper()
	clientID := uuid.NewString()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO auth_clients (id, device_id, device_name, account_id, platform, created_at, updated_at)
		 VALUES ($1, $2, $2, $3, $4, $5, $5)`, clientID, deviceID, accountID, int(platform), now); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	return clientID
}

func seedSession(t *testing.T, pool *pgxpool.Pool, accountID, appID string, clientID *string, sessionType model.SessionType, now time.Time) string {
	t.Helper()
	sessionID := uuid.NewString()
	var client any
	if clientID != nil {
		client = *clientID
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO auth_sessions
		   (id, account_id, app_id, audiences, client_id, created_at, epoch, last_granted_at, scopes, type, updated_at)
		 VALUES ($1, $2, $3, '[]'::jsonb, $4, $5, 0, $5, '[]'::jsonb, $6, $5)`,
		sessionID, accountID, appID, client, now, int(sessionType)); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return sessionID
}

func cleanupAccount(t *testing.T, pool *pgxpool.Pool, accountID string) {
	t.Helper()
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM auth_sessions WHERE account_id = $1`, accountID)
		_, _ = pool.Exec(ctx, `DELETE FROM auth_clients WHERE account_id = $1`, accountID)
		_, _ = pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, accountID)
	})
}

// TestFindValidOauthSessionIsDeviceScoped pins that an OAuth session created
// from one device is only reused for that device (or for device-less legacy
// flows when client_id is NULL), never for a different device.
func TestFindValidOauthSessionIsDeviceScoped(t *testing.T) {
	st, pool := oidcDeviceStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	accountID := seedAccount(t, pool, "oauth_device_test", now)
	cleanupAccount(t, pool, accountID)

	deviceA := seedDevice(t, pool, accountID, "device-a", model.ClientPlatformIos, now)
	deviceB := seedDevice(t, pool, accountID, "device-b", model.ClientPlatformIos, now)

	appID := uuid.NewString()
	sessionID := seedSession(t, pool, accountID, appID, &deviceA, model.SessionTypeOAuth, now)

	if got, err := st.FindValidOauthSession(ctx, accountID, appID, &deviceA); err != nil {
		t.Fatalf("lookup for authorizing device: %v", err)
	} else if got.Id != sessionID {
		t.Fatalf("lookup for authorizing device returned %q, want %q", got.Id, sessionID)
	}

	if _, err := st.FindValidOauthSession(ctx, accountID, appID, &deviceB); !errors.Is(err, ErrNotFound) {
		t.Fatalf("lookup from another device: got err %v, want ErrNotFound (must not extend)", err)
	}
	if _, err := st.FindValidOauthSession(ctx, accountID, appID, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("device-less lookup: got err %v, want ErrNotFound", err)
	}

	// A legacy device-less session is only reusable by device-less flows.
	appID2 := uuid.NewString()
	legacyID := seedSession(t, pool, accountID, appID2, nil, model.SessionTypeOAuth, now)
	if got, err := st.FindValidOauthSession(ctx, accountID, appID2, nil); err != nil {
		t.Fatalf("device-less lookup: %v", err)
	} else if got.Id != legacyID {
		t.Fatalf("device-less lookup returned %q, want %q", got.Id, legacyID)
	}
	if _, err := st.FindValidOauthSession(ctx, accountID, appID2, &deviceA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("device lookup against device-less session: got err %v, want ErrNotFound", err)
	}
}

// TestIsTrustedSessionExcludesOAuth pins that binding a device to OAuth/OIDC
// sessions does not let them pass the trust predicate (which gates challenge
// approval and QR scanning), while a login session on the same device still
// qualifies.
func TestIsTrustedSessionExcludesOAuth(t *testing.T) {
	st, pool := oidcDeviceStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	gap := 720 * time.Hour
	accountID := seedAccount(t, pool, "oauth_trust_test", now)
	cleanupAccount(t, pool, accountID)

	nativeClient := seedDevice(t, pool, accountID, "trusted-native", model.ClientPlatformIos, now)
	appID := uuid.NewString()

	for _, tc := range []struct {
		name        string
		sessionType model.SessionType
		want        bool
	}{
		{"oauth session is never trusted", model.SessionTypeOAuth, false},
		{"oidc session is never trusted", model.SessionTypeOidc, false},
		{"login session on the same device is trusted", model.SessionTypeLogin, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := &model.AuthSession{
				AccountId:     accountID,
				AppId:         &appID,
				ClientId:      &nativeClient,
				LastGrantedAt: model.NewTime(now),
				Type:          tc.sessionType,
			}
			trusted, err := st.IsTrustedSession(ctx, session, gap)
			if err != nil {
				t.Fatalf("IsTrustedSession: %v", err)
			}
			if trusted != tc.want {
				t.Fatalf("IsTrustedSession = %v, want %v", trusted, tc.want)
			}
		})
	}
}
