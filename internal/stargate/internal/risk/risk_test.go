package risk

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"

	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/redis"
)

func redisClient(t *testing.T) *redis.Client {
	t.Helper()
	rc, err := redis.Connect(context.Background(), "localhost:6379", "", 0)
	if err != nil {
		t.Skipf("redis unavailable: %v", err)
	}
	t.Cleanup(func() { _ = rc.Raw.Close() })
	return rc
}

func TestIPAllowedNilRedis(t *testing.T) {
	// Nil Redis → always allowed (degraded mode).
	if !IPAllowed(context.Background(), nil, config.Default(), "192.168.1.1") {
		t.Fatal("nil Redis should degrade to allowed")
	}
}

func TestIPAllowedEmptyIP(t *testing.T) {
	// Empty IP → always allowed.
	if !IPAllowed(context.Background(), nil, config.Default(), "") {
		t.Fatal("empty IP should be allowed")
	}
}

func TestFail2banLifecycle(t *testing.T) {
	rc := redisClient(t)
	cfg := config.Default()
	cfg.Security.Fail2banMaxFails = 3
	cfg.Security.Fail2banWindow = "1m"
	cfg.Security.Fail2banBlockFor = "1m"
	ip := "test:fail2ban:" + t.Name()

	ctx := context.Background()
	ClearFailures(ctx, rc, ip) // clean slate

	// Initially allowed.
	if !IPAllowed(ctx, rc, cfg, ip) {
		t.Fatal("should be allowed before any failures")
	}

	// Record failures up to the limit.
	for range cfg.Security.Fail2banMaxFails {
		RecordFailure(ctx, rc, cfg, ip)
	}

	// Still allowed (at limit, not exceeded).
	if !IPAllowed(ctx, rc, cfg, ip) {
		t.Fatal("should be allowed at exact limit")
	}

	// One more pushes over the limit.
	RecordFailure(ctx, rc, cfg, ip)

	// Now blocked.
	if IPAllowed(ctx, rc, cfg, ip) {
		t.Fatal("should be blocked after exceeding max fails")
	}

	// Clear → allowed again.
	ClearFailures(ctx, rc, ip)
	if !IPAllowed(ctx, rc, cfg, ip) {
		t.Fatal("should be allowed after clearing failures")
	}
}

// TestFail2banDeclineLifecycle pins the declined-challenge fail2ban: declines
// are charged per requester IP, the block lands only once the count exceeds
// Fail2banDeclineMax, and a later successful login (ClearFailures) does not
// lift it — an IP must not clear its decline record by logging into its own
// account.
func TestFail2banDeclineLifecycle(t *testing.T) {
	rc := redisClient(t)
	cfg := config.Default()
	cfg.Security.Fail2banDeclineMax = 2
	cfg.Security.Fail2banWindow = "1m"
	cfg.Security.Fail2banBlockFor = "1m"
	ip := "test:decline:" + t.Name()
	other := "test:decline-other:" + t.Name()

	ctx := context.Background()
	t.Cleanup(func() {
		_ = rc.Raw.Del(ctx,
			fail2banDeclineCounterPrefix+ip, fail2banDeclineBlockPrefix+ip,
			fail2banDeclineCounterPrefix+other, fail2banDeclineBlockPrefix+other).Err()
	})

	// At the exact limit the requester is still allowed.
	for range cfg.Security.Fail2banDeclineMax {
		RecordDecline(ctx, rc, cfg, ip)
	}
	if !IPAllowed(ctx, rc, cfg, ip) {
		t.Fatal("should be allowed at the exact decline limit")
	}

	// One more decline blocks the requesting IP.
	RecordDecline(ctx, rc, cfg, ip)
	if IPAllowed(ctx, rc, cfg, ip) {
		t.Fatal("should be blocked after exceeding the decline limit")
	}

	// A successful login must not clear the decline block.
	ClearFailures(ctx, rc, ip)
	if IPAllowed(ctx, rc, cfg, ip) {
		t.Fatal("successful login must not lift a decline block")
	}

	// Declines are per IP, and "0" disables the tracking entirely.
	if !IPAllowed(ctx, rc, cfg, other) {
		t.Fatal("declines must be charged to the requesting IP only")
	}
	disabled := config.Default()
	disabled.Security.Fail2banDeclineMax = 0
	for range cfg.Security.Fail2banDeclineMax + 2 {
		RecordDecline(ctx, rc, disabled, other)
	}
	if !IPAllowed(ctx, rc, disabled, other) {
		t.Fatal("a disabled decline limit must never block")
	}
}

// TestChallengeQuotaLifecycle pins the per-IP new-challenge quota: an IP may
// create up to MaxChallengesPerIp challenges within ChallengeWindow, one more
// is refused, and releasing (successful login) frees the slot again.
func TestChallengeQuotaLifecycle(t *testing.T) {
	rc := redisClient(t)
	cfg := config.Default()
	cfg.Security.MaxChallengesPerIp = 3
	cfg.Security.ChallengeWindow = "1m"
	ip := "test:challenge-quota:" + t.Name()
	key := challengeQuotaPrefix + ip

	ctx := context.Background()
	t.Cleanup(func() { _ = rc.Raw.Del(ctx, key).Err() })

	claimed := make([]string, 0, cfg.Security.MaxChallengesPerIp)
	for range cfg.Security.MaxChallengesPerIp {
		id := uuid.NewString()
		if !ChallengeAllowed(ctx, rc, cfg, ip, id) {
			t.Fatalf("challenge %d should be admitted", len(claimed)+1)
		}
		claimed = append(claimed, id)
	}

	// Over the limit → refused, and the refused attempt does not burn a slot.
	if ChallengeAllowed(ctx, rc, cfg, ip, uuid.NewString()) {
		t.Fatal("should be refused once the quota is exhausted")
	}
	if n, err := rc.Raw.ZCard(ctx, key).Result(); err != nil || n != int64(cfg.Security.MaxChallengesPerIp) {
		t.Fatalf("refused attempt changed the quota: members = %d, err = %v", n, err)
	}

	// A successful login releases its slot.
	ReleaseChallenge(ctx, rc, ip, claimed[0])
	if !ChallengeAllowed(ctx, rc, cfg, ip, uuid.NewString()) {
		t.Fatal("released slot should be reusable")
	}

	// Other IPs are unaffected.
	if !ChallengeAllowed(ctx, rc, cfg, "198.51.100.77", uuid.NewString()) {
		t.Fatal("quota must be per IP")
	}
}

// TestChallengeQuotaWindowSlides pins that slots older than the window stop
// counting, so the quota is a rolling hourly limit instead of a permanent ban.
func TestChallengeQuotaWindowSlides(t *testing.T) {
	rc := redisClient(t)
	cfg := config.Default()
	cfg.Security.MaxChallengesPerIp = 1
	cfg.Security.ChallengeWindow = "1m"
	ip := "test:challenge-quota-window:" + t.Name()
	key := challengeQuotaPrefix + ip

	ctx := context.Background()
	t.Cleanup(func() { _ = rc.Raw.Del(ctx, key).Err() })

	stale := time.Now().UTC().Add(-2 * time.Minute)
	if err := rc.Raw.ZAdd(ctx, key, goredis.Z{Score: float64(stale.UnixMilli()), Member: uuid.NewString()}).Err(); err != nil {
		t.Fatalf("seed stale slot: %v", err)
	}
	if !ChallengeAllowed(ctx, rc, cfg, ip, uuid.NewString()) {
		t.Fatal("a slot older than the window must not count")
	}
	if n, err := rc.Raw.ZCard(ctx, key).Result(); err != nil || n != 1 {
		t.Fatalf("stale slot was not pruned: members = %d, err = %v", n, err)
	}
}

// TestChallengeQuotaDisabled pins that "0" turns the quota off without
// recording anything.
func TestChallengeQuotaDisabled(t *testing.T) {
	rc := redisClient(t)
	cfg := config.Default()
	cfg.Security.MaxChallengesPerIp = 0
	ip := "test:challenge-quota-off:" + t.Name()
	key := challengeQuotaPrefix + ip

	ctx := context.Background()
	t.Cleanup(func() { _ = rc.Raw.Del(ctx, key).Err() })

	for i := range 10 {
		if !ChallengeAllowed(ctx, rc, cfg, ip, uuid.NewString()) {
			t.Fatalf("challenge %d should be admitted with the quota disabled", i+1)
		}
	}
	if n, err := rc.Raw.ZCard(ctx, key).Result(); err != nil || n != 0 {
		t.Fatalf("disabled quota recorded slots: members = %d, err = %v", n, err)
	}
}

func TestChallengeAllowedDegraded(t *testing.T) {
	// Nil Redis, empty IP and empty challenge id all allow (degraded mode).
	if !ChallengeAllowed(context.Background(), nil, config.Default(), "192.168.1.1", uuid.NewString()) {
		t.Fatal("nil Redis should degrade to allowed")
	}
	if !ChallengeAllowed(context.Background(), nil, config.Default(), "", uuid.NewString()) {
		t.Fatal("empty IP should be allowed")
	}
	if !ChallengeAllowed(context.Background(), nil, config.Default(), "192.168.1.1", "") {
		t.Fatal("empty challenge id should be allowed")
	}
}
