package chat

import (
	"sort"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/localization"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/model"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/store"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
)

// maxRedirectMessages mirrors ChatController.RedirectMessages' limit.
const maxRedirectMessages = 100

type redirectMessagesRequest struct {
	MessageIDs []string `json:"message_ids"`
}

// redirectMessages ports ChatController.RedirectMessages: it freezes the
// selected source history into `meta.redirect` on one new destination message.
func (i *instance) redirectMessages(c *gin.Context) {
	ctx := c.Request.Context()
	destinationRoomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	user := currentUser(c)
	if user == nil {
		httpx.Unauthorized(c)
		return
	}
	var request redirectMessagesRequest
	if !httpx.BindJSON(c, &request) {
		return
	}

	messageIDs := make([]uuid.UUID, 0, len(request.MessageIDs))
	seen := map[uuid.UUID]bool{}
	for _, raw := range request.MessageIDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			failBadRequest(c, "CHAT_REDIRECT_MESSAGES_NOT_FOUND", "One or more selected messages do not exist.")
			return
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		messageIDs = append(messageIDs, id)
	}
	if len(messageIDs) == 0 {
		failBadRequest(c, "CHAT_REDIRECT_NO_MESSAGES", "You need to select at least one message to redirect.")
		return
	}
	if len(messageIDs) > maxRedirectMessages {
		failBadRequest(c, "CHAT_REDIRECT_TOO_MANY", "You can redirect up to 100 messages at once.")
		return
	}

	member, err := i.activeMember(ctx, destinationRoomID, user.GetId())
	if err != nil {
		failForbidden(c, "You need to be a member to redirect messages here.")
		return
	}
	destinationRoom, err := i.st.GetRoom(ctx, destinationRoomID)
	if err != nil {
		failNotFound(c)
		return
	}
	if destinationRoom.EncryptionMode != int(model.ChatEncryptionNone) {
		failBadRequest(c, "CHAT_REDIRECT_ENCRYPTED_DEST", "Redirect is not supported for encrypted chat rooms.")
		return
	}

	var entities []*store.ChatMessageEntity
	if err := i.db.WithContext(ctx).Where("id IN ?", messageIDs).Find(&entities).Error; err != nil {
		failInternal(c, err)
		return
	}
	if len(entities) != len(messageIDs) {
		failBadRequest(c, "CHAT_REDIRECT_MESSAGES_NOT_FOUND", "One or more selected messages do not exist.")
		return
	}
	sort.SliceStable(entities, func(a, b int) bool {
		if entities[a].CreatedAt.Equal(entities[b].CreatedAt) {
			return entities[a].ID.String() < entities[b].ID.String()
		}
		return entities[a].CreatedAt.Before(entities[b].CreatedAt)
	})

	sourceRoomIDs := make([]uuid.UUID, 0, 1)
	for _, entity := range entities {
		if entity.Type != "text" {
			failBadRequest(c, "CHAT_REDIRECT_ONLY_TEXT", "Only regular text messages can be redirected right now.")
			return
		}
		if len(sourceRoomIDs) == 0 || sourceRoomIDs[0] != entity.ChatRoomID {
			sourceRoomIDs = append(sourceRoomIDs, entity.ChatRoomID)
		}
	}
	if len(sourceRoomIDs) != 1 {
		failBadRequest(c, "CHAT_REDIRECT_SINGLE_SOURCE", "You can only redirect a section of history from one source chat room at a time.")
		return
	}
	sourceRoomID := sourceRoomIDs[0]
	sourceRoom, err := i.st.GetRoom(ctx, sourceRoomID)
	if err != nil {
		failBadRequest(c, "CHAT_REDIRECT_MESSAGES_NOT_FOUND", "One or more selected messages do not exist.")
		return
	}
	if sourceRoom.EncryptionMode != int(model.ChatEncryptionNone) {
		failBadRequest(c, "CHAT_REDIRECT_ENCRYPTED_SOURCE", "Redirect is not supported for encrypted source messages.")
		return
	}

	var sourceMembershipCount int64
	if err := i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).
		Where("account_id = ? AND chat_room_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", member.AccountID, sourceRoomID).
		Count(&sourceMembershipCount).Error; err != nil {
		failInternal(c, err)
		return
	}
	if sourceMembershipCount == 0 {
		failForbidden(c, "You can only redirect messages from chat rooms you are currently in.")
		return
	}

	senderIDs := make([]uuid.UUID, 0, len(entities))
	senderSeen := map[uuid.UUID]bool{}
	for _, entity := range entities {
		if senderSeen[entity.SenderID] {
			continue
		}
		senderSeen[entity.SenderID] = true
		senderIDs = append(senderIDs, entity.SenderID)
	}
	var senderEntities []*store.ChatMemberEntity
	if err := i.db.WithContext(ctx).Where("id IN ?", senderIDs).Find(&senderEntities).Error; err != nil {
		failInternal(c, err)
		return
	}
	senderModels := i.memberModels(ctx, senderEntities)
	senders := make(map[string]model.ChatMember, len(senderModels))
	for _, sender := range senderModels {
		senders[sender.ID] = sender
	}
	redirectors := i.memberModels(ctx, []*store.ChatMemberEntity{member})
	redirector := model.ChatMember{ID: member.ID.String(), ChatRoomID: destinationRoomID.String(), AccountID: member.AccountID.String()}
	if len(redirectors) > 0 {
		redirector = redirectors[0]
	}

	messages := make([]model.ChatMessage, 0, len(entities))
	for _, entity := range entities {
		messages = append(messages, store.MessageToModel(entity))
	}
	snapshot := buildRedirectSnapshot(
		messages,
		store.RoomToModel(sourceRoom),
		senders,
		redirector,
		store.RoomToModel(destinationRoom),
	)

	now := utcNow()
	content := localization.Localize(user.GetLanguage(), "chatRedirectHistory", nil)
	entity := &store.ChatMessageEntity{
		ID:                 uuid.New(),
		Type:               "text",
		SenderID:           member.ID,
		ChatRoomID:         destinationRoom.ID,
		Content:            &content,
		Meta:               store.ToJSON(map[string]any{"redirect": snapshot}),
		Attachments:        store.ToJSON([]model.CloudFileReferenceObject{}),
		MembersMentioned:   store.ToJSON([]string{}),
		ReactionsCount:     store.ToJSON(map[string]int{}),
		ForwardedMessageID: &entities[0].ID,
	}
	entity.CreatedAt = now
	entity.UpdatedAt = now
	if err := i.insertMessage(ctx, i.db, entity); err != nil {
		failInternal(c, err)
		return
	}
	// Sending implies read-through for the sender.
	_ = i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).
		Where("id = ?", member.ID).
		Update("last_read_at", entity.CreatedAt).Error

	i.deliverMessage(ctx, destinationRoom, member, entity, "messages.new", true)
	messagesOut := i.hydrateMessages(ctx, []*store.ChatMessageEntity{entity}, member.AccountID.String())
	httpx.OK(c, messagesOut[0])
}

// --- snapshot -------------------------------------------------------------

func redirectMillis(value *model.Time) any {
	if value == nil {
		return nil
	}
	return value.Time().UnixMilli()
}

func redirectFileSnapshot(ref model.CloudFileReferenceObject) map[string]any {
	out := map[string]any{
		"id":              ref.Id,
		"name":            ref.Name,
		"mime_type":       ref.MimeType,
		"size":            ref.Size,
		"url":             ref.Url,
		"has_compression": ref.HasCompression,
		"blurhash":        ref.Blurhash,
	}
	if ref.Width != nil {
		out["width"] = *ref.Width
	} else {
		out["width"] = nil
	}
	if ref.Height != nil {
		out["height"] = *ref.Height
	} else {
		out["height"] = nil
	}
	return out
}

func redirectFilesSnapshot(refs []model.CloudFileReferenceObject) []map[string]any {
	out := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		out = append(out, redirectFileSnapshot(ref))
	}
	return out
}

func redirectRoomTypeName(value int) string {
	if value == int(model.ChatRoomDirectMessage) {
		return "DirectMessage"
	}
	return "Group"
}

func redirectEncryptionName(value int) string {
	if value == int(model.ChatEncryptionE2eeMls) {
		return "E2eeMls"
	}
	return "None"
}

func redirectNotifyName(value int) string {
	switch value {
	case int(model.ChatNotifyMentions):
		return "Mentions"
	case int(model.ChatNotifyNone):
		return "None"
	default:
		return "All"
	}
}

func redirectRoomSnapshot(room model.ChatRoom) map[string]any {
	return map[string]any{
		"id":              room.ID,
		"name":            room.Name,
		"description":     room.Description,
		"type":            redirectRoomTypeName(room.Type),
		"is_community":    room.IsCommunity,
		"is_public":       room.IsPublic,
		"encryption_mode": redirectEncryptionName(room.EncryptionMode),
		"realm_id":        room.RealmID,
		"account_id":      room.AccountID,
		"picture":         room.Picture,
		"background":      room.Background,
		"created_at":      redirectMillis(room.CreatedAt),
		"updated_at":      redirectMillis(room.UpdatedAt),
	}
}

func redirectMemberName(member model.ChatMember) string {
	if member.Nick != nil && *member.Nick != "" {
		return *member.Nick
	}
	if member.RealmNick != nil && *member.RealmNick != "" {
		return *member.RealmNick
	}
	if object, ok := member.Account.(map[string]any); ok {
		if nick, _ := object["nick"].(string); nick != "" {
			return nick
		}
	}
	return "Someone"
}

func redirectSenderSnapshot(member model.ChatMember) map[string]any {
	return map[string]any{
		"id":                      member.ID,
		"chat_room_id":            member.ChatRoomID,
		"account_id":              member.AccountID,
		"nick":                    member.Nick,
		"realm_nick":              member.RealmNick,
		"realm_bio":               member.RealmBio,
		"realm_experience":        member.RealmExperience,
		"realm_level":             member.RealmLevel,
		"realm_leveling_progress": member.RealmLevelingProgress,
		"notify":                  redirectNotifyName(member.Notify),
		"joined_at":               redirectMillis(member.JoinedAt),
		"leave_at":                redirectMillis(member.LeaveAt),
		"created_at":              redirectMillis(member.CreatedAt),
		"updated_at":              redirectMillis(member.UpdatedAt),
		"account":                 member.Account,
	}
}

func redirectSenderMapSnapshot(senders map[string]model.ChatMember) map[string]any {
	out := make(map[string]any, len(senders))
	for id, sender := range senders {
		out[id] = redirectSenderSnapshot(sender)
	}
	return out
}

// redirectMetaClone drops a nested redirect so a snapshot of a redirect
// message does not embed the previous snapshot again.
func redirectMetaClone(meta any) map[string]any {
	out := map[string]any{}
	object, ok := meta.(map[string]any)
	if !ok {
		return out
	}
	for key, value := range object {
		if key == "redirect" {
			continue
		}
		out[key] = value
	}
	return out
}

func redirectMessageEntrySnapshot(message model.ChatMessage) map[string]any {
	content := ""
	if message.Content != nil {
		content = *message.Content
	}
	return map[string]any{
		"id":                   message.ID,
		"type":                 message.Type,
		"content":              content,
		"meta":                 redirectMetaClone(message.Meta),
		"client_message_id":    message.ClientMessageID,
		"edited_at":            redirectMillis(message.EditedAt),
		"replied_message_id":   message.RepliedMessageID,
		"forwarded_message_id": message.ForwardedMessageID,
		"sender_id":            message.SenderID,
		"chat_room_id":         message.ChatRoomID,
		"created_at":           redirectMillis(message.CreatedAt),
		"updated_at":           redirectMillis(message.UpdatedAt),
		"deleted_at":           redirectMillis(message.DeletedAt),
		"attachments":          redirectFilesSnapshot(message.Attachments),
		"reactions_count":      message.ReactionsCount,
	}
}

// buildRedirectSnapshot ports ChatService.BuildRedirectSnapshot: version 1 for
// one source message, version 2 (history segment) for several.
func buildRedirectSnapshot(
	messages []model.ChatMessage,
	sourceRoom model.ChatRoom,
	senders map[string]model.ChatMember,
	redirector model.ChatMember,
	destinationRoom model.ChatRoom,
) map[string]any {
	if len(messages) == 0 {
		return map[string]any{}
	}
	if len(messages) == 1 {
		source := messages[0]
		sender := senders[source.SenderID]
		return map[string]any{
			"version":            1,
			"source_message_id":  source.ID,
			"source_room_id":     sourceRoom.ID,
			"source_room":        redirectRoomSnapshot(sourceRoom),
			"source_sender_id":   sender.AccountID,
			"source_sender_name": redirectMemberName(sender),
			"source_type":        source.Type,
			"source_content":     redirectMessageEntrySnapshot(source)["content"],
			"source_created_at":  redirectMillis(source.CreatedAt),
			"source_attachments": redirectFilesSnapshot(source.Attachments),
			"source_meta":        redirectMetaClone(source.Meta),
			"sender_map":         redirectSenderMapSnapshot(senders),
			"source_message":     redirectMessageEntrySnapshot(source),
			"redirected_by":      redirectSenderSnapshot(redirector),
			"redirected_to_room": redirectRoomSnapshot(destinationRoom),
		}
	}
	entries := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		entries = append(entries, redirectMessageEntrySnapshot(message))
	}
	return map[string]any{
		"version":            2,
		"kind":               "history_segment",
		"source_room_id":     sourceRoom.ID,
		"source_room":        redirectRoomSnapshot(sourceRoom),
		"range":              redirectRangeSnapshot(messages),
		"sender_map":         redirectSenderMapSnapshot(senders),
		"messages":           entries,
		"redirected_by":      redirectSenderSnapshot(redirector),
		"redirected_to_room": redirectRoomSnapshot(destinationRoom),
	}
}

func redirectRangeSnapshot(messages []model.ChatMessage) map[string]any {
	first := messages[0]
	last := messages[len(messages)-1]
	return map[string]any{
		"start_message_id": first.ID,
		"end_message_id":   last.ID,
		"message_count":    len(messages),
		"started_at":       redirectMillis(first.CreatedAt),
		"ended_at":         redirectMillis(last.CreatedAt),
	}
}
