package chat

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/model"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/store"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
)

var defaultReactions = []string{
	"thumb_up", "thumb_down", "cry", "confuse", "hello", "shock", "speechless",
	"salute", "ridicule", "clap", "laugh", "angry", "party", "pray", "heart",
}

func isDefaultReaction(symbol string) bool {
	for _, allowed := range defaultReactions {
		if allowed == symbol {
			return true
		}
	}
	return false
}

// --- list / get -----------------------------------------------------------

func (i *instance) listMessages(c *gin.Context) {
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	room, ok := i.readableRoom(c.Request.Context(), c, roomID)
	if !ok {
		return
	}
	page, ok := httpx.PageQuery(c, 1000)
	if !ok {
		return
	}
	query := i.db.WithContext(c.Request.Context()).
		Model(&store.ChatMessageEntity{}).
		Where("chat_room_id = ? AND thread_id IS NULL", room.ID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		failInternal(c, err)
		return
	}
	var entities []*store.ChatMessageEntity
	if err := query.Order("room_sequence DESC").Order("created_at DESC").
		Offset(page.Offset).Limit(page.Take).Find(&entities).Error; err != nil {
		failInternal(c, err)
		return
	}
	messages := i.hydrateMessages(c.Request.Context(), entities, currentAccountID(c))
	httpx.List(c, messages, total)
	_ = room
}

func (i *instance) getMessage(c *gin.Context) {
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	if _, ok := i.readableRoom(c.Request.Context(), c, roomID); !ok {
		return
	}
	messageID, ok := uuidParam(c, "messageId")
	if !ok {
		return
	}
	var entity store.ChatMessageEntity
	if err := i.db.WithContext(c.Request.Context()).
		Where("id = ? AND chat_room_id = ?", messageID, roomID).First(&entity).Error; err != nil {
		failNotFound(c)
		return
	}
	messages := i.hydrateMessages(c.Request.Context(), []*store.ChatMessageEntity{&entity}, currentAccountID(c))
	httpx.OK(c, messages[0])
}

// --- send -----------------------------------------------------------------

func (i *instance) sendMessage(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	user := currentUser(c)
	if user == nil {
		httpx.Unauthorized(c)
		return
	}
	identity := strings.TrimSpace(c.Query("identity"))
	if identity != "" {
		// Bot identity requires the bot developer/publisher binding, which has
		// no port on this deployment.
		failForbidden(c, "Bot identity is not available on this deployment.")
		return
	}
	member, err := i.activeMember(ctx, roomID, user.GetId())
	if err != nil {
		failForbidden(c, "You need to be a member to send messages here.")
		return
	}
	room, err := i.st.GetRoom(ctx, roomID)
	if err != nil {
		failNotFound(c)
		return
	}
	if room.RealmID != nil && !i.hasRealmPermission(ctx, room.RealmID, member.AccountID.String(), "chat.send") {
		failForbidden(c, "You do not have permission to send messages in this realm.")
		return
	}

	var request sendMessageRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	if !validateSendLengths(c, request) {
		return
	}
	request.Content = sanitizeText(request.Content)
	if !validWKT(request.LocationWKT) {
		failBadRequest(c, "CHAT_MESSAGE_LOCATION_INVALID", "Invalid location WKT.")
		return
	}
	if member.TimeoutUntil != nil && member.TimeoutUntil.After(utcNow()) {
		failForbidden(c, "You has been timed out in this chat.")
		return
	}
	e2ee := room.EncryptionMode != int(model.ChatEncryptionNone)
	if e2ee {
		if !i.validateE2ee(ctx, c, room, request) {
			return
		}
	} else if request.isEmpty() {
		failValidation(c, "content", "You cannot send an empty message.", "CHAT_MESSAGE_EMPTY")
		return
	}
	if !e2ee && request.FundID != nil {
		if !i.validateFund(ctx, c, request.FundID, member.AccountID.String()) {
			return
		}
	}

	entity, ok := i.buildMessageEntity(ctx, c, room, member, request)
	if !ok {
		return
	}
	if existing := i.existingClientMessage(ctx, entity); existing != nil {
		messages := i.hydrateMessages(ctx, []*store.ChatMessageEntity{existing}, member.AccountID.String())
		httpx.OK(c, messages[0])
		return
	}
	if err := i.insertMessage(ctx, i.db, entity); err != nil {
		failInternal(c, err)
		return
	}
	// Sending implies read-through for the sender.
	_ = i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).
		Where("id = ?", member.ID).
		Update("last_read_at", entity.CreatedAt).Error

	i.deliverMessage(ctx, room, member, entity, "messages.new", !e2ee)
	messages := i.hydrateMessages(ctx, []*store.ChatMessageEntity{entity}, member.AccountID.String())
	httpx.OK(c, messages[0])
}

// --- update ---------------------------------------------------------------

func (i *instance) updateMessage(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	messageID, ok := uuidParam(c, "messageId")
	if !ok {
		return
	}
	user := currentUser(c)
	if user == nil {
		httpx.Unauthorized(c)
		return
	}
	var request sendMessageRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	if !validateSendLengths(c, request) {
		return
	}
	if !validWKT(request.LocationWKT) {
		failBadRequest(c, "CHAT_MESSAGE_LOCATION_INVALID", "Invalid location WKT.")
		return
	}
	var entity store.ChatMessageEntity
	if err := i.db.WithContext(ctx).Where("id = ? AND chat_room_id = ?", messageID, roomID).First(&entity).Error; err != nil {
		failNotFound(c)
		return
	}
	room, err := i.st.GetRoom(ctx, roomID)
	if err != nil {
		failNotFound(c)
		return
	}
	var sender store.ChatMemberEntity
	if err := i.db.WithContext(ctx).Where("id = ?", entity.SenderID).First(&sender).Error; err != nil {
		failNotFound(c)
		return
	}
	e2ee := room.EncryptionMode != int(model.ChatEncryptionNone)
	if sender.AccountID.String() != user.GetId() {
		failForbidden(c, "You can only edit your own messages.")
		return
	}
	if sender.TimeoutUntil != nil && sender.TimeoutUntil.After(utcNow()) {
		failForbidden(c, "You has been timed out in this chat.")
		return
	}
	if e2ee {
		if !i.validateE2ee(ctx, c, room, request) {
			return
		}
	} else if request.isEmpty() {
		failValidation(c, "content", "You cannot send an empty message.", "CHAT_MESSAGE_EMPTY")
		return
	}
	if entity.Type != "text" {
		failBadRequest(c, "CHAT_MESSAGE_NOT_EDITABLE", "Only regular messages can be edited.")
		return
	}

	previousContent := ""
	if entity.Content != nil {
		previousContent = *entity.Content
	}
	contentChanged := request.Content != nil && (entity.Content == nil || *entity.Content != *request.Content)
	attachmentsChanged := request.AttachmentsID != nil
	ciphertextChanged := request.EncryptionMeta != nil
	wasEncrypted := len(entity.EncryptionMeta) > 0
	encryptedFlagChanged := ciphertextChanged != wasEncrypted

	meta := messageMeta(&entity)
	if !e2ee && len(request.Embeds) > 0 {
		embeds := make([]any, 0, len(request.Embeds))
		for _, embed := range request.Embeds {
			embeds = append(embeds, embed)
		}
		meta["embeds"] = embeds
	} else if !e2ee {
		if request.FundID != nil {
			if !i.validateFund(ctx, c, request.FundID, sender.AccountID.String()) {
				return
			}
			removeEmbed(meta, "fund")
			addEmbed(meta, map[string]any{"type": "fund", "id": *request.FundID})
		} else {
			removeEmbed(meta, "fund")
		}
		if request.SurveyID != nil {
			if survey, ok := i.validateSurvey(ctx, c, request.SurveyID); !ok {
				return
			} else if survey != nil {
				removeEmbed(meta, "poll")
				addEmbed(meta, map[string]any{"type": "survey", "id": survey.GetId()})
			}
		} else {
			removeEmbed(meta, "poll")
		}
		if request.MeetID != nil {
			removeEmbed(meta, "meet")
			addEmbed(meta, map[string]any{"type": "meet", "id": *request.MeetID})
		} else {
			removeEmbed(meta, "meet")
		}
		if request.NotableDayID != nil {
			removeEmbed(meta, "notable_day")
			addEmbed(meta, map[string]any{"type": "notable_day", "id": *request.NotableDayID})
		} else {
			removeEmbed(meta, "notable_day")
		}
		if request.CalendarEventID != nil {
			removeEmbed(meta, "calendar_event")
			addEmbed(meta, map[string]any{"type": "calendar_event", "id": *request.CalendarEventID})
		} else {
			removeEmbed(meta, "calendar_event")
		}
		if request.hasLocation() {
			removeEmbed(meta, "location")
			addEmbed(meta, locationEmbed(request.LocationName, request.LocationAddress, request.LocationWKT))
		} else {
			removeEmbed(meta, "location")
		}
	}

	updates := map[string]any{"meta": store.ToJSON(meta)}
	if !e2ee {
		mentions := i.extractMentions(ctx, request.Content, entity.RepliedMessageID, entity.ForwardedMessageID, room.ID, sender.AccountID.String())
		updates["members_mentioned"] = store.ToJSON(mentions)
	}
	if request.AttachmentsID != nil {
		if e2ee {
			unique := make([]string, 0, len(request.AttachmentsID))
			seen := map[string]bool{}
			for _, id := range request.AttachmentsID {
				if seen[id] {
					continue
				}
				seen[id] = true
				unique = append(unique, id)
			}
			meta["attachments_id"] = unique
			updates["meta"] = store.ToJSON(meta)
			updates["attachments"] = store.ToJSON([]model.CloudFileReferenceObject{})
		} else {
			attachments, ok := i.attachFiles(ctx, c, request.AttachmentsID)
			if !ok {
				return
			}
			updates["attachments"] = store.ToJSON(attachments)
		}
	}
	if request.Content != nil {
		updates["content"] = *request.Content
	}
	if e2ee {
		if request.EncryptionMeta != nil {
			updates["encryption_meta"] = store.ToJSON(request.EncryptionMeta)
		}
		updates["content"] = nil
		updates["client_message_id"] = request.ClientMessageID
	}
	if contentChanged || attachmentsChanged || ciphertextChanged || encryptedFlagChanged {
		now := utcNow()
		updates["edited_at"] = now
	}
	if err := i.db.WithContext(ctx).Model(&store.ChatMessageEntity{}).Where("id = ?", entity.ID).Updates(updates).Error; err != nil {
		failInternal(c, err)
		return
	}
	if err := i.db.WithContext(ctx).Where("id = ?", entity.ID).First(&entity).Error; err != nil {
		failInternal(c, err)
		return
	}

	// Sync row so other clients reconcile the edit.
	syncMeta := messageMeta(&entity)
	syncMeta["message_id"] = entity.ID.String()
	if !e2ee && contentChanged {
		syncMeta["previous_content"] = previousContent
	}
	syncEntity := &store.ChatMessageEntity{
		ID:                 uuid.New(),
		Type:               "messages.update",
		ChatRoomID:         entity.ChatRoomID,
		SenderID:           entity.SenderID,
		Content:            entity.Content,
		EncryptionMeta:     entity.EncryptionMeta,
		Attachments:        entity.Attachments,
		MembersMentioned:   entity.MembersMentioned,
		RepliedMessageID:   entity.RepliedMessageID,
		ForwardedMessageID: entity.ForwardedMessageID,
		EditedAt:           entity.EditedAt,
		Meta:               store.ToJSON(syncMeta),
		ReactionsCount:     store.ToJSON(map[string]int{}),
	}
	if err := i.insertMessage(ctx, i.db, syncEntity); err == nil {
		i.deliverMessage(ctx, room, &sender, syncEntity, "messages.update", false)
	}
	messages := i.hydrateMessages(ctx, []*store.ChatMessageEntity{&entity}, sender.AccountID.String())
	httpx.OK(c, messages[0])
}

// --- delete ---------------------------------------------------------------

func (i *instance) deleteMessage(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	messageID, ok := uuidParam(c, "messageId")
	if !ok {
		return
	}
	user := currentUser(c)
	if user == nil {
		httpx.Unauthorized(c)
		return
	}
	var request deleteMessageRequest
	if c.Request.ContentLength > 0 {
		if !httpx.BindJSON(c, &request) {
			return
		}
	}
	var entity store.ChatMessageEntity
	if err := i.db.WithContext(ctx).Where("id = ? AND chat_room_id = ?", messageID, roomID).First(&entity).Error; err != nil {
		failNotFound(c)
		return
	}
	room, err := i.st.GetRoom(ctx, roomID)
	if err != nil {
		failNotFound(c)
		return
	}
	var sender store.ChatMemberEntity
	if err := i.db.WithContext(ctx).Where("id = ?", entity.SenderID).First(&sender).Error; err != nil {
		failNotFound(c)
		return
	}
	e2ee := room.EncryptionMode != int(model.ChatEncryptionNone)
	mls := room.EncryptionMode == int(model.ChatEncryptionE2eeMls)
	if e2ee {
		if mls && (request.EncryptionMeta == nil || request.EncryptionMeta.Scheme != mlsEncryptionScheme || request.EncryptionMeta.Epoch == nil) {
			failConflict(c, "chat.mls_payload_required", "MLS rooms require scheme chat.mls.v2 and encryption_epoch.")
			return
		}
		if mls && !i.ensureMlsEpoch(ctx, c, room, request.EncryptionMeta.Epoch) {
			return
		}
		if looksLikePlaintextJSON(request.EncryptionMeta.Ciphertext) {
			failConflict(c, "chat.e2ee_ciphertext_invalid", "Ciphertext appears to be plaintext JSON.")
			return
		}
	}
	if sender.AccountID.String() != user.GetId() {
		failForbidden(c, "You can only delete your own messages.")
		return
	}
	if entity.Type != "text" && entity.Type != "voice" {
		failBadRequest(c, "CHAT_MESSAGE_NOT_DELETABLE", "Only regular text/voice messages can be deleted.")
		return
	}
	now := utcNow()
	if err := i.db.WithContext(ctx).Model(&store.ChatMessageEntity{}).Where("id = ?", entity.ID).
		Updates(map[string]any{"deleted_at": now, "updated_at": now}).Error; err != nil {
		failInternal(c, err)
		return
	}

	syncMeta := map[string]any{"message_id": entity.ID.String()}
	syncEntity := &store.ChatMessageEntity{
		ID:             uuid.New(),
		Type:           "messages.delete",
		ChatRoomID:     entity.ChatRoomID,
		SenderID:       entity.SenderID,
		EncryptionMeta: entity.EncryptionMeta,
		Meta:           store.ToJSON(syncMeta),
		ReactionsCount: store.ToJSON(map[string]int{}),
	}
	if request.EncryptionMeta != nil {
		syncEntity.EncryptionMeta = store.ToJSON(request.EncryptionMeta)
	}
	if request.ClientMessageID != nil {
		syncEntity.ClientMessageID = request.ClientMessageID
	} else if request.Nonce != nil {
		syncEntity.ClientMessageID = request.Nonce
	}
	if err := i.insertMessage(ctx, i.db, syncEntity); err == nil {
		i.deliverMessage(ctx, room, &sender, syncEntity, "messages.delete", false)
	}
	httpx.OKEmpty(c)
}

// --- reactions ------------------------------------------------------------

func (i *instance) rebuildReactionCounts(ctx context.Context, tx *gorm.DB, messageID uuid.UUID) map[string]int {
	type row struct {
		Symbol string
		Count  int
	}
	var rows []row
	_ = tx.WithContext(ctx).Raw(
		`SELECT symbol, COUNT(*)::int AS count FROM chat_reactions
		 WHERE message_id = ? AND deleted_at IS NULL GROUP BY symbol`, messageID,
	).Scan(&rows).Error
	counts := map[string]int{}
	for _, item := range rows {
		counts[item.Symbol] = item.Count
	}
	return counts
}

func (i *instance) reactMessage(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	messageID, ok := uuidParam(c, "messageId")
	if !ok {
		return
	}
	user := currentUser(c)
	if user == nil {
		httpx.Unauthorized(c)
		return
	}
	var request messageReactionRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	if request.Symbol == "" {
		failValidation(c, "symbol", "The symbol field is required.", "")
		return
	}
	if !isDefaultReaction(request.Symbol) && user.GetPerkSubscription() == nil {
		failBadRequest(c, "CHAT_REACTION_SUBSCRIPTION_REQUIRED", "You need subscription to send custom reactions")
		return
	}
	member, err := i.activeMember(ctx, roomID, user.GetId())
	if err != nil {
		failForbidden(c, "You need to be a member to react to messages here.")
		return
	}
	room, err := i.st.GetRoom(ctx, roomID)
	if err != nil {
		failNotFound(c)
		return
	}
	var message store.ChatMessageEntity
	if err := i.db.WithContext(ctx).Where("id = ? AND chat_room_id = ?", messageID, roomID).First(&message).Error; err != nil {
		failNotFound(c)
		return
	}
	var existing store.ChatReactionEntity
	err = i.db.WithContext(ctx).Where("message_id = ? AND sender_id = ? AND symbol = ?", messageID, member.ID, request.Symbol).First(&existing).Error
	if err == nil {
		i.removeReactionTx(ctx, room, &message, &existing, member)
		httpx.NoContent(c)
		return
	}

	reaction := &store.ChatReactionEntity{
		ID:        uuid.New(),
		MessageID: messageID,
		SenderID:  member.ID,
		Symbol:    request.Symbol,
		Attitude:  request.Attitude,
	}
	var counts map[string]int
	txErr := i.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT id FROM chat_messages WHERE id = ? FOR UPDATE", messageID).Error; err != nil {
			return err
		}
		if err := tx.Create(reaction).Error; err != nil {
			return err
		}
		counts = i.rebuildReactionCounts(ctx, tx, messageID)
		return tx.Model(&store.ChatMessageEntity{}).Where("id = ?", messageID).
			Update("reactions_count", store.ToJSON(counts)).Error
	})
	if txErr != nil {
		failInternal(c, txErr)
		return
	}
	meta := map[string]any{
		"message_id": messageID.String(),
		"symbol":     reaction.Symbol,
		"reaction": map[string]any{
			"id":         reaction.ID.String(),
			"symbol":     reaction.Symbol,
			"attitude":   reaction.Attitude,
			"message_id": reaction.MessageID.String(),
			"sender_id":  reaction.SenderID.String(),
		},
		"reactions_count": counts,
	}
	syncEntity := &store.ChatMessageEntity{
		ID:             uuid.New(),
		Type:           "messages.reaction.added",
		ChatRoomID:     room.ID,
		SenderID:       member.ID,
		Meta:           store.ToJSON(meta),
		ReactionsCount: store.ToJSON(map[string]int{}),
	}
	_ = i.insertMessage(ctx, i.db, syncEntity)
	i.deliverMessage(ctx, room, member, syncEntity, "messages.new", false)

	// Delivery of the updated message itself.
	var refreshed store.ChatMessageEntity
	if err := i.db.WithContext(ctx).Where("id = ?", messageID).First(&refreshed).Error; err == nil {
		i.deliverMessage(ctx, room, member, &refreshed, "messages.new", false)
	}
	i.notifyReaction(ctx, room, &message, member, true, request.Symbol)

	reactionModel := store.ReactionToModel(reaction)
	members := i.memberModels(ctx, []*store.ChatMemberEntity{member})
	if len(members) > 0 {
		reactionModel.Sender = &members[0]
	}
	httpx.OK(c, reactionModel)
}

func (i *instance) removeReaction(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	messageID, ok := uuidParam(c, "messageId")
	if !ok {
		return
	}
	symbol := c.Param("symbol")
	user := currentUser(c)
	if user == nil {
		httpx.Unauthorized(c)
		return
	}
	member, err := i.activeMember(ctx, roomID, user.GetId())
	if err != nil {
		failForbidden(c, "You need to be a member to remove reaction from messages here.")
		return
	}
	room, err := i.st.GetRoom(ctx, roomID)
	if err != nil {
		failNotFound(c)
		return
	}
	var message store.ChatMessageEntity
	if err := i.db.WithContext(ctx).Where("id = ? AND chat_room_id = ?", messageID, roomID).First(&message).Error; err != nil {
		failNotFound(c)
		return
	}
	var reaction store.ChatReactionEntity
	if err := i.db.WithContext(ctx).Where("message_id = ? AND sender_id = ? AND symbol = ?", messageID, member.ID, symbol).First(&reaction).Error; err != nil {
		httpx.NoContent(c)
		return
	}
	i.removeReactionTx(ctx, room, &message, &reaction, member)
	httpx.NoContent(c)
}

func (i *instance) removeReactionTx(ctx context.Context, room *store.ChatRoomEntity, message *store.ChatMessageEntity, reaction *store.ChatReactionEntity, member *store.ChatMemberEntity) {
	var counts map[string]int
	_ = i.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT id FROM chat_messages WHERE id = ? FOR UPDATE", message.ID).Error; err != nil {
			return err
		}
		if err := tx.Delete(reaction).Error; err != nil {
			return err
		}
		counts = i.rebuildReactionCounts(ctx, tx, message.ID)
		return tx.Model(&store.ChatMessageEntity{}).Where("id = ?", message.ID).
			Update("reactions_count", store.ToJSON(counts)).Error
	})
	meta := map[string]any{
		"message_id":      message.ID.String(),
		"symbol":          reaction.Symbol,
		"reactions_count": counts,
	}
	syncEntity := &store.ChatMessageEntity{
		ID:             uuid.New(),
		Type:           "messages.reaction.removed",
		ChatRoomID:     room.ID,
		SenderID:       member.ID,
		Meta:           store.ToJSON(meta),
		ReactionsCount: store.ToJSON(map[string]int{}),
	}
	_ = i.insertMessage(ctx, i.db, syncEntity)
	i.deliverMessage(ctx, room, member, syncEntity, "messages.new", false)
	var refreshed store.ChatMessageEntity
	if err := i.db.WithContext(ctx).Where("id = ?", message.ID).First(&refreshed).Error; err == nil {
		i.deliverMessage(ctx, room, member, &refreshed, "messages.new", false)
	}
	i.notifyReaction(ctx, room, message, member, false, reaction.Symbol)
}

func (i *instance) notifyReaction(ctx context.Context, room *store.ChatRoomEntity, message *store.ChatMessageEntity, reactor *store.ChatMemberEntity, added bool, symbol string) {
	var author store.ChatMemberEntity
	if err := i.db.WithContext(ctx).Where("id = ? AND chat_room_id = ?", message.SenderID, room.ID).First(&author).Error; err != nil {
		return
	}
	if author.AccountID == reactor.AccountID || author.Notify == int(model.ChatNotifyNone) {
		return
	}
	if author.BreakUntil != nil && author.BreakUntil.After(utcNow()) {
		return
	}
	topic := "messages.reaction.removed"
	verb := "removed a reaction"
	if added {
		topic = "messages.reaction.added"
		verb = "reacted"
	}
	title := i.displayName(ctx, reactor)
	body := verb + " with " + symbol
	meta := map[string]string{
		"user_id":    reactor.AccountID.String(),
		"reactor_id": reactor.ID.String(),
		"message_id": message.ID.String(),
		"room_id":    room.ID.String(),
		"symbol":     symbol,
	}
	i.sendPush(ctx, []string{author.AccountID.String()}, topic, title, body, meta)
}

func (i *instance) listMessageReactions(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	messageID, ok := uuidParam(c, "messageId")
	if !ok {
		return
	}
	if _, ok := i.readableRoom(ctx, c, roomID); !ok {
		return
	}
	var exists int64
	if err := i.db.WithContext(ctx).Model(&store.ChatMessageEntity{}).
		Where("id = ? AND chat_room_id = ?", messageID, roomID).Count(&exists).Error; err != nil || exists == 0 {
		failNotFound(c)
		return
	}
	page, ok := httpx.PageQuery(c, 100)
	if !ok {
		return
	}
	query := i.db.WithContext(ctx).Model(&store.ChatReactionEntity{}).Where("message_id = ?", messageID)
	if symbol := strings.TrimSpace(c.Query("symbol")); symbol != "" {
		query = query.Where("symbol = ?", symbol)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		failInternal(c, err)
		return
	}
	if strings.ToLower(c.Query("order")) == "created" {
		query = query.Order("created_at DESC")
	} else {
		query = query.Order("symbol ASC").Order("created_at DESC")
	}
	var entities []*store.ChatReactionEntity
	if err := query.Offset(page.Offset).Limit(page.Take).Find(&entities).Error; err != nil {
		failInternal(c, err)
		return
	}
	senderIDs := make([]uuid.UUID, 0, len(entities))
	for _, reaction := range entities {
		senderIDs = append(senderIDs, reaction.SenderID)
	}
	var senders []*store.ChatMemberEntity
	if len(senderIDs) > 0 {
		_ = i.db.WithContext(ctx).Where("id IN ?", senderIDs).Find(&senders).Error
	}
	senderMap := map[string]model.ChatMember{}
	for _, sender := range i.memberModels(ctx, senders) {
		senderMap[sender.ID] = sender
	}
	reactions := make([]model.ChatReaction, 0, len(entities))
	for _, entity := range entities {
		modelValue := store.ReactionToModel(entity)
		if sender, ok := senderMap[entity.SenderID.String()]; ok {
			senderCopy := sender
			modelValue.Sender = &senderCopy
		}
		reactions = append(reactions, modelValue)
	}
	httpx.List(c, reactions, total)
}

// --- thread ---------------------------------------------------------------

func (i *instance) getMessageThread(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	messageID, ok := uuidParam(c, "messageId")
	if !ok {
		return
	}
	if _, ok := i.readableRoom(ctx, c, roomID); !ok {
		return
	}
	page, ok := httpx.PageQuery(c, 100)
	if !ok {
		return
	}
	var root store.ChatMessageEntity
	if err := i.db.WithContext(ctx).Where("id = ? AND chat_room_id = ?", messageID, roomID).First(&root).Error; err != nil {
		failNotFound(c)
		return
	}
	var total int64
	_ = i.db.WithContext(ctx).Model(&store.ChatMessageEntity{}).Where("thread_id = ?", messageID).Count(&total).Error

	var rootReplies []*store.ChatMessageEntity
	if err := i.db.WithContext(ctx).Where("thread_id = ?", messageID).
		Order("room_sequence DESC").Order("created_at DESC").
		Offset(page.Offset).Limit(page.Take).Find(&rootReplies).Error; err != nil {
		failInternal(c, err)
		return
	}
	c.Header("X-Total", strconv.FormatInt(total, 10))
	if len(rootReplies) == 0 {
		rootMessages := i.hydrateMessages(ctx, []*store.ChatMessageEntity{&root}, currentAccountID(c))
		httpx.OK(c, threadReplyListResponse{Root: rootMessages[0], Replies: []model.ThreadReplyNode{}})
		return
	}

	byParent := map[string][]*store.ChatMessageEntity{}
	visited := map[string]bool{}
	frontier := make([]uuid.UUID, 0, len(rootReplies))
	for _, reply := range rootReplies {
		visited[reply.ID.String()] = true
		frontier = append(frontier, reply.ID)
	}
	for len(frontier) > 0 {
		var children []*store.ChatMessageEntity
		if err := i.db.WithContext(ctx).Where("replied_message_id IN ?", frontier).
			Order("room_sequence ASC").Order("created_at ASC").Find(&children).Error; err != nil {
			break
		}
		frontier = nil
		for _, child := range children {
			if visited[child.ID.String()] || child.RepliedMessageID == nil {
				continue
			}
			visited[child.ID.String()] = true
			parent := child.RepliedMessageID.String()
			byParent[parent] = append(byParent[parent], child)
			frontier = append(frontier, child.ID)
		}
	}

	nodes := []*store.ChatMessageEntity{}
	depths := []int{}
	var flatten func(entity *store.ChatMessageEntity, depth int)
	flatten = func(entity *store.ChatMessageEntity, depth int) {
		nodes = append(nodes, entity)
		depths = append(depths, depth)
		for _, child := range byParent[entity.ID.String()] {
			flatten(child, depth+1)
		}
	}
	for _, reply := range rootReplies {
		flatten(reply, 0)
	}
	replies := i.hydrateMessages(ctx, nodes, currentAccountID(c))
	tree := make([]model.ThreadReplyNode, 0, len(replies))
	for idx := range replies {
		tree = append(tree, model.ThreadReplyNode{Message: replies[idx], Depth: depths[idx]})
	}
	rootMessages := i.hydrateMessages(ctx, []*store.ChatMessageEntity{&root}, currentAccountID(c))
	httpx.OK(c, threadReplyListResponse{Root: rootMessages[0], Replies: tree})
}

// --- search ---------------------------------------------------------------

func (i *instance) searchMessages(c *gin.Context) {
	ctx := c.Request.Context()
	user := currentUser(c)
	if user == nil {
		httpx.Unauthorized(c)
		return
	}
	if user.GetPerkLevel() < 1 {
		failForbidden(c, "Perk level 1 is required to search cloud messages.")
		return
	}
	searchQuery := strings.TrimSpace(c.Query("query"))
	if searchQuery == "" {
		failValidation(c, "query", "Search query cannot be empty.", "CHAT_SEARCH_QUERY_REQUIRED")
		return
	}
	page, ok := httpx.PageQuery(c, 100)
	if !ok {
		return
	}
	after, afterOK := parseQueryInstant(c.Query("after"))
	if !afterOK {
		failBadRequest(c, "CHAT_SEARCH_DATE_RANGE_INVALID", "Invalid after timestamp.")
		return
	}
	before, beforeOK := parseQueryInstant(c.Query("before"))
	if !beforeOK {
		failBadRequest(c, "CHAT_SEARCH_DATE_RANGE_INVALID", "Invalid before timestamp.")
		return
	}
	if after != nil && before != nil && !after.Before(*before) {
		failBadRequest(c, "CHAT_SEARCH_DATE_RANGE_INVALID", "The after timestamp must be earlier than the before timestamp.")
		return
	}
	accountID := user.GetId()
	query := i.db.WithContext(ctx).Model(&store.ChatMessageEntity{}).
		Where("type = 'text' AND encryption_meta IS NULL AND content IS NOT NULL").
		Where("EXISTS (SELECT 1 FROM chat_members m WHERE m.chat_room_id = chat_messages.chat_room_id AND m.account_id = ? AND m.joined_at IS NOT NULL AND m.leave_at IS NULL)", accountID).
		Where("content ILIKE ?", "%"+searchQuery+"%")
	if roomIDs := c.QueryArray("room_ids"); len(roomIDs) > 0 {
		parsed := make([]uuid.UUID, 0, len(roomIDs))
		seen := map[uuid.UUID]bool{}
		for _, raw := range roomIDs {
			id, err := uuid.Parse(raw)
			if err != nil || seen[id] {
				continue
			}
			seen[id] = true
			parsed = append(parsed, id)
		}
		if len(parsed) > 0 {
			query = query.Where("chat_room_id IN ?", parsed)
		}
	}
	if sender := strings.TrimSpace(c.Query("sender")); sender != "" {
		accountIDs := i.searchAccountIDs(ctx, sender)
		if len(accountIDs) == 0 {
			c.Header("X-Total", "0")
			httpx.OK(c, []messageSearchRoomResponse{})
			return
		}
		query = query.Where("EXISTS (SELECT 1 FROM chat_members m WHERE m.id = chat_messages.sender_id AND m.account_id IN ?)", accountIDs)
	}
	if after != nil {
		query = query.Where("created_at >= ?", *after)
	}
	if before != nil {
		query = query.Where("created_at < ?", *before)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		failInternal(c, err)
		return
	}
	var entities []*store.ChatMessageEntity
	if err := query.Order("created_at DESC").Order("id DESC").
		Offset(page.Offset).Limit(page.Take).Find(&entities).Error; err != nil {
		failInternal(c, err)
		return
	}
	c.Header("X-Total", strconv.FormatInt(total, 10))
	if len(entities) == 0 {
		httpx.OK(c, []messageSearchRoomResponse{})
		return
	}
	messages := i.hydrateMessages(ctx, entities, accountID)

	roomIDs := make([]uuid.UUID, 0, len(entities))
	seenRoom := map[uuid.UUID]bool{}
	for _, entity := range entities {
		if seenRoom[entity.ChatRoomID] {
			continue
		}
		seenRoom[entity.ChatRoomID] = true
		roomIDs = append(roomIDs, entity.ChatRoomID)
	}
	var roomEntities []*store.ChatRoomEntity
	_ = i.db.WithContext(ctx).Where("id IN ?", roomIDs).Find(&roomEntities).Error
	rooms := i.hydrateRooms(ctx, roomEntities, accountID)
	roomMap := map[string]model.ChatRoom{}
	for _, room := range rooms {
		roomMap[room.ID] = room
	}
	grouped := map[string][]model.ChatMessage{}
	order := []string{}
	for _, message := range messages {
		if _, ok := grouped[message.ChatRoomID]; !ok {
			order = append(order, message.ChatRoomID)
		}
		grouped[message.ChatRoomID] = append(grouped[message.ChatRoomID], message)
	}
	response := make([]messageSearchRoomResponse, 0, len(order))
	for _, roomID := range order {
		response = append(response, messageSearchRoomResponse{Room: roomMap[roomID], Messages: grouped[roomID]})
	}
	httpx.OK(c, response)
}

func (i *instance) searchAccountIDs(ctx context.Context, query string) []string {
	if i.host == nil || i.host.Core.Clients == nil || i.host.Core.Clients.Profile == nil {
		return nil
	}
	resp, err := i.host.Core.Clients.Profile.SearchAccount(ctx, &gen.DySearchAccountRequest{Query: query})
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(resp.GetAccounts()))
	for _, account := range resp.GetAccounts() {
		ids = append(ids, account.GetId())
	}
	return ids
}

func parseQueryInstant(value string) (*time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, true
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, false
	}
	parsed = parsed.UTC()
	return &parsed, true
}
