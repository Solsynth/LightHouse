package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	models "src.solsynth.dev/sosys/lighthouse/shared/pkg/models"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/core"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/model"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/store"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
)

// Permission keys used by the call routes, matching DysonNetwork.Shared.
const (
	permissionMessagesCreate = "chat.messages.create"
	permissionCallInvite     = "chat.call.invite"
	permissionCallKick       = "chat.call.kick"
	permissionCallMute       = "chat.call.mute"
)

// Error codes mirrored from the C# controller.
const (
	codeSessionNotConfigured = "CHAT_CALL_SESSION_NOT_CONFIGURED"
	codeTargetNotFound       = "CHAT_CALL_TARGET_NOT_FOUND"
	codeCannotKickOwner      = "CHAT_CALL_CANNOT_KICK_OWNER"
	codeParticipantNotFound  = "CHAT_CALL_PARTICIPANT_NOT_FOUND"
	codeNoTrack              = "CHAT_CALL_NO_TRACK"
)

// services is the core graph plus the Messager store helpers this module
// queries.
type services struct {
	*core.Services
	Store *store.Store
}

// server holds the module's dependencies.
type server struct {
	core     *services
	log      *slog.Logger
	livekit  *livekitClient
	voice    *voiceService
	accounts ports.Accounts
	notify   ports.Notify
	realtime ports.Realtime
	ring     gen.DyRingServiceClient
}

// account returns the authenticated account, writing 401 when anonymous.
func (s *server) account(c *gin.Context) (*gen.DyAccount, bool) {
	user := authn.CurrentUser(c)
	if user == nil || user.GetId() == "" {
		httpx.Unauthorized(c)
		return nil, false
	}
	return user, true
}

func (s *server) joinedMember(ctx context.Context, roomID, accountID uuid.UUID) (*store.ChatMemberEntity, error) {
	var entity store.ChatMemberEntity
	err := s.core.DB.WithContext(ctx).
		Where("chat_room_id = ? AND account_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", roomID, accountID).
		First(&entity).Error
	if err != nil {
		return nil, err
	}
	return &entity, nil
}

func (s *server) memberModel(ctx context.Context, entity *store.ChatMemberEntity) model.ChatMember {
	member := store.MemberToModel(entity)
	if s.accounts != nil {
		if account, err := s.accounts.GetAccount(ctx, entity.AccountID.String()); err == nil && account != nil {
			member.Account = accountRefOf(account)
		}
	}
	return member
}

func accountRefOf(account *gen.DyAccount) *accountRef {
	ref := &accountRef{
		ID:       account.GetId(),
		Name:     account.GetName(),
		Nick:     account.GetNick(),
		Region:   account.GetRegion(),
		Language: account.GetLanguage(),
	}
	if picture := account.GetPicture(); picture != nil {
		ref.Picture = models.FromProtoValue(picture)
	}
	return ref
}

func (s *server) displayName(ctx context.Context, member *store.ChatMemberEntity, fallback string) string {
	if member == nil {
		return fallback
	}
	if member.Nick != nil && strings.TrimSpace(*member.Nick) != "" {
		return *member.Nick
	}
	if s.accounts != nil {
		if account, err := s.accounts.GetAccount(ctx, member.AccountID.String()); err == nil && account != nil {
			if nick := strings.TrimSpace(account.GetNick()); nick != "" {
				return nick
			}
		}
	}
	return fallback
}

// roomTitle renders the human-readable room subject used by calls.
func roomTitle(room *store.ChatRoomEntity) *string {
	if room.Type == int(model.ChatRoomDirectMessage) && room.Name == nil {
		return new("DM")
	}
	if room.Name != nil && strings.TrimSpace(*room.Name) != "" {
		return room.Name
	}
	return new("Unknown")
}

func isRoomAdmin(room *store.ChatRoomEntity, member *store.ChatMemberEntity) bool {
	if room.Type == int(model.ChatRoomDirectMessage) {
		return true
	}
	if room.AccountID == nil {
		return false
	}
	return member.AccountID == *room.AccountID
}

// ensureCall implements the ensure-on-open semantics: one open call row per
// room, its provider session created (or reused) on every call route.
func (s *server) ensureCall(ctx context.Context, roomID uuid.UUID, member *store.ChatMemberEntity) (*store.RealtimeCallEntity, error) {
	call := &store.RealtimeCallEntity{}
	err := s.core.DB.WithContext(ctx).
		Where("room_id = ? AND ended_at IS NULL", roomID).
		Order("created_at DESC").
		First(call).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		call = &store.RealtimeCallEntity{
			ID:           uuid.New(),
			RoomID:       roomID,
			SenderID:     member.ID,
			ProviderName: new(s.livekit.provider()),
		}
		if err := s.core.DB.WithContext(ctx).Create(call).Error; err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	}

	sessionID, params, err := s.livekit.createSession(ctx, roomID, map[string]any{
		"room_id": roomID,
		"call_id": call.ID,
		"user_id": member.AccountID,
	})
	if err != nil {
		return nil, err
	}

	updates := map[string]any{}
	if call.ProviderName == nil || *call.ProviderName != s.livekit.provider() {
		updates["provider_name"] = s.livekit.provider()
	}
	if sessionID != "" && (call.SessionID == nil || *call.SessionID != sessionID) {
		updates["session_id"] = sessionID
	}
	if len(params) > 0 {
		if encoded, err := json.Marshal(params); err == nil {
			// datatypes.JSON makes pgx write the bytes as JSON instead of a
			// quoted JSON string.
			updates["upstream"] = datatypes.JSON(encoded)
		}
	}
	if len(updates) > 0 {
		if err := s.core.DB.WithContext(ctx).Model(&store.RealtimeCallEntity{}).
			Where("id = ?", call.ID).Updates(updates).Error; err != nil {
			return nil, err
		}
		if err := s.core.DB.WithContext(ctx).First(call, "id = ?", call.ID).Error; err != nil {
			return nil, err
		}
	}
	return call, nil
}

func (s *server) callModel(ctx context.Context, call *store.RealtimeCallEntity, room *store.ChatRoomEntity, sender *store.ChatMemberEntity) model.RealtimeCall {
	wire := store.CallToModel(call)
	if room != nil {
		roomWire := store.RoomToModel(room)
		wire.Room = &roomWire
	}
	if sender != nil {
		senderWire := s.memberModel(ctx, sender)
		wire.Sender = &senderWire
	}
	return wire
}

func (s *server) insertMessage(ctx context.Context, roomID uuid.UUID, entity *store.ChatMessageEntity) error {
	return s.core.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		sequence, err := s.core.Store.NextRoomSequence(ctx, tx, roomID)
		if err != nil {
			return err
		}
		entity.RoomSequence = sequence
		return tx.Create(entity).Error
	})
}

// broadcast pushes a message packet to every active room member through the
// realtime port. Delivery is best-effort, exactly like the C# background
// queue.
func (s *server) broadcast(ctx context.Context, roomID uuid.UUID, payload any) {
	if s.realtime == nil {
		return
	}
	var memberIDs []uuid.UUID
	if err := s.core.DB.WithContext(ctx).Model(&store.ChatMemberEntity{}).
		Where("chat_room_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", roomID).
		Pluck("account_id", &memberIDs).Error; err != nil {
		s.log.Warn("failed to list call room members for broadcast", "room_id", roomID, "error", err)
		return
	}
	accounts := make([]string, 0, len(memberIDs))
	for _, id := range memberIDs {
		accounts = append(accounts, id.String())
	}
	if len(accounts) == 0 {
		return
	}
	eventCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	go func() {
		defer cancel()
		if err := s.realtime.Push(eventCtx, accounts, "messages.new", payload); err != nil {
			s.log.Warn("failed to push call system message", "room_id", roomID, "error", err)
		}
	}()
}

// sendSystemMessage persists and delivers one system.call.* message.
func (s *server) sendSystemMessage(ctx context.Context, room *store.ChatRoomEntity, member *store.ChatMemberEntity, messageType, content string, meta map[string]any) {
	entity := &store.ChatMessageEntity{
		ID:         uuid.New(),
		Type:       messageType,
		ChatRoomID: room.ID,
		SenderID:   member.ID,
		Content:    new(content),
		Meta:       store.ToJSON(meta),
	}
	if err := s.insertMessage(ctx, room.ID, entity); err != nil {
		s.log.Warn("failed to persist call system message", "room_id", room.ID, "type", messageType, "error", err)
		return
	}
	message := store.MessageToModel(entity)
	sender := s.memberModel(ctx, member)
	message.Sender = &sender
	s.broadcast(ctx, room.ID, message)
}

func (s *server) buildParticipants(ctx context.Context, roomID uuid.UUID, participants []participantCacheItem) []CallParticipant {
	result := make([]CallParticipant, 0, len(participants))
	for _, participant := range participants {
		item := CallParticipant{
			Identity:  participant.Identity,
			Name:      participant.Name,
			AccountID: participant.AccountID,
			JoinedAt:  model.NewTime(participant.JoinedAt),
			TrackSid:  participant.TrackSid,
		}
		if participant.AccountID != nil {
			if accountID, err := uuid.Parse(*participant.AccountID); err == nil {
				if member, err := s.joinedMember(ctx, roomID, accountID); err == nil {
					profile := s.memberModel(ctx, member)
					item.Profile = &profile
				}
			}
		}
		result = append(result, item)
	}
	return result
}

func findParticipant(participants []participantCacheItem, accountID uuid.UUID) *participantCacheItem {
	for index := range participants {
		if participants[index].AccountID == nil {
			continue
		}
		if parsed, err := uuid.Parse(*participants[index].AccountID); err == nil && parsed == accountID {
			return &participants[index]
		}
	}
	return nil
}

func parseRoomID(c *gin.Context) (uuid.UUID, bool) {
	raw := c.Param("roomId")
	if raw == "" {
		// The chat module owns the `/chat/:id` wildcard these routes share.
		raw = c.Param("id")
	}
	roomID, err := uuid.Parse(raw)
	if err != nil {
		httpx.NotFound(c, "Room not found.")
		return uuid.UUID{}, false
	}
	return roomID, true
}

func parseAccountParam(c *gin.Context, name string) (uuid.UUID, bool) {
	accountID, err := uuid.Parse(c.Param(name))
	if err != nil {
		httpx.NotFound(c, "Account not found.")
		return uuid.UUID{}, false
	}
	return accountID, true
}

func (s *server) sessionNotConfigured(c *gin.Context) {
	httpx.Fail(c, errs.New(codeSessionNotConfigured, "Call session is not properly configured.", http.StatusBadRequest))
}

// getParticipants handles GET /api/chat/realtime/{roomId}/participants.
func (s *server) getParticipants(c *gin.Context) {
	user, ok := s.account(c)
	if !ok {
		return
	}
	roomID, ok := parseRoomID(c)
	if !ok {
		return
	}
	accountID, err := uuid.Parse(user.GetId())
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	member, err := s.joinedMember(c.Request.Context(), roomID, accountID)
	if err != nil {
		httpx.Forbidden(c, "You need to be a member to view participants.")
		return
	}
	if !s.livekit.configured() {
		s.sessionNotConfigured(c)
		return
	}
	call, err := s.ensureCall(c.Request.Context(), roomID, member)
	if err != nil {
		s.log.Error("failed to ensure realtime call", "room_id", roomID, "error", err)
		httpx.Internal(c)
		return
	}
	if call.SessionID == nil || strings.TrimSpace(*call.SessionID) == "" {
		s.sessionNotConfigured(c)
		return
	}
	participants, err := s.livekit.syncParticipants(c.Request.Context(), *call.SessionID)
	if err != nil {
		s.log.Error("failed to list livekit participants", "session", *call.SessionID, "error", err)
		httpx.Internal(c)
		return
	}
	httpx.OK(c, s.buildParticipants(c.Request.Context(), roomID, participants))
}

// kickParticipant handles POST /api/chat/realtime/{roomId}/kick/{targetAccountId}.
func (s *server) kickParticipant(c *gin.Context) {
	user, ok := s.account(c)
	if !ok {
		return
	}
	roomID, ok := parseRoomID(c)
	if !ok {
		return
	}
	targetAccountID, ok := parseAccountParam(c, "targetAccountId")
	if !ok {
		return
	}
	accountID, err := uuid.Parse(user.GetId())
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	ctx := c.Request.Context()
	member, err := s.joinedMember(ctx, roomID, accountID)
	if err != nil {
		httpx.Forbidden(c, "You need to be a member.")
		return
	}
	room, err := s.core.Store.GetRoom(ctx, roomID)
	if err != nil {
		httpx.NotFound(c, "Room not found.")
		return
	}
	if !s.livekit.configured() {
		s.sessionNotConfigured(c)
		return
	}
	call, err := s.ensureCall(ctx, roomID, member)
	if err != nil {
		s.log.Error("failed to ensure realtime call", "room_id", roomID, "error", err)
		httpx.Internal(c)
		return
	}
	if !isRoomAdmin(room, member) {
		httpx.Forbidden(c, "Only room admin can kick participants.")
		return
	}

	var request KickParticipantRequest
	if c.Request.ContentLength > 0 {
		if !httpx.BindJSON(c, &request) {
			return
		}
	}

	targetMember, err := s.joinedMember(ctx, roomID, targetAccountID)
	if err != nil {
		httpx.Fail(c, errs.New(codeTargetNotFound, "Target member not found.", http.StatusNotFound))
		return
	}
	if room.AccountID != nil && targetMember.AccountID == *room.AccountID {
		httpx.Fail(c, errs.New(codeCannotKickOwner, "Cannot kick the room owner.", http.StatusBadRequest))
		return
	}

	removed := false
	if call.SessionID != nil && strings.TrimSpace(*call.SessionID) != "" {
		participants, err := s.livekit.syncParticipants(ctx, *call.SessionID)
		if err != nil {
			s.log.Error("failed to list livekit participants", "session", *call.SessionID, "error", err)
			httpx.Internal(c)
			return
		}
		if target := findParticipant(participants, targetAccountID); target != nil {
			if err := s.livekit.kickParticipant(ctx, *call.SessionID, target.Identity); err != nil {
				s.log.Error("failed to kick call participant", "session", *call.SessionID, "identity", target.Identity, "error", err)
				httpx.Internal(c)
				return
			}
			removed = true
		}
	}

	if request.BanDurationMinutes != nil && *request.BanDurationMinutes > 0 {
		now := time.Now().UTC()
		reason := "Kicked from call"
		if request.Reason != nil && strings.TrimSpace(*request.Reason) != "" {
			reason = *request.Reason
		}
		cause := map[string]any{
			"reason":    reason,
			"type":      int(model.ChatTimeoutModerator),
			"sender_id": accountID.String(),
			"since":     model.NewTime(now),
		}
		until := now.Add(time.Duration(*request.BanDurationMinutes) * time.Minute)
		if err := s.core.DB.WithContext(ctx).Model(&store.ChatMemberEntity{}).
			Where("id = ?", targetMember.ID).
			Updates(map[string]any{
				"timeout_until": until,
				"timeout_cause": store.ToJSON(cause),
			}).Error; err != nil {
			s.log.Error("failed to apply call kick timeout", "member_id", targetMember.ID, "error", err)
			httpx.Internal(c)
			return
		}
	}

	if removed {
		memberName := s.displayName(ctx, targetMember, "Someone")
		operatorName := s.displayName(ctx, member, "Moderator")
		s.sendSystemMessage(ctx, room, member, "system.call.member.left",
			fmt.Sprintf("%s was removed from the call by %s.", memberName, operatorName),
			map[string]any{
				"event":               "call_member_left",
				"reason":              "removed",
				"call_id":             call.ID.String(),
				"member_id":           targetMember.ID.String(),
				"account_id":          targetMember.AccountID.String(),
				"operator_member_id":  member.ID.String(),
				"operator_account_id": member.AccountID.String(),
			})
	}
	httpx.NoContent(c)
}

// muteParticipant handles POST /api/chat/realtime/{roomId}/mute/{targetAccountId}.
func (s *server) muteParticipant(c *gin.Context) { s.toggleMute(c, true) }

// unmuteParticipant handles POST /api/chat/realtime/{roomId}/unmute/{targetAccountId}.
func (s *server) unmuteParticipant(c *gin.Context) { s.toggleMute(c, false) }

func (s *server) toggleMute(c *gin.Context, mute bool) {
	user, ok := s.account(c)
	if !ok {
		return
	}
	roomID, ok := parseRoomID(c)
	if !ok {
		return
	}
	targetAccountID, ok := parseAccountParam(c, "targetAccountId")
	if !ok {
		return
	}
	accountID, err := uuid.Parse(user.GetId())
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	ctx := c.Request.Context()
	member, err := s.joinedMember(ctx, roomID, accountID)
	if err != nil {
		httpx.Forbidden(c, "You need to be a member.")
		return
	}
	room, err := s.core.Store.GetRoom(ctx, roomID)
	if err != nil {
		httpx.NotFound(c, "Room not found.")
		return
	}
	if !s.livekit.configured() {
		s.sessionNotConfigured(c)
		return
	}
	call, err := s.ensureCall(ctx, roomID, member)
	if err != nil {
		s.log.Error("failed to ensure realtime call", "room_id", roomID, "error", err)
		httpx.Internal(c)
		return
	}
	if !isRoomAdmin(room, member) {
		httpx.Forbidden(c, "Only room admin can mute participants.")
		return
	}
	if call.SessionID == nil || strings.TrimSpace(*call.SessionID) == "" {
		s.sessionNotConfigured(c)
		return
	}
	participants, err := s.livekit.syncParticipants(ctx, *call.SessionID)
	if err != nil {
		s.log.Error("failed to list livekit participants", "session", *call.SessionID, "error", err)
		httpx.Internal(c)
		return
	}
	target := findParticipant(participants, targetAccountID)
	if target == nil {
		httpx.Fail(c, errs.New(codeParticipantNotFound, "Target participant not found in call.", http.StatusNotFound))
		return
	}
	if target.TrackSid == nil || strings.TrimSpace(*target.TrackSid) == "" {
		httpx.Fail(c, errs.New(codeNoTrack, "No track available to mute.", http.StatusBadRequest))
		return
	}
	if err := s.livekit.muteParticipant(ctx, *call.SessionID, target.Identity, *target.TrackSid, mute); err != nil {
		s.log.Error("failed to change call mute state", "session", *call.SessionID, "identity", target.Identity, "error", err)
		httpx.Internal(c)
		return
	}
	httpx.NoContent(c)
}

// getOngoingCall handles GET /api/chat/realtime/{roomId}.
func (s *server) getOngoingCall(c *gin.Context) {
	user, ok := s.account(c)
	if !ok {
		return
	}
	roomID, ok := parseRoomID(c)
	if !ok {
		return
	}
	accountID, err := uuid.Parse(user.GetId())
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	ctx := c.Request.Context()
	member, err := s.joinedMember(ctx, roomID, accountID)
	if err != nil {
		httpx.Forbidden(c, "You need to be a member to view call status.")
		return
	}
	if !s.livekit.configured() {
		s.sessionNotConfigured(c)
		return
	}
	call, err := s.ensureCall(ctx, roomID, member)
	if err != nil {
		s.log.Error("failed to ensure realtime call", "room_id", roomID, "error", err)
		httpx.Internal(c)
		return
	}
	room, err := s.core.Store.GetRoom(ctx, roomID)
	if err != nil {
		httpx.NotFound(c, "Room not found.")
		return
	}
	httpx.OK(c, s.callModel(ctx, call, room, member))
}

// joinCall handles GET /api/chat/realtime/{roomId}/join.
func (s *server) joinCall(c *gin.Context) {
	user, ok := s.account(c)
	if !ok {
		return
	}
	roomID, ok := parseRoomID(c)
	if !ok {
		return
	}
	accountID, err := uuid.Parse(user.GetId())
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	tool := c.Query("tool")
	ctx := c.Request.Context()
	member, err := s.joinedMember(ctx, roomID, accountID)
	if err != nil {
		httpx.Forbidden(c, "You need to be a member to join a call.")
		return
	}
	if member.TimeoutUntil != nil && member.TimeoutUntil.After(time.Now()) {
		httpx.Forbidden(c, "You has been timed out in this chat.")
		return
	}
	if !s.livekit.configured() {
		s.sessionNotConfigured(c)
		return
	}
	call, err := s.ensureCall(ctx, roomID, member)
	if err != nil {
		s.log.Error("failed to ensure realtime call", "room_id", roomID, "error", err)
		httpx.Internal(c)
		return
	}
	if call.SessionID == nil || strings.TrimSpace(*call.SessionID) == "" {
		s.sessionNotConfigured(c)
		return
	}
	room, err := s.core.Store.GetRoom(ctx, roomID)
	if err != nil {
		httpx.NotFound(c, "Room not found.")
		return
	}

	participants, err := s.livekit.syncParticipants(ctx, *call.SessionID)
	if err != nil {
		s.log.Error("failed to list livekit participants", "session", *call.SessionID, "error", err)
		httpx.Internal(c)
		return
	}
	defaultIdentity := participantIdentity(user, "")
	identity := participantIdentity(user, tool)
	isTool := identity != defaultIdentity
	alreadyInCall := false
	for _, participant := range participants {
		if participant.Identity == identity {
			alreadyInCall = true
			break
		}
	}
	admin := isRoomAdmin(room, member)
	token, err := s.livekit.userToken(user, identity, *call.SessionID, normalizeToolName(tool), admin)
	if err != nil {
		s.log.Error("failed to issue livekit token", "session", *call.SessionID, "error", err)
		httpx.Internal(c)
		return
	}
	if !isTool && !alreadyInCall {
		memberName := s.displayName(ctx, member, "Someone")
		s.sendSystemMessage(ctx, room, member, "system.call.member.joined",
			fmt.Sprintf("%s joined the call.", memberName),
			map[string]any{
				"event":      "call_member_joined",
				"call_id":    call.ID.String(),
				"member_id":  member.ID.String(),
				"account_id": member.AccountID.String(),
			})
	}

	httpx.OK(c, JoinCallResponse{
		Provider:     s.livekit.provider(),
		Endpoint:     s.livekit.endpoint,
		Token:        token,
		CallID:       call.ID.String(),
		RoomName:     *call.SessionID,
		RoomTitle:    roomTitle(room),
		IsAdmin:      admin,
		Participants: s.buildParticipants(ctx, roomID, participants),
	})
}

// startCall handles POST /api/chat/realtime/{roomId}: it only ensures the
// long-lived call record and session exist.
func (s *server) startCall(c *gin.Context) {
	user, ok := s.account(c)
	if !ok {
		return
	}
	roomID, ok := parseRoomID(c)
	if !ok {
		return
	}
	accountID, err := uuid.Parse(user.GetId())
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	ctx := c.Request.Context()
	member, err := s.joinedMember(ctx, roomID, accountID)
	if err != nil {
		httpx.Forbidden(c, "You need to be a member to start a call.")
		return
	}
	if member.TimeoutUntil != nil && member.TimeoutUntil.After(time.Now()) {
		httpx.Forbidden(c, "You has been timed out in this chat.")
		return
	}
	if !s.livekit.configured() {
		s.sessionNotConfigured(c)
		return
	}
	call, err := s.ensureCall(ctx, roomID, member)
	if err != nil {
		s.log.Error("failed to ensure realtime call", "room_id", roomID, "error", err)
		httpx.Internal(c)
		return
	}
	room, err := s.core.Store.GetRoom(ctx, roomID)
	if err != nil {
		httpx.NotFound(c, "Room not found.")
		return
	}
	httpx.OK(c, s.callModel(ctx, call, room, member))
}

// inviteToCall handles POST /api/chat/realtime/{roomId}/invite/{targetAccountId}.
func (s *server) inviteToCall(c *gin.Context) {
	user, ok := s.account(c)
	if !ok {
		return
	}
	roomID, ok := parseRoomID(c)
	if !ok {
		return
	}
	targetAccountID, ok := parseAccountParam(c, "targetAccountId")
	if !ok {
		return
	}
	accountID, err := uuid.Parse(user.GetId())
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	ctx := c.Request.Context()
	member, err := s.joinedMember(ctx, roomID, accountID)
	if err != nil {
		httpx.Forbidden(c, "You need to be a member to invite someone.")
		return
	}
	if member.TimeoutUntil != nil && member.TimeoutUntil.After(time.Now()) {
		httpx.Forbidden(c, "You have been timed out in this chat.")
		return
	}
	if _, err := s.joinedMember(ctx, roomID, targetAccountID); err != nil {
		httpx.Forbidden(c, "Target user is not a member of this room.")
		return
	}
	if !s.livekit.configured() {
		s.sessionNotConfigured(c)
		return
	}
	call, err := s.ensureCall(ctx, roomID, member)
	if err != nil {
		s.log.Error("failed to ensure realtime call", "room_id", roomID, "error", err)
		httpx.Internal(c)
		return
	}
	if call.SessionID == nil || strings.TrimSpace(*call.SessionID) == "" {
		s.sessionNotConfigured(c)
		return
	}
	room, err := s.core.Store.GetRoom(ctx, roomID)
	if err != nil {
		httpx.NotFound(c, "Room not found.")
		return
	}

	callerName := s.displayName(ctx, member, "Someone")
	subject := "Unknown"
	if title := roomTitle(room); title != nil {
		subject = *title
	}
	payload := map[string]any{
		"uuid":        uuid.New().String(),
		"caller_id":   accountID.String(),
		"caller_name": callerName,
		"room_id":     roomID.String(),
		"call_id":     call.ID.String(),
		"room_name":   subject,
		"session_id":  *call.SessionID,
	}
	if picture := user.GetPicture(); picture != nil && picture.GetId() != "" {
		payload["pfp"] = picture.GetId()
	}
	language := "en"
	if s.accounts != nil {
		if target, err := s.accounts.GetAccount(ctx, targetAccountID.String()); err == nil && target != nil {
			if value := strings.TrimSpace(target.GetLanguage()); value != "" {
				language = value
			}
		}
	}
	title, body := callInviteText(language, callerName, subject)

	s.pushCallInvite(ctx, targetAccountID.String(), title, body, payload)
	s.pushCallInviteWS(ctx, targetAccountID.String(), payload)
	httpx.NoContent(c)
}

func callInviteText(language, senderName, roomName string) (string, string) {
	switch strings.ToLower(language) {
	case "zh-hans", "zh-cn", "zh":
		return fmt.Sprintf("%s（%s）", senderName, roomName), fmt.Sprintf("%s 正在呼叫您", senderName)
	default:
		return fmt.Sprintf("%s (%s)", senderName, roomName), fmt.Sprintf("%s is calling you", senderName)
	}
}

// pushCallInvite sends the VoIP push (topic call.incoming, push type VoIP),
// best-effort. The notify port cannot express a push type, so the C# path
// through the Ring service is used when it is wired.
func (s *server) pushCallInvite(ctx context.Context, accountID, title, body string, payload map[string]any) {
	if s.notify == nil && s.ring == nil {
		return
	}
	meta := make(map[string]string, len(payload)+1)
	for key, value := range payload {
		meta[key] = fmt.Sprint(value)
	}
	meta["push_type"] = "VoIP"
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = nil
	}
	eventCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	go func() {
		defer cancel()
		var pushErr error
		switch {
		case s.ring != nil:
			_, pushErr = s.ring.SendPushNotificationToUsers(eventCtx, &gen.DySendPushNotificationToUsersRequest{
				UserIds: []string{accountID},
				Notification: &gen.DyPushNotification{
					Topic:     "call.incoming",
					Title:     title,
					Body:      body,
					Meta:      raw,
					PushType:  new("VoIP"),
					IsSavable: false,
				},
			})
		case s.notify != nil:
			if voip, ok := s.notify.(voipPushPort); ok {
				pushErr = voip.SendPushWithType(eventCtx, accountID, "call.incoming", title, body, "VoIP", meta)
			} else {
				pushErr = s.notify.SendPush(eventCtx, accountID, "call.incoming", title, body, meta)
			}
		}
		if pushErr != nil {
			s.log.Warn("failed to send call invite push", "account_id", accountID, "error", pushErr)
		}
	}()
}

// pushCallInviteWS sends the call.invited websocket packet, best-effort.
func (s *server) pushCallInviteWS(ctx context.Context, accountID string, payload map[string]any) {
	if s.realtime == nil {
		return
	}
	eventCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	go func() {
		defer cancel()
		if err := s.realtime.Push(eventCtx, []string{accountID}, "call.invited", payload); err != nil {
			s.log.Warn("failed to push call invite packet", "account_id", accountID, "error", err)
		}
	}()
}

// voipPushPort is the richer push contract (push type aware) a notify
// provider may implement.
type voipPushPort interface {
	SendPushWithType(ctx context.Context, accountID, topic, title, body, pushType string, meta map[string]string) error
}

// leaveCall handles DELETE /api/chat/realtime/{roomId}.
func (s *server) leaveCall(c *gin.Context) {
	user, ok := s.account(c)
	if !ok {
		return
	}
	roomID, ok := parseRoomID(c)
	if !ok {
		return
	}
	accountID, err := uuid.Parse(user.GetId())
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	tool := c.Query("tool")
	ctx := c.Request.Context()
	member, err := s.joinedMember(ctx, roomID, accountID)
	if err != nil {
		httpx.Forbidden(c, "You need to be a member to leave a call.")
		return
	}
	if !s.livekit.configured() {
		httpx.NoContent(c)
		return
	}
	call, err := s.ensureCall(ctx, roomID, member)
	if err != nil {
		s.log.Error("failed to ensure realtime call", "room_id", roomID, "error", err)
		httpx.Internal(c)
		return
	}
	if call.SessionID == nil || strings.TrimSpace(*call.SessionID) == "" {
		httpx.NoContent(c)
		return
	}
	room, err := s.core.Store.GetRoom(ctx, roomID)
	if err != nil {
		httpx.NotFound(c, "Room not found.")
		return
	}
	identity := participantIdentity(user, tool)
	isTool := identity != participantIdentity(user, "")

	participants, err := s.livekit.syncParticipants(ctx, *call.SessionID)
	if err != nil {
		s.log.Error("failed to list livekit participants", "session", *call.SessionID, "error", err)
		httpx.Internal(c)
		return
	}
	wasInCall := false
	remaining := 0
	for _, participant := range participants {
		if participant.Identity == identity {
			wasInCall = true
			continue
		}
		remaining++
	}
	if wasInCall {
		if err := s.livekit.kickParticipant(ctx, *call.SessionID, identity); err != nil {
			s.log.Error("failed to remove call participant", "session", *call.SessionID, "identity", identity, "error", err)
			httpx.Internal(c)
			return
		}
	}
	if wasInCall && !isTool {
		memberName := s.displayName(ctx, member, "Someone")
		s.sendSystemMessage(ctx, room, member, "system.call.member.left",
			fmt.Sprintf("%s left the call.", memberName),
			map[string]any{
				"event":      "call_member_left",
				"reason":     "left",
				"call_id":    call.ID.String(),
				"member_id":  member.ID.String(),
				"account_id": member.AccountID.String(),
			})
	}

	// The last participant leaving ends the call: the provider room is torn
	// down and the row is closed so the next open starts a fresh session.
	if wasInCall && remaining == 0 {
		if err := s.livekit.endSession(ctx, *call.SessionID); err != nil {
			s.log.Warn("failed to delete livekit room", "session", *call.SessionID, "error", err)
		}
		now := time.Now().UTC()
		if err := s.core.DB.WithContext(ctx).Model(&store.RealtimeCallEntity{}).
			Where("id = ?", call.ID).
			Update("ended_at", now).Error; err != nil {
			s.log.Error("failed to close realtime call", "call_id", call.ID, "error", err)
		}
	}
	httpx.NoContent(c)
}
