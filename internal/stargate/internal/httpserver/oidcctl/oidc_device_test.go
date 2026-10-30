package oidcctl

// End-to-end regression test for binding OAuth sessions to the authorizing
// device: the authorization_code grant must persist the device on the session
// and scope reuse to it, so authorizing the same app from another device
// creates a separate session instead of extending the first device's.

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

func TestAuthorizationCodeFlowIsDeviceScoped(t *testing.T) {
	ctx := context.Background()
	svc, _, _, pool := newRefreshTestService(t)

	var accountID string
	if err := pool.QueryRow(ctx, `SELECT id FROM accounts ORDER BY created_at LIMIT 1`).Scan(&accountID); err != nil {
		t.Skipf("no local account to attach the session: %v", err)
	}

	now := time.Now().UTC()
	deviceA := uuid.NewString()
	deviceB := uuid.NewString()
	for _, d := range []string{deviceA, deviceB} {
		if _, err := pool.Exec(ctx, `INSERT INTO auth_clients (id, device_id, device_name, account_id, platform, created_at, updated_at)
			VALUES ($1, $2, 'device', $3, $4, $5, $5)`, d, "dev-"+d[:8], accountID, int(model.ClientPlatformIos), now); err != nil {
			t.Fatalf("seed device: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM auth_sessions WHERE account_id = $1 AND client_id IN ($2, $3)`, accountID, deviceA, deviceB)
		_, _ = pool.Exec(ctx, `DELETE FROM auth_clients WHERE account_id = $1 AND id IN ($2, $3)`, accountID, deviceA, deviceB)
	})

	appID := uuid.NewString()
	scopes := []string{"openid"}
	authorize := func(device *string) string {
		session, _, gotScopes, err := svc.handleAuthorizationCodeFlow(ctx, &authorizationCodeInfo{
			ClientId:  appID,
			AccountId: &accountID,
			Scopes:    scopes,
			DeviceId:  device,
		}, appID, "", "")
		if err != nil {
			t.Fatalf("handle authorization code flow: %v", err)
		}
		if len(gotScopes) != 1 {
			t.Fatalf("scopes = %v, want [openid]", gotScopes)
		}
		return session.Id
	}

	// First authorization from device A creates a session bound to A.
	firstID := authorize(&deviceA)
	assertSessionClient(t, pool, firstID, deviceA)

	// Re-authorizing from the same device reuses that session.
	if againID := authorize(&deviceA); againID != firstID {
		t.Fatalf("re-authorizing from same device created a new session %s, want reuse of %s", againID, firstID)
	}

	// Authorizing from another device must create its own session.
	secondID := authorize(&deviceB)
	if secondID == firstID {
		t.Fatalf("authorizing from another device reused session %s, want a new one", firstID)
	}
	assertSessionClient(t, pool, secondID, deviceB)
}

func assertSessionClient(t *testing.T, pool *pgxpool.Pool, sessionID, want string) {
	t.Helper()
	var got *string
	if err := pool.QueryRow(context.Background(), `SELECT client_id FROM auth_sessions WHERE id = $1`, sessionID).Scan(&got); err != nil {
		t.Fatalf("load session client: %v", err)
	}
	if got == nil || *got != want {
		t.Fatalf("session client_id = %v, want %s", got, want)
	}
}
