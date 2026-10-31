package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

type memoryEntry struct {
	data      string
	codec     string
	typeName  string
	cachedAt  int64
	expiresAt time.Time
}

// MemoryCacheService implements CacheService using an in-memory LRU cache with TTL.
type MemoryCacheService struct {
	cache     *lru.Cache[string, memoryEntry]
	stopSweep chan struct{}
	mu        sync.RWMutex
	groups    map[string]map[string]struct{}
	locks     map[string]memoryLock
}

type memoryLock struct {
	resource string
	lockID   string
	expiresAt time.Time
}

// NewMemoryCacheService creates an in-memory cache with the given max entries.
// A background goroutine sweeps expired entries every 30 seconds.
func NewMemoryCacheService(maxEntries int) *MemoryCacheService {
	c, _ := lru.New[string, memoryEntry](maxEntries)
	m := &MemoryCacheService{
		cache:     c,
		stopSweep: make(chan struct{}),
		groups:    make(map[string]map[string]struct{}),
		locks:     make(map[string]memoryLock),
	}
	go m.sweepLoop(30 * time.Second)
	return m
}

func (m *MemoryCacheService) Set(_ context.Context, key string, value any, ttl time.Duration) error {
	data, err := serialize(value)
	if err != nil {
		return err
	}
	entry := memoryEntry{data: data}
	if ttl > 0 {
		entry.expiresAt = time.Now().Add(ttl)
	}
	m.cache.Add(NormalizeKey(key), entry)
	return nil
}

func (m *MemoryCacheService) Get(_ context.Context, key string, dest any) (bool, error) {
	entry, ok := m.cache.Get(NormalizeKey(key))
	if !ok {
		return false, nil
	}
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		m.cache.Remove(NormalizeKey(key))
		return false, nil
	}
	return true, deserialize(entry.data, dest)
}

func (m *MemoryCacheService) Remove(_ context.Context, key string) error {
	m.cache.Remove(NormalizeKey(key))
	return nil
}

func (m *MemoryCacheService) SetData(ctx context.Context, key string, value any, typeName string, ttl time.Duration) error {
	if value == nil {
		return errors.New("shared cache value is nil")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	entry := memoryEntry{
		data:     string(data),
		codec:    sharedCacheCodec,
		typeName: typeName,
		cachedAt: time.Now().Unix(),
	}
	if ttl > 0 {
		entry.expiresAt = time.Now().Add(ttl)
	}
	m.cache.Add(NormalizeKey(key), entry)
	return nil
}

func (m *MemoryCacheService) GetData(ctx context.Context, key string, dest any, typeName string) (bool, error) {
	entry, ok := m.cache.Get(NormalizeKey(key))
	if !ok {
		return false, nil
	}
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		m.cache.Remove(NormalizeKey(key))
		return false, nil
	}
	if entry.codec != sharedCacheCodec {
		return false, errors.New("unexpected cache codec")
	}
	if typeName != "" && entry.typeName != "" && entry.typeName != typeName {
		return false, errors.New("unexpected cache type")
	}
	return true, json.Unmarshal([]byte(entry.data), dest)
}

func (m *MemoryCacheService) SetFlag(_ context.Context, key string, ttl time.Duration) error {
	entry := memoryEntry{data: flagValue}
	entry.codec = "flag"
	if ttl > 0 {
		entry.expiresAt = time.Now().Add(ttl)
	}
	m.cache.Add(NormalizeKey(key), entry)
	return nil
}

func (m *MemoryCacheService) HasFlag(_ context.Context, key string) (bool, error) {
	entry, ok := m.cache.Get(NormalizeKey(key))
	if !ok {
		return false, nil
	}
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		m.cache.Remove(NormalizeKey(key))
		return false, nil
	}
	return entry.data == flagValue, nil
}

func (m *MemoryCacheService) RemoveFlag(_ context.Context, key string) error {
	return m.Remove(context.Background(), key)
}

func (m *MemoryCacheService) AddToGroup(_ context.Context, key string, group string) error {
	normalized := NormalizeKey(key)
	groupKey := globalPrefix + "cg:" + group
	keyGroupIndex := globalPrefix + "cgk:" + normalized
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.groups[groupKey]; !ok {
		m.groups[groupKey] = make(map[string]struct{})
	}
	m.groups[groupKey][normalized] = struct{}{}
	if _, ok := m.groups[keyGroupIndex]; !ok {
		m.groups[keyGroupIndex] = make(map[string]struct{})
	}
	m.groups[keyGroupIndex][groupKey] = struct{}{}
	return nil
}

func (m *MemoryCacheService) RemoveGroup(_ context.Context, group string) error {
	groupKey := globalPrefix + "cg:" + group
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := m.groups[groupKey]
	for key := range keys {
		m.cache.Remove(key)
		delete(m.groups, globalPrefix+"cgk:"+key)
	}
	delete(m.groups, groupKey)
	return nil
}

func (m *MemoryCacheService) GetGroupKeys(_ context.Context, group string) ([]string, error) {
	groupKey := globalPrefix + "cg:" + group
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys := m.groups[groupKey]
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	return out, nil
}

func (m *MemoryCacheService) SetWithGroups(ctx context.Context, key string, value any, groups []string, ttl time.Duration) error {
	if err := m.Set(ctx, key, value, ttl); err != nil {
		return err
	}
	for _, group := range groups {
		if err := m.AddToGroup(ctx, key, group); err != nil {
			return err
		}
	}
	return nil
}

func (m *MemoryCacheService) AcquireLock(_ context.Context, resource string, expiry time.Duration, waitTime time.Duration, retryInterval time.Duration) (DistributedLock, error) {
	if strings.TrimSpace(resource) == "" {
		return nil, errors.New("resource cannot be empty")
	}
	if retryInterval <= 0 {
		retryInterval = 100 * time.Millisecond
	}
	if waitTime < 0 {
		waitTime = 0
	}
	key := globalPrefix + "lock:" + resource
	deadline := time.Now().Add(waitTime)
	for {
		lockID := fmt.Sprintf("%d", time.Now().UnixNano())
		m.mu.Lock()
		lock, ok := m.locks[key]
		if ok && !lock.expiresAt.IsZero() && time.Now().After(lock.expiresAt) {
			delete(m.locks, key)
			ok = false
		}
		if !ok {
			m.locks[key] = memoryLock{resource: resource, lockID: lockID, expiresAt: time.Now().Add(expiry)}
			m.mu.Unlock()
			return &memoryDistributedLock{svc: m, key: key, lockID: lockID, resource: resource}, nil
		}
		m.mu.Unlock()
		if waitTime == 0 || time.Now().After(deadline) {
			return nil, nil
		}
		time.Sleep(retryInterval)
	}
}

func (m *MemoryCacheService) ExecuteWithLock(ctx context.Context, resource string, expiry time.Duration, waitTime time.Duration, retryInterval time.Duration, action func(context.Context) error) (bool, error) {
	lock, err := m.AcquireLock(ctx, resource, expiry, waitTime, retryInterval)
	if err != nil || lock == nil {
		return false, err
	}
	defer func() { _ = lock.Release(ctx) }()
	if err := action(ctx); err != nil {
		return false, err
	}
	return true, nil
}

type memoryDistributedLock struct {
	svc      *MemoryCacheService
	key      string
	lockID   string
	resource string
}

func (l *memoryDistributedLock) Resource() string { return l.resource }

func (l *memoryDistributedLock) LockID() string { return l.lockID }

func (l *memoryDistributedLock) Release(_ context.Context) error {
	l.svc.mu.Lock()
	defer l.svc.mu.Unlock()
	lock, ok := l.svc.locks[l.key]
	if !ok || lock.lockID != l.lockID {
		return nil
	}
	delete(l.svc.locks, l.key)
	return nil
}

// Close stops the background sweeper goroutine.
func (m *MemoryCacheService) Close() {
	close(m.stopSweep)
}

func (m *MemoryCacheService) sweepLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.sweepExpired()
		case <-m.stopSweep:
			return
		}
	}
}

func (m *MemoryCacheService) sweepExpired() {
	now := time.Now()
	for _, key := range m.cache.Keys() {
		entry, ok := m.cache.Peek(key)
		if !ok {
			continue
		}
		if !entry.expiresAt.IsZero() && now.After(entry.expiresAt) {
			m.cache.Remove(key)
		}
	}
}
