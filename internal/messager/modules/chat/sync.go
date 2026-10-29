package chat

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/model"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/store"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
)

// --- unread ---------------------------------------------------------------

type unreadSummary struct {
	count     int
	hasUnread bool
}

func (i *instance) unreadForAccount(ctx context.Context, accountID string, roomIDs []uuid.UUID) map[string]unreadSummary {
	out := map[string]unreadSummary{}
	if accountID == "" {
		return out
	}
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return out
	}
	query := i.db.WithContext(ctx).Where("account_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", accountUUID)
	if len(roomIDs) > 0 {
		query = query.Where("chat_room_id IN ?", roomIDs)
	}
	var members []*store.ChatMemberEntity
	if err := query.Find(&members).Error; err != nil {
		return out
	}
	for _, member := range members {
		summary := unreadSummary{}
		base := i.db.WithContext(ctx).Model(&store.ChatMessageEntity{}).
			Where("chat_room_id = ? AND sender_id <> ?", member.ChatRoomID, member.ID).
			Where("type <> 'placeholder' AND type NOT LIKE 'messages.%'")
		if member.LastReadAt != nil {
			base = base.Where("created_at > ?", *member.LastReadAt)
		}
		var hasUnread int64
		_ = base.Count(&hasUnread).Error
		summary.hasUnread = hasUnread > 0
		switch member.Notify {
		case int(model.ChatNotifyNone):
			summary.count = 0
		case int(model.ChatNotifyMentions):
			var count int64
			_ = base.Where("members_mentioned @> ?", `["`+accountID+`"]`).Count(&count).Error
			summary.count = int(count)
		default:
			summary.count = int(hasUnread)
		}
		out[member.ChatRoomID.String()] = summary
	}
	return out
}

func (i *instance) lastMessagesForAccount(ctx context.Context, accountID string) map[string]*model.ChatMessage {
	out := map[string]*model.ChatMessage{}
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return out
	}
	var members []*store.ChatMemberEntity
	_ = i.db.WithContext(ctx).
		Where("account_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", accountUUID).
		Find(&members).Error
	for _, member := range members {
		if message := i.latestMessage(ctx, member.ChatRoomID, accountID); message != nil {
			out[member.ChatRoomID.String()] = message
		}
	}
	return out
}

func (i *instance) getChatSummary(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := currentAccountID(c)
	unread := i.unreadForAccount(ctx, accountID, nil)
	last := i.lastMessagesForAccount(ctx, accountID)
	keys := map[string]bool{}
	for key := range unread {
		keys[key] = true
	}
	for key := range last {
		keys[key] = true
	}
	result := map[string]model.ChatRoomSummary{}
	for key := range keys {
		summary := model.ChatRoomSummary{}
		if value, ok := unread[key]; ok {
			summary.UnreadCount = value.count
			summary.HasUnread = value.hasUnread
		}
		summary.LastMessage = last[key]
		result[key] = summary
	}
	httpx.OK(c, result)
}

func (i *instance) getTotalUnread(c *gin.Context) {
	unread := i.unreadForAccount(c.Request.Context(), currentAccountID(c), nil)
	total := 0
	for _, value := range unread {
		total += value.count
	}
	httpx.OK(c, total)
}

func (i *instance) markAllRead(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := currentAccountID(c)
	now := utcNow()
	var members []*store.ChatMemberEntity
	_ = i.db.WithContext(ctx).
		Where("account_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", accountID).
		Find(&members).Error
	if len(members) > 0 {
		_ = i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).
			Where("account_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", accountID).
			Update("last_read_at", now).Error
	}
	payload := map[string]any{
		"chat_room_id": nil,
		"account_id":   accountID,
		"member_id":    nil,
		"last_read_at": model.NewTime(now),
	}
	for _, member := range members {
		payload["chat_room_id"] = member.ChatRoomID.String()
		payload["member_id"] = member.ID.String()
		i.pushToAccounts(ctx, []string{accountID}, "messages.read", payload)
	}
	httpx.OKEmpty(c)
}

// --- subscriptions --------------------------------------------------------

const (
	subscriptionPrefix        = "chatroom:subs:"
	accountSubscriptionPrefix = "chatroom:acctsubs:"
	subscriptionTTL           = 5 * time.Minute
)

type cachedDevice struct {
	DeviceToken string    `json:"device_token"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func subscriptionKey(roomID uuid.UUID) string { return subscriptionPrefix + roomID.String() }

func accountSubscriptionKey(accountID string) string {
	return accountSubscriptionPrefix + accountID
}

func activeDevices(devices []cachedDevice) []cachedDevice {
	now := utcNow()
	byToken := map[string]cachedDevice{}
	for _, device := range devices {
		if !device.ExpiresAt.After(now) {
			continue
		}
		if existing, ok := byToken[device.DeviceToken]; !ok || device.ExpiresAt.After(existing.ExpiresAt) {
			byToken[device.DeviceToken] = device
		}
	}
	out := make([]cachedDevice, 0, len(byToken))
	for _, device := range byToken {
		out = append(out, device)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].DeviceToken < out[b].DeviceToken })
	return out
}

func (i *instance) roomSubscriptions(ctx context.Context, roomID uuid.UUID) map[string][]cachedDevice {
	out := map[string][]cachedDevice{}
	if i.host == nil || i.host.Core.Redis == nil {
		return out
	}
	if _, err := i.host.Core.Redis.Get(ctx, subscriptionKey(roomID), &out); err != nil {
		i.log.Warn("chat: reading room subscriptions failed", "error", err)
	}
	return out
}

func (i *instance) storeRoomSubscriptions(ctx context.Context, roomID uuid.UUID, subs map[string][]cachedDevice) {
	if i.host == nil || i.host.Core.Redis == nil {
		return
	}
	nonEmpty := map[string][]cachedDevice{}
	for memberID, devices := range subs {
		if len(devices) > 0 {
			nonEmpty[memberID] = devices
		}
	}
	if len(nonEmpty) == 0 {
		_ = i.host.Core.Redis.Remove(ctx, subscriptionKey(roomID))
		return
	}
	_ = i.host.Core.Redis.Set(ctx, subscriptionKey(roomID), nonEmpty, subscriptionTTL)
}

func (i *instance) accountSubscriptionRooms(ctx context.Context, accountID string) []uuid.UUID {
	if i.host == nil || i.host.Core.Redis == nil {
		return nil
	}
	var raw []string
	if _, err := i.host.Core.Redis.Get(ctx, accountSubscriptionKey(accountID), &raw); err != nil {
		return nil
	}
	rooms := make([]uuid.UUID, 0, len(raw))
	for _, value := range raw {
		if id, err := uuid.Parse(value); err == nil {
			rooms = append(rooms, id)
		}
	}
	return rooms
}

func (i *instance) addAccountSubscription(ctx context.Context, accountID string, roomID uuid.UUID) {
	if i.host == nil || i.host.Core.Redis == nil {
		return
	}
	rooms := i.accountSubscriptionRooms(ctx, accountID)
	for _, existing := range rooms {
		if existing == roomID {
			return
		}
	}
	rooms = append(rooms, roomID)
	values := make([]string, 0, len(rooms))
	for _, id := range rooms {
		values = append(values, id.String())
	}
	_ = i.host.Core.Redis.Set(ctx, accountSubscriptionKey(accountID), values, subscriptionTTL)
}

func (i *instance) removeAccountSubscription(ctx context.Context, accountID string, roomID uuid.UUID) {
	if i.host == nil || i.host.Core.Redis == nil {
		return
	}
	rooms := i.accountSubscriptionRooms(ctx, accountID)
	values := make([]string, 0, len(rooms))
	for _, existing := range rooms {
		if existing != roomID {
			values = append(values, existing.String())
		}
	}
	if len(values) == 0 {
		_ = i.host.Core.Redis.Remove(ctx, accountSubscriptionKey(accountID))
		return
	}
	_ = i.host.Core.Redis.Set(ctx, accountSubscriptionKey(accountID), values, subscriptionTTL)
}

func wireDevices(devices []cachedDevice) []deviceSubscription {
	out := make([]deviceSubscription, 0, len(devices))
	for _, device := range devices {
		expires := device.ExpiresAt.UTC()
		out = append(out, deviceSubscription{DeviceToken: device.DeviceToken, ExpiresAt: model.NewTime(expires)})
	}
	return out
}

func (i *instance) getRoomSubscriptions(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	accountID := currentAccountID(c)
	if !i.isChatMember(ctx, roomID, accountID) {
		failForbidden(c, "You are not a member of this chat room.")
		return
	}
	subs := i.roomSubscriptions(ctx, roomID)
	if len(subs) == 0 {
		httpx.OK(c, []roomSubscriptionEntry{})
		return
	}
	memberIDs := make([]uuid.UUID, 0, len(subs))
	for memberID := range subs {
		if id, err := uuid.Parse(memberID); err == nil {
			memberIDs = append(memberIDs, id)
		}
	}
	var entities []*store.ChatMemberEntity
	_ = i.db.WithContext(ctx).Where("id IN ?", memberIDs).Find(&entities).Error
	members := map[string]model.ChatMember{}
	for _, member := range i.memberModels(ctx, entities) {
		members[member.ID] = member
	}
	entries := make([]roomSubscriptionEntry, 0, len(subs))
	for memberID, devices := range subs {
		member, ok := members[memberID]
		if !ok {
			continue
		}
		active := activeDevices(devices)
		if len(active) == 0 {
			continue
		}
		entries = append(entries, roomSubscriptionEntry{
			RoomID:    roomID.String(),
			MemberID:  memberID,
			AccountID: member.AccountID,
			Member:    member,
			Devices:   wireDevices(active),
		})
	}
	httpx.OK(c, entries)
}

func (i *instance) getMyAccountSubscriptions(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := currentAccountID(c)
	roomIDs := i.accountSubscriptionRooms(ctx, accountID)
	entries := make([]accountSubscriptionEntry, 0, len(roomIDs))
	if len(roomIDs) == 0 {
		httpx.OK(c, entries)
		return
	}
	var roomEntities []*store.ChatRoomEntity
	_ = i.db.WithContext(ctx).Where("id IN ?", roomIDs).Find(&roomEntities).Error
	rooms := i.hydrateRooms(ctx, roomEntities, accountID)
	roomMap := map[string]model.ChatRoom{}
	for _, room := range rooms {
		roomMap[room.ID] = room
	}
	for _, roomID := range roomIDs {
		subs := i.roomSubscriptions(ctx, roomID)
		room, ok := roomMap[roomID.String()]
		if !ok {
			continue
		}
		for memberID, devices := range subs {
			var member store.ChatMemberEntity
			if err := i.db.WithContext(ctx).Where("id = ? AND account_id = ?", memberID, accountID).First(&member).Error; err != nil {
				continue
			}
			active := activeDevices(devices)
			if len(active) == 0 {
				continue
			}
			entries = append(entries, accountSubscriptionEntry{
				RoomID:   roomID.String(),
				MemberID: memberID,
				Room:     room,
				Devices:  wireDevices(active),
			})
		}
	}
	httpx.OK(c, entries)
}

func (i *instance) getMyChatStatus(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := currentAccountID(c)
	roomIDs := i.accountSubscriptionRooms(ctx, accountID)
	hasConnection := i.websocketConnected(ctx, "", accountID)
	subscriptions := make([]subscriptionRoomStatus, 0, len(roomIDs))
	for _, roomID := range roomIDs {
		var roomEntity store.ChatRoomEntity
		if err := i.db.WithContext(ctx).Where("id = ?", roomID).First(&roomEntity).Error; err != nil {
			continue
		}
		rooms := i.hydrateRooms(ctx, []*store.ChatRoomEntity{&roomEntity}, accountID)
		subs := i.roomSubscriptions(ctx, roomID)
		var memberID string
		var devices []cachedDevice
		for id, roomDevices := range subs {
			var member store.ChatMemberEntity
			if err := i.db.WithContext(ctx).Where("id = ? AND account_id = ?", id, accountID).First(&member).Error; err == nil {
				memberID = id
				devices = activeDevices(roomDevices)
			}
		}
		if len(devices) == 0 {
			continue
		}
		statuses := make([]subscriptionDeviceStatus, 0, len(devices))
		for _, device := range devices {
			expires := device.ExpiresAt.UTC()
			statuses = append(statuses, subscriptionDeviceStatus{
				DeviceToken:          device.DeviceToken,
				ExpiresAt:            model.NewTime(expires),
				IsWebSocketConnected: i.websocketConnected(ctx, device.DeviceToken, ""),
			})
		}
		subscriptions = append(subscriptions, subscriptionRoomStatus{
			RoomID:                      roomID.String(),
			MemberID:                    memberID,
			Room:                        rooms[0],
			IsSubscribed:                true,
			PushNotificationsSuppressed: true,
			Devices:                     statuses,
		})
	}
	httpx.OK(c, accountStatusResponse{
		AccountID:                               accountID,
		HasActiveSubscriptions:                  len(subscriptions) > 0,
		HasAnyWebSocketConnection:               hasConnection,
		PushNotificationsMaySendForUnsubscribed: true,
		Subscriptions:                           subscriptions,
	})
}

func (i *instance) websocketConnected(ctx context.Context, deviceID, accountID string) bool {
	if i.blade == nil {
		return false
	}
	request := &gen.DyGetWebsocketConnectionStatusRequest{}
	if deviceID != "" {
		request.Id = &gen.DyGetWebsocketConnectionStatusRequest_DeviceId{DeviceId: deviceID}
	} else {
		request.Id = &gen.DyGetWebsocketConnectionStatusRequest_UserId{UserId: accountID}
	}
	resp, err := i.blade.GetWebsocketConnectionStatus(ctx, request)
	if err != nil {
		return false
	}
	return resp.GetIsConnected()
}

// --- sync -----------------------------------------------------------------

func (i *instance) roomSyncData(c *gin.Context) {
	ctx := c.Request.Context()
	if currentUser(c) == nil {
		httpx.Unauthorized(c)
		return
	}
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	if _, ok := i.readableRoom(ctx, c, roomID); !ok {
		return
	}
	var request syncRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	limit := 1000
	query := i.db.WithContext(ctx).Model(&store.ChatMessageEntity{}).
		Where("chat_room_id = ? AND thread_id IS NULL", roomID)
	sequences := make([]int64, 0, len(request.MissingSequences))
	for _, sequence := range request.MissingSequences {
		if sequence > 0 {
			sequences = append(sequences, sequence)
		}
	}
	ranges := normalizeSequenceRanges(request.MissingSequenceRanges)
	useSequences := len(sequences) > 0 || len(ranges) > 0
	if useSequences {
		parts := []string{}
		args := []any{}
		if len(sequences) > 0 {
			parts = append(parts, "room_sequence IN ?")
			args = append(args, sequences)
		}
		for _, item := range ranges {
			parts = append(parts, "(room_sequence >= ? AND room_sequence <= ?)")
			args = append(args, item.StartSequence, item.EndSequence)
		}
		query = query.Where(strings.Join(parts, " OR "), args...)
	} else {
		since := time.UnixMilli(request.LastSyncTimestamp).UTC()
		query = query.Where("created_at > ?", since)
	}
	var entities []*store.ChatMessageEntity
	if err := query.Order("room_sequence ASC").Order("created_at ASC").Limit(limit).Find(&entities).Error; err != nil {
		failInternal(c, err)
		return
	}
	total := len(entities)
	if total >= limit {
		var count int64
		_ = query.Count(&count).Error
		total = int(count)
	}
	messages := i.hydrateMessages(ctx, entities, currentAccountID(c))
	latest := utcNow()
	if len(messages) > 0 && messages[len(messages)-1].CreatedAt != nil {
		latest = time.Time(*messages[len(messages)-1].CreatedAt)
	}
	c.Header("X-Total", strconv.Itoa(total))
	httpx.OK(c, syncResponse{
		Messages:         messages,
		CurrentTimestamp: model.NewTime(latest),
		TotalCount:       total,
	})
}

type normalizedRange struct {
	StartSequence int64
	EndSequence   int64
}

func normalizeSequenceRanges(ranges []syncSequenceRangeRequest) []normalizedRange {
	items := make([]normalizedRange, 0, len(ranges))
	for _, item := range ranges {
		start, end := item.StartSequence, item.EndSequence
		if start > end {
			start, end = end, start
		}
		if start <= 0 {
			continue
		}
		items = append(items, normalizedRange{start, end})
	}
	sort.Slice(items, func(a, b int) bool {
		if items[a].StartSequence == items[b].StartSequence {
			return items[a].EndSequence < items[b].EndSequence
		}
		return items[a].StartSequence < items[b].StartSequence
	})
	if len(items) <= 1 {
		return items
	}
	merged := []normalizedRange{items[0]}
	for _, item := range items[1:] {
		last := &merged[len(merged)-1]
		if item.StartSequence <= last.EndSequence+1 {
			if item.EndSequence > last.EndSequence {
				last.EndSequence = item.EndSequence
			}
			continue
		}
		merged = append(merged, item)
	}
	return merged
}

func (i *instance) globalSyncData(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := currentAccountID(c)
	var request syncRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	accountUUID, _ := uuid.Parse(accountID)
	var roomIDs []uuid.UUID
	_ = i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).
		Where("account_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", accountUUID).
		Pluck("chat_room_id", &roomIDs).Error
	messages := []model.ChatMessage{}
	if len(roomIDs) > 0 {
		since := time.UnixMilli(request.LastSyncTimestamp).UTC()
		query := i.db.WithContext(ctx).Model(&store.ChatMessageEntity{}).
			Where("chat_room_id IN ? AND thread_id IS NULL", roomIDs).
			Where("created_at > ?", since)
		if request.LastSyncMessageID != nil && *request.LastSyncMessageID != "" {
			if lastID, err := uuid.Parse(*request.LastSyncMessageID); err == nil {
				query = query.Where("(created_at > ? OR (created_at = ? AND id > ?))", since, since, lastID)
			}
		}
		var entities []*store.ChatMessageEntity
		if err := query.Order("created_at ASC").Order("id ASC").Limit(500).Find(&entities).Error; err != nil {
			failInternal(c, err)
			return
		}
		messages = i.hydrateMessages(ctx, entities, accountID)
	}
	var currentTimestamp *model.Time
	var currentMessageID *string
	latest := utcNow()
	if len(messages) > 0 {
		last := messages[len(messages)-1]
		if last.CreatedAt != nil {
			latest = time.Time(*last.CreatedAt)
		}
		id := last.ID
		currentMessageID = &id
	}
	currentTimestamp = model.NewTime(latest)
	httpx.OK(c, globalSyncResponse{
		Messages:         messages,
		CurrentTimestamp: currentTimestamp,
		CurrentMessageID: currentMessageID,
		TotalCount:       len(messages),
	})
}

func latestModelTime(created, updated time.Time, deleted *time.Time) time.Time {
	value := updated
	if created.After(value) {
		value = created
	}
	if deleted != nil && deleted.After(value) {
		value = *deleted
	}
	return value
}

func (i *instance) syncJoinedRooms(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := currentAccountID(c)
	var request syncRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	accountUUID, _ := uuid.Parse(accountID)
	since := time.UnixMilli(request.LastSyncTimestamp).UTC()
	const limit = 500

	var activeRoomIDs []uuid.UUID
	_ = i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).
		Where("account_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", accountUUID).
		Pluck("chat_room_id", &activeRoomIDs).Error
	activeSet := map[uuid.UUID]bool{}
	for _, id := range activeRoomIDs {
		activeSet[id] = true
	}

	memberQuery := i.db.WithContext(ctx).Unscoped().
		Where("account_id = ?", accountUUID).
		Where("created_at > ? OR updated_at > ? OR deleted_at > ?", since, since, since).
		Order("COALESCE(deleted_at, updated_at) ASC").Limit(limit)
	if request.LastSyncTimestamp == 0 {
		memberQuery = memberQuery.Where("joined_at IS NOT NULL AND leave_at IS NULL")
	}
	var changedMembers []*store.ChatMemberEntity
	_ = memberQuery.Find(&changedMembers).Error
	changedMemberRoomIDs := map[uuid.UUID]bool{}
	for _, member := range changedMembers {
		changedMemberRoomIDs[member.ChatRoomID] = true
	}

	var changedRooms []*store.ChatRoomEntity
	if len(activeRoomIDs) > 0 {
		roomQuery := i.db.WithContext(ctx).Unscoped().
			Where("id IN ?", activeRoomIDs).
			Where("created_at > ? OR updated_at > ? OR deleted_at > ?", since, since, since)
		if len(changedMemberRoomIDs) > 0 {
			excluded := make([]uuid.UUID, 0, len(changedMemberRoomIDs))
			for id := range changedMemberRoomIDs {
				excluded = append(excluded, id)
			}
			roomQuery = roomQuery.Where("id NOT IN ?", excluded)
		}
		_ = roomQuery.Order("COALESCE(deleted_at, updated_at) ASC").Limit(limit).Find(&changedRooms).Error
	}

	type changedMessageRoom struct {
		RoomID    uuid.UUID
		ChangedAt time.Time
	}
	var changedMessageRooms []changedMessageRoom
	if len(activeRoomIDs) > 0 {
		_ = i.db.WithContext(ctx).Model(&store.ChatMessageEntity{}).Unscoped().
			Select("chat_room_id AS room_id, MAX(COALESCE(deleted_at, updated_at)) AS changed_at").
			Where("chat_room_id IN ?", activeRoomIDs).
			Where("created_at > ? OR updated_at > ? OR deleted_at > ?", since, since, since).
			Group("chat_room_id").
			Order("changed_at ASC").Limit(limit).Scan(&changedMessageRooms).Error
	}

	// Rooms attached to member changes (including removed memberships).
	roomIDsToHydrate := []uuid.UUID{}
	for _, member := range changedMembers {
		roomIDsToHydrate = append(roomIDsToHydrate, member.ChatRoomID)
	}
	for _, room := range changedRooms {
		if !room.DeletedAt.Valid {
			roomIDsToHydrate = append(roomIDsToHydrate, room.ID)
		}
	}
	var roomEntities []*store.ChatRoomEntity
	if len(roomIDsToHydrate) > 0 {
		_ = i.db.WithContext(ctx).Where("id IN ?", roomIDsToHydrate).Find(&roomEntities).Error
	}
	hydrated := map[string]model.ChatRoom{}
	for _, room := range i.hydrateRooms(ctx, roomEntities, accountID) {
		hydrated[room.ID] = room
	}

	changes := make([]roomSyncChange, 0)
	memberModels := i.memberModels(ctx, changedMembers)
	for idx, member := range changedMembers {
		changeType := "updated"
		if member.DeletedAt.Valid || member.LeaveAt != nil || member.JoinedAt == nil {
			changeType = "removed"
		} else if member.JoinedAt.After(since) {
			changeType = "joined"
		}
		var deleted *time.Time
		if member.DeletedAt.Valid {
			deleted = &member.DeletedAt.Time
		}
		changedAt := latestModelTime(member.CreatedAt, member.UpdatedAt, deleted)
		change := roomSyncChange{
			RoomID:    member.ChatRoomID.String(),
			Type:      changeType,
			Member:    &memberModels[idx],
			ChangedAt: model.NewTime(changedAt),
		}
		if room, ok := hydrated[member.ChatRoomID.String()]; ok {
			change.Room = &room
		}
		changes = append(changes, change)
	}
	for _, room := range changedRooms {
		changeType := "updated"
		if room.DeletedAt.Valid {
			changeType = "removed"
		}
		var deleted *time.Time
		if room.DeletedAt.Valid {
			deleted = &room.DeletedAt.Time
		}
		change := roomSyncChange{
			RoomID:    room.ID.String(),
			Type:      changeType,
			ChangedAt: model.NewTime(latestModelTime(room.CreatedAt, room.UpdatedAt, deleted)),
		}
		if modelRoom, ok := hydrated[room.ID.String()]; ok {
			change.Room = &modelRoom
		}
		changes = append(changes, change)
	}
	sort.SliceStable(changes, func(a, b int) bool {
		if changes[a].ChangedAt == nil || changes[b].ChangedAt == nil {
			return false
		}
		return time.Time(*changes[a].ChangedAt).Before(time.Time(*changes[b].ChangedAt))
	})
	if len(changes) > limit {
		changes = changes[:limit]
	}

	summaryChangedAt := map[uuid.UUID]time.Time{}
	bump := func(roomID uuid.UUID, value time.Time) {
		if existing, ok := summaryChangedAt[roomID]; !ok || value.After(existing) {
			summaryChangedAt[roomID] = value
		}
	}
	for idx, member := range changedMembers {
		if !activeSet[member.ChatRoomID] {
			continue
		}
		var deleted *time.Time
		if member.DeletedAt.Valid {
			deleted = &member.DeletedAt.Time
		}
		bump(member.ChatRoomID, latestModelTime(member.CreatedAt, member.UpdatedAt, deleted))
		_ = idx
	}
	for _, room := range changedRooms {
		if !activeSet[room.ID] {
			continue
		}
		var deleted *time.Time
		if room.DeletedAt.Valid {
			deleted = &room.DeletedAt.Time
		}
		bump(room.ID, latestModelTime(room.CreatedAt, room.UpdatedAt, deleted))
	}
	for _, item := range changedMessageRooms {
		bump(item.RoomID, item.ChangedAt)
	}
	summaryRoomIDs := make([]uuid.UUID, 0, len(summaryChangedAt))
	for id := range summaryChangedAt {
		summaryRoomIDs = append(summaryRoomIDs, id)
	}
	sort.Slice(summaryRoomIDs, func(a, b int) bool {
		return summaryChangedAt[summaryRoomIDs[a]].Before(summaryChangedAt[summaryRoomIDs[b]])
	})
	if len(summaryRoomIDs) > limit {
		summaryRoomIDs = summaryRoomIDs[:limit]
	}
	var summaryMembers []*store.ChatMemberEntity
	if len(summaryRoomIDs) > 0 {
		_ = i.db.WithContext(ctx).
			Where("chat_room_id IN ? AND account_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", summaryRoomIDs, accountUUID).
			Find(&summaryMembers).Error
	}
	summaryIDs := make([]uuid.UUID, 0, len(summaryMembers))
	summarySet := map[uuid.UUID]bool{}
	for _, member := range summaryMembers {
		if summarySet[member.ChatRoomID] {
			continue
		}
		summarySet[member.ChatRoomID] = true
		summaryIDs = append(summaryIDs, member.ChatRoomID)
	}
	unread := i.unreadForAccount(ctx, accountID, summaryIDs)
	summaries := make([]roomSummarySyncChange, 0, len(summaryIDs))
	for _, roomID := range summaryIDs {
		summary := roomSummarySyncChange{
			RoomID:      roomID.String(),
			ChangedAt:   model.NewTime(summaryChangedAt[roomID]),
			LastMessage: i.latestMessage(ctx, roomID, accountID),
		}
		if value, ok := unread[roomID.String()]; ok {
			summary.UnreadCount = value.count
			summary.HasUnread = value.hasUnread
		}
		summaries = append(summaries, summary)
	}
	sort.SliceStable(summaries, func(a, b int) bool {
		return time.Time(*summaries[a].ChangedAt).Before(time.Time(*summaries[b].ChangedAt))
	})

	watermarks := []time.Time{}
	if len(changes) > 0 && changes[len(changes)-1].ChangedAt != nil {
		watermarks = append(watermarks, time.Time(*changes[len(changes)-1].ChangedAt))
	}
	if len(summaries) > 0 && summaries[len(summaries)-1].ChangedAt != nil {
		watermarks = append(watermarks, time.Time(*summaries[len(summaries)-1].ChangedAt))
	}
	capped := []time.Time{}
	if len(changes) == limit && len(watermarks) > 0 {
		capped = append(capped, watermarks[0])
	}
	if len(summaries) == limit && len(summaries) > 0 {
		capped = append(capped, time.Time(*summaries[len(summaries)-1].ChangedAt))
	}
	latest := utcNow()
	if len(capped) > 0 {
		latest = capped[0]
		for _, value := range capped[1:] {
			if value.Before(latest) {
				latest = value
			}
		}
	} else if len(watermarks) > 0 {
		latest = watermarks[0]
		for _, value := range watermarks[1:] {
			if value.After(latest) {
				latest = value
			}
		}
	}

	httpx.OK(c, roomSyncResponse{
		Changes:           changes,
		Summaries:         summaries,
		Groups:            i.listGroupModels(ctx, accountID),
		CurrentTimestamp:  model.NewTime(latest),
		TotalCount:        len(changes),
		SummaryTotalCount: len(summaries),
	})
}

// --- autocomplete / devices ----------------------------------------------

type autocompleteResponse struct {
	Results []autocompleteResult `json:"results"`
}

type autocompleteResult struct {
	Type    string `json:"type"`
	Keyword string `json:"keyword"`
	Data    string `json:"data"`
}

func (i *instance) autocompleteChat(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	accountID := currentAccountID(c)
	if accountID == "" {
		httpx.Unauthorized(c)
		return
	}
	if !i.isChatMember(ctx, roomID, accountID) {
		failForbidden(c, "You need to be a member to use autocomplete here.")
		return
	}
	var request struct {
		Content string `json:"content"`
	}
	if !httpx.BindJSON(c, &request) {
		return
	}
	results := []autocompletion{}
	if strings.HasPrefix(request.Content, "@") {
		afterAt := request.Content[1:]
		hadSlash := strings.Contains(afterAt, "/")
		kind := "u"
		query := afterAt
		if hadSlash {
			parts := strings.SplitN(afterAt, "/", 2)
			kind = parts[0]
			query = ""
			if len(parts) > 1 {
				query = parts[1]
			}
		}
		if kind == "u" && strings.TrimSpace(query) != "" {
			members := i.activeMemberEntities(ctx, roomID)
			models := i.memberModels(ctx, members)
			for _, member := range models {
				account, ok := member.Account.(map[string]any)
				if !ok {
					continue
				}
				name, _ := account["name"].(string)
				if name == "" || !strings.HasPrefix(strings.ToLower(name), strings.ToLower(query)) {
					continue
				}
				keyword := "@"
				if hadSlash {
					keyword += "u/"
				}
				keyword += name
				results = append(results, autocompletion{Type: "user", Keyword: keyword, Data: member.Account})
				if len(results) >= 10 {
					break
				}
			}
			httpx.OK(c, results)
			return
		}
	}
	if i.sphere != nil {
		raw, err := i.sphere.Autocomplete(ctx, request.Content, roomID.String(), "", 0)
		if err == nil {
			var response autocompleteResponse
			if err := json.Unmarshal(raw, &response); err == nil {
				for _, item := range response.Results {
					var data any
					if item.Data != "" {
						_ = json.Unmarshal([]byte(item.Data), &data)
					}
					results = append(results, autocompletion{Type: item.Type, Keyword: item.Keyword, Data: data})
				}
			}
		}
	}
	httpx.OK(c, results)
}

func (i *instance) deviceJoined(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	accountID := currentAccountID(c)
	if !i.isChatMember(ctx, roomID, accountID) {
		failForbidden(c, "You need to be a member to use this endpoint.")
		return
	}
	room, err := i.st.GetRoom(ctx, roomID)
	if err != nil {
		failNotFound(c)
		return
	}
	if room.EncryptionMode != int(model.ChatEncryptionE2eeMls) {
		failBadRequest(c, "CHAT_DEVICE_MLS_ROOM_REQUIRED", "Room is not an MLS room.")
		return
	}
	if room.MLSGroupID == nil || strings.TrimSpace(*room.MLSGroupID) == "" {
		failBadRequest(c, "CHAT_DEVICE_MLS_GROUP_REQUIRED", "Room does not have MLS group configured.")
		return
	}
	var request deviceJoinedRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	if i.mls != nil {
		if _, err := i.mls.AddMlsDeviceMembership(ctx, &gen.AddMlsDeviceMembershipRequest{
			GroupId:   *room.MLSGroupID,
			AccountId: accountID,
			DeviceId:  request.MlsDeviceID,
			Epoch:     request.Epoch,
		}); err != nil {
			failInternal(c, err)
			return
		}
	}
	httpx.OKEmpty(c)
}

func (i *instance) botCommands(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	if _, ok := i.readableRoom(ctx, c, roomID); !ok {
		return
	}
	// Bot chat manifests are not exposed through any port in this deployment.
	httpx.OK(c, map[string]any{})
}
