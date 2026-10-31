package chat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/datatypes"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/model"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/store"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
)

const mlsEncryptionScheme = "chat.mls.v2"

func parseOptionalUUID(value *string) (*uuid.UUID, bool) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil, true
	}
	id, err := uuid.Parse(strings.TrimSpace(*value))
	if err != nil {
		return nil, false
	}
	return &id, true
}

func (r sendMessageRequest) hasEncryptedPayload() bool {
	return r.EncryptionMeta != nil && len(r.EncryptionMeta.Ciphertext) > 0 && strings.TrimSpace(r.EncryptionMeta.Scheme) != ""
}

func (r sendMessageRequest) isMlsPayloadValid() bool {
	return r.EncryptionMeta != nil && r.EncryptionMeta.Scheme == mlsEncryptionScheme && r.EncryptionMeta.Epoch != nil
}

func (r sendMessageRequest) hasLocation() bool {
	return hasLocationPayload(r.LocationName, r.LocationAddress, r.LocationWKT)
}

func (r sendMessageRequest) isEmpty() bool {
	return !nonEmpty(r.Content) &&
		len(r.AttachmentsID) == 0 &&
		r.FundID == nil && r.MeetID == nil && r.SurveyID == nil &&
		r.NotableDayID == nil && r.CalendarEventID == nil &&
		len(r.Embeds) == 0 && !r.hasLocation()
}

func (r sendMessageRequest) hasPlaintextFields() bool {
	return nonEmpty(r.Content) || r.FundID != nil || r.MeetID != nil || r.SurveyID != nil ||
		r.NotableDayID != nil || r.CalendarEventID != nil || len(r.Embeds) > 0 || r.hasLocation()
}

func validateSendLengths(c *gin.Context, request sendMessageRequest) bool {
	if request.Content != nil && len([]rune(*request.Content)) > 4096 {
		failValidation(c, "content", "The content field must be at most 4096 characters.", "")
		return false
	}
	if request.Nonce != nil && len(*request.Nonce) > 36 {
		failValidation(c, "nonce", "The nonce field must be at most 36 characters.", "")
		return false
	}
	if request.ClientMessageID != nil && len(*request.ClientMessageID) > 128 {
		failValidation(c, "client_message_id", "The client_message_id field must be at most 128 characters.", "")
		return false
	}
	return true
}

func looksLikePlaintextJSON(payload []byte) bool {
	if len(payload) <= 1 {
		return false
	}
	text := strings.TrimSpace(string(payload))
	if !strings.HasPrefix(text, "{") || !strings.HasSuffix(text, "}") {
		return false
	}
	lower := strings.ToLower(text)
	return strings.Contains(lower, `"content"`) ||
		strings.Contains(lower, `"attachments_id"`) ||
		strings.Contains(lower, `"nonce"`)
}

func (i *instance) ensureMlsEpoch(ctx context.Context, c *gin.Context, room *store.ChatRoomEntity, epoch *int64) bool {
	if room.EncryptionMode != int(model.ChatEncryptionE2eeMls) {
		return true
	}
	if room.MLSGroupID == nil || strings.TrimSpace(*room.MLSGroupID) == "" || epoch == nil {
		failConflict(c, "chat.mls_payload_required", "MLS rooms require a configured group and encryption epoch.")
		return false
	}
	if i.mls == nil {
		return true
	}
	state, err := i.mls.GetGroupState(ctx, &gen.GetMlsGroupStateRequest{GroupId: *room.MLSGroupID})
	if err != nil {
		switch status.Code(err) {
		case codes.NotFound:
			failConflict(c, "chat.e2ee_mls_group_not_ready", "The MLS group has not been bootstrapped yet.")
		case codes.Unavailable:
			httpx.Fail(c, newStatusError("CHAT_E2EE_MLS_STATE_UNAVAILABLE", "MLS group state is temporarily unavailable.", 503))
		default:
			failConflict(c, "chat.e2ee_mls_group_not_ready", "The MLS group has not been bootstrapped yet.")
		}
		return false
	}
	if state.GetEpoch() != *epoch {
		apiErr := newStatusError("CHAT_E2EE_MLS_EPOCH_MISMATCH", "MLS epoch mismatch.", 409)
		apiErr.Meta = map[string]any{"current_epoch": state.GetEpoch(), "message_epoch": *epoch}
		httpx.Fail(c, apiErr)
		return false
	}
	return true
}

func (i *instance) validateE2ee(ctx context.Context, c *gin.Context, room *store.ChatRoomEntity, request sendMessageRequest) bool {
	e2ee := room.EncryptionMode != int(model.ChatEncryptionNone)
	if !e2ee {
		if request.isEmpty() {
			failValidation(c, "content", "You cannot send an empty message.", "CHAT_MESSAGE_EMPTY")
			return false
		}
		return true
	}
	if !request.hasEncryptedPayload() {
		failConflict(c, "chat.e2ee_payload_required", "Encrypted payload is required for E2EE rooms.")
		return false
	}
	mls := room.EncryptionMode == int(model.ChatEncryptionE2eeMls)
	if mls && !request.isMlsPayloadValid() {
		failConflict(c, "chat.mls_payload_required", "MLS rooms require scheme chat.mls.v2 and encryption_epoch.")
		return false
	}
	if mls && !i.ensureMlsEpoch(ctx, c, room, request.EncryptionMeta.Epoch) {
		return false
	}
	if looksLikePlaintextJSON(request.EncryptionMeta.Ciphertext) {
		failConflict(c, "chat.e2ee_ciphertext_invalid", "Ciphertext appears to be plaintext JSON.")
		return false
	}
	if request.hasPlaintextFields() {
		failConflict(c, "chat.e2ee_plaintext_forbidden", "Plaintext fields are forbidden for E2EE rooms.")
		return false
	}
	return true
}

// validateFund mirrors the fund ownership check in ChatController.
func (i *instance) validateFund(ctx context.Context, c *gin.Context, fundID *string, accountID string) bool {
	if fundID == nil {
		return true
	}
	if i.host == nil || i.host.Core.Clients == nil || i.host.Core.Clients.Wallet == nil {
		return true
	}
	fund, err := i.host.Core.Clients.Wallet.GetWalletFund(ctx, &gen.DyGetWalletFundRequest{FundId: *fundID})
	if err != nil {
		switch status.Code(err) {
		case codes.NotFound:
			failBadRequest(c, "CHAT_FUND_NOT_FOUND", "The specified fund does not exist.")
		case codes.InvalidArgument:
			failBadRequest(c, "CHAT_FUND_INVALID_ID", "Invalid fund ID.")
		default:
			failInternal(c, err)
		}
		return false
	}
	if fund.GetCreatorAccountId() != accountID {
		failBadRequest(c, "CHAT_FUND_NOT_OWNER", "You can only share funds that you created.")
		return false
	}
	return true
}

// validateSurvey resolves a poll embed. The C# controller only maps the gRPC
// errors; the survey service itself validates the poll.
func (i *instance) validateSurvey(ctx context.Context, c *gin.Context, surveyID *string) (*gen.DySurvey, bool) {
	if surveyID == nil || i.sphere == nil {
		return nil, true
	}
	survey, err := i.sphere.GetSurvey(ctx, *surveyID)
	if err != nil {
		if errors.Is(err, ports.ErrUnavailable) {
			return nil, true
		}
		switch status.Code(err) {
		case codes.NotFound:
			failBadRequest(c, "CHAT_POLL_NOT_FOUND", "The specified poll does not exist.")
		case codes.InvalidArgument:
			failBadRequest(c, "CHAT_POLL_INVALID_ID", "Invalid poll ID.")
		default:
			failInternal(c, err)
		}
		return nil, false
	}
	return survey, true
}

// resolveAttachments resolves attachment ids through the Drive port, preserving
// the request order. Ports that are unavailable keep the reference list empty.
func (i *instance) resolveAttachments(ctx context.Context, ids []string) ([]model.CloudFileReferenceObject, error) {
	attachments := []model.CloudFileReferenceObject{}
	if len(ids) == 0 || i.files == nil {
		return attachments, nil
	}
	files, err := i.files.GetFiles(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if file, ok := files[id]; ok {
			attachments = append(attachments, file)
		}
	}
	return attachments, nil
}

// attachFiles is the gin-flavoured wrapper used by the HTTP handlers.
func (i *instance) attachFiles(ctx context.Context, c *gin.Context, ids []string) ([]model.CloudFileReferenceObject, bool) {
	attachments, err := i.resolveAttachments(ctx, ids)
	if err != nil {
		failInternal(c, err)
		return nil, false
	}
	return attachments, true
}

// resolveRepliedAndThread applies the thread/reply/forward rules from the C#
// controller and mutates the entity accordingly.
func (i *instance) resolveRepliedAndThread(ctx context.Context, c *gin.Context, roomID uuid.UUID, request sendMessageRequest, entity *store.ChatMessageEntity) bool {
	if request.ThreadID != nil && *request.ThreadID != "" {
		threadID, err := uuid.Parse(*request.ThreadID)
		if err != nil {
			failBadRequest(c, "CHAT_THREAD_NOT_FOUND", "The thread root message does not exist.")
			return false
		}
		var root store.ChatMessageEntity
		if err := i.db.WithContext(ctx).Where("id = ? AND chat_room_id = ?", threadID, roomID).First(&root).Error; err != nil {
			failBadRequest(c, "CHAT_THREAD_NOT_FOUND", "The thread root message does not exist.")
			return false
		}
		if root.RepliedMessageID != nil {
			failBadRequest(c, "CHAT_THREAD_ROOT_IS_REPLY", "Thread replies must target a top-level message.")
			return false
		}
		entity.ThreadID = &root.ID
		if request.RepliedMessageID != nil && *request.RepliedMessageID != "" {
			parentID, err := uuid.Parse(*request.RepliedMessageID)
			if err != nil {
				failBadRequest(c, "CHAT_REPLY_MESSAGE_NOT_FOUND", "The message you're replying to does not exist.")
				return false
			}
			var parent store.ChatMessageEntity
			if err := i.db.WithContext(ctx).Where("id = ? AND chat_room_id = ?", parentID, roomID).First(&parent).Error; err != nil {
				failBadRequest(c, "CHAT_REPLY_MESSAGE_NOT_FOUND", "The message you're replying to does not exist.")
				return false
			}
			if parent.ThreadID != nil && *parent.ThreadID != *entity.ThreadID {
				failBadRequest(c, "CHAT_THREAD_PARENT_MISMATCH", "The parent must belong to the same thread.")
				return false
			}
			entity.RepliedMessageID = &parent.ID
		}
	} else if request.RepliedMessageID != nil && *request.RepliedMessageID != "" {
		repliedID, err := uuid.Parse(*request.RepliedMessageID)
		if err != nil {
			failBadRequest(c, "CHAT_REPLY_MESSAGE_NOT_FOUND", "The message you're replying to does not exist.")
			return false
		}
		var replied store.ChatMessageEntity
		if err := i.db.WithContext(ctx).Where("id = ? AND chat_room_id = ?", repliedID, roomID).First(&replied).Error; err != nil {
			failBadRequest(c, "CHAT_REPLY_MESSAGE_NOT_FOUND", "The message you're replying to does not exist.")
			return false
		}
		entity.RepliedMessageID = &replied.ID
		entity.ThreadID = nil
	}

	if request.ForwardedMessageID != nil && *request.ForwardedMessageID != "" {
		forwardedID, err := uuid.Parse(*request.ForwardedMessageID)
		if err != nil {
			failBadRequest(c, "CHAT_FORWARD_MESSAGE_NOT_FOUND", "The message you're forwarding does not exist.")
			return false
		}
		var forwarded store.ChatMessageEntity
		if err := i.db.WithContext(ctx).Where("id = ?", forwardedID).First(&forwarded).Error; err != nil {
			failBadRequest(c, "CHAT_FORWARD_MESSAGE_NOT_FOUND", "The message you're forwarding does not exist.")
			return false
		}
		entity.ForwardedMessageID = &forwarded.ID
	}
	return true
}

// buildMessageEntity assembles a new message row from the request.
func (i *instance) buildMessageEntity(ctx context.Context, c *gin.Context, room *store.ChatRoomEntity, member *store.ChatMemberEntity, request sendMessageRequest) (*store.ChatMessageEntity, bool) {
	e2ee := room.EncryptionMode != int(model.ChatEncryptionNone)

	entity := &store.ChatMessageEntity{
		ID:              uuid.New(),
		Type:            "text",
		SenderID:        member.ID,
		ChatRoomID:      room.ID,
		ClientMessageID: request.clientID(),
	}
	entity.EncryptionMeta = store.ToJSON(request.EncryptionMeta)
	if request.EncryptionMeta == nil {
		entity.EncryptionMeta = nil
	}

	meta := map[string]any{}
	for key, value := range request.Meta {
		meta[key] = value
	}

	if !e2ee && len(request.Embeds) > 0 {
		embeds := make([]any, 0, len(request.Embeds))
		for _, embed := range request.Embeds {
			embeds = append(embeds, embed)
		}
		meta["embeds"] = embeds
	} else if !e2ee {
		if request.FundID != nil {
			addEmbed(meta, map[string]any{"type": "fund", "id": *request.FundID})
		}
		if survey, ok := i.validateSurvey(ctx, c, request.SurveyID); !ok {
			return nil, false
		} else if survey != nil {
			addEmbed(meta, map[string]any{"type": "survey", "id": survey.GetId()})
		}
		if request.MeetID != nil {
			addEmbed(meta, map[string]any{"type": "meet", "id": *request.MeetID})
		}
		if request.NotableDayID != nil {
			addEmbed(meta, map[string]any{"type": "notable_day", "id": *request.NotableDayID})
		}
		if request.CalendarEventID != nil {
			addEmbed(meta, map[string]any{"type": "calendar_event", "id": *request.CalendarEventID})
		}
		if request.hasLocation() {
			addEmbed(meta, locationEmbed(request.LocationName, request.LocationAddress, request.LocationWKT))
		}
	}
	if !e2ee && request.Content != nil {
		entity.Content = request.Content
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
			entity.Attachments = store.ToJSON([]model.CloudFileReferenceObject{})
		} else {
			attachments, ok := i.attachFiles(ctx, c, request.AttachmentsID)
			if !ok {
				return nil, false
			}
			entity.Attachments = store.ToJSON(attachments)
		}
	}
	if entity.Attachments == nil {
		entity.Attachments = store.ToJSON([]model.CloudFileReferenceObject{})
	}
	entity.Meta = store.ToJSON(meta)

	if !i.resolveRepliedAndThread(ctx, c, room.ID, request, entity) {
		return nil, false
	}
	if !e2ee {
		mentions := i.extractMentions(ctx, request.Content, entity.RepliedMessageID, entity.ForwardedMessageID, room.ID, member.AccountID.String())
		entity.MembersMentioned = store.ToJSON(mentions)
	}
	entity.ReactionsCount = store.ToJSON(map[string]int{})
	return entity, true
}

// existingClientMessage implements the client_message_id idempotency check.
func (i *instance) existingClientMessage(ctx context.Context, entity *store.ChatMessageEntity) *store.ChatMessageEntity {
	if entity.ClientMessageID == nil || *entity.ClientMessageID == "" {
		return nil
	}
	var existing store.ChatMessageEntity
	err := i.db.WithContext(ctx).
		Where("chat_room_id = ? AND sender_id = ? AND client_message_id = ? AND type = ?",
			entity.ChatRoomID, entity.SenderID, *entity.ClientMessageID, entity.Type).
		First(&existing).Error
	if err != nil {
		return nil
	}
	return &existing
}

// deliverMessage broadcasts a persisted message and, when notify is set, fans
// out push notifications to the room members.
func (i *instance) deliverMessage(ctx context.Context, room *store.ChatRoomEntity, sender *store.ChatMemberEntity, entity *store.ChatMessageEntity, event string, notify bool) {
	message := i.messageModel(ctx, entity)
	i.broadcast(ctx, room.ID, event, message)
	if !notify {
		return
	}
	members := i.activeMemberEntities(ctx, room.ID)
	if len(members) == 0 {
		return
	}
	accountIDs := make([]string, 0, len(members))
	mentioned := map[string]bool{}
	for _, id := range jsonStringList(entity.MembersMentioned) {
		mentioned[id] = true
	}
	now := utcNow()
	for _, member := range members {
		if member.AccountID == sender.AccountID || member.Notify == int(model.ChatNotifyNone) {
			continue
		}
		isMentioned := mentioned[member.AccountID.String()]
		if !isMentioned {
			if member.BreakUntil != nil && member.BreakUntil.After(now) {
				continue
			}
			if member.Notify == int(model.ChatNotifyMentions) {
				continue
			}
		}
		accountIDs = append(accountIDs, member.AccountID.String())
	}
	if len(accountIDs) == 0 {
		return
	}
	senderAccount := i.account(ctx, sender.AccountID.String())
	title, body, meta := chatPushPayload(room, entity, sender, senderAccount)
	i.sendPush(ctx, accountIDs, "messages.new", title, body, meta)
}

// chatPushPayload renders the title, body and meta of a `messages.new` push.
// Clients display the chat push as a communication notification driven by
// meta: sender_name and pfp give it the member identity instead of the bare
// room subject, and image feeds the media attachment, mirroring the C#
// BuildNotification/BuildNotificationMeta contract.
func chatPushPayload(room *store.ChatRoomEntity, entity *store.ChatMessageEntity, sender *store.ChatMemberEntity, senderAccount *gen.DyAccount) (string, string, map[string]string) {
	roomSubject := "Unknown"
	if room.Type == int(model.ChatRoomDirectMessage) && room.Name == nil {
		roomSubject = "DM"
	} else if room.Name != nil {
		roomSubject = *room.Name
	}
	body := ""
	if entity.Content != nil {
		body = *entity.Content
		if len(body) > 100 {
			body = body[:100]
		}
	}
	if body == "" {
		body = "Attachment"
	}
	senderName := memberDisplayName(sender, senderAccount)
	title := senderName + " (" + roomSubject + ")"
	meta := map[string]string{
		"user_id":     sender.AccountID.String(),
		"sender_id":   sender.ID.String(),
		"sender_name": senderName,
		"message_id":  entity.ID.String(),
		"room_id":     room.ID.String(),
	}
	if pfp := accountPictureID(senderAccount); pfp != "" {
		meta["pfp"] = pfp
	}
	if room.Name != nil && strings.TrimSpace(*room.Name) != "" {
		meta["room_name"] = strings.TrimSpace(*room.Name)
	}
	if image := firstImageAttachmentID(entity.Attachments); image != "" {
		meta["image"] = image
	}
	return title, body, meta
}

// --- system messages ------------------------------------------------------

func (i *instance) sendSystemMessage(ctx context.Context, room *store.ChatRoomEntity, sender *store.ChatMemberEntity, messageType, content string, meta map[string]any) *store.ChatMessageEntity {
	entity := &store.ChatMessageEntity{
		ID:         uuid.New(),
		Type:       messageType,
		ChatRoomID: room.ID,
		SenderID:   sender.ID,
	}
	if content != "" {
		entity.Content = &content
	}
	entity.Meta = store.ToJSON(meta)
	entity.Attachments = store.ToJSON([]model.CloudFileReferenceObject{})
	entity.MembersMentioned = store.ToJSON([]string{})
	entity.ReactionsCount = store.ToJSON(map[string]int{})
	if err := i.insertMessage(ctx, i.db, entity); err != nil {
		i.log.Warn("chat: storing system message failed", "type", messageType, "error", err)
		return nil
	}
	i.deliverMessage(ctx, room, sender, entity, messageType, false)
	return entity
}

func (i *instance) displayName(ctx context.Context, member *store.ChatMemberEntity) string {
	return memberDisplayName(member, i.account(ctx, member.AccountID.String()))
}

// memberDisplayName resolves the member's visible name with the account nick
// fallback, mirroring the C# sender name chain in BuildNotification.
func memberDisplayName(member *store.ChatMemberEntity, account *gen.DyAccount) string {
	if member != nil && member.Nick != nil && strings.TrimSpace(*member.Nick) != "" {
		return strings.TrimSpace(*member.Nick)
	}
	if account != nil && strings.TrimSpace(account.GetNick()) != "" {
		return strings.TrimSpace(account.GetNick())
	}
	return "Someone"
}

// accountPictureID resolves the account avatar file id. Profile RPC accounts
// carry it on the nested profile (DyAccount.Picture is only set by callers
// that embed the gateway wire shape), the same split accountProfilePicture
// handles in Sphere.
func accountPictureID(account *gen.DyAccount) string {
	if account == nil {
		return ""
	}
	if file := account.GetProfile().GetPicture(); file != nil && file.GetId() != "" {
		return file.GetId()
	}
	return account.GetPicture().GetId()
}

// firstImageAttachmentID returns the first image attachment's file id, the
// preview the C# notification meta promotes to `image`.
func firstImageAttachmentID(raw datatypes.JSON) string {
	if len(raw) == 0 {
		return ""
	}
	var attachments []model.CloudFileReferenceObject
	if err := json.Unmarshal(raw, &attachments); err != nil {
		return ""
	}
	for _, attachment := range attachments {
		if attachment.Id != "" && strings.HasPrefix(attachment.MimeType, "image") {
			return attachment.Id
		}
	}
	return ""
}

func (i *instance) sendMemberJoined(ctx context.Context, room *store.ChatRoomEntity, member *store.ChatMemberEntity) {
	name := i.displayName(ctx, member)
	i.sendSystemMessage(ctx, room, member, "system.member.joined", name+" joined the chat.", map[string]any{
		"event":      "member_joined",
		"member_id":  member.ID.String(),
		"account_id": member.AccountID.String(),
	})
}

func (i *instance) sendMemberLeft(ctx context.Context, room *store.ChatRoomEntity, member *store.ChatMemberEntity, operator *store.ChatMemberEntity) {
	name := i.displayName(ctx, member)
	meta := map[string]any{
		"event":      "member_left",
		"member_id":  member.ID.String(),
		"account_id": member.AccountID.String(),
	}
	content := name + " left the chat."
	meta["reason"] = "left"
	if operator != nil && operator.ID != member.ID {
		content = name + " was removed from the chat by " + i.displayName(ctx, operator) + "."
		meta["operator_member_id"] = operator.ID.String()
		meta["operator_account_id"] = operator.AccountID.String()
		meta["reason"] = "removed"
	}
	i.sendSystemMessage(ctx, room, member, "system.member.left", content, meta)
}

func (i *instance) sendChatInfoUpdated(ctx context.Context, room *store.ChatRoomEntity, operator *store.ChatMemberEntity, changes map[string]any) {
	keys := sortedKeys(changes)
	i.sendSystemMessage(ctx, room, operator, "system.chat.updated",
		i.displayName(ctx, operator)+" updated chat info ("+strings.Join(keys, ", ")+").",
		map[string]any{"event": "chat_info_updated", "changes": changes})
}

func (i *instance) sendE2eeEnabled(ctx context.Context, room *store.ChatRoomEntity, sender *store.ChatMemberEntity) {
	content := "This chat now uses E2EE."
	if room.EncryptionMode == int(model.ChatEncryptionE2eeMls) {
		content = "This chat now uses MLS."
	}
	mlsGroupID := ""
	if room.MLSGroupID != nil {
		mlsGroupID = *room.MLSGroupID
	}
	i.sendSystemMessage(ctx, room, sender, "system.e2ee.enabled", content, map[string]any{
		"event":        "e2ee_enabled",
		"room_id":      room.ID.String(),
		"mode":         encryptionModeName(room.EncryptionMode),
		"mls_group_id": mlsGroupID,
	})
}

func (i *instance) sendMlsEpochChanged(ctx context.Context, room *store.ChatRoomEntity, sender *store.ChatMemberEntity, reason string) {
	mlsGroupID := ""
	if room.MLSGroupID != nil {
		mlsGroupID = *room.MLSGroupID
	}
	i.sendSystemMessage(ctx, room, sender, "system.mls.epoch_changed", "MLS epoch updated.", map[string]any{
		"event":        "mls_epoch_changed",
		"room_id":      room.ID.String(),
		"mls_group_id": mlsGroupID,
		"epoch":        time.Now().UnixMilli(),
		"reason":       reason,
	})
}

func (i *instance) sendMemberTimedOut(ctx context.Context, room *store.ChatRoomEntity, operator, target *store.ChatMemberEntity, reason *string, until time.Time) {
	content := i.displayName(ctx, target) + " was timed out by " + i.displayName(ctx, operator) + "."
	if nonEmpty(reason) {
		content = i.displayName(ctx, target) + " was timed out by " + i.displayName(ctx, operator) + ": " + *reason + "."
	}
	reasonText := ""
	if reason != nil {
		reasonText = *reason
	}
	i.sendSystemMessage(ctx, room, operator, "system.member.timed_out", content, map[string]any{
		"event":               "member_timed_out",
		"target_member_id":    target.ID.String(),
		"target_account_id":   target.AccountID.String(),
		"operator_member_id":  operator.ID.String(),
		"operator_account_id": operator.AccountID.String(),
		"reason":              reasonText,
		"timeout_until":       until.UnixMilli(),
	})
}

func (i *instance) sendMemberTimeoutRemoved(ctx context.Context, room *store.ChatRoomEntity, operator, target *store.ChatMemberEntity) {
	i.sendSystemMessage(ctx, room, operator, "system.member.timeout_removed",
		i.displayName(ctx, target)+"'s timeout was removed by "+i.displayName(ctx, operator)+".",
		map[string]any{
			"event":               "member_timeout_removed",
			"target_member_id":    target.ID.String(),
			"target_account_id":   target.AccountID.String(),
			"operator_member_id":  operator.ID.String(),
			"operator_account_id": operator.AccountID.String(),
		})
}

func encryptionModeName(mode int) string {
	if mode == int(model.ChatEncryptionE2eeMls) {
		return "E2eeMls"
	}
	return "None"
}

func newStatusError(code, message string, statusCode int) *errs.ApiError {
	return errs.New(code, message, statusCode)
}
