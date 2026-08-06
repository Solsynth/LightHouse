package push

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"src.solsynth.dev/sosys/go/pkg/cache"

	"src.solsynth.dev/sosys/metoer/internal/model"
)

// SopNotificationReplayBuffer ports
// DysonNetwork.Ring.Notification.SopNotificationReplayBuffer: the offline
// safety net for SOP devices, stored under the shared Redis keys
// ring:sop:replay:{accountId} (data + lock, mirroring the C# GetCacheKey /
// GetLockKey).
type SopNotificationReplayBuffer struct {
	cache cache.CacheService
}

const (
	maxReplayNotifications = 100
	replayTtl              = 30 * 24 * time.Hour
	replayLockExpiry       = 10 * time.Second
	replayLockWait         = 2 * time.Second
	replayLockRetry        = 100 * time.Millisecond
)

// NewSopNotificationReplayBuffer builds the buffer over the shared cache
// service.
func NewSopNotificationReplayBuffer(c cache.CacheService) *SopNotificationReplayBuffer {
	return &SopNotificationReplayBuffer{cache: c}
}

func replayCacheKey(accountID uuid.UUID) string { return "ring:sop:replay:" + accountID.String() }

// GetNotifications loads the replay list (empty when absent).
func (b *SopNotificationReplayBuffer) GetNotifications(ctx context.Context, accountID uuid.UUID) ([]model.Notification, error) {
	var notifications []model.Notification
	found, err := b.cache.GetData(ctx, replayCacheKey(accountID), &notifications, "")
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return notifications, nil
}

// AppendNotification adds a notification under the lock, deduplicating by
// id, ordering created_at DESC and capping at MaxReplayNotifications.
func (b *SopNotificationReplayBuffer) AppendNotification(ctx context.Context, n *model.Notification) error {
	accountID := n.AccountId
	_, err := b.cache.ExecuteWithLock(ctx, replayCacheKey(accountID), replayLockExpiry, replayLockWait, replayLockRetry, func(ctx context.Context) error {
		notifications, err := b.GetNotifications(ctx, accountID)
		if err != nil {
			return err
		}
		filtered := notifications[:0]
		for _, item := range notifications {
			if item.Id != n.Id {
				filtered = append(filtered, item)
			}
		}
		filtered = append(filtered, *n)
		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].CreatedAt.Time().After(filtered[j].CreatedAt.Time())
		})
		if len(filtered) > maxReplayNotifications {
			filtered = filtered[:maxReplayNotifications]
		}
		return b.cache.SetData(ctx, replayCacheKey(accountID), filtered, "List`1", replayTtl)
	})
	return err
}

// RemoveNotifications removes the given ids under the lock (no-op when the
// id set is empty or nothing matched).
func (b *SopNotificationReplayBuffer) RemoveNotifications(ctx context.Context, accountID uuid.UUID, ids []uuid.UUID) error {
	removeSet := map[uuid.UUID]struct{}{}
	for _, id := range ids {
		removeSet[id] = struct{}{}
	}
	if len(removeSet) == 0 {
		return nil
	}
	_, err := b.cache.ExecuteWithLock(ctx, replayCacheKey(accountID), replayLockExpiry, replayLockWait, replayLockRetry, func(ctx context.Context) error {
		notifications, err := b.GetNotifications(ctx, accountID)
		if err != nil {
			return err
		}
		remaining := notifications[:0]
		for _, item := range notifications {
			if _, ok := removeSet[item.Id]; !ok {
				remaining = append(remaining, item)
			}
		}
		if len(remaining) == len(notifications) {
			return nil // nothing removed (the C# skips the write)
		}
		return b.cache.SetData(ctx, replayCacheKey(accountID), remaining, "List`1", replayTtl)
	})
	return err
}

// NormalizeSopDeviceId mirrors PushService.NormalizeSopDeviceId: strips the
// ":sop" suffix.
func NormalizeSopDeviceId(deviceId string) string {
	const sopSuffix = ":sop"
	if strings.HasSuffix(deviceId, sopSuffix) {
		return deviceId[:len(deviceId)-len(sopSuffix)]
	}
	return deviceId
}
