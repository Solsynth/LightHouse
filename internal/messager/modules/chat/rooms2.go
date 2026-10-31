package chat

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/model"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/store"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
)

const defaultMlsCiphersuite = "MLS_128_DHKEMX25519_AES128GCM_SHA256_Ed25519"

func (i *instance) createChatRoom(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := currentAccountID(c)
	var request chatRoomRequest
	if !httpx.BindJSON(c, (&request)) {
		return
	}
	if request.Name == nil {
		failBadRequest(c, "CHAT_ROOM_NAME_REQUIRED", "You cannot create a chat room without a name.")
		return
	}
	if len([]rune(*request.Name)) > 1024 {
		failValidation(c, "name", "The name field must be at most 1024 characters.", "")
		return
	}
	mode := int(model.ChatEncryptionNone)
	if request.EncryptionMode != nil {
		mode = *request.EncryptionMode
	}
	if !encryptionModeValidForRoomType(int(model.ChatRoomGroup), mode) {
		failConflict(c, "CHAT_E2EE_MODE_INVALID_FOR_ROOM", "Invalid encryption mode for group room.")
		return
	}
	if !newEncryptedModeAllowed(mode) {
		failConflict(c, "CHAT_E2EE_LEGACY_MODE_FORBIDDEN", "Legacy encrypted room modes are not allowed for new rooms. Use E2eeMls.")
		return
	}
	accountUUID, _ := uuid.Parse(accountID)
	room := &store.ChatRoomEntity{
		ID:             uuid.New(),
		Name:           request.Name,
		Description:    new(""),
		Type:           int(model.ChatRoomGroup),
		AccountID:      &accountUUID,
		EncryptionMode: mode,
		E2eePolicy:     nil,
		Picture:        store.ToJSON(nil),
		Background:     store.ToJSON(nil),
	}
	if request.Description != nil {
		room.Description = request.Description
	}
	if request.IsCommunity != nil {
		room.IsCommunity = *request.IsCommunity
	}
	if request.IsPublic != nil {
		room.IsPublic = *request.IsPublic
	}
	if request.IsReadReceiptsPublic != nil {
		room.IsReadReceiptsPublic = *request.IsReadReceiptsPublic
	}
	if request.E2eePolicy != nil {
		room.E2eePolicy = store.ToJSON(request.E2eePolicy)
	}
	if request.RealmID != nil && *request.RealmID != "" {
		realmID, err := uuid.Parse(*request.RealmID)
		if err != nil {
			failNotFound(c)
			return
		}
		if !i.isRealmModerator(ctx, &realmID, accountID) {
			failForbidden(c, "You need at least be a moderator to create chat linked to the realm.")
			return
		}
		room.RealmID = &realmID
	}
	normalizedSlug := normalizeSlug(request.Slug)
	if normalizedSlug != nil {
		if !chatRoomSlugRegex.MatchString(*normalizedSlug) {
			failBadRequest(c, "CHAT_ROOM_SLUG_INVALID", "Slug must be URL-safe (lowercase alphanumeric, hyphens, underscores, or periods) and cannot start or end with special characters.")
			return
		}
		if !i.slugAvailable(ctx, *normalizedSlug, accountID, room.RealmID, nil) {
			failConflict(c, "CHAT_ROOM_SLUG_EXISTS", "A chat room with this slug already exists in this scope.")
			return
		}
		room.Slug = normalizedSlug
	}
	if request.PictureID != nil {
		file, ok := i.fileReference(ctx, *request.PictureID)
		if !ok {
			failBadRequest(c, "CHAT_PICTURE_NOT_FOUND", "Invalid picture id, unable to find the file on cloud.")
			return
		}
		room.Picture = store.ToJSON(file)
	}
	if request.BackgroundID != nil {
		file, ok := i.fileReference(ctx, *request.BackgroundID)
		if !ok {
			failBadRequest(c, "CHAT_BACKGROUND_NOT_FOUND", "Invalid background id, unable to find the file on cloud.")
			return
		}
		room.Background = store.ToJSON(file)
	}
	now := utcNow()
	owner := &store.ChatMemberEntity{
		ID:         uuid.New(),
		ChatRoomID: room.ID,
		AccountID:  accountUUID,
		Username:   new(currentUserName(c)),
		JoinedAt:   &now,
	}
	if err := i.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(room).Error; err != nil {
			return err
		}
		return tx.Create(owner).Error
	}); err != nil {
		failInternal(c, err)
		return
	}
	i.logAction(ctx, c, accountID, "chatrooms.create", map[string]any{"chatroom_id": room.ID.String()})
	rooms := i.hydrateRooms(ctx, []*store.ChatRoomEntity{room}, accountID)
	httpx.OK(c, rooms[0])
}

func (i *instance) updateChatRoom(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	accountID := currentAccountID(c)
	var request chatRoomRequest
	if !httpx.BindJSON(c, (&request)) {
		return
	}
	room, err := i.st.GetRoom(ctx, roomID)
	if err != nil {
		failNotFound(c)
		return
	}
	if !i.roomPermission(ctx, c, room, accountID,
		"You need at least be a realm moderator to update this chat.",
		"You need be part of the DM to update this chat.",
		"You need be the owner to update for this chat.") {
		return
	}

	previousName := room.Name
	previousDescription := room.Description
	previousCommunity := room.IsCommunity
	previousPublic := room.IsPublic
	previousReceipts := room.IsReadReceiptsPublic
	previousRealm := room.RealmID
	previousSlug := room.Slug
	previousPicture := room.Picture
	previousBackground := room.Background

	if request.RealmID != nil && *request.RealmID != "" {
		realmID, err := uuid.Parse(*request.RealmID)
		if err != nil {
			failNotFound(c)
			return
		}
		if !i.isRealmModerator(ctx, &realmID, accountID) {
			failForbidden(c, "You need at least be a moderator to transfer the chat linked to the realm.")
			return
		}
		room.RealmID = &realmID
	}
	if request.PictureID != nil {
		file, ok := i.fileReference(ctx, *request.PictureID)
		if !ok {
			failBadRequest(c, "CHAT_PICTURE_NOT_FOUND", "Invalid picture id, unable to find the file on cloud.")
			return
		}
		room.Picture = store.ToJSON(file)
	}
	if request.BackgroundID != nil {
		file, ok := i.fileReference(ctx, *request.BackgroundID)
		if !ok {
			failBadRequest(c, "CHAT_BACKGROUND_NOT_FOUND", "Invalid background id, unable to find the file on cloud.")
			return
		}
		room.Background = store.ToJSON(file)
	}
	if request.Name != nil {
		room.Name = request.Name
	}
	if request.Description != nil {
		room.Description = request.Description
	}
	if request.IsCommunity != nil {
		room.IsCommunity = *request.IsCommunity
	}
	if request.IsPublic != nil {
		room.IsPublic = *request.IsPublic
	}
	if request.IsReadReceiptsPublic != nil {
		room.IsReadReceiptsPublic = *request.IsReadReceiptsPublic
	}
	if request.EncryptionMode != nil {
		failConflict(c, "CHAT_MLS_ENABLE_ENDPOINT_REQUIRED", "Use POST /api/chat/{id}/mls/enable to enable encryption. Encryption cannot be disabled.")
		return
	}
	if request.E2eePolicy != nil {
		room.E2eePolicy = store.ToJSON(request.E2eePolicy)
	}
	if request.Slug != nil {
		if room.Type != int(model.ChatRoomGroup) {
			failBadRequest(c, "CHAT_ROOM_SLUG_INVALID", "Only group chat rooms can have a shareable slug.")
			return
		}
		normalized := normalizeSlug(request.Slug)
		if normalized == nil {
			room.Slug = nil
		} else {
			if !chatRoomSlugRegex.MatchString(*normalized) {
				failBadRequest(c, "CHAT_ROOM_SLUG_INVALID", "Slug must be URL-safe (lowercase alphanumeric, hyphens, underscores, or periods) and cannot start or end with special characters.")
				return
			}
			if !i.slugAvailable(ctx, *normalized, accountID, room.RealmID, &room.ID) {
				failConflict(c, "CHAT_ROOM_SLUG_EXISTS", "A chat room with this slug already exists in this scope.")
				return
			}
			room.Slug = normalized
		}
	}
	if !encryptionModeValidForRoomType(room.Type, room.EncryptionMode) {
		failConflict(c, "CHAT_E2EE_MODE_INVALID_FOR_ROOM", "Invalid encryption mode for this room type.")
		return
	}
	if err := i.db.WithContext(ctx).Save(room).Error; err != nil {
		failInternal(c, err)
		return
	}

	changes := map[string]any{}
	if !sameString(previousName, room.Name) {
		changes["name"] = changeValue(previousName, room.Name)
	}
	if !sameString(previousDescription, room.Description) {
		changes["description"] = changeValue(previousDescription, room.Description)
	}
	if previousCommunity != room.IsCommunity {
		changes["is_community"] = changeValue(previousCommunity, room.IsCommunity)
	}
	if previousPublic != room.IsPublic {
		changes["is_public"] = changeValue(previousPublic, room.IsPublic)
	}
	if previousReceipts != room.IsReadReceiptsPublic {
		changes["is_read_receipts_public"] = changeValue(previousReceipts, room.IsReadReceiptsPublic)
	}
	if !sameUUID(previousRealm, room.RealmID) {
		changes["realm_id"] = changeValue(uuidString(previousRealm), uuidString(room.RealmID))
	}
	if !sameString(previousSlug, room.Slug) {
		changes["slug"] = changeValue(previousSlug, room.Slug)
	}
	if string(previousPicture) != string(room.Picture) {
		changes["picture_id"] = changeValue(jsonMap(previousPicture)["id"], jsonMap(room.Picture)["id"])
	}
	if string(previousBackground) != string(room.Background) {
		changes["background_id"] = changeValue(jsonMap(previousBackground)["id"], jsonMap(room.Background)["id"])
	}
	if len(changes) > 0 {
		accountUUID, _ := uuid.Parse(accountID)
		var operator store.ChatMemberEntity
		if err := i.db.WithContext(ctx).
			Where("chat_room_id = ? AND account_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", room.ID, accountUUID).
			First(&operator).Error; err == nil {
			i.sendChatInfoUpdated(ctx, room, &operator, changes)
		}
	}
	i.logAction(ctx, c, accountID, "chatrooms.update", map[string]any{"chatroom_id": room.ID.String()})
	rooms := i.hydrateRooms(ctx, []*store.ChatRoomEntity{room}, accountID)
	httpx.OK(c, rooms[0])
}

func sameString(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sameUUID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func uuidString(value *uuid.UUID) *string {
	if value == nil {
		return nil
	}
	text := value.String()
	return &text
}

func changeValue(old, current any) map[string]any {
	return map[string]any{"old": old, "new": current}
}

func (i *instance) enableE2eeLegacy(c *gin.Context) {
	httpx.Fail(c, newStatusError("CHAT_ROOM_E2EE_LEGACY_REMOVED", "Legacy e2ee/enable endpoint is removed. Use POST /api/chat/{id}/mls/enable.", 410))
}

func (i *instance) enableMls(c *gin.Context) {
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
	if !i.roomPermission(ctx, c, room, accountID,
		"You need at least be a realm moderator to enable MLS for this chat.",
		"You need be part of the DM to enable MLS for this chat.",
		"You need be the owner to enable MLS for this chat.") {
		return
	}
	if room.EncryptionMode != int(model.ChatEncryptionNone) {
		failConflict(c, "CHAT_E2EE_ALREADY_ENABLED", "Encryption is already enabled for this room and cannot be disabled.")
		return
	}
	var request enableMlsRequest
	if c.Request.ContentLength > 0 {
		if !httpx.BindJSON(c, &request) {
			return
		}
	}
	groupID := "room:" + room.ID.String()
	if request.MlsGroupID != nil && strings.TrimSpace(*request.MlsGroupID) != "" {
		groupID = *request.MlsGroupID
	}
	policy := jsonMap(room.E2eePolicy)
	if policy["ciphersuite"] == nil {
		policy["ciphersuite"] = defaultMlsCiphersuite
	}
	for key, value := range request.E2eePolicy {
		policy[key] = value
	}
	room.EncryptionMode = int(model.ChatEncryptionE2eeMls)
	room.MLSGroupID = &groupID
	room.E2eePolicy = store.ToJSON(policy)
	if err := i.db.WithContext(ctx).Save(room).Error; err != nil {
		failInternal(c, err)
		return
	}
	accountUUID, _ := uuid.Parse(accountID)
	var operator store.ChatMemberEntity
	if err := i.db.WithContext(ctx).
		Where("chat_room_id = ? AND account_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", room.ID, accountUUID).
		First(&operator).Error; err == nil {
		i.sendE2eeEnabled(ctx, room, &operator)
	}
	i.logAction(ctx, c, accountID, "chatrooms.mls.enable", map[string]any{
		"chatroom_id":     room.ID.String(),
		"encryption_mode": encryptionModeName(room.EncryptionMode),
		"mls_group_id":    groupID,
	})
	rooms := i.hydrateRooms(ctx, []*store.ChatRoomEntity{room}, accountID)
	httpx.OK(c, rooms[0])
}

func (i *instance) deleteChatRoom(c *gin.Context) {
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
	if !i.roomPermission(ctx, c, room, accountID,
		"You need at least be a realm moderator to delete for this chat.",
		"You need be part of the DM to delete this chat.",
		"You need be the owner to delete for this chat.") {
		return
	}
	now := utcNow()
	if err := i.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&store.ChatMessageEntity{}).Where("chat_room_id = ?", roomID).
			Update("deleted_at", now).Error; err != nil {
			return err
		}
		if err := tx.Model(&store.ChatMemberEntity{}).Where("chat_room_id = ?", roomID).
			Update("deleted_at", now).Error; err != nil {
			return err
		}
		return tx.Delete(room).Error
	}); err != nil {
		failInternal(c, err)
		return
	}
	i.logAction(ctx, c, accountID, "chatrooms.delete", map[string]any{"chatroom_id": roomID.String()})
	httpx.NoContent(c)
}

// --- members --------------------------------------------------------------

func (i *instance) getRoomIdentity(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	member, err := i.activeMember(ctx, roomID, currentAccountID(c))
	if err != nil {
		failNotFound(c)
		return
	}
	models := i.memberModels(ctx, []*store.ChatMemberEntity{member})
	httpx.OK(c, models[0])
}

func (i *instance) updateRoomIdentity(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	user := currentUser(c)
	if user.GetPerkLevel() < 2 {
		failForbidden(c, "Perk level 2 is required to customize chat profile.")
		return
	}
	var request chatMemberProfileRequest
	if !httpx.BindJSON(c, (&request)) {
		return
	}
	member, err := i.activeMember(ctx, roomID, user.GetId())
	if err != nil {
		failNotFound(c)
		return
	}
	_ = i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).Where("id = ?", member.ID).
		Update("nick", request.Nick).Error
	member.Nick = request.Nick
	models := i.memberModels(ctx, []*store.ChatMemberEntity{member})
	httpx.OK(c, models[0])
}

func (i *instance) clearRoomIdentity(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	member, err := i.activeMember(ctx, roomID, currentAccountID(c))
	if err != nil {
		failNotFound(c)
		return
	}
	_ = i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).Where("id = ?", member.ID).
		Update("nick", nil).Error
	member.Nick = nil
	models := i.memberModels(ctx, []*store.ChatMemberEntity{member})
	httpx.OK(c, models[0])
}

func (i *instance) membersOnline(c *gin.Context) {
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
	accountID := currentAccountID(c)
	if !room.IsPublic {
		if accountID == "" {
			httpx.Unauthorized(c)
			return
		}
		accountUUID, _ := uuid.Parse(accountID)
		var member store.ChatMemberEntity
		if err := i.db.WithContext(ctx).
			Where("chat_room_id = ? AND account_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", roomID, accountUUID).
			First(&member).Error; err != nil {
			failForbidden(c, "You need to be a member to see online count of private chat room.")
			return
		}
	}
	members := i.activeMemberEntities(ctx, roomID)
	accountIDs := make([]string, 0, len(members))
	for _, member := range members {
		accountIDs = append(accountIDs, member.AccountID.String())
	}
	statuses := i.accountStatuses(ctx, accountIDs)
	onlineIDs := make([]string, 0, len(statuses))
	for id, value := range statuses {
		if value.GetIsOnline() {
			onlineIDs = append(onlineIDs, id)
		}
	}
	response := onlineMembersResponse{
		OnlineCount:     len(onlineIDs),
		OnlineUserNames: []string{},
		OnlineAccounts:  []any{},
	}
	if room.Type == int(model.ChatRoomDirectMessage) {
		if accountID == "" {
			httpx.Unauthorized(c)
			return
		}
		for _, id := range accountIDs {
			if id != accountID {
				if status, ok := statuses[id]; ok {
					response.DirectMessageStatus = protoJSON(status)
				}
				break
			}
		}
		httpx.OK(c, response)
		return
	}
	if len(onlineIDs) == 0 {
		httpx.OK(c, response)
		return
	}
	if i.host != nil && i.host.Core.Clients != nil && i.host.Core.Clients.Profile != nil {
		resp, err := i.host.Core.Clients.Profile.GetAccountBatch(ctx, &gen.DyGetAccountBatchRequest{Id: onlineIDs})
		if err == nil {
			for _, account := range resp.GetAccounts() {
				response.OnlineAccounts = append(response.OnlineAccounts, protoJSON(account))
				if account.GetNick() != "" {
					response.OnlineUserNames = append(response.OnlineUserNames, account.GetNick())
				}
			}
		}
	}
	httpx.OK(c, response)
}

func (i *instance) listMembers(c *gin.Context) {
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
	accountID := currentAccountID(c)
	if !room.IsPublic {
		if accountID == "" {
			httpx.Unauthorized(c)
			return
		}
		accountUUID, _ := uuid.Parse(accountID)
		var member store.ChatMemberEntity
		if err := i.db.WithContext(ctx).
			Where("chat_room_id = ? AND account_id = ? AND leave_at IS NULL", roomID, accountUUID).
			First(&member).Error; err != nil {
			failForbidden(c, "You need to be a member to see members of private chat room.")
			return
		}
	}
	page, ok := httpx.PageQuery(c, 100)
	if !ok {
		return
	}
	mask := !room.IsReadReceiptsPublic && accountID != ""
	query := i.db.WithContext(ctx).Where("chat_room_id = ? AND leave_at IS NULL", roomID)
	if name := strings.TrimSpace(c.Query("accountName")); name != "" {
		ids := i.searchAccountIDs(ctx, name)
		if len(ids) == 0 {
			c.Header("X-Total", "0")
			httpx.OK(c, []model.ChatMember{})
			return
		}
		query = query.Where("account_id IN ?", ids)
	}
	withStatus := c.Query("withStatus") == "true"
	if withStatus {
		var entities []*store.ChatMemberEntity
		if err := query.Order("joined_at ASC").Find(&entities).Error; err != nil {
			failInternal(c, err)
			return
		}
		accountIDs := make([]string, 0, len(entities))
		for _, entity := range entities {
			accountIDs = append(accountIDs, entity.AccountID.String())
		}
		statuses := i.accountStatuses(ctx, accountIDs)
		members := i.memberModels(ctx, entities)
		online := map[string]bool{}
		for idx := range members {
			if status, ok := statuses[members[idx].AccountID]; ok {
				members[idx].Status = protoJSON(status)
				online[members[idx].ID] = status.GetIsOnline()
			}
		}
		for a := 1; a < len(members); a++ {
			for b := a; b > 0 && online[members[b].ID] && !online[members[b-1].ID]; b-- {
				members[b], members[b-1] = members[b-1], members[b]
			}
		}
		total := len(members)
		c.Header("X-Total", strconv.Itoa(total))
		start := page.Offset
		if start > len(members) {
			start = len(members)
		}
		end := start + page.Take
		if end > len(members) {
			end = len(members)
		}
		result := members[start:end]
		if mask {
			for idx := range result {
				if result[idx].AccountID != accountID {
					result[idx].LastReadAt = nil
				}
			}
		}
		httpx.OK(c, filterWithAccount(result))
		return
	}
	var total int64
	if err := query.Model(&store.ChatMemberEntity{}).Count(&total).Error; err != nil {
		failInternal(c, err)
		return
	}
	c.Header("X-Total", strconv.Itoa(int(total)))
	var entities []*store.ChatMemberEntity
	if err := query.Order("joined_at ASC").Offset(page.Offset).Limit(page.Take).Find(&entities).Error; err != nil {
		failInternal(c, err)
		return
	}
	members := i.memberModels(ctx, entities)
	if mask {
		for idx := range members {
			if members[idx].AccountID != accountID {
				members[idx].LastReadAt = nil
			}
		}
	}
	httpx.OK(c, filterWithAccount(members))
}

func filterWithAccount(members []model.ChatMember) []model.ChatMember {
	out := make([]model.ChatMember, 0, len(members))
	for _, member := range members {
		if member.Account != nil {
			out = append(out, member)
		}
	}
	return out
}

// --- invites --------------------------------------------------------------

func (i *instance) inviteMember(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	accountID := currentAccountID(c)
	var request chatMemberRequest
	if !httpx.BindJSON(c, (&request)) {
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
	if relationshipBlocked(ctx, i, accountID, relatedID) {
		failForbidden(c, "You cannot invite a user that you have blocked or who has blocked you.")
		return
	}
	room, err := i.st.GetRoom(ctx, roomID)
	if err != nil {
		failNotFound(c)
		return
	}
	accountUUID, _ := uuid.Parse(accountID)
	var operator store.ChatMemberEntity
	if err := i.db.WithContext(ctx).Where("account_id = ? AND chat_room_id = ?", accountUUID, roomID).First(&operator).Error; err != nil {
		failForbidden(c, "You need to be a part of chat to invite member to the chat.")
		return
	}
	if !i.roomPermission(ctx, c, room, accountID,
		"You need at least be a realm moderator to invite member for this chat.",
		"You need be part of the DM to invite member to this chat.",
		"You need be the owner to invite member for this chat.") {
		return
	}
	if room.Type == int(model.ChatRoomDirectMessage) && room.EncryptionMode == int(model.ChatEncryptionE2eeMls) {
		var joined int64
		_ = i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).
			Where("chat_room_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", roomID).Count(&joined).Error
		if joined >= 2 {
			failConflict(c, "CHAT_E2EE_DM_MEMBER_LIMIT", "MLS direct-message rooms only support two active members.")
			return
		}
	}
	relatedUUID, _ := uuid.Parse(relatedID)
	var existing store.ChatMemberEntity
	found := i.db.WithContext(ctx).Where("account_id = ? AND chat_room_id = ?", relatedUUID, roomID).First(&existing).Error == nil
	if found {
		_ = i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).Where("id = ?", existing.ID).
			Updates(map[string]any{"invited_by_id": operator.ID, "leave_at": nil, "joined_at": nil}).Error
		existing.InvitedByID = &operator.ID
		existing.LeaveAt = nil
		existing.JoinedAt = nil
		i.notifyInvite(ctx, room, &existing, c)
		i.logAction(ctx, c, accountID, "chatrooms.invite", map[string]any{
			"chatroom_id": roomID.String(), "account_id": relatedID,
		})
		models := i.memberModels(ctx, []*store.ChatMemberEntity{&existing})
		httpx.OK(c, models[0])
		return
	}
	member := &store.ChatMemberEntity{
		ID:          uuid.New(),
		InvitedByID: &operator.ID,
		AccountID:   relatedUUID,
		Username:    new(relatedUser.GetName()),
		ChatRoomID:  roomID,
	}
	if err := i.db.WithContext(ctx).Create(member).Error; err != nil {
		failInternal(c, err)
		return
	}
	i.notifyInvite(ctx, room, member, c)
	i.logAction(ctx, c, accountID, "chatrooms.invite", map[string]any{
		"chatroom_id": roomID.String(), "account_id": relatedID,
	})
	models := i.memberModels(ctx, []*store.ChatMemberEntity{member})
	httpx.OK(c, models[0])
}

func (i *instance) notifyInvite(ctx context.Context, room *store.ChatRoomEntity, member *store.ChatMemberEntity, c *gin.Context) {
	title := "Chat invite"
	body := "You were invited to a chat."
	if room.Type == int(model.ChatRoomDirectMessage) {
		body = currentUserName(c) + " started a direct message with you."
	} else if room.Name != nil {
		body = "You were invited to " + *room.Name + "."
	}
	i.sendPush(ctx, []string{member.AccountID.String()}, "invites.chats", title, body, map[string]string{
		"room_id": member.ChatRoomID.String(),
	})
}

func (i *instance) listInvites(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := currentAccountID(c)
	accountUUID, _ := uuid.Parse(accountID)
	var members []*store.ChatMemberEntity
	_ = i.db.WithContext(ctx).Where("account_id = ? AND joined_at IS NULL", accountUUID).Find(&members).Error
	models := i.memberModels(ctx, members)
	httpx.OK(c, models)
}

func (i *instance) acceptInvite(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	accountID := currentAccountID(c)
	accountUUID, _ := uuid.Parse(accountID)
	var member store.ChatMemberEntity
	if err := i.db.WithContext(ctx).
		Where("account_id = ? AND chat_room_id = ? AND joined_at IS NULL", accountUUID, roomID).
		First(&member).Error; err != nil {
		failNotFound(c)
		return
	}
	now := utcNow()
	_ = i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).Where("id = ?", member.ID).Update("joined_at", now).Error
	member.JoinedAt = &now
	if room, err := i.st.GetRoom(ctx, roomID); err == nil {
		i.sendMemberJoined(ctx, room, &member)
		i.emitEncryptionMembershipChanged(ctx, room, &member, "member_joined")
	}
	i.logAction(ctx, c, accountID, "chatrooms.join", map[string]any{"chatroom_id": roomID.String()})
	models := i.memberModels(ctx, []*store.ChatMemberEntity{&member})
	httpx.OK(c, models[0])
}

func (i *instance) declineInvite(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	accountUUID, _ := uuid.Parse(currentAccountID(c))
	var member store.ChatMemberEntity
	if err := i.db.WithContext(ctx).
		Where("account_id = ? AND chat_room_id = ? AND joined_at IS NULL", accountUUID, roomID).
		First(&member).Error; err != nil {
		failNotFound(c)
		return
	}
	_ = i.db.WithContext(ctx).Delete(&member).Error
	httpx.NoContent(c)
}

func (i *instance) updateMemberNotify(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	if _, err := i.st.GetRoom(ctx, roomID); err != nil {
		failNotFound(c)
		return
	}
	accountUUID, _ := uuid.Parse(currentAccountID(c))
	member, err := i.activeMember(ctx, roomID, accountUUID.String())
	if err != nil {
		failBadRequest(c, "CHAT_NOT_JOINED", "You have not joined this chat room.")
		return
	}
	var request chatMemberNotifyRequest
	if !httpx.BindJSON(c, (&request)) {
		return
	}
	updates := map[string]any{}
	if request.NotifyLevel != nil {
		updates["notify"] = *request.NotifyLevel
	}
	if request.BreakUntil != nil {
		updates["break_until"] = time.Time(*request.BreakUntil)
	}
	if len(updates) > 0 {
		_ = i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).Where("id = ?", member.ID).Updates(updates).Error
		_ = i.db.WithContext(ctx).Where("id = ?", member.ID).First(member).Error
	}
	models := i.memberModels(ctx, []*store.ChatMemberEntity{member})
	httpx.OK(c, models[0])
}

// --- moderation -----------------------------------------------------------

func (i *instance) timeoutChatMember(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	memberID, ok := uuidParam(c, "memberId")
	if !ok {
		return
	}
	accountID := currentAccountID(c)
	var request chatTimeoutRequest
	if !httpx.BindJSON(c, (&request)) {
		return
	}
	if request.TimeoutUntil == nil {
		failBadRequest(c, "CHAT_TIMEOUT_FUTURE_REQUIRED", "Timeout can only until a time in the future.")
		return
	}
	until := time.Time(*request.TimeoutUntil)
	now := utcNow()
	if !now.Before(until) {
		failBadRequest(c, "CHAT_TIMEOUT_FUTURE_REQUIRED", "Timeout can only until a time in the future.")
		return
	}
	room, err := i.st.GetRoom(ctx, roomID)
	if err != nil {
		failNotFound(c)
		return
	}
	accountUUID, _ := uuid.Parse(accountID)
	var operator store.ChatMemberEntity
	if err := i.db.WithContext(ctx).Where("account_id = ? AND chat_room_id = ?", accountUUID, roomID).First(&operator).Error; err != nil {
		failBadRequest(c, "CHAT_NOT_JOINED", "You have not joined this chat room.")
		return
	}
	if room.RealmID != nil {
		if !i.isRealmModerator(ctx, room.RealmID, accountID) {
			failForbidden(c, "You need at least be a realm moderator to timeout member for this chat.")
			return
		}
	} else {
		switch model.ChatRoomType(room.Type) {
		case model.ChatRoomDirectMessage:
			failBadRequest(c, "CHAT_TIMEOUT_DM_NOT_ALLOWED", "You cannot timeout member in DM")
			return
		case model.ChatRoomGroup:
			if room.AccountID == nil || room.AccountID.String() != accountID {
				failForbidden(c, "You need be the owner to timeout member in this chat.")
				return
			}
		}
	}
	var member store.ChatMemberEntity
	if err := i.db.WithContext(ctx).
		Where("id = ? AND chat_room_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", memberID, roomID).
		First(&member).Error; err != nil {
		failNotFound(c)
		return
	}
	reason := ""
	if request.Reason != nil {
		reason = *request.Reason
	}
	cause := map[string]any{
		"reason":    reason,
		"sender_id": operator.ID.String(),
		"type":      int(model.ChatTimeoutModerator),
		"since":     model.NewTime(now),
	}
	if err := i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).Where("id = ?", member.ID).
		Updates(map[string]any{"timeout_cause": store.ToJSON(cause), "timeout_until": until}).Error; err != nil {
		failInternal(c, err)
		return
	}
	member.TimeoutUntil = &until
	i.sendMemberTimedOut(ctx, room, &operator, &member, request.Reason, until)
	i.logAction(ctx, c, accountID, "chatrooms.timeout", map[string]any{
		"chatroom_id": roomID.String(), "account_id": member.AccountID.String(),
	})
	httpx.NoContent(c)
}

func (i *instance) removeChatMemberTimeout(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	memberID, ok := uuidParam(c, "memberId")
	if !ok {
		return
	}
	accountID := currentAccountID(c)
	room, err := i.st.GetRoom(ctx, roomID)
	if err != nil {
		failNotFound(c)
		return
	}
	if !i.roomPermission(ctx, c, room, accountID,
		"You need at least be a realm moderator to cancel timeout in this chat.",
		"You need be part of the DM to cancel timeout in this chat.",
		"You need be the owner to cancel timeout for this chat.") {
		return
	}
	var member store.ChatMemberEntity
	if err := i.db.WithContext(ctx).
		Where("id = ? AND chat_room_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", memberID, roomID).
		First(&member).Error; err != nil {
		failNotFound(c)
		return
	}
	_ = i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).Where("id = ?", member.ID).
		Updates(map[string]any{"timeout_cause": nil, "timeout_until": nil}).Error
	accountUUID, _ := uuid.Parse(accountID)
	var operator store.ChatMemberEntity
	if err := i.db.WithContext(ctx).
		Where("chat_room_id = ? AND account_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", roomID, accountUUID).
		First(&operator).Error; err == nil {
		i.sendMemberTimeoutRemoved(ctx, room, &operator, &member)
	}
	i.logAction(ctx, c, accountID, "chatrooms.timeout.remove", map[string]any{
		"chatroom_id": roomID.String(), "account_id": member.AccountID.String(),
	})
	httpx.NoContent(c)
}

func (i *instance) removeChatMember(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	memberID, ok := uuidParam(c, "memberId")
	if !ok {
		return
	}
	accountID := currentAccountID(c)
	room, err := i.st.GetRoom(ctx, roomID)
	if err != nil {
		failNotFound(c)
		return
	}
	if !i.roomPermission(ctx, c, room, accountID,
		"You need at least be a realm moderator to remove member for this chat.",
		"You need be part of the DM to remove member for this chat.",
		"You need be the owner to remove member for this chat.") {
		return
	}
	var member store.ChatMemberEntity
	if err := i.db.WithContext(ctx).
		Where("id = ? AND chat_room_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", memberID, roomID).
		First(&member).Error; err != nil {
		failNotFound(c)
		return
	}
	accountUUID, _ := uuid.Parse(accountID)
	var operator store.ChatMemberEntity
	hasOperator := i.db.WithContext(ctx).
		Where("chat_room_id = ? AND account_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", roomID, accountUUID).
		First(&operator).Error == nil
	now := utcNow()
	_ = i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).Where("id = ?", member.ID).
		Update("leave_at", now).Error
	member.LeaveAt = &now
	if hasOperator {
		i.sendMemberLeft(ctx, room, &member, &operator)
		i.emitEncryptionMembershipChanged(ctx, room, &operator, "member_removed")
	}
	i.logAction(ctx, c, accountID, "chatrooms.kick", map[string]any{
		"chatroom_id": roomID.String(), "account_id": member.AccountID.String(),
	})
	httpx.NoContent(c)
}

func (i *instance) joinChatRoom(c *gin.Context) {
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
	if !room.IsCommunity {
		failForbidden(c, "This chat room isn't a community. You need an invitation to join.")
		return
	}
	accountUUID, _ := uuid.Parse(accountID)
	var existing store.ChatMemberEntity
	if err := i.db.WithContext(ctx).Where("account_id = ? AND chat_room_id = ?", accountUUID, roomID).First(&existing).Error; err == nil {
		if existing.LeaveAt == nil {
			failBadRequest(c, "CHAT_ALREADY_MEMBER", "You are already a member of this chat room.")
			return
		}
		now := utcNow()
		_ = i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).Where("id = ?", existing.ID).
			Updates(map[string]any{"leave_at": nil, "joined_at": now}).Error
		existing.LeaveAt = nil
		existing.JoinedAt = &now
		i.sendMemberJoined(ctx, room, &existing)
		i.emitEncryptionMembershipChanged(ctx, room, &existing, "member_joined")
		models := i.memberModels(ctx, []*store.ChatMemberEntity{&existing})
		httpx.OK(c, models[0])
		return
	}
	now := utcNow()
	member := &store.ChatMemberEntity{
		ID:         uuid.New(),
		AccountID:  accountUUID,
		Username:   new(currentUserName(c)),
		ChatRoomID: roomID,
		JoinedAt:   &now,
	}
	if err := i.db.WithContext(ctx).Create(member).Error; err != nil {
		failInternal(c, err)
		return
	}
	i.sendMemberJoined(ctx, room, member)
	i.emitEncryptionMembershipChanged(ctx, room, member, "member_joined")
	i.logAction(ctx, c, accountID, "chatrooms.join", map[string]any{"chatroom_id": roomID.String()})
	rooms := i.hydrateRooms(ctx, []*store.ChatRoomEntity{room}, accountID)
	httpx.OK(c, rooms[0])
}

func (i *instance) leaveChat(c *gin.Context) {
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
	if room.AccountID != nil && room.AccountID.String() == accountID {
		failBadRequest(c, "CHAT_CANNOT_LEAVE_OWN_ROOM", "You cannot leave you own chat room")
		return
	}
	member, err := i.activeMember(ctx, roomID, accountID)
	if err != nil {
		failNotFound(c)
		return
	}
	now := utcNow()
	_ = i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).Where("id = ?", member.ID).
		Update("leave_at", now).Error
	member.LeaveAt = &now
	i.sendMemberLeft(ctx, room, member, nil)
	i.emitEncryptionMembershipChanged(ctx, room, member, "member_left")
	i.logAction(ctx, c, accountID, "chatrooms.leave", map[string]any{"chatroom_id": roomID.String()})
	httpx.NoContent(c)
}
