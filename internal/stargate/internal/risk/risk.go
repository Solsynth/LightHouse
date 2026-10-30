// Package risk provides fail2ban-style IP blocking backed by Redis. It is
// degraded-safe: when Redis is unavailable or unconfigured, all operations
// are no-ops and every IP is allowed.
package risk

import (
	"context"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/redis"
)

const (
	fail2banCounterPrefix        = "auth:fail2ban:ip:"
	fail2banBlockPrefix          = "auth:fail2ban:block:"
	fail2banDeclineCounterPrefix = "auth:fail2ban:decline:"
	fail2banDeclineBlockPrefix   = "auth:fail2ban:decline-block:"
	challengeQuotaPrefix         = "auth:challenge:ip:"
)

// IPAllowed reports whether the given IP is currently permitted to attempt
// login. Returns true when Redis is unavailable, IP is empty, or no block
// key is present.
func IPAllowed(ctx context.Context, rc *redis.Client, cfg *config.Config, ip string) bool {
	if rc == nil || !rc.Available() || strings.TrimSpace(ip) == "" {
		return true
	}
	// Both blocks apply: the password-failure one and the declined-challenge
	// one (which a later successful login must not lift).
	exists, err := rc.Raw.Exists(ctx, fail2banBlockPrefix+ip, fail2banDeclineBlockPrefix+ip).Result()
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
// login). Decline evidence (RecordDecline) deliberately survives: an IP that
// spams challenges must not clear its record by logging into its own account.
func ClearFailures(ctx context.Context, rc *redis.Client, ip string) {
	if rc == nil || !rc.Available() || strings.TrimSpace(ip) == "" {
		return
	}
	_ = rc.Raw.Del(ctx, fail2banCounterPrefix+ip, fail2banBlockPrefix+ip).Err()
}

// RecordDecline charges a declined challenge to the IP that requested it and
// blocks that IP once the declines exceed cfg.Security.Fail2banDeclineMax
// within cfg.Security.Fail2banWindow. Only the requesting IP is charged: the
// trusted session declining the prompt is doing exactly what the flow asks.
//
// The counter and its block are separate from the password-failure fail2ban
// pair so ClearFailures (successful login) cannot wash away decline evidence.
// A quota of "0" disables the tracking.
func RecordDecline(ctx context.Context, rc *redis.Client, cfg *config.Config, ip string) {
	if rc == nil || !rc.Available() || strings.TrimSpace(ip) == "" || cfg.Security.Fail2banDeclineMax <= 0 {
		return
	}
	counterKey := fail2banDeclineCounterPrefix + ip
	blockKey := fail2banDeclineBlockPrefix + ip
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
	if n > int64(cfg.Security.Fail2banDeclineMax) {
		_ = rc.Raw.Set(ctx, blockKey, "1", blockFor).Err()
	}
}

// ChallengeAllowed reports whether the IP may create a new challenge and, when
// the quota has room, claims a slot for challengeID.
//
// The quota is a rolling window (cfg.Security.ChallengeWindowDuration) holding
// the challenges this IP created but never completed. Callers that mint a
// session from a challenge MUST call ReleaseChallenge so successful logins
// never count against the limit; abandoned or failed challenges keep their
// slot until the window slides past them.
//
// Degraded-safe: Redis unavailable, empty IP/ID or a quota <= 0 all allow.
func ChallengeAllowed(ctx context.Context, rc *redis.Client, cfg *config.Config, ip, challengeID string) bool {
	if rc == nil || !rc.Available() || strings.TrimSpace(ip) == "" || strings.TrimSpace(challengeID) == "" {
		return true
	}
	limit := cfg.Security.MaxChallengesPerIp
	if limit <= 0 {
		return true // quota disabled
	}
	window := cfg.Security.ChallengeWindowDuration()
	key := challengeQuotaPrefix + ip
	now := time.Now().UTC()

	pipe := rc.Raw.Pipeline()
	// Drop slots older than the window, then claim one. The claimed slot is
	// counted by our own ZCARD (this client's commands run in order), so a
	// concurrent caller can only ever make the count larger — nobody is
	// admitted past the limit.
	pipe.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(now.Add(-window).UnixMilli(), 10))
	pipe.ZAdd(ctx, key, goredis.Z{Score: float64(now.UnixMilli()), Member: challengeID})
	pipe.Expire(ctx, key, window)
	count := pipe.ZCard(ctx, key)
	if _, err := pipe.Exec(ctx); err != nil {
		return true // degrade: allow on Redis error
	}
	if int(count.Val()) > limit {
		_ = rc.Raw.ZRem(ctx, key, challengeID).Err() // undo the claim
		return false
	}
	return true
}

// ReleaseChallenge frees the quota slot held by challengeID. It is called when
// a challenge completes into a session, so a successful login does not count
// against the per-IP new-challenge limit.
func ReleaseChallenge(ctx context.Context, rc *redis.Client, ip, challengeID string) {
	if rc == nil || !rc.Available() || strings.TrimSpace(ip) == "" || strings.TrimSpace(challengeID) == "" {
		return
	}
	_ = rc.Raw.ZRem(ctx, challengeQuotaPrefix+ip, challengeID).Err()
}
