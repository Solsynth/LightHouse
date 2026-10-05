package oidcctl

// Tests for the device-authorization-grant device binding: the polling client
// declares the device it runs on, and that identity — never the approving
// user's device — labels and scopes the granted OAuth session.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/model"
)

func TestOAuthDeviceKeyNamespacing(t *testing.T) {
	appID := uuid.NewString()

	// A declared id is namespaced, so it can never equal a real device id.
	if got := oauthDeviceKey(appID, "tv-1"); got != "oauth:"+appID+":tv-1" {
		t.Fatalf("oauthDeviceKey = %q", got)
	}
	// Distinct declared ids stay distinct.
	if oauthDeviceKey(appID, "a") == oauthDeviceKey(appID, "b") {
		t.Fatal("distinct declared ids collided")
	}
	// Long ids collapse to a hash but stay within the column limit.
	long := strings.Repeat("x", 4000)
	key := oauthDeviceKey(appID, long)
	if len(key) > maxAuthClientDeviceID {
		t.Fatalf("key length %d exceeds column limit", len(key))
	}
	if key != oauthDeviceKey(appID, long) {
		t.Fatal("hash fallback is not deterministic")
	}
}

func TestParseOAuthDeviceIdentity(t *testing.T) {
	if got := parseOAuthDeviceIdentity("", "", ""); got != nil {
		t.Fatalf("blank device id should yield nil, got %+v", got)
	}
	got := parseOAuthDeviceIdentity("  tv-1  ", "  Living room TV  ", "3")
	if got == nil || got.Id != "tv-1" {
		t.Fatalf("identity = %+v", got)
	}
	if got.Name == nil || *got.Name != "Living room TV" {
		t.Fatalf("name = %v", got.Name)
	}
	if got.Platform != model.ClientPlatformAndroid {
		t.Fatalf("platform = %v", got.Platform)
	}
	// Out-of-range platform is ignored (Unidentified), long name is clamped.
	bad := parseOAuthDeviceIdentity("tv-2", strings.Repeat("n", 5000), "99")
	if bad == nil || bad.Platform != model.ClientPlatformUnidentified {
		t.Fatalf("identity = %+v", bad)
	}
	if bad.Name == nil || len([]rune(*bad.Name)) != maxDeviceNameRunes {
		t.Fatalf("name not clamped: %v", bad.Name)
	}
}

// TestGrantDeviceClientIDIsNamespaced pins that a client-declared device id
// creates its own auth_clients row and never attaches to a real device row that
// happens to carry the same id.
func TestGrantDeviceClientIDIsNamespaced(t *testing.T) {
	ctx := context.Background()
	svc, _, _, pool := newRefreshTestService(t)

	var accountID string
	if err := pool.QueryRow(ctx, `SELECT id FROM accounts ORDER BY created_at LIMIT 1`).Scan(&accountID); err != nil {
		t.Skipf("no local account: %v", err)
	}
	now := time.Now().UTC()

	// A real (wsgateway) device row whose device_id equals the declared id.
	realDevice := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_clients (id, device_id, device_name, account_id, platform, created_at, updated_at)
		VALUES ($1, 'tv-1', 'Real Device', $2, $3, $4, $4)`, realDevice, accountID, int(model.ClientPlatformWeb), now); err != nil {
		t.Fatalf("seed real device: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM auth_clients WHERE id = $1`, realDevice)
		_, _ = pool.Exec(ctx, `DELETE FROM auth_clients WHERE account_id = $1 AND device_id LIKE 'oauth:%'`, accountID)
	})

	appID := uuid.NewString()
	declared, deviceName := "tv-1", "Living room TV"
	declaredInfo := func() *deviceCodeInfo {
		return &deviceCodeInfo{
			ClientId:       appID,
			DeviceId:       &declared,
			DeviceName:     &deviceName,
			DevicePlatform: int(model.ClientPlatformAndroid),
		}
	}

	got, err := svc.grantDeviceClientID(ctx, accountID, declaredInfo())
	if err != nil {
		t.Fatalf("grant device client: %v", err)
	}
	if got == nil {
		t.Fatal("declared device produced no client")
	}
	if *got == realDevice {
		t.Fatal("declared device attached to the real device row")
	}
	var storedKey, storedName string
	var storedPlatform int
	if err := pool.QueryRow(ctx, `SELECT device_id, device_name, platform FROM auth_clients WHERE id = $1`, *got).
		Scan(&storedKey, &storedName, &storedPlatform); err != nil {
		t.Fatalf("load created device: %v", err)
	}
	if storedKey != oauthDeviceKey(appID, declared) {
		t.Fatalf("stored device_id = %q, want %q", storedKey, oauthDeviceKey(appID, declared))
	}
	if storedName != deviceName || storedPlatform != int(model.ClientPlatformAndroid) {
		t.Fatalf("stored device name/platform = %q/%d", storedName, storedPlatform)
	}

	// The same app + declared device reuses the row.
	again, err := svc.grantDeviceClientID(ctx, accountID, declaredInfo())
	if err != nil {
		t.Fatalf("grant device client (again): %v", err)
	}
	if again == nil || *again != *got {
		t.Fatalf("reuse returned %v, want %v", again, got)
	}

	// No declaration => no device.
	none, err := svc.grantDeviceClientID(ctx, accountID, &deviceCodeInfo{ClientId: appID})
	if err != nil {
		t.Fatalf("grant device client (none): %v", err)
	}
	if none != nil {
		t.Fatalf("undeclared device returned %v, want nil", none)
	}
}

// TestDeviceCodeGrantBindsDeclaredDevice runs the full device authorization
// grant and asserts the granted session is bound to the client-declared device.
func TestDeviceCodeGrantBindsDeclaredDevice(t *testing.T) {
	ctx := context.Background()
	svc, _, _, pool := newRefreshTestService(t)

	var accountID string
	if err := pool.QueryRow(ctx, `SELECT id FROM accounts ORDER BY created_at LIMIT 1`).Scan(&accountID); err != nil {
		t.Skipf("no local account: %v", err)
	}

	appID := uuid.NewString()
	svc.cfg.OidcProvider.Clients = append(svc.cfg.OidcProvider.Clients, config.OAuthClient{
		Id: appID, Slug: "tv-app", Name: "TV App", Status: 2,
		AllowedScopes: []string{"openid"}, IsPublicClient: true,
	})
	declared, deviceName := "living-room-tv", "Living room TV"
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM auth_sessions WHERE account_id = $1 AND app_id = $2`, accountID, appID)
		_, _ = pool.Exec(ctx, `DELETE FROM auth_clients WHERE account_id = $1 AND device_id = $2`, accountID, oauthDeviceKey(appID, declared))
	})

	info, err := svc.generateDeviceCode(ctx, appID, []string{"openid"}, nil,
		&oauthDeviceIdentity{Id: declared, Name: &deviceName, Platform: model.ClientPlatformAndroid})
	if err != nil {
		t.Fatalf("generate device code: %v", err)
	}
	info.AccountId = &accountID
	info.Status = deviceCodeStatusApproved
	if err := svc.updateDeviceCode(ctx, info); err != nil {
		t.Fatalf("approve device code: %v", err)
	}

	resp, err := svc.handleDeviceCodeGrant(ctx, info.DeviceCode, appID, "", "tv-user-agent")
	if err != nil {
		t.Fatalf("device code grant: %v", err)
	}
	if resp.AccessToken == nil || *resp.AccessToken == "" {
		t.Fatal("device code grant issued no access token")
	}

	var clientID *string
	if err := pool.QueryRow(ctx,
		`SELECT client_id FROM auth_sessions WHERE account_id = $1 AND app_id = $2 AND type = $3 ORDER BY created_at DESC LIMIT 1`,
		accountID, appID, int(model.SessionTypeOAuth)).Scan(&clientID); err != nil {
		t.Fatalf("load granted session: %v", err)
	}
	if clientID == nil {
		t.Fatal("granted session has no device")
	}
	var key string
	if err := pool.QueryRow(ctx, `SELECT device_id FROM auth_clients WHERE id = $1`, *clientID).Scan(&key); err != nil {
		t.Fatalf("load session device: %v", err)
	}
	if key != oauthDeviceKey(appID, declared) {
		t.Fatalf("session device_id = %q, want %q", key, oauthDeviceKey(appID, declared))
	}
	var storedName string
	var storedPlatform int
	if err := pool.QueryRow(ctx, `SELECT device_name, platform FROM auth_clients WHERE id = $1`, *clientID).
		Scan(&storedName, &storedPlatform); err != nil {
		t.Fatalf("load session device fields: %v", err)
	}
	if storedName != deviceName || storedPlatform != int(model.ClientPlatformAndroid) {
		t.Fatalf("device name/platform = %q/%d", storedName, storedPlatform)
	}
}

// TestFallbackDeviceFromUserAgentAndIP pins the last-resort device: it is
// derived deterministically from the request's user agent and IP, namespaced
// like a declared device, and absent when there is nothing to fingerprint.
func TestFallbackDeviceFromUserAgentAndIP(t *testing.T) {
	ctx := context.Background()
	svc, _, _, pool := newRefreshTestService(t)

	var accountID string
	if err := pool.QueryRow(ctx, `SELECT id FROM accounts ORDER BY created_at LIMIT 1`).Scan(&accountID); err != nil {
		t.Skipf("no local account: %v", err)
	}
	appID := uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM auth_clients WHERE account_id = $1 AND device_id LIKE 'oauth:' || $2 || ':fp:%'`, accountID, appID)
	})

	const ua = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)"
	first, err := svc.fallbackDeviceClientID(ctx, accountID, appID, "203.0.113.7", ua)
	if err != nil {
		t.Fatalf("fallback device: %v", err)
	}
	if first == nil {
		t.Fatal("fallback produced no device")
	}
	var key, name string
	var platform int
	if err := pool.QueryRow(ctx, `SELECT device_id, device_name, platform FROM auth_clients WHERE id = $1`, *first).
		Scan(&key, &name, &platform); err != nil {
		t.Fatalf("load fallback device: %v", err)
	}
	if want := oauthDeviceKey(appID, "fp:"+oauthDeviceFingerprint(ua, "203.0.113.7")); key != want {
		t.Fatalf("device_id = %q, want %q", key, want)
	}
	if name != "iOS device" || platform != int(model.ClientPlatformIos) {
		t.Fatalf("device name/platform = %q/%d", name, platform)
	}

	// Deterministic: same user agent + IP reuse the same device row.
	again, err := svc.fallbackDeviceClientID(ctx, accountID, appID, "203.0.113.7", ua)
	if err != nil {
		t.Fatalf("fallback device (again): %v", err)
	}
	if again == nil || *again != *first {
		t.Fatalf("reuse returned %v, want %v", again, first)
	}

	// A different IP is a different device.
	other, err := svc.fallbackDeviceClientID(ctx, accountID, appID, "203.0.113.8", ua)
	if err != nil {
		t.Fatalf("fallback device (other ip): %v", err)
	}
	if other == nil || *other == *first {
		t.Fatalf("different IP reused device %v", first)
	}

	// Nothing to fingerprint => no device.
	if none, err := svc.fallbackDeviceClientID(ctx, accountID, appID, "", ""); err != nil || none != nil {
		t.Fatalf("empty request returned %v / %v, want nil", none, err)
	}
}

// TestDeviceCodeGrantFallsBackToFingerprintDevice runs the device grant with
// no declared device and asserts the session is bound to the user-agent/IP
// fingerprint device.
func TestDeviceCodeGrantFallsBackToFingerprintDevice(t *testing.T) {
	ctx := context.Background()
	svc, _, _, pool := newRefreshTestService(t)

	var accountID string
	if err := pool.QueryRow(ctx, `SELECT id FROM accounts ORDER BY created_at LIMIT 1`).Scan(&accountID); err != nil {
		t.Skipf("no local account: %v", err)
	}

	appID := uuid.NewString()
	svc.cfg.OidcProvider.Clients = append(svc.cfg.OidcProvider.Clients, config.OAuthClient{
		Id: appID, Slug: "tv-app", Name: "TV App", Status: 2,
		AllowedScopes: []string{"openid"}, IsPublicClient: true,
	})
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM auth_sessions WHERE account_id = $1 AND app_id = $2`, accountID, appID)
		_, _ = pool.Exec(ctx, `DELETE FROM auth_clients WHERE account_id = $1 AND device_id LIKE 'oauth:' || $2 || ':fp:%'`, accountID, appID)
	})

	info, err := svc.generateDeviceCode(ctx, appID, []string{"openid"}, nil, nil)
	if err != nil {
		t.Fatalf("generate device code: %v", err)
	}
	info.AccountId = &accountID
	info.Status = deviceCodeStatusApproved
	if err := svc.updateDeviceCode(ctx, info); err != nil {
		t.Fatalf("approve device code: %v", err)
	}

	const ua = "Mozilla/5.0 (Linux; Android 14; SmartTV) AppleWebKit/537.36"
	const ip = "198.51.100.9"
	if _, err := svc.handleDeviceCodeGrant(ctx, info.DeviceCode, appID, ip, ua); err != nil {
		t.Fatalf("device code grant: %v", err)
	}

	var clientID *string
	if err := pool.QueryRow(ctx,
		`SELECT client_id FROM auth_sessions WHERE account_id = $1 AND app_id = $2 AND type = $3 ORDER BY created_at DESC LIMIT 1`,
		accountID, appID, int(model.SessionTypeOAuth)).Scan(&clientID); err != nil {
		t.Fatalf("load granted session: %v", err)
	}
	if clientID == nil {
		t.Fatal("granted session has no device")
	}
	var key string
	var platform int
	if err := pool.QueryRow(ctx, `SELECT device_id, platform FROM auth_clients WHERE id = $1`, *clientID).
		Scan(&key, &platform); err != nil {
		t.Fatalf("load session device: %v", err)
	}
	if want := oauthDeviceKey(appID, "fp:"+oauthDeviceFingerprint(ua, ip)); key != want {
		t.Fatalf("session device_id = %q, want %q", key, want)
	}
	if platform != int(model.ClientPlatformAndroid) {
		t.Fatalf("platform = %d, want Android", platform)
	}
}
