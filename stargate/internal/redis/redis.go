// Package redis wires the raw go-redis client plus the Golaunch shared-cache
// service (dyson: key prefix, envelope HSET) that the C# fleet interoperates
// with.
package redis

import (
	"context"
	"fmt"

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
