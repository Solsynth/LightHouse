package chat

import (
	"context"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"gorm.io/gorm"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/model"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/store"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
)

// --- helpers --------------------------------------------------------------

func (i *instance) logAction(ctx context.Context, c *gin.Context, accountID, action string, meta map[string]any) {
	if i.host == nil || i.host.Core.Clients == nil {
		return
	}
	client := i.host.Core.Clients.ActionLog
	if client == nil {
		return
	}
	values := map[string]*structpb.Value{}
	for key, value := range meta {
		if converted, err := structpb.NewValue(value); err == nil {
			values[key] = converted
		}
	}
	request := &gen.DyCreateActionLogRequest{Action: action, AccountId: accountID, Meta: values}
	if agent := c.Request.UserAgent(); agent != "" {
		request.UserAgent = wrapperspb.String(agent)
	}
	if ip := c.ClientIP(); ip != "" {
		request.IpAddress = wrapperspb.String(ip)
	}
	if _, err := client.CreateActionLog(ctx, request); err != nil {
		i.log.Debug("chat: action log failed", "action", action, "error", err)
	}
}

// roomPermission applies the room authorization matrix used by the C#
// controller: realm rooms require a moderator, DMs require membership and
// group rooms require ownership.
func (i *instance) roomPermission(ctx context.Context, c *gin.Context, room *store.ChatRoomEntity, accountID string, realmMessage, dmMessage, groupMessage string) bool {
	if room.RealmID != nil {
		if !i.isRealmModerator(ctx, room.RealmID, accountID) {
			failForbidden(c, realmMessage)
			return false
		}
		return true
	}
	switch model.ChatRoomType(room.Type) {
	case model.ChatRoomDirectMessage:
		if !i.isChatMember(ctx, room.ID, accountID) {
			failForbidden(c, dmMessage)
			return false
		}
	case model.ChatRoomGroup:
		if room.AccountID == nil || room.AccountID.String() != accountID {
			failForbidden(c, groupMessage)
			return false
		}
	}
	return true
}

func (i *instance) fileReference(ctx context.Context, id string) (model.CloudFileReferenceObject, bool) {
	if i.files == nil {
		return model.CloudFileReferenceObject{}, false
	}
	files, err := i.files.GetFiles(ctx, []string{id})
	if err != nil {
		return model.CloudFileReferenceObject{}, false
	}
	file, ok := files[id]
	return file, ok
}

func relationshipBlocked(ctx context.Context, i *instance, accountID, relatedID string) bool {
	if i.host == nil || i.host.Core.Clients == nil || i.host.Core.Clients.Profile == nil {
		return false
	}
	blocked := int32(-100)
	value, err := i.host.Core.Clients.Profile.HasRelationship(ctx, &gen.DyGetRelationshipRequest{
		AccountId:       accountID,
		RelatedId:       relatedID,
		Status:          &blocked,
		EitherDirection: true,
	})
	return err == nil && value.GetValue()
}

func (i *instance) findDirectRoom(ctx context.Context, accountID, relatedID string, encryptionMode *int) (*store.ChatRoomEntity, uuid.UUID, uuid.UUID) {
	accountUUID, err1 := uuid.Parse(accountID)
	relatedUUID, err2 := uuid.Parse(relatedID)
	if err1 != nil || err2 != nil {
		return nil, uuid.Nil, uuid.Nil
	}
	query := i.db.WithContext(ctx).Model(&store.ChatRoomEntity{}).
		Where("type = ?", int(model.ChatRoomDirectMessage)).
		Where("EXISTS (SELECT 1 FROM chat_members m WHERE m.chat_room_id = chat_rooms.id AND m.account_id = ?)", accountUUID).
		Where("EXISTS (SELECT 1 FROM chat_members m WHERE m.chat_room_id = chat_rooms.id AND m.account_id = ?)", relatedUUID).
		Where("(SELECT COUNT(*) FROM chat_members m WHERE m.chat_room_id = chat_rooms.id) = 2")
	if encryptionMode != nil {
		query = query.Where("encryption_mode = ?", *encryptionMode)
	}
	var room store.ChatRoomEntity
	if err := query.First(&room).Error; err != nil {
		return nil, uuid.Nil, uuid.Nil
	}
	var members []*store.ChatMemberEntity
	_ = i.db.WithContext(ctx).Where("chat_room_id = ?", room.ID).Find(&members).Error
	var selfMember, otherMember uuid.UUID
	for _, member := range members {
		if member.AccountID == accountUUID {
			selfMember = member.ID
		} else {
			otherMember = member.ID
		}
	}
	return &room, selfMember, otherMember
}

func encryptionModeValidForRoomType(roomType int, mode int) bool {
	return mode == int(model.ChatEncryptionNone) || mode == int(model.ChatEncryptionE2eeMls)
}

func newEncryptedModeAllowed(mode int) bool {
	return mode == int(model.ChatEncryptionNone) || mode == int(model.ChatEncryptionE2eeMls)
}

// --- routes ---------------------------------------------------------------

func (i *instance) getChatRoom(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	room, err := i.st.GetRoom(ctx, roomID)
	if err != nil {
		failNotFound(c)
		return
	}
	viewer := currentAccountID(c)
	rooms := i.hydrateRooms(ctx, []*store.ChatRoomEntity{room}, viewer)
	httpx.OK(c, rooms[0])
}

func (i *instance) getChatRoomBySlug(c *gin.Context) {
	ctx := c.Request.Context()
	room, err := i.roomBySlug(ctx, c.Param("scope"), c.Param("chatSlug"))
	if err != nil {
		failNotFound(c)
		return
	}
	rooms := i.hydrateRooms(ctx, []*store.ChatRoomEntity{room}, currentAccountID(c))
	httpx.OK(c, rooms[0])
}

func (i *instance) sortRoomsByLastMessage(ctx context.Context, roomIDs []uuid.UUID) []uuid.UUID {
	type row struct {
		ChatRoomID uuid.UUID
		Latest     time.Time
	}
	var rows []row
	_ = i.db.WithContext(ctx).Model(&store.ChatMessageEntity{}).
		Select("chat_room_id, MAX(created_at) AS latest").
		Where("chat_room_id IN ?", roomIDs).Group("chat_room_id").Scan(&rows).Error
	latest := map[uuid.UUID]time.Time{}
	for _, item := range rows {
		latest[item.ChatRoomID] = item.Latest
	}
	now := utcNow()
	sorted := append([]uuid.UUID{}, roomIDs...)
	sortUUIDs(sorted, func(id uuid.UUID) time.Time {
		if value, ok := latest[id]; ok {
			return value
		}
		return now
	})
	return sorted
}

func sortUUIDs(ids []uuid.UUID, key func(uuid.UUID) time.Time) {
	for a := 1; a < len(ids); a++ {
		for b := a; b > 0 && key(ids[b]).After(key(ids[b-1])); b-- {
			ids[b], ids[b-1] = ids[b-1], ids[b]
		}
	}
}

func (i *instance) listJoinedRooms(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := currentAccountID(c)
	accountUUID, _ := uuid.Parse(accountID)
	var members []*store.ChatMemberEntity
	_ = i.db.WithContext(ctx).
		Where("account_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", accountUUID).
		Find(&members).Error
	roomIDs := make([]uuid.UUID, 0, len(members))
	for _, member := range members {
		roomIDs = append(roomIDs, member.ChatRoomID)
	}
	var rooms []model.ChatRoom
	if len(roomIDs) > 0 {
		sorted := i.sortRoomsByLastMessage(ctx, roomIDs)
		var entities []*store.ChatRoomEntity
		_ = i.db.WithContext(ctx).Where("id IN ?", sorted).Find(&entities).Error
		byID := map[string]*store.ChatRoomEntity{}
		for _, entity := range entities {
			byID[entity.ID.String()] = entity
		}
		ordered := make([]*store.ChatRoomEntity, 0, len(sorted))
		for _, id := range sorted {
			if entity, ok := byID[id.String()]; ok {
				ordered = append(ordered, entity)
			}
		}
		rooms = i.hydrateRooms(ctx, ordered, accountID)
	} else {
		rooms = []model.ChatRoom{}
	}
	httpx.OK(c, gin.H{"rooms": rooms, "groups": i.listGroupModels(ctx, accountID)})
}

func (i *instance) createDirectMessage(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := currentAccountID(c)
	var request directMessageRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	relatedID := strings.TrimSpace(request.RelatedUserID)
	if _, err := uuid.Parse(relatedID); err != nil {
		failBadRequest(c, "CHAT_RELATED_USER_NOT_FOUND", "Related user was not found")
		return
	}
	if i.accounts == nil {
		failInternal(c, portsUnavailable())
		return
	}
	relatedUser, err := i.accounts.GetAccount(ctx, relatedID)
	if err != nil || relatedUser == nil {
		failBadRequest(c, "CHAT_RELATED_USER_NOT_FOUND", "Related user was not found")
		return
	}
	if relatedID == accountID {
		failBadRequest(c, "CHAT_RELATED_USER_NOT_FOUND", "Related user was not found")
		return
	}
	if relationshipBlocked(ctx, i, accountID, relatedID) {
		failForbidden(c, "You cannot create direct message with a user that you have blocked or who has blocked you.")
		return
	}
	requestedMode := int(model.ChatEncryptionNone)
	if request.EncryptionMode != nil {
		requestedMode = *request.EncryptionMode
	}
	if !encryptionModeValidForRoomType(int(model.ChatRoomDirectMessage), requestedMode) {
		failConflict(c, "CHAT_E2EE_MODE_INVALID_FOR_ROOM", "Invalid encryption mode for direct message room.")
		return
	}
	if !newEncryptedModeAllowed(requestedMode) {
		failConflict(c, "CHAT_E2EE_LEGACY_MODE_FORBIDDEN", "Legacy encrypted room modes are not allowed for new rooms. Use E2eeMls.")
		return
	}
	if existing, _, _ := i.findDirectRoom(ctx, accountID, relatedID, &requestedMode); existing != nil {
		failBadRequest(c, "CHAT_DM_ALREADY_EXISTS", "You already have a DM with this user.")
		return
	}

	now := utcNow()
	selfUUID, _ := uuid.Parse(accountID)
	roomID := uuid.New()
	room := &store.ChatRoomEntity{
		ID:             roomID,
		Type:           int(model.ChatRoomDirectMessage),
		IsPublic:       false,
		AccountID:      &selfUUID,
		EncryptionMode: requestedMode,
		E2eePolicy:     nil,
		Background:     store.ToJSON(nil),
	}
	members := []*store.ChatMemberEntity{
		{
			ID:          uuid.New(),
			ChatRoomID:  roomID,
			AccountID:   selfUUID,
			Username:    new(currentUserName(c)),
			JoinedAt:    &now,
			ConfirmedAt: &now,
		},
		{
			ID:         uuid.New(),
			ChatRoomID: roomID,
			AccountID:  uuid.MustParse(relatedID),
			Username:   new(relatedUser.GetName()),
			JoinedAt:   &now,
		},
	}
	if err := i.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(room).Error; err != nil {
			return err
		}
		for _, member := range members {
			if err := tx.Create(member).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		failInternal(c, err)
		return
	}
	i.logAction(ctx, c, accountID, "chatrooms.create", map[string]any{"chatroom_id": roomID.String()})
	rooms := i.hydrateRooms(ctx, []*store.ChatRoomEntity{room}, accountID)
	httpx.OK(c, rooms[0])
}

func currentUserName(c *gin.Context) string {
	if user := currentUser(c); user != nil {
		return user.GetName()
	}
	return ""
}

func (i *instance) confirmDirectMessage(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	accountID := currentAccountID(c)
	room, err := i.st.GetRoom(ctx, roomID)
	if err != nil {
		failNotFound(c)
		return
	}
	var members []*store.ChatMemberEntity
	_ = i.db.WithContext(ctx).Where("chat_room_id = ?", roomID).Find(&members).Error
	if room.Type != int(model.ChatRoomDirectMessage) || len(members) != 2 {
		failBadRequest(c, "CHAT_DM_CONFIRM_NOT_ALLOWED", "Only one-to-one direct messages can be confirmed.")
		return
	}
	accountUUID, _ := uuid.Parse(accountID)
	var member *store.ChatMemberEntity
	for _, candidate := range members {
		if candidate.AccountID == accountUUID {
			member = candidate
		}
	}
	if member == nil || (room.AccountID != nil && room.AccountID.String() == accountID) {
		failForbidden(c, "Only the active recipient can confirm this direct message.")
		return
	}
	if member.ConfirmedAt != nil {
		failBadRequest(c, "CHAT_DM_ALREADY_CONFIRMED", "This direct message has already been confirmed.")
		return
	}
	if member.JoinedAt == nil || member.LeaveAt != nil {
		failForbidden(c, "Only the active recipient can confirm this direct message.")
		return
	}
	now := utcNow()
	_ = i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).Where("id = ?", member.ID).Update("confirmed_at", now).Error
	i.logAction(ctx, c, accountID, "chatrooms.dm.confirm", map[string]any{"chatroom_id": roomID.String()})
	rooms := i.hydrateRooms(ctx, []*store.ChatRoomEntity{room}, accountID)
	httpx.OK(c, rooms[0])
}

func (i *instance) blockDirectMessage(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	accountID := currentAccountID(c)
	accountUUID, _ := uuid.Parse(accountID)
	room, err := i.st.GetRoom(ctx, roomID)
	if err != nil {
		failNotFound(c)
		return
	}
	var members []*store.ChatMemberEntity
	_ = i.db.WithContext(ctx).Where("chat_room_id = ?", roomID).Find(&members).Error
	if room.Type != int(model.ChatRoomDirectMessage) || len(members) != 2 {
		httpx.Fail(c, errs.New("CHAT_DM_BLOCK_NOT_ALLOWED", "Only an unconfirmed one-to-one direct message can be blocked.", 403))
		return
	}
	var member *store.ChatMemberEntity
	for _, candidate := range members {
		if candidate.AccountID == accountUUID {
			member = candidate
		}
	}
	if member == nil {
		failNotFound(c)
		return
	}
	if (room.AccountID != nil && room.AccountID.String() == accountID) || member.ConfirmedAt != nil || member.JoinedAt == nil {
		httpx.Fail(c, errs.New("CHAT_DM_BLOCK_NOT_ALLOWED", "Only the active recipient can block this direct message.", 403))
		return
	}
	if member.LeaveAt != nil {
		failBadRequest(c, "CHAT_DM_ALREADY_BLOCKED", "This direct message is already blocked.")
		return
	}
	now := utcNow()
	_ = i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).Where("id = ?", member.ID).Update("leave_at", now).Error
	member.LeaveAt = &now
	i.sendMemberLeft(ctx, room, member, nil)
	i.emitEncryptionMembershipChanged(ctx, room, member, "member_left")
	i.logAction(ctx, c, accountID, "chatrooms.dm.block", map[string]any{"chatroom_id": roomID.String()})
	httpx.NoContent(c)
}

func (i *instance) unblockDirectMessage(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	accountID := currentAccountID(c)
	accountUUID, _ := uuid.Parse(accountID)
	room, err := i.st.GetRoom(ctx, roomID)
	if err != nil {
		failNotFound(c)
		return
	}
	var members []*store.ChatMemberEntity
	_ = i.db.WithContext(ctx).Where("chat_room_id = ?", roomID).Find(&members).Error
	if room.Type != int(model.ChatRoomDirectMessage) || len(members) != 2 {
		httpx.Fail(c, errs.New("CHAT_DM_BLOCK_NOT_ALLOWED", "Only a one-to-one direct message can be unblocked.", 403))
		return
	}
	var member *store.ChatMemberEntity
	for _, candidate := range members {
		if candidate.AccountID == accountUUID {
			member = candidate
		}
	}
	if member == nil {
		failNotFound(c)
		return
	}
	if (room.AccountID != nil && room.AccountID.String() == accountID) || member.ConfirmedAt != nil {
		httpx.Fail(c, errs.New("CHAT_DM_BLOCK_NOT_ALLOWED", "Only the recipient can unblock this direct message.", 403))
		return
	}
	if member.LeaveAt == nil {
		failBadRequest(c, "CHAT_DM_NOT_BLOCKED", "This direct message is not blocked.")
		return
	}
	now := utcNow()
	_ = i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).Where("id = ?", member.ID).
		Updates(map[string]any{"leave_at": nil, "joined_at": now}).Error
	member.LeaveAt = nil
	member.JoinedAt = &now
	i.sendMemberJoined(ctx, room, member)
	i.emitEncryptionMembershipChanged(ctx, room, member, "member_joined")
	i.logAction(ctx, c, accountID, "chatrooms.dm.unblock", map[string]any{"chatroom_id": roomID.String()})
	models := i.memberModels(ctx, []*store.ChatMemberEntity{member})
	httpx.OK(c, models[0])
}

func (i *instance) getDirectChatRoom(c *gin.Context) {
	ctx := c.Request.Context()
	currentID := currentAccountID(c)
	relatedID := c.Param("accountId")
	if _, err := uuid.Parse(relatedID); err != nil {
		failNotFound(c)
		return
	}
	room, _, _ := i.findDirectRoom(ctx, currentID, relatedID, nil)
	if room == nil {
		failNotFound(c)
		return
	}
	rooms := i.hydrateRooms(ctx, []*store.ChatRoomEntity{room}, currentID)
	httpx.OK(c, rooms[0])
}

func (i *instance) emitEncryptionMembershipChanged(ctx context.Context, room *store.ChatRoomEntity, actor *store.ChatMemberEntity, reason string) {
	if room.EncryptionMode == int(model.ChatEncryptionE2eeMls) {
		i.sendMlsEpochChanged(ctx, room, actor, reason)
	}
}
