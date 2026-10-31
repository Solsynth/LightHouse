package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	sharedCacheCodecKey  = "codec"
	sharedCacheDataKey   = "data"
	sharedCacheTypeKey   = "type"
	sharedCacheCachedKey = "cached_at"
	sharedCacheCodec     = "json"
	flagValue            = "1"
	groupKeyPrefix       = globalPrefix + "cg:"
	keyGroupPrefix       = globalPrefix + "cgk:"
	lockKeyPrefix        = globalPrefix + "lock:"
)

type cacheEnvelope struct {
	Data     json.RawMessage `json:"data"`
	Codec    string          `json:"codec"`
	Type     string          `json:"type"`
	CachedAt int64           `json:"cached_at"`
}

// RedisCacheService implements CacheService using a Redis backend.
type RedisCacheService struct {
	client redis.Cmdable
}

type redisDistributedLock struct {
	client   redis.Cmdable
	resource string
	key      string
	lockID   string
	expiry   time.Duration
}

// NewRedisCacheService creates a Redis-backed cache service.
func NewRedisCacheService(client redis.Cmdable) *RedisCacheService {
	return &RedisCacheService{client: client}
}

func (c *RedisCacheService) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	data, err := serialize(value)
	if err != nil {
		return err
	}
	return c.client.Set(ctx, NormalizeKey(key), data, ttl).Err()
}

func (c *RedisCacheService) Get(ctx context.Context, key string, dest any) (bool, error) {
	data, err := c.client.Get(ctx, NormalizeKey(key)).Result()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, deserialize(data, dest)
}

func (c *RedisCacheService) Remove(ctx context.Context, key string) error {
	return c.client.Del(ctx, NormalizeKey(key)).Err()
}

func (c *RedisCacheService) SetData(ctx context.Context, key string, value any, typeName string, ttl time.Duration) error {
	if value == nil {
		return errors.New("shared cache value is nil")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	fields := map[string]any{
		sharedCacheDataKey:   string(data),
		sharedCacheCodecKey:  sharedCacheCodec,
		sharedCacheTypeKey:   typeName,
		sharedCacheCachedKey: time.Now().Unix(),
	}
	if err := c.client.HSet(ctx, NormalizeKey(key), fields).Err(); err != nil {
		return err
	}
	if ttl > 0 {
		return c.client.Expire(ctx, NormalizeKey(key), ttl).Err()
	}
	return c.client.Persist(ctx, NormalizeKey(key)).Err()
}

func (c *RedisCacheService) GetData(ctx context.Context, key string, dest any, typeName string) (bool, error) {
	if dest == nil {
		return false, errors.New("shared cache destination is nil")
	}
	hash, err := c.client.HGetAll(ctx, NormalizeKey(key)).Result()
	if err == redis.Nil || len(hash) == 0 {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if hash[sharedCacheCodecKey] != sharedCacheCodec {
		return false, fmt.Errorf("unexpected cache codec %q", hash[sharedCacheCodecKey])
	}
	if typeName != "" && hash[sharedCacheTypeKey] != "" && hash[sharedCacheTypeKey] != typeName {
		return false, fmt.Errorf("unexpected cache type %q", hash[sharedCacheTypeKey])
	}
	if hash[sharedCacheDataKey] == "" {
		return false, nil
	}
	if err := json.Unmarshal([]byte(hash[sharedCacheDataKey]), dest); err != nil {
		return false, err
	}
	return true, nil
}

func (c *RedisCacheService) SetFlag(ctx context.Context, key string, ttl time.Duration) error {
	return c.client.Set(ctx, NormalizeKey(key), flagValue, ttl).Err()
}

func (c *RedisCacheService) HasFlag(ctx context.Context, key string) (bool, error) {
	val, err := c.client.Get(ctx, NormalizeKey(key)).Result()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return val != "", nil
}

func (c *RedisCacheService) RemoveFlag(ctx context.Context, key string) error {
	return c.client.Del(ctx, NormalizeKey(key)).Err()
}

func (c *RedisCacheService) AddToGroup(ctx context.Context, key string, group string) error {
	normalized := NormalizeKey(key)
	groupKey := groupKeyPrefix + group
	keyGroupIndex := keyGroupPrefix + normalized
	db := c.client
	if err := db.SAdd(ctx, groupKey, normalized).Err(); err != nil {
		return err
	}
	return db.SAdd(ctx, keyGroupIndex, groupKey).Err()
}

func (c *RedisCacheService) RemoveGroup(ctx context.Context, group string) error {
	groupKey := groupKeyPrefix + group
	db := c.client
	keys, err := db.SMembers(ctx, groupKey).Result()
	if err != nil {
		return err
	}
	for _, key := range keys {
		if err := db.Del(ctx, key).Err(); err != nil {
			return err
		}
		if err := db.SRem(ctx, keyGroupPrefix+key, groupKey).Err(); err != nil {
			return err
		}
	}
	return db.Del(ctx, groupKey).Err()
}

func (c *RedisCacheService) GetGroupKeys(ctx context.Context, group string) ([]string, error) {
	return c.client.SMembers(ctx, groupKeyPrefix+group).Result()
}

func (c *RedisCacheService) SetWithGroups(ctx context.Context, key string, value any, groups []string, ttl time.Duration) error {
	if err := c.Set(ctx, key, value, ttl); err != nil {
		return err
	}
	for _, group := range groups {
		if err := c.AddToGroup(ctx, key, group); err != nil {
			return err
		}
	}
	return nil
}

func (c *RedisCacheService) AcquireLock(ctx context.Context, resource string, expiry time.Duration, waitTime time.Duration, retryInterval time.Duration) (DistributedLock, error) {
	if strings.TrimSpace(resource) == "" {
		return nil, errors.New("resource cannot be empty")
	}
	if retryInterval <= 0 {
		retryInterval = 100 * time.Millisecond
	}
	if waitTime < 0 {
		waitTime = 0
	}
	key := lockKeyPrefix + resource
	deadline := time.Now().Add(waitTime)
	for {
		lockID := fmt.Sprintf("%d", time.Now().UnixNano())
		ok, err := c.client.SetNX(ctx, key, lockID, expiry).Result()
		if err != nil {
			return nil, err
		}
		if ok {
			return &redisDistributedLock{client: c.client, resource: resource, key: key, lockID: lockID, expiry: expiry}, nil
		}
		if waitTime == 0 || time.Now().After(deadline) {
			return nil, nil
		}
		time.Sleep(retryInterval)
	}
}

func (c *RedisCacheService) ExecuteWithLock(ctx context.Context, resource string, expiry time.Duration, waitTime time.Duration, retryInterval time.Duration, action func(context.Context) error) (bool, error) {
	lock, err := c.AcquireLock(ctx, resource, expiry, waitTime, retryInterval)
	if err != nil || lock == nil {
		return false, err
	}
	defer func() { _ = lock.Release(ctx) }()
	if err := action(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (c *redisDistributedLock) Resource() string { return c.resource }

func (c *redisDistributedLock) LockID() string { return c.lockID }

func (c *redisDistributedLock) Release(ctx context.Context) error {
	const lua = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
else
  return 0
end`
	return c.client.Eval(ctx, lua, []string{c.key}, c.lockID).Err()
}
