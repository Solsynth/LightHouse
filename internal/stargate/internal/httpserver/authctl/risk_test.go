package authctl

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// smokeDSN mirrors config.example.toml (same convention as e2eectl and
// spell's smoke tests).
const smokeDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

// seedRiskAccount inserts an account plus the given enabled factor types and
// returns the account id.
func seedRiskAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, factorTypes ...model.AuthFactorType) string {
	t.Helper()
	accountID := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, "risk_"+uuid.NewString()[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	for _, ft := range factorTypes {
		if _, err := pool.Exec(ctx, `INSERT INTO account_auth_factors (id, account_id, type, secret, config, trustworthy, enabled_at, created_at, updated_at)
			VALUES ($1, $2, $3, '', '{}', 1, $4, $4, $4)`,
			uuid.NewString(), accountID, int(ft), now); err != nil {
			t.Fatalf("seed factor type %d: %v", ft, err)
		}
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, accountID) })
	return accountID
}

// TestDetectChallengeRiskSkipsUncompletableFactors pins the step-count
// contract: Passkey (7) and InAppCode (2) factors can never satisfy a step of
// the username-challenge flow (the client picker offers passkeys only via the
// separate discoverable flow, and in-app approval is offered for every
// challenge instead of as a pickable factor), so they must not inflate
// StepTotal — a password+passkey account on a fresh device requires exactly one
// step instead of stranding the login at an empty factor picker. NfcToken (6)
// does count: its verification runs through Passport's DyNfcService.
func TestDetectChallengeRiskSkipsUncompletableFactors(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), smokeDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}

	h := &handler{d: Deps{Store: store.New(pool)}}

	// Fresh IP/UA/device signals, so the risk score is high enough that every
	// counted factor is demanded.
	freshIP := "203.0.113." + uuid.NewString()[:2]
	freshUA := "RiskTestUA-" + uuid.NewString()[:8]

	t.Run("password plus passkey requires only the password step", func(t *testing.T) {
		accountID := seedRiskAccount(t, ctx, pool, model.AuthFactorTypePassword, model.AuthFactorTypePasskey)
		steps, err := h.detectChallengeRisk(ctx, accountID, freshIP, freshUA, model.SecurityModeDefault)
		if err != nil {
			t.Fatalf("detectChallengeRisk: %v", err)
		}
		if steps != 1 {
			t.Fatalf("password+passkey steps = %d, want 1 (passkey cannot satisfy a challenge step)", steps)
		}
	})

	t.Run("password plus nfc token demands both steps", func(t *testing.T) {
		accountID := seedRiskAccount(t, ctx, pool, model.AuthFactorTypePassword, model.AuthFactorTypeNfcToken)
		steps, err := h.detectChallengeRisk(ctx, accountID, freshIP, freshUA, model.SecurityModeDefault)
		if err != nil {
			t.Fatalf("detectChallengeRisk: %v", err)
		}
		if steps != 2 {
			t.Fatalf("password+nfc steps = %d, want 2 (the tag is verifiable via Passport)", steps)
		}
	})

	t.Run("password plus in-app code requires only the password step", func(t *testing.T) {
		accountID := seedRiskAccount(t, ctx, pool, model.AuthFactorTypePassword, model.AuthFactorTypeInAppCode)
		steps, err := h.detectChallengeRisk(ctx, accountID, freshIP, freshUA, model.SecurityModeDefault)
		if err != nil {
			t.Fatalf("detectChallengeRisk: %v", err)
		}
		if steps != 1 {
			t.Fatalf("password+in-app-code steps = %d, want 1 (in-app is no longer selectable)", steps)
		}
	})

	t.Run("in-app code only still yields a challenge for cross-device approval", func(t *testing.T) {
		accountID := seedRiskAccount(t, ctx, pool, model.AuthFactorTypeInAppCode)
		steps, err := h.detectChallengeRisk(ctx, accountID, freshIP, freshUA, model.SecurityModeDefault)
		if err != nil {
			t.Fatalf("detectChallengeRisk: %v", err)
		}
		if steps != 1 {
			t.Fatalf("in-app-only steps = %d, want 1 (approvable from a trusted device)", steps)
		}
	})

	t.Run("lockdown forces maxSteps even on fresh IP", func(t *testing.T) {
		accountID := seedRiskAccount(t, ctx, pool, model.AuthFactorTypePassword, model.AuthFactorTypeEmailCode)
		steps, err := h.detectChallengeRisk(ctx, accountID, freshIP, freshUA, model.SecurityModeLockdown)
		if err != nil {
			t.Fatalf("detectChallengeRisk: %v", err)
		}
		if steps != 2 {
			t.Fatalf("lockdown steps = %d, want 2 (maxSteps)", steps)
		}
	})

	t.Run("lockoff forces 1 step even on fresh IP", func(t *testing.T) {
		accountID := seedRiskAccount(t, ctx, pool, model.AuthFactorTypePassword, model.AuthFactorTypeEmailCode)
		steps, err := h.detectChallengeRisk(ctx, accountID, freshIP, freshUA, model.SecurityModeLockoff)
		if err != nil {
			t.Fatalf("detectChallengeRisk: %v", err)
		}
		if steps != 1 {
			t.Fatalf("lockoff steps = %d, want 1", steps)
		}
	})
}

// seedCompletedLogin inserts what a finished login leaves behind: a challenge
// holding the ip/user-agent/failure count plus the session it minted, granted
// grantedAgo before now. clientBound mirrors the device binding a
// username-challenge login always writes (auth_clients row + client_id).
func seedCompletedLogin(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID, ip, ua string, grantedAgo time.Duration, failedAttempts int) {
	t.Helper()
	granted := time.Now().UTC().Add(-grantedAgo)
	challengeID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_challenges (id, account_id, step_total, step_remain, device_id, platform, ip_address, user_agent, blacklist_factors, failed_attempts, scopes, audiences, created_at, updated_at, expired_at)
		VALUES ($1,$2,1,0,'grace-device',0,$3,$4,'[]',$5,'[]','[]',$6,$6,$6)`,
		challengeID, accountID, ip, ua, failedAttempts, granted); err != nil {
		t.Fatalf("seed challenge: %v", err)
	}
	clientID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_clients (id, account_id, device_id, device_name, platform, created_at, updated_at)
		VALUES ($1,$2,'grace-device','grace device',0,$3,$3)`, clientID, accountID, granted); err != nil {
		t.Fatalf("seed client: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions (id, account_id, type, epoch, challenge_id, client_id, ip_address, user_agent, last_granted_at, expired_at, refreshed_at, scopes, audiences, created_at, updated_at)
		VALUES ($1,$2,0,0,$3,$4,$5,$6,$7,$8,$7,'[]','[]',$7,$7)`,
		uuid.New(), accountID, challengeID, clientID, ip, ua, granted, granted.Add(720*time.Hour)); err != nil {
		t.Fatalf("seed session: %v", err)
	}
}

// TestDetectChallengeRiskRecentLoginGrace pins the roaming-client contract: a
// login completed inside [security] recentLoginGrace from the same user agent
// cancels the ip-novelty terms, so a rotated IP (mobile NAT, IPv6 privacy
// addresses, Wi-Fi/cellular switch) cannot demand MFA again seconds after a
// successful login. The grace is anchored on the user agent — an
// unrecognised client, a blank stored user agent, a login older than the
// grace, or renewed failures all keep the escalated step count.
func TestDetectChallengeRiskRecentLoginGrace(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), smokeDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}

	const (
		lastIP   = "203.0.113.7"
		lastUA   = "Solian/1.9.2 (macOS)"
		roamedIP = "198.51.100.9"
		otherUA  = "Solian/1.9.3 (macOS)"
	)
	// password + email code + NFC token: three completable factors, so the
	// escalated count is 2 and a single step is unambiguous.
	seedAccount := func(t *testing.T) string {
		t.Helper()
		return seedRiskAccount(t, ctx, pool,
			model.AuthFactorTypePassword, model.AuthFactorTypeEmailCode, model.AuthFactorTypeNfcToken)
	}
	handlerWithGrace := func(grace string) *handler {
		return &handler{d: Deps{
			Store: store.New(pool),
			Cfg:   &config.Config{Security: config.SecurityConfig{RecentLoginGrace: grace}},
		}}
	}

	cases := []struct {
		name           string
		grace          string
		storedIP       string
		storedUA       string
		grantedAgo     time.Duration
		failedAttempts int
		requestIP      string
		requestUA      string
		want           int
	}{
		{
			name:     "rotated IP from the same client inside the grace stays at one step",
			grace:    "30m",
			storedIP: lastIP, storedUA: lastUA,
			requestIP: roamedIP, requestUA: lastUA,
			want: 1,
		},
		{
			name:     "unrecognised client still escalates on a rotated IP",
			grace:    "30m",
			storedIP: lastIP, storedUA: lastUA,
			requestIP: roamedIP, requestUA: otherUA,
			want: 2,
		},
		{
			name:     "blank stored user agent never earns the grace",
			grace:    "30m",
			storedIP: lastIP, storedUA: "",
			requestIP: roamedIP, requestUA: otherUA,
			want: 2,
		},
		{
			name:     "login outside the grace escalates again",
			grace:    "30m",
			storedIP: lastIP, storedUA: lastUA,
			grantedAgo: 45 * time.Minute,
			requestIP:  roamedIP, requestUA: lastUA,
			want: 2,
		},
		{
			name:     "grace disabled by config escalates on a rotated IP",
			grace:    "0",
			storedIP: lastIP, storedUA: lastUA,
			requestIP: roamedIP, requestUA: lastUA,
			want: 2,
		},
		{
			name:     "failures inside the grace still escalate",
			grace:    "30m",
			storedIP: lastIP, storedUA: lastUA,
			failedAttempts: 4,
			requestIP:      roamedIP, requestUA: lastUA,
			want: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			accountID := seedAccount(t)
			seedCompletedLogin(t, ctx, pool, accountID, tc.storedIP, tc.storedUA, tc.grantedAgo, tc.failedAttempts)
			steps, err := handlerWithGrace(tc.grace).detectChallengeRisk(ctx, accountID, tc.requestIP, tc.requestUA, model.SecurityModeDefault)
			if err != nil {
				t.Fatalf("detectChallengeRisk: %v", err)
			}
			if steps != tc.want {
				t.Fatalf("steps = %d, want %d", steps, tc.want)
			}
		})
	}
}
