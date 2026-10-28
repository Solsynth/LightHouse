package chat

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/model"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/store"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
)

func (i *instance) pinPermission(ctx context.Context, c *gin.Context, room *store.ChatRoomEntity, accountID string) bool {
	if room.RealmID != nil {
		if !i.isRealmModerator(ctx, room.RealmID, accountID) {
			failForbidden(c, "You need at least be a realm moderator to manage pins.")
			return false
		}
		return true
	}
	switch model.ChatRoomType(room.Type) {
	case model.ChatRoomDirectMessage:
		if !i.isChatMember(ctx, room.ID, accountID) {
			failForbidden(c, "You need be part of the DM to manage pins.")
			return false
		}
	case model.ChatRoomGroup:
		if room.AccountID == nil || room.AccountID.String() != accountID {
			failForbidden(c, "You need be the owner to manage pins.")
			return false
		}
	}
	return true
}

func (i *instance) pinMessage(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	accountID := currentAccountID(c)
	var request pinMessageRequest
	if !httpx.BindJSON(c, (&request)) {
		return
	}
	room, err := i.st.GetRoom(ctx, roomID)
	if err != nil {
		failNotFound(c)
		return
	}
	if !i.pinPermission(ctx, c, room, accountID) {
		return
	}
	member, err := i.activeMember(ctx, roomID, accountID)
	if err != nil {
		failForbidden(c, "You need to be a member to pin messages.")
		return
	}
	messageID, err := uuid.Parse(request.MessageID)
	if err != nil {
		failBadRequest(c, "CHAT_PIN_FAILED", "Message not found in this room.")
		return
	}
	var message store.ChatMessageEntity
	if err := i.db.WithContext(ctx).Where("id = ? AND chat_room_id = ?", messageID, roomID).First(&message).Error; err != nil {
		failBadRequest(c, "CHAT_PIN_FAILED", "Message not found in this room.")
		return
	}
	var existing store.ChatMessagePinEntity
	if err := i.db.WithContext(ctx).
		Where("chat_room_id = ? AND message_id = ? AND deleted_at IS NULL", roomID, messageID).
		First(&existing).Error; err == nil {
		failBadRequest(c, "CHAT_PIN_FAILED", "This message is already pinned.")
		return
	}
	var expiresAt *time.Time
	if request.ExpiresAt != nil {
		value := time.Time(*request.ExpiresAt)
		expiresAt = &value
	}
	pin := &store.ChatMessagePinEntity{
		ID:               uuid.New(),
		MessageID:        messageID,
		ChatRoomID:       roomID,
		PinnedByMemberID: member.ID,
		ExpiresAt:        expiresAt,
	}
	if err := i.db.WithContext(ctx).Create(pin).Error; err != nil {
		failBadRequest(c, "CHAT_PIN_FAILED", "This message is already pinned.")
		return
	}
	meta := map[string]any{
		"pin_id":                pin.ID.String(),
		"message_id":            messageID.String(),
		"pinned_by_member_id":   member.ID.String(),
	}
	if expiresAt != nil {
		meta["expires_at"] = expiresAt.UnixMilli()
	}
	i.sendSystemMessage(ctx, room, member, "messages.pinned", "", meta)
	httpx.OK(c, store.PinToModel(pin))
}

func (i *instance) unpinMessage(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	pinID, ok := uuidParam(c, "pinId")
	if !ok {
		return
	}
	accountID := currentAccountID(c)
	room, err := i.st.GetRoom(ctx, roomID)
	if err != nil {
		failNotFound(c)
		return
	}
	if !i.pinPermission(ctx, c, room, accountID) {
		return
	}
	member, err := i.activeMember(ctx, roomID, accountID)
	if err != nil {
		failForbidden(c, "You need to be a member to unpin messages.")
		return
	}
	var pin store.ChatMessagePinEntity
	if err := i.db.WithContext(ctx).
		Where("id = ? AND chat_room_id = ? AND deleted_at IS NULL", pinID, roomID).
		First(&pin).Error; err != nil {
		failBadRequest(c, "CHAT_UNPIN_FAILED", "Pin not found.")
		return
	}
	now := utcNow()
	_ = i.db.WithContext(ctx).Model(&store.ChatMessagePinEntity{}).Where("id = ?", pin.ID).
		Update("deleted_at", now).Error
	i.sendSystemMessage(ctx, room, member, "messages.unpinned", "", map[string]any{
		"pin_id":     pin.ID.String(),
		"message_id": pin.MessageID.String(),
	})
	httpx.NoContent(c)
}

func (i *instance) listPins(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	if _, ok := i.readableRoom(ctx, c, roomID); !ok {
		return
	}
	includeExpired := c.Query("includeExpired") == "true"
	query := i.db.WithContext(ctx).Where("chat_room_id = ? AND deleted_at IS NULL", roomID)
	if !includeExpired {
		query = query.Where("expires_at IS NULL OR expires_at > ?", utcNow())
	}
	var pins []*store.ChatMessagePinEntity
	if err := query.Order("created_at DESC").Find(&pins).Error; err != nil {
		failInternal(c, err)
		return
	}
	messageIDs := make([]uuid.UUID, 0, len(pins))
	pinnerIDs := make([]uuid.UUID, 0, len(pins))
	for _, pin := range pins {
		messageIDs = append(messageIDs, pin.MessageID)
		pinnerIDs = append(pinnerIDs, pin.PinnedByMemberID)
	}
	var messages []*store.ChatMessageEntity
	if len(messageIDs) > 0 {
		_ = i.db.WithContext(ctx).Where("id IN ?", messageIDs).Find(&messages).Error
	}
	var pinners []*store.ChatMemberEntity
	if len(pinnerIDs) > 0 {
		_ = i.db.WithContext(ctx).Where("id IN ?", pinnerIDs).Find(&pinners).Error
	}
	messageModels := i.hydrateMessages(ctx, messages, currentAccountID(c))
	messageMap := map[string]*model.ChatMessage{}
	for idx := range messageModels {
		message := messageModels[idx]
		messageMap[message.ID] = &message
	}
	pinnerModels := i.memberModels(ctx, pinners)
	pinnerMap := map[string]model.ChatMember{}
	for _, member := range pinnerModels {
		pinnerMap[member.ID] = member
	}
	out := make([]model.ChatMessagePin, 0, len(pins))
	for _, pin := range pins {
		modelPin := store.PinToModel(pin)
		if message, ok := messageMap[pin.MessageID.String()]; ok {
			modelPin.Message = message
		}
		if member, ok := pinnerMap[pin.PinnedByMemberID.String()]; ok {
			memberCopy := member
			modelPin.PinnedBy = &memberCopy
		}
		out = append(out, modelPin)
	}
	httpx.OK(c, out)
}
