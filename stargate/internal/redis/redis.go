// Package redis wires the raw go-redis client plus the Golaunch shared-cache
// service (dyson: key prefix, envelope HSET) that the C# fleet interoperates
// with.
package redis

import (
	"context"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
	"src.solsynth.dev/sosys/go/pkg/cache"
)

// Client bundles the raw client and the shared cache service.
type Client struct {
	Raw   *redis.Client
	Cache *cache.RedisCacheService
}

// Connect creates the Redis client and shared cache service.
func Connect(ctx context.Context, addr, password string, dbIndex int) (*Client, error) {
	raw := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       dbIndex,
	})
	if err := raw.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return &Client{
		Raw:   raw,
		Cache: cache.NewRedisCacheService(raw),
	}, nil
}

// ClearActorPermissionCache removes every C#-side permission cache entry for
// an actor. The Go permission service is DB-backed, but the C# fleet still
// reads the perm:* keys, so the clear is kept for interop (best-effort).
func (c *Client) ClearActorPermissionCache(ctx context.Context, actor string) {
	if c == nil || c.Cache == nil {
		return
	}
	_ = c.Cache.Remove(ctx, "perm-cg:"+actor)
	_ = c.Cache.RemoveGroup(ctx, "perm-g:"+actor)
	_ = c.Cache.Remove(ctx, "perm-blocked:"+actor)

	// Permission values may have been cached without the group index (for
	// example, by an older Padlock instance). Remove those keys by pattern as
	// well; otherwise a permission-node update can leave a stale decision
	// behind until its one-minute TTL expires.
	if c.Raw == nil {
		return
	}
	iter := c.Raw.Scan(ctx, 0, "dyson:perm:*:"+escapeRedisPattern(actor)+":*", 0).Iterator()
	for iter.Next(ctx) {
		_ = c.Raw.Del(ctx, iter.Val()).Err()
	}
}

func escapeRedisPattern(value string) string {
	var escaped strings.Builder
	escaped.Grow(len(value))
	for _, r := range value {
		switch r {
		case '\\', '*', '?', '[':
			escaped.WriteByte('\\')
		}
		escaped.WriteRune(r)
	}
	return escaped.String()
}
