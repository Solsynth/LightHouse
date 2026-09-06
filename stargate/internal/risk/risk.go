// Package risk provides fail2ban-style IP blocking backed by Redis. It is
// degraded-safe: when Redis is unavailable or unconfigured, all operations
// are no-ops and every IP is allowed.
package risk

import (
	"context"
	"strings"

	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/redis"
)

const (
	fail2banCounterPrefix = "auth:fail2ban:ip:"
	fail2banBlockPrefix   = "auth:fail2ban:block:"
)

// IPAllowed reports whether the given IP is currently permitted to attempt
// login. Returns true when Redis is unavailable, IP is empty, or no block
// key is present.
func IPAllowed(ctx context.Context, rc *redis.Client, cfg *config.Config, ip string) bool {
	if rc == nil || !rc.Available() || strings.TrimSpace(ip) == "" {
		return true
	}
	blockKey := fail2banBlockPrefix + ip
	exists, err := rc.Raw.Exists(ctx, blockKey).Result()
	if err != nil {
		return true // degrade: allow on Redis error
	}
	return exists == 0
}

// RecordFailure increments the failure counter for the IP. When the count
// exceeds cfg.Security.Fail2banMaxFails, a block key is set.
func RecordFailure(ctx context.Context, rc *redis.Client, cfg *config.Config, ip string) {
	if rc == nil || !rc.Available() || strings.TrimSpace(ip) == "" {
		return
	}
	counterKey := fail2banCounterPrefix + ip
	blockKey := fail2banBlockPrefix + ip
	window := cfg.Security.Fail2banWindowDuration()
	blockFor := cfg.Security.Fail2banBlockForDuration()

	n, err := rc.Raw.Incr(ctx, counterKey).Result()
	if err != nil {
		return
	}
	// Set window TTL on first increment so the counter resets naturally.
	if n == 1 {
		_ = rc.Raw.Expire(ctx, counterKey, window).Err()
	}
	if n > int64(cfg.Security.Fail2banMaxFails) {
		_ = rc.Raw.Set(ctx, blockKey, "1", blockFor).Err()
	}
}

// ClearFailures deletes the IP counter and block key (called on successful
// login).
func ClearFailures(ctx context.Context, rc *redis.Client, ip string) {
	if rc == nil || !rc.Available() || strings.TrimSpace(ip) == "" {
		return
	}
	_ = rc.Raw.Del(ctx, fail2banCounterPrefix+ip, fail2banBlockPrefix+ip).Err()
}
