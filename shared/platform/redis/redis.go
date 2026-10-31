// Package redis connects the shared cache service. Redis is optional: without
// it a process-local memory cache keeps session validation working.
package redis

import (
	"context"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/cache"
)

// Connect dials Redis and returns a cache service. An empty address yields a
// process-local memory cache so the caller never has to branch on availability.
func Connect(ctx context.Context, addr, password string, database int) (cache.CacheService, error) {
	if strings.TrimSpace(addr) == "" {
		return cache.NewMemoryCacheService(8192), nil
	}
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       database,
	})
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, err
	}
	return cache.NewRedisCacheService(client), nil
}
