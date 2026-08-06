package push

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/metoer/internal/model"
)

// marshalNotificationPacket mirrors
// InfraObjectCoder.ConvertObjectToByteString(notification): snake_case JSON
// with nulls included and RFC3339 instants (the packet data inside
// DyWebSocketPacket). Meta keys are NOT snake-cased here (the coder sets no
// DictionaryKeyPolicy).
func marshalNotificationPacket(n *model.Notification) []byte {
	data, err := json.Marshal(n)
	if err != nil {
		return []byte{}
	}
	return data
}

// ListSopNotifications mirrors PushService.ListSopNotifications: the replay
// buffer ∪ DB page, deduplicated by id (newest copy wins), ordered created_at
// DESC, offset/take applied. Total = dbTotal + replayCount − duplicates.
func (s *Service) ListSopNotifications(ctx context.Context, accountID uuid.UUID, offset, take int, appId string) ([]*model.Notification, int, error) {
	replayNotifications, err := s.replay.GetNotifications(ctx, accountID)
	if err != nil {
		return nil, 0, err
	}
	replayIds := make([]uuid.UUID, 0, len(replayNotifications))
	replaySet := map[uuid.UUID]struct{}{}
	for _, n := range replayNotifications {
		if _, ok := replaySet[n.Id]; !ok {
			replaySet[n.Id] = struct{}{}
			replayIds = append(replayIds, n.Id)
		}
	}

	appId = s.ResolveAppId(appId, true)
	defaultApp := s.GetDefaultAppId()
	dbNotifications, dbTotalCount, duplicateCount, err := s.st.ListNotificationsForSop(
		ctx, accountID, &appId, new(defaultApp), offset, take, len(replayNotifications), replayIds)
	if err != nil {
		return nil, 0, err
	}

	merged := map[uuid.UUID]*model.Notification{}
	for i := range replayNotifications {
		merged[replayNotifications[i].Id] = &replayNotifications[i]
	}
	for _, n := range dbNotifications {
		if existing, ok := merged[n.Id]; !ok || n.CreatedAt.Time().After(existing.CreatedAt.Time()) {
			merged[n.Id] = n
		}
	}

	ordered := make([]*model.Notification, 0, len(merged))
	for _, n := range merged {
		ordered = append(ordered, n)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].CreatedAt.Time().After(ordered[j].CreatedAt.Time())
	})

	if offset > len(ordered) {
		offset = len(ordered)
	}
	end := offset + take
	if end > len(ordered) {
		end = len(ordered)
	}
	page := ordered[offset:end]

	return page, dbTotalCount + len(replayNotifications) - duplicateCount, nil
}

// RemoveSopReplayNotifications mirrors PushService.RemoveSopReplayNotifications.
func (s *Service) RemoveSopReplayNotifications(ctx context.Context, accountID uuid.UUID, notificationIds []uuid.UUID) error {
	return s.replay.RemoveNotifications(ctx, accountID, notificationIds)
}

// MarkSopDeliveryReadAsync mirrors PushService.MarkSopDeliveryReadAsync.
func (s *Service) MarkSopDeliveryReadAsync(ctx context.Context, subscriptionID uuid.UUID, notificationIds []uuid.UUID) {
	s.obs.MarkSopDeliveryRead(ctx, subscriptionID, notificationIds)
}

// SaveNotification mirrors PushService.SaveNotification(SnNotification).
func (s *Service) SaveNotification(ctx context.Context, n *model.Notification) error {
	return s.st.SaveNotification(ctx, n)
}

// SaveNotificationBatch mirrors PushService.SaveNotification(SnNotification,
// List<Guid> accounts): inserts one copy per account, copying the
// notification's display fields and timestamps (fresh ids).
func (s *Service) SaveNotificationBatch(ctx context.Context, n *model.Notification, accounts []uuid.UUID) error {
	var rows []*model.Notification
	for _, account := range accounts {
		rows = append(rows, &model.Notification{
			ModelBase: model.ModelBase{Id: uuid.New(), CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt},
			Topic:     n.Topic,
			Content:   n.Content,
			Title:     n.Title,
			Subtitle:  n.Subtitle,
			Meta:      n.Meta,
			Priority:  n.Priority,
			AccountId: account,
		})
	}
	return s.st.BatchInsertNotifications(ctx, rows)
}
