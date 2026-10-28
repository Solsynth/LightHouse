package chat

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/model"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/store"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
)

const (
	placeholderTypeStreaming = "streaming"
	placeholderTypeUploading = "uploading"
	placeholderTTL           = 60 * time.Minute
)

type sendPlaceholderRequest struct {
	Kind string `json:"kind"`
}

func placeholderString(meta map[string]any, key string) string {
	if value, ok := meta[key]; ok {
		if text, ok := value.(string); ok {
			return text
		}
	}
	return ""
}

func placeholderNumber(meta map[string]any, key string) (float64, bool) {
	value, ok := meta[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return typed, true
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	case int64:
		return float64(typed), true
	case int:
		return float64(typed), true
	}
	return 0, false
}

func (i *instance) sendPlaceholderMessage(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var request sendPlaceholderRequest
	if !httpx.BindJSON(c, (&request)) {
		return
	}
	member, err := i.activeMember(ctx, roomID, currentAccountID(c))
	if err != nil {
		failForbidden(c, "You need to be a member to send messages here.")
		return
	}
	if member.TimeoutUntil != nil && member.TimeoutUntil.After(utcNow()) {
		failForbidden(c, "You has been timed out in this chat.")
		return
	}
	room, err := i.st.GetRoom(ctx, roomID)
	if err != nil {
		failNotFound(c)
		return
	}
	if room.EncryptionMode != int(model.ChatEncryptionNone) {
		failConflict(c, "CHAT_E2EE_PLACEHOLDER_NOT_SUPPORTED", "Placeholder messages are not supported in encrypted rooms.")
		return
	}
	if request.Kind != placeholderTypeStreaming && request.Kind != placeholderTypeUploading {
		failBadRequest(c, "CHAT_PLACEHOLDER_KIND_INVALID", "Invalid placeholder kind. Must be 'streaming' or 'uploading'.")
		return
	}

	// At most one active placeholder per member per room: finalize a running
	// one, expire an empty one.
	var existing store.ChatMessageEntity
	if err := i.db.WithContext(ctx).
		Where("chat_room_id = ? AND sender_id = ? AND type = 'placeholder'", roomID, member.ID).
		First(&existing).Error; err == nil {
		meta := messageMeta(&existing)
		if content := placeholderString(meta, "placeholder_content"); strings.TrimSpace(content) != "" {
			i.finalizePlaceholder(ctx, room, member, &existing, content)
		} else {
			i.expirePlaceholder(ctx, &existing)
		}
	}

	now := utcNow()
	expires := now.Add(placeholderTTL)
	meta := map[string]any{
		"placeholder_kind":       request.Kind,
		"placeholder_expires_at": expires.UnixMilli(),
	}
	if request.Kind == placeholderTypeStreaming {
		meta["placeholder_content"] = ""
	} else {
		meta["placeholder_progress"] = 0.0
	}
	entity := &store.ChatMessageEntity{
		ID:               uuid.New(),
		Type:             "placeholder",
		SenderID:         member.ID,
		ChatRoomID:       roomID,
		Meta:             store.ToJSON(meta),
		Attachments:      store.ToJSON([]model.CloudFileReferenceObject{}),
		MembersMentioned: store.ToJSON([]string{}),
		ReactionsCount:   store.ToJSON(map[string]int{}),
	}
	if err := i.insertMessage(ctx, i.db, entity); err != nil {
		failInternal(c, err)
		return
	}
	i.deliverMessage(ctx, room, member, entity, "messages.new", false)
	messages := i.hydrateMessages(ctx, []*store.ChatMessageEntity{entity}, member.AccountID.String())
	httpx.OK(c, messages[0])
}

func (i *instance) expirePlaceholder(ctx context.Context, entity *store.ChatMessageEntity) {
	now := utcNow()
	_ = i.db.WithContext(ctx).Model(&store.ChatMessageEntity{}).Where("id = ?", entity.ID).
		Updates(map[string]any{"deleted_at": now, "updated_at": now}).Error
	entity.DeletedAt = gormDeletedAt(now)
	i.pushToAccounts(ctx, i.roomAccountIDs(ctx, entity.ChatRoomID), "messages.placeholder.expired", map[string]any{
		"message_id":   entity.ID.String(),
		"chat_room_id": entity.ChatRoomID.String(),
	})
}

func (i *instance) finalizePlaceholder(ctx context.Context, room *store.ChatRoomEntity, sender *store.ChatMemberEntity, entity *store.ChatMessageEntity, content string) {
	i.finalizePlaceholderWith(ctx, room, sender, entity, content, nil)
}

// finalizePlaceholderWith converts a placeholder into a text message and
// delivers the `messages.sync.finalize` shadow message clients apply. A nil
// attachment list keeps the placeholder's existing attachments.
func (i *instance) finalizePlaceholderWith(ctx context.Context, room *store.ChatRoomEntity, sender *store.ChatMemberEntity, entity *store.ChatMessageEntity, content string, attachments []model.CloudFileReferenceObject) {
	meta := messageMeta(entity)
	for _, key := range []string{"placeholder_kind", "placeholder_content", "placeholder_progress", "placeholder_expires_at"} {
		delete(meta, key)
	}
	updates := map[string]any{
		"type":    "text",
		"content": content,
	}
	if attachments != nil {
		updates["attachments"] = store.ToJSON(attachments)
	}
	if len(meta) == 0 {
		updates["meta"] = nil
	} else {
		updates["meta"] = store.ToJSON(meta)
	}
	_ = i.db.WithContext(ctx).Model(&store.ChatMessageEntity{}).Where("id = ?", entity.ID).
		Updates(updates).Error
	if err := i.db.WithContext(ctx).Where("id = ?", entity.ID).First(entity).Error; err != nil {
		return
	}
	syncMeta := messageMeta(entity)
	syncMeta["message_id"] = entity.ID.String()
	syncEntity := &store.ChatMessageEntity{
		ID:             uuid.New(),
		Type:           "messages.sync.finalize",
		ChatRoomID:     entity.ChatRoomID,
		SenderID:       entity.SenderID,
		Content:        entity.Content,
		Attachments:    entity.Attachments,
		Meta:           store.ToJSON(syncMeta),
		ReactionsCount: store.ToJSON(map[string]int{}),
	}
	if err := i.insertMessage(ctx, i.db, syncEntity); err == nil {
		i.deliverMessage(ctx, room, sender, syncEntity, "messages.update", false)
	}
}

// expireStalePlaceholders is the minute job backing PlaceholderExpirationJob.
func (i *instance) expireStalePlaceholders(ctx context.Context) error {
	var placeholders []*store.ChatMessageEntity
	if err := i.db.WithContext(ctx).Where("type = 'placeholder'").Find(&placeholders).Error; err != nil {
		return err
	}
	now := utcNow()
	for _, entity := range placeholders {
		meta := messageMeta(entity)
		expires, ok := placeholderNumber(meta, "placeholder_expires_at")
		if !ok || expires > float64(now.UnixMilli()) {
			continue
		}
		i.expirePlaceholder(ctx, entity)
	}
	return nil
}

func gormDeletedAt(value time.Time) gorm.DeletedAt {
	return gorm.DeletedAt{Time: value, Valid: true}
}
