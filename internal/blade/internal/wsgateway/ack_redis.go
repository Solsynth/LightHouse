package wsgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"srv.solsynth.dev/sosys/blade/internal/logging"
)

const ackRedisPrefix = "ws:ack"

// RedisAckTracker keeps unacknowledged frames in Redis so a push survives a
// queue-full drop, a write error, an offline device and a replica restart.
//
// Layout per account:
//
//	ws:ack:pending:<namespace>:<accountID>  list, append order = send order
//	ws:ack:index:<namespace>:<accountID>    hash, ack_id -> device_id
//
// Acking is a single HDEL on the index; the list entry it orphans is removed
// lazily by List/Drop, which keeps the ack path O(1) and Lua-free.
type RedisAckTracker struct {
	client     *redis.Client
	retention  time.Duration
	maxPending int
}

func NewRedisAckTracker(client *redis.Client, retention time.Duration, maxPending int) *RedisAckTracker {
	if retention <= 0 {
		retention = ackDefaultRetention
	}
	if maxPending <= 0 {
		maxPending = ackDefaultMaxPending
	}
	return &RedisAckTracker{client: client, retention: retention, maxPending: maxPending}
}

func (t *RedisAckTracker) Retention() time.Duration { return t.retention }

func (t *RedisAckTracker) MaxPending() int { return t.maxPending }

func (t *RedisAckTracker) pendingKey(namespace, accountID string) string {
	return ackRedisPrefix + ":pending:" + namespace + ":" + accountID
}

func (t *RedisAckTracker) indexKey(namespace, accountID string) string {
	return ackRedisPrefix + ":index:" + namespace + ":" + accountID
}

func (t *RedisAckTracker) Append(ctx context.Context, namespace, accountID string, entry PendingPacket) (int, error) {
	if t == nil || t.client == nil {
		return 0, nil
	}
	if strings.TrimSpace(entry.AckID) == "" {
		return 0, fmt.Errorf("ack id is required")
	}
	body, err := json.Marshal(entry)
	if err != nil {
		return 0, err
	}
	pendingKey := t.pendingKey(namespace, accountID)
	indexKey := t.indexKey(namespace, accountID)

	pipe := t.client.Pipeline()
	pipe.RPush(ctx, pendingKey, body)
	pipe.HSet(ctx, indexKey, entry.AckID, entry.DeviceID)
	pipe.Expire(ctx, pendingKey, t.retention)
	pipe.Expire(ctx, indexKey, t.retention)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}

	// Bound the buffer oldest-first: the entries a client is least likely to
	// still need are the ones it has been ignoring the longest.
	for {
		length, err := t.client.LLen(ctx, pendingKey).Result()
		if err != nil {
			return 0, err
		}
		if int(length) <= t.maxPending {
			return int(length), nil
		}
		raw, err := t.client.LPop(ctx, pendingKey).Result()
		if err != nil {
			// Empty list already (a concurrent ack pruned it): nothing to trim.
			if err == redis.Nil {
				return 0, nil
			}
			return 0, err
		}
		var evicted PendingPacket
		if err := json.Unmarshal([]byte(raw), &evicted); err == nil && evicted.AckID != "" {
			_ = t.client.HDel(ctx, indexKey, evicted.AckID).Err()
			logging.Log.Warn().
				Str("namespace", namespace).
				Str("accountId", accountID).
				Str("deviceId", evicted.DeviceID).
				Str("evictedAckId", evicted.AckID).
				Int("maxPending", t.maxPending).
				Msg("Evicted unacknowledged websocket packet from full pending buffer")
		}
	}
}

func (t *RedisAckTracker) Ack(ctx context.Context, namespace, accountID, ackID string) (bool, error) {
	if t == nil || t.client == nil {
		return false, nil
	}
	ackID = strings.TrimSpace(ackID)
	if ackID == "" {
		return false, nil
	}
	indexKey := t.indexKey(namespace, accountID)
	removed, err := t.client.HDel(ctx, indexKey, ackID).Result()
	if err != nil {
		return false, err
	}
	if removed == 0 {
		return false, nil
	}
	_ = t.client.Expire(ctx, indexKey, t.retention).Err()
	_ = t.client.Expire(ctx, t.pendingKey(namespace, accountID), t.retention).Err()
	return true, nil
}

func (t *RedisAckTracker) List(ctx context.Context, namespace, accountID string) ([]PendingPacket, error) {
	if t == nil || t.client == nil {
		return nil, nil
	}
	return t.list(ctx, namespace, accountID, true)
}

// list reads the pending list in append order. When prune is set, entries whose
// index row is gone (acked) are removed from the list as a side effect.
func (t *RedisAckTracker) list(ctx context.Context, namespace, accountID string, prune bool) ([]PendingPacket, error) {
	pendingKey := t.pendingKey(namespace, accountID)
	indexKey := t.indexKey(namespace, accountID)

	raw, err := t.client.LRange(ctx, pendingKey, 0, -1).Result()
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	index, err := t.client.HGetAll(ctx, indexKey).Result()
	if err != nil {
		return nil, err
	}

	entries := make([]PendingPacket, 0, len(raw))
	orphans := make([]string, 0)
	for _, frame := range raw {
		var entry PendingPacket
		if err := json.Unmarshal([]byte(frame), &entry); err != nil || entry.AckID == "" {
			// Unreadable element can never be acked or replayed; treat it as
			// garbage so the buffer drains instead of rotting.
			orphans = append(orphans, frame)
			continue
		}
		if _, ok := index[entry.AckID]; !ok {
			orphans = append(orphans, frame)
			continue
		}
		entries = append(entries, entry)
	}
	if prune && len(orphans) > 0 {
		for _, frame := range orphans {
			_ = t.client.LRem(ctx, pendingKey, 1, frame).Err()
		}
	}
	return entries, nil
}

func (t *RedisAckTracker) CountAttempt(ctx context.Context, namespace, accountID, ackID string, nextTry time.Time) error {
	if t == nil || t.client == nil {
		return nil
	}
	ackID = strings.TrimSpace(ackID)
	if ackID == "" {
		return nil
	}
	pendingKey := t.pendingKey(namespace, accountID)
	indexKey := t.indexKey(namespace, accountID)

	index, err := t.client.HGetAll(ctx, indexKey).Result()
	if err != nil {
		return err
	}
	// A stale index means the entry was already acked: nothing to retry.
	if _, ok := index[ackID]; !ok {
		return nil
	}

	raw, err := t.client.LRange(ctx, pendingKey, 0, -1).Result()
	if err != nil {
		return err
	}
	for offset, frame := range raw {
		var entry PendingPacket
		if err := json.Unmarshal([]byte(frame), &entry); err != nil {
			continue
		}
		if entry.AckID != ackID {
			continue
		}
		entry.Attempts++
		entry.NextTryAt = nextTry
		body, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		if err := t.client.LSet(ctx, pendingKey, int64(offset), body).Err(); err != nil {
			return err
		}
		_ = t.client.Expire(ctx, pendingKey, t.retention).Err()
		return nil
	}
	return nil
}

func (t *RedisAckTracker) Drop(ctx context.Context, namespace, accountID string, ackIDs []string) error {
	if t == nil || t.client == nil {
		return nil
	}
	if len(ackIDs) == 0 {
		return nil
	}
	pendingKey := t.pendingKey(namespace, accountID)
	indexKey := t.indexKey(namespace, accountID)
	drop := make(map[string]struct{}, len(ackIDs))
	for _, ackID := range ackIDs {
		if trimmed := strings.TrimSpace(ackID); trimmed != "" {
			drop[trimmed] = struct{}{}
		}
	}
	if len(drop) == 0 {
		return nil
	}

	raw, err := t.client.LRange(ctx, pendingKey, 0, -1).Result()
	if err != nil {
		return err
	}
	// LREM by exact element keeps concurrent appends intact, which a
	// read-filter-rewrite would not.
	for _, frame := range raw {
		var entry PendingPacket
		if err := json.Unmarshal([]byte(frame), &entry); err != nil {
			continue
		}
		if _, ok := drop[entry.AckID]; !ok {
			continue
		}
		if err := t.client.LRem(ctx, pendingKey, 1, frame).Err(); err != nil {
			return err
		}
	}

	keys := make([]string, 0, len(drop))
	for ackID := range drop {
		keys = append(keys, ackID)
	}
	if err := t.client.HDel(ctx, indexKey, keys...).Err(); err != nil {
		return err
	}
	return nil
}
