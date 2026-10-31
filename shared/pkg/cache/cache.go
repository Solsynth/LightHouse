package cache

import (
	"context"
	"time"
)

const globalPrefix = "dyson:"

// CacheService is the interface for a key-value cache with TTL support.
// Keys are normalized with a "dyson:" prefix to be cross-compatible
// with the C# DysonNetwork cache service.
type CacheService interface {
	Set(ctx context.Context, key string, value any, ttl time.Duration) error
	Get(ctx context.Context, key string, dest any) (bool, error)
	Remove(ctx context.Context, key string) error

	SetData(ctx context.Context, key string, value any, typeName string, ttl time.Duration) error
	GetData(ctx context.Context, key string, dest any, typeName string) (bool, error)
	SetFlag(ctx context.Context, key string, ttl time.Duration) error
	HasFlag(ctx context.Context, key string) (bool, error)
	RemoveFlag(ctx context.Context, key string) error

	AddToGroup(ctx context.Context, key string, group string) error
	RemoveGroup(ctx context.Context, group string) error
	GetGroupKeys(ctx context.Context, group string) ([]string, error)
	SetWithGroups(ctx context.Context, key string, value any, groups []string, ttl time.Duration) error

	AcquireLock(ctx context.Context, resource string, expiry time.Duration, waitTime time.Duration, retryInterval time.Duration) (DistributedLock, error)
	ExecuteWithLock(ctx context.Context, resource string, expiry time.Duration, waitTime time.Duration, retryInterval time.Duration, action func(context.Context) error) (bool, error)
}

type DistributedLock interface {
	Resource() string
	LockID() string
	Release(ctx context.Context) error
}

// ExecuteWithLockValue runs action while holding a lock and returns its result.
// It is kept as a helper instead of an interface method because Go interfaces
// cannot declare generic methods.
func ExecuteWithLockValue[T any](ctx context.Context, svc CacheService, resource string, expiry time.Duration, waitTime time.Duration, retryInterval time.Duration, action func(context.Context) (T, error)) (bool, T, error) {
	var zero T
	lock, err := svc.AcquireLock(ctx, resource, expiry, waitTime, retryInterval)
	if err != nil {
		return false, zero, err
	}
	if lock == nil {
		return false, zero, nil
	}
	defer func() { _ = lock.Release(ctx) }()
	result, err := action(ctx)
	if err != nil {
		return false, zero, err
	}
	return true, result, nil
}

// NormalizeKey prepends the global "dyson:" prefix to a user-supplied key.
// e.g. "auth:session:abc" → "dyson:auth:session:abc"
func NormalizeKey(key string) string {
	return globalPrefix + key
}
