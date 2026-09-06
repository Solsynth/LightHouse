package risk

import (
	"context"
	"testing"

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
