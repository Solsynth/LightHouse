package chat

// Inbound websocket packet consumer. Blade's gateway relays every client packet
// it does not answer itself onto `websocket_<endpoint>`; the C# Messager
// listened on `websocket_messager` for chat commands the HTTP API never
// exposed (send/read/typing/subscribe/placeholder streaming). The wire shape is
// the fleet envelope (Blade's natsWebSocketPacketEvent): a relayed
// `packet_bytes` payload holding the client's `{type, data}` packet.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"src.solsynth.dev/sosys/go/pkg/errs"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/model"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/store"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/authn"
)

const (
	// websocketEventsStream is WebSocketPacketEvent.StreamName in the C# fleet
	// (and the `stream_name` Blade writes into every relayed packet event).
	websocketEventsStream = "websocket_events"
	// websocketMessagerSubject is WebSocketPacketEvent.SubjectPrefix +
	// "messager", the subject the C# Messager listener subscribed to.
	websocketMessagerSubject = "websocket_messager"
	// websocketMessagerConsumer is the durable consumer name. The C# listener
	// was core NATS (UseJetStream = false) and therefore had no consumer name;
	// this one keeps at-least-once delivery without replaying old packets
	// (DeliverPolicy New).
	websocketMessagerConsumer = "messager_websocket_packets"

	subscriptionLockPrefix = "chatroom:sublock:"
)

// websocketPacketEvent mirrors Blade's natsWebSocketPacketEvent and the C#
// WebSocketPacketEvent envelope: snake_case keys and a `packet_bytes` byte
// array (encoding/json accepts the fleet's base64 form).
type websocketPacketEvent struct {
	EventID     string    `json:"event_id"`
	Timestamp   time.Time `json:"timestamp"`
	StreamName  string    `json:"stream_name"`
	AccountID   string    `json:"account_id"`
	DeviceID    string    `json:"device_id"`
	PacketBytes []byte    `json:"packet_bytes"`
}

// websocketPacket is the relayed client packet (`WebSocketPacket` in C#).
type websocketPacket struct {
	Type         string          `json:"type"`
	Data         json.RawMessage `json:"data"`
	Endpoint     string          `json:"endpoint"`
	ErrorMessage string          `json:"error_message"`
}

// startWebSocketConsumer runs the inbound packet consumer until the process
// context is cancelled. A disabled NATS client makes Consume block, so the
// goroutine is a no-op on deployments without an event bus.
func (i *instance) startWebSocketConsumer(ctx context.Context) {
	if i.host == nil || i.host.Core == nil || i.host.Core.NATS == nil {
		return
	}
	nats := i.host.Core.NATS
	go func() {
		err := nats.Consume(ctx, websocketEventsStream, websocketMessagerSubject, websocketMessagerConsumer,
			func(payload []byte) error {
				i.handleWebSocketPacket(ctx, payload)
				// Never NAK: relayed client commands are not idempotent (a
				// replayed messages.send duplicates a message), so a failed
				// handling is logged and dropped.
				return nil
			})
		if err != nil && ctx.Err() == nil {
			i.log.Warn("chat: websocket packet consumer stopped", "error", err)
		}
	}()
}

func (i *instance) handleWebSocketPacket(ctx context.Context, payload []byte) {
	var event websocketPacketEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		i.log.Warn("chat: malformed websocket event", "error", err)
		return
	}
	accountID := strings.TrimSpace(event.AccountID)
	if accountID == "" {
		i.log.Warn("chat: websocket event without account id")
		return
	}
	var packet websocketPacket
	if err := json.Unmarshal(event.PacketBytes, &packet); err != nil {
		i.log.Warn("chat: malformed websocket packet", "account_id", accountID, "error", err)
		return
	}
	switch packet.Type {
	case "messages.send":
		i.wsSendMessage(ctx, accountID, packet.Data)
	case "messages.read":
		i.wsReadMessage(ctx, accountID, packet.Data)
	case "messages.typing":
		i.wsTyping(ctx, accountID, packet.Data)
	case "messages.subscribe":
		i.wsSubscribe(ctx, accountID, event.DeviceID, packet.Data, true)
	case "messages.unsubscribe":
		i.wsSubscribe(ctx, accountID, event.DeviceID, packet.Data, false)
	case "messages.test":
		i.wsTest(ctx, accountID, packet.Data)
	case "messages.placeholder.update":
		i.wsPlaceholderUpdate(ctx, accountID, packet.Data)
	case "messages.placeholder.finalize":
		i.wsPlaceholderFinalize(ctx, accountID, packet.Data)
	default:
		i.wsError(ctx, accountID, fmt.Sprintf("Unhandled websocket packet type: %s", packet.Type))
	}
}

// --- helpers --------------------------------------------------------------

// wsError answers a failed command with an `error` packet addressed to the
// sender's account. The C# service targeted the originating device with the
// message also set as packet error_message; the Realtime port addresses
// accounts, so every device of the sender receives it.
func (i *instance) wsError(ctx context.Context, accountID, message string) {
	i.pushToAccounts(ctx, []string{accountID}, "error", message)
}

// wsField returns the first present property of the packet data object.
func wsField(data json.RawMessage, keys ...string) (json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, false
	}
	for _, key := range keys {
		if raw, ok := object[key]; ok {
			return raw, true
		}
	}
	return nil, false
}

// wsObjectID reads a UUID string property, mirroring the C# Guid parsing.
func wsObjectID(data json.RawMessage, keys ...string) (uuid.UUID, bool) {
	raw, ok := wsField(data, keys...)
	if !ok {
		return uuid.Nil, false
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(strings.TrimSpace(text))
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

func wsHasData(data json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(data))
	return trimmed != "" && trimmed != "null"
}

// wsErrorMessage renders the ApiError body an HTTP handler produced as the
// single-line message an `error` packet carries.
func wsErrorMessage(body []byte) string {
	var apiErr errs.ApiError
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Message != "" {
		return apiErr.Message
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "Failed to process the request."
	}
	return text
}

// --- messages.send --------------------------------------------------------

// wsSendMessage reuses the HTTP send pipeline (validation, persistence,
// messages.new broadcast and push fan-out) by driving the handler with a
// synthetic gin context, then acks the sender with `messages.delivered` like
// ChatService.SendMessageAsync's onPersisted callback.
func (i *instance) wsSendMessage(ctx context.Context, accountID string, data json.RawMessage) {
	if !wsHasData(data) {
		i.wsError(ctx, accountID, "messages.send requires request payload.")
		return
	}
	roomID, ok := wsObjectID(data, "chat_room_id")
	if !ok {
		i.wsError(ctx, accountID, "messages.send requires a valid chat_room_id.")
		return
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/chat/"+roomID.String()+"/messages", bytes.NewReader(data))
	request = request.WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	c, _ := gin.CreateTestContext(recorder)
	c.Request = request
	c.Params = gin.Params{{Key: "id", Value: roomID.String()}}
	c.Set(authn.ContextKeyUser, &gen.DyAccount{Id: accountID})

	i.sendMessage(c)

	if recorder.Code >= http.StatusBadRequest {
		i.log.Debug("chat: websocket send failed", "account_id", accountID, "room_id", roomID, "status", recorder.Code)
		i.wsError(ctx, accountID, wsErrorMessage(recorder.Body.Bytes()))
		return
	}
	i.pushToAccounts(ctx, []string{accountID}, "messages.delivered", json.RawMessage(recorder.Body.Bytes()))
}

// --- messages.read --------------------------------------------------------

func (i *instance) wsReadMessage(ctx context.Context, accountID string, data json.RawMessage) {
	if !wsHasData(data) {
		i.wsError(ctx, accountID, "Mark message as read requires you to provide the ChatRoomId")
		return
	}
	roomID, ok := wsObjectID(data, "chat_room_id", "chatRoomId", "room_id")
	if !ok {
		i.wsError(ctx, accountID, "Invalid request data")
		return
	}
	member, err := i.activeMember(ctx, roomID, accountID)
	if err != nil {
		i.wsError(ctx, accountID, "User is not a member of the chat room.")
		return
	}
	now := utcNow()
	if err := i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).
		Where("id = ?", member.ID).Update("last_read_at", now).Error; err != nil {
		i.log.Warn("chat: mark messages.read failed", "account_id", accountID, "room_id", roomID, "error", err)
		i.wsError(ctx, accountID, "Failed to mark messages as read.")
		return
	}
	payload := map[string]any{
		"chat_room_id": roomID.String(),
		"account_id":   accountID,
		"member_id":    member.ID.String(),
		"last_read_at": model.NewTime(now),
	}
	// The reader's other devices learn about the receipt too (the C# fleet
	// also excluded the originating device; the Realtime port cannot address a
	// device subset).
	i.pushToAccounts(ctx, []string{accountID}, "messages.read", payload)

	room, err := i.st.GetRoom(ctx, roomID)
	if err != nil || !room.IsReadReceiptsPublic {
		return
	}
	peers := i.subscribedAccountIDs(ctx, roomID, accountID)
	i.pushToAccounts(ctx, peers, "messages.read", payload)
}

// --- messages.typing ------------------------------------------------------

type wsTypingRequest struct {
	ChatRoomID string   `json:"chat_room_id"`
	Ts         *int64   `json:"ts"`
	Type       *string  `json:"type"`
	Progress   *float64 `json:"progress"`
}

type wsRoomRequest struct {
	ChatRoomID string `json:"chat_room_id"`
}

func (i *instance) wsTyping(ctx context.Context, accountID string, data json.RawMessage) {
	if !wsHasData(data) {
		i.wsError(ctx, accountID, "messages.typing requires you to provide the ChatRoomId")
		return
	}
	var request wsTypingRequest
	if err := json.Unmarshal(data, &request); err != nil {
		i.wsError(ctx, accountID, "Invalid request data")
		return
	}
	typingType := "typing"
	if request.Type != nil && strings.TrimSpace(*request.Type) != "" {
		typingType = strings.ToLower(strings.TrimSpace(*request.Type))
	}
	switch typingType {
	case "typing", "speaking", "uploading":
	default:
		i.wsError(ctx, accountID, "messages.typing type must be typing, speaking, or uploading.")
		return
	}
	progress := 0.0
	if request.Progress != nil {
		if *request.Progress < 0 || *request.Progress > 1 {
			i.wsError(ctx, accountID, "messages.typing progress must be between 0 and 1.")
			return
		}
		progress = *request.Progress
	}
	roomID, err := uuid.Parse(strings.TrimSpace(request.ChatRoomID))
	if err != nil {
		i.wsError(ctx, accountID, "User is not a member of the chat room.")
		return
	}
	timestamp := utcNow()
	if request.Ts != nil {
		timestamp = time.UnixMilli(*request.Ts).UTC()
	}
	member, err := i.activeMember(ctx, roomID, accountID)
	if err != nil {
		i.wsError(ctx, accountID, "User is not a member of the chat room.")
		return
	}
	var sender any
	if models := i.memberModels(ctx, []*store.ChatMemberEntity{member}); len(models) > 0 {
		sender = models[0]
	}
	payload := map[string]any{
		"room_id":   member.ChatRoomID.String(),
		"sender_id": member.ID.String(),
		"sender":    sender,
		"timestamp": model.NewTime(timestamp),
		"type":      typingType,
		"progress":  progress,
	}
	i.pushToAccounts(ctx, i.subscribedAccountIDs(ctx, roomID, accountID), "messages.typing", payload)
}

// --- messages.subscribe / messages.unsubscribe ----------------------------

func (i *instance) wsSubscribe(ctx context.Context, accountID, deviceID string, data json.RawMessage, subscribe bool) {
	action := "messages.subscribe"
	if !subscribe {
		action = "messages.unsubscribe"
	}
	if !wsHasData(data) {
		i.wsError(ctx, accountID, action+" requires you to provide the ChatRoomId")
		return
	}
	var request wsRoomRequest
	if err := json.Unmarshal(data, &request); err != nil {
		i.wsError(ctx, accountID, "Invalid request data")
		return
	}
	roomID, err := uuid.Parse(strings.TrimSpace(request.ChatRoomID))
	if err != nil {
		i.wsError(ctx, accountID, "User is not a member of the chat room.")
		return
	}
	member, err := i.activeMember(ctx, roomID, accountID)
	if err != nil {
		i.wsError(ctx, accountID, "User is not a member of the chat room.")
		return
	}
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		i.log.Debug("chat: subscription packet without device id", "account_id", accountID, "room_id", roomID)
		return
	}
	if subscribe {
		i.subscribeRoomDevice(ctx, member, deviceID)
		return
	}
	i.unsubscribeRoomDevice(ctx, member, deviceID)
}

// mutateRoomSubscriptions applies a read-modify-write on one room's subscriber
// map under the per-member cache lock the C# service used.
func (i *instance) mutateRoomSubscriptions(ctx context.Context, roomID, memberID uuid.UUID, action func(map[string][]cachedDevice)) {
	apply := func(ctx context.Context) error {
		subs := i.roomSubscriptions(ctx, roomID)
		action(subs)
		i.storeRoomSubscriptions(ctx, roomID, subs)
		return nil
	}
	if i.host != nil && i.host.Core.Redis != nil {
		if _, err := i.host.Core.Redis.ExecuteWithLock(ctx,
			subscriptionLockPrefix+roomID.String()+":"+memberID.String(),
			5*time.Second, 5*time.Second, 100*time.Millisecond, apply); err != nil {
			i.log.Warn("chat: subscription lock failed", "room_id", roomID, "member_id", memberID, "error", err)
		}
		return
	}
	_ = apply(ctx)
}

func (i *instance) subscribeRoomDevice(ctx context.Context, member *store.ChatMemberEntity, deviceID string) {
	memberID := member.ID.String()
	expires := utcNow().Add(subscriptionTTL)
	i.mutateRoomSubscriptions(ctx, member.ChatRoomID, member.ID, func(subs map[string][]cachedDevice) {
		devices := subs[memberID]
		replaced := false
		for idx := range devices {
			if devices[idx].DeviceToken == deviceID {
				devices[idx].ExpiresAt = expires
				replaced = true
			}
		}
		if !replaced {
			devices = append(devices, cachedDevice{DeviceToken: deviceID, ExpiresAt: expires})
		}
		subs[memberID] = devices
	})
	i.addAccountSubscription(ctx, member.AccountID.String(), member.ChatRoomID)
}

func (i *instance) unsubscribeRoomDevice(ctx context.Context, member *store.ChatMemberEntity, deviceID string) {
	memberID := member.ID.String()
	removed := false
	i.mutateRoomSubscriptions(ctx, member.ChatRoomID, member.ID, func(subs map[string][]cachedDevice) {
		devices := subs[memberID]
		remaining := make([]cachedDevice, 0, len(devices))
		for _, device := range devices {
			if device.DeviceToken != deviceID {
				remaining = append(remaining, device)
			}
		}
		if len(remaining) == 0 {
			delete(subs, memberID)
			removed = true
			return
		}
		subs[memberID] = remaining
	})
	if removed && !i.accountStillSubscribed(ctx, member.ChatRoomID, member.AccountID, member.ID) {
		i.removeAccountSubscription(ctx, member.AccountID.String(), member.ChatRoomID)
	}
}

// accountStillSubscribed reports whether another member of the same account
// keeps an active subscription in the room.
func (i *instance) accountStillSubscribed(ctx context.Context, roomID, accountID, exceptMemberID uuid.UUID) bool {
	for memberID, devices := range i.roomSubscriptions(ctx, roomID) {
		if memberID == exceptMemberID.String() || len(activeDevices(devices)) == 0 {
			continue
		}
		id, err := uuid.Parse(memberID)
		if err != nil {
			continue
		}
		var member store.ChatMemberEntity
		if err := i.db.WithContext(ctx).Where("id = ? AND account_id = ?", id, accountID).First(&member).Error; err == nil {
			return true
		}
	}
	return false
}

// subscribedAccountIDs resolves the accounts of the room's currently subscribed
// members (ChatRoomService.GetSubscribedMembers), minus the excluded account.
func (i *instance) subscribedAccountIDs(ctx context.Context, roomID uuid.UUID, excludeAccountID string) []string {
	memberIDs := make([]uuid.UUID, 0)
	for memberID, devices := range i.roomSubscriptions(ctx, roomID) {
		if len(activeDevices(devices)) == 0 {
			continue
		}
		if id, err := uuid.Parse(memberID); err == nil {
			memberIDs = append(memberIDs, id)
		}
	}
	if len(memberIDs) == 0 {
		return nil
	}
	var members []*store.ChatMemberEntity
	if err := i.db.WithContext(ctx).
		Where("id IN ? AND joined_at IS NOT NULL AND leave_at IS NULL", memberIDs).
		Find(&members).Error; err != nil {
		i.log.Warn("chat: resolving subscribed members failed", "room_id", roomID, "error", err)
		return nil
	}
	accounts := make([]string, 0, len(members))
	seen := map[string]bool{}
	for _, member := range members {
		accountID := member.AccountID.String()
		if accountID == excludeAccountID || seen[accountID] {
			continue
		}
		seen[accountID] = true
		accounts = append(accounts, accountID)
	}
	sort.Strings(accounts)
	return accounts
}

// --- messages.test --------------------------------------------------------

func (i *instance) wsTest(ctx context.Context, accountID string, data json.RawMessage) {
	payload := data
	if !wsHasData(data) {
		payload = json.RawMessage(`{}`)
	}
	i.pushToAccounts(ctx, []string{accountID}, "messages.test", payload)
}

// --- messages.placeholder.update / .finalize ------------------------------

type wsPlaceholderUpdateRequest struct {
	MessageID    string   `json:"message_id"`
	ContentChunk *string  `json:"content_chunk"`
	Progress     *float64 `json:"progress"`
}

type wsPlaceholderFinalizeRequest struct {
	MessageID     string   `json:"message_id"`
	Content       *string  `json:"content"`
	AttachmentsID []string `json:"attachments_id"`
}

// wsPlaceholder loads a live placeholder sent by the packet's account.
func (i *instance) wsPlaceholder(ctx context.Context, accountID string, messageID uuid.UUID, action string) (*store.ChatMessageEntity, *store.ChatMemberEntity) {
	var entity store.ChatMessageEntity
	if err := i.db.WithContext(ctx).Where("id = ?", messageID).First(&entity).Error; err != nil || entity.Type != "placeholder" {
		i.wsError(ctx, accountID, "Placeholder message not found.")
		return nil, nil
	}
	member, err := i.activeMember(ctx, entity.ChatRoomID, accountID)
	if err != nil || member.ID != entity.SenderID {
		i.wsError(ctx, accountID, "You can only "+action+" your own placeholders.")
		return nil, nil
	}
	return &entity, member
}

func (i *instance) wsPlaceholderUpdate(ctx context.Context, accountID string, data json.RawMessage) {
	if !wsHasData(data) {
		i.wsError(ctx, accountID, "messages.placeholder.update requires request payload.")
		return
	}
	var request wsPlaceholderUpdateRequest
	if err := json.Unmarshal(data, &request); err != nil {
		i.wsError(ctx, accountID, "messages.placeholder.update requires a valid message_id.")
		return
	}
	messageID, err := uuid.Parse(strings.TrimSpace(request.MessageID))
	if err != nil {
		i.wsError(ctx, accountID, "messages.placeholder.update requires a valid message_id.")
		return
	}
	entity, _ := i.wsPlaceholder(ctx, accountID, messageID, "update")
	if entity == nil {
		return
	}
	meta := messageMeta(entity)
	switch placeholderString(meta, "placeholder_kind") {
	case placeholderTypeStreaming:
		if request.ContentChunk != nil {
			meta["placeholder_content"] = placeholderString(meta, "placeholder_content") + *request.ContentChunk
		}
	case placeholderTypeUploading:
		if request.Progress != nil {
			meta["placeholder_progress"] = math.Min(1, math.Max(0, *request.Progress))
		}
	}
	meta["placeholder_expires_at"] = utcNow().Add(placeholderTTL).UnixMilli()
	if err := i.db.WithContext(ctx).Model(&store.ChatMessageEntity{}).
		Where("id = ?", entity.ID).Update("meta", store.ToJSON(meta)).Error; err != nil {
		i.log.Warn("chat: placeholder update failed", "message_id", entity.ID, "error", err)
		i.wsError(ctx, accountID, "Failed to update placeholder.")
		return
	}
	entity.Meta = store.ToJSON(meta)
	i.broadcast(ctx, entity.ChatRoomID, "messages.placeholder.update", i.messageModel(ctx, entity))
}

func (i *instance) wsPlaceholderFinalize(ctx context.Context, accountID string, data json.RawMessage) {
	if !wsHasData(data) {
		i.wsError(ctx, accountID, "messages.placeholder.finalize requires request payload.")
		return
	}
	var request wsPlaceholderFinalizeRequest
	if err := json.Unmarshal(data, &request); err != nil {
		i.wsError(ctx, accountID, "messages.placeholder.finalize requires a valid message_id.")
		return
	}
	messageID, err := uuid.Parse(strings.TrimSpace(request.MessageID))
	if err != nil {
		i.wsError(ctx, accountID, "messages.placeholder.finalize requires a valid message_id.")
		return
	}
	entity, member := i.wsPlaceholder(ctx, accountID, messageID, "finalize")
	if entity == nil {
		return
	}
	var attachments []model.CloudFileReferenceObject
	if len(request.AttachmentsID) > 0 {
		attachments, err = i.resolveAttachments(ctx, request.AttachmentsID)
		if err != nil {
			i.log.Warn("chat: placeholder attachments failed", "message_id", entity.ID, "error", err)
			i.wsError(ctx, accountID, "Failed to finalize placeholder.")
			return
		}
	}
	room, err := i.st.GetRoom(ctx, entity.ChatRoomID)
	if err != nil {
		i.log.Warn("chat: placeholder room lookup failed", "room_id", entity.ChatRoomID, "error", err)
		i.wsError(ctx, accountID, "Failed to finalize placeholder.")
		return
	}
	content := placeholderString(messageMeta(entity), "placeholder_content")
	if request.Content != nil {
		content = *request.Content
	}
	i.finalizePlaceholderWith(ctx, room, member, entity, content, attachments)
	if entity.Type != "text" {
		i.wsError(ctx, accountID, "Failed to finalize placeholder.")
		return
	}
	i.pushToAccounts(ctx, []string{accountID}, "messages.placeholder.finalize", i.messageModel(ctx, entity))
}
