package chat

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/model"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/store"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
)

// moderateDeleteMessage is ChatRoomController's realm moderation delete: a
// moderator removes any message and the sender is notified.
func (i *instance) moderateDeleteMessage(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "roomId")
	if !ok {
		return
	}
	messageID, ok := uuidParam(c, "messageId")
	if !ok {
		return
	}
	accountID := currentAccountID(c)
	room, err := i.st.GetRoom(ctx, roomID)
	if err != nil {
		failNotFound(c)
		return
	}
	if room.RealmID == nil {
		failForbidden(c, "Chat moderation is only available for realm chat rooms.")
		return
	}
	if !i.hasRealmPermission(ctx, room.RealmID, accountID, "chat.moderate") {
		failForbidden(c, "You do not have permission to moderate chat in this realm.")
		return
	}
	var request deleteRoomMessageRequest
	if c.Request.ContentLength > 0 {
		if !httpx.BindJSON(c, &request) {
			return
		}
	}
	var message store.ChatMessageEntity
	if err := i.db.WithContext(ctx).Where("id = ? AND chat_room_id = ?", messageID, roomID).First(&message).Error; err != nil {
		failNotFound(c)
		return
	}
	now := utcNow()
	content := "[Message deleted by moderator]"
	_ = i.db.WithContext(ctx).Model(&store.ChatMessageEntity{}).Where("id = ?", message.ID).
		Updates(map[string]any{"deleted_at": now, "content": content}).Error

	var sender store.ChatMemberEntity
	if err := i.db.WithContext(ctx).Where("id = ?", message.SenderID).First(&sender).Error; err == nil {
		reason := "No reason provided"
		if request.Reason != nil && *request.Reason != "" {
			reason = *request.Reason
		}
		i.sendPush(ctx, []string{sender.AccountID.String()}, "chat.moderation",
			"Message deleted", "Your message was deleted by a moderator: "+reason+".",
			map[string]string{"room_id": roomID.String(), "message_id": messageID.String()})
	}
	httpx.NoContent(c)
}

// moderateTimeoutMember is the realm moderation timeout endpoint. Unlike the
// room-manager timeout it takes a duration in minutes.
func (i *instance) moderateTimeoutMember(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "roomId")
	if !ok {
		return
	}
	accountParam, ok := uuidParam(c, "accountId")
	if !ok {
		return
	}
	currentID := currentAccountID(c)
	room, err := i.st.GetRoom(ctx, roomID)
	if err != nil {
		failNotFound(c)
		return
	}
	if room.RealmID == nil {
		failForbidden(c, "Chat moderation is only available for realm chat rooms.")
		return
	}
	if !i.hasRealmPermission(ctx, room.RealmID, currentID, "chat.moderate") {
		failForbidden(c, "You do not have permission to moderate chat in this realm.")
		return
	}
	var request timeoutUserRequest
	if !httpx.BindJSON(c, (&request)) {
		return
	}
	var member store.ChatMemberEntity
	if err := i.db.WithContext(ctx).
		Where("chat_room_id = ? AND account_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", roomID, accountParam).
		First(&member).Error; err != nil {
		failNotFound(c)
		return
	}
	now := utcNow()
	until := now.Add(time.Duration(request.DurationMinutes) * time.Minute)
	reason := ""
	if request.Reason != nil {
		reason = *request.Reason
	}
	cause := map[string]any{
		"reason":    reason,
		"type":      int(model.ChatTimeoutModerator),
		"since":     model.NewTime(now),
	}
	_ = i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).Where("id = ?", member.ID).
		Updates(map[string]any{"timeout_until": until, "timeout_cause": store.ToJSON(cause)}).Error
	body := "You were timed out for " + strconv.Itoa(request.DurationMinutes) + " minutes: " + reason + "."
	i.sendPush(ctx, []string{accountParam.String()}, "chat.timeout", "Timed out", body,
		map[string]string{"room_id": roomID.String()})
	httpx.OK(c, gin.H{
		"success":       true,
		"message":       "User timed out",
		"timeout_until": model.NewTime(until),
	})
}

func itoaMinutes(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}
