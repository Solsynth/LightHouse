package chat

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/model"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/store"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
)

func (i *instance) listGroupModels(ctx context.Context, accountID string) []model.ChatGroup {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return []model.ChatGroup{}
	}
	var entities []*store.ChatGroupEntity
	_ = i.db.WithContext(ctx).Where("account_id = ?", accountUUID).
		Order("\"order\" ASC").Find(&entities).Error
	groupIDs := make([]uuid.UUID, 0, len(entities))
	for _, entity := range entities {
		groupIDs = append(groupIDs, entity.ID)
	}
	roomsByGroup := map[string][]string{}
	if len(groupIDs) > 0 {
		var members []*store.ChatMemberEntity
		_ = i.db.WithContext(ctx).
			Where("chat_group_id IN ? AND joined_at IS NOT NULL AND leave_at IS NULL", groupIDs).
			Find(&members).Error
		for _, member := range members {
			if member.ChatGroupID == nil {
				continue
			}
			key := member.ChatGroupID.String()
			roomsByGroup[key] = append(roomsByGroup[key], member.ChatRoomID.String())
		}
	}
	groups := make([]model.ChatGroup, 0, len(entities))
	for _, entity := range entities {
		group := store.GroupToModel(entity)
		if rooms, ok := roomsByGroup[group.ID]; ok {
			group.RoomIDs = rooms
		}
		groups = append(groups, group)
	}
	return groups
}

func (i *instance) listGroups(c *gin.Context) {
	httpx.OK(c, i.listGroupModels(c.Request.Context(), currentAccountID(c)))
}

func (i *instance) createGroup(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := currentAccountID(c)
	var request createGroupRequest
	if !httpx.BindJSON(c, (&request)) {
		return
	}
	if strings.TrimSpace(request.Name) == "" {
		failValidation(c, "name", "The name field is required.", "")
		return
	}
	accountUUID, _ := uuid.Parse(accountID)
	order := 0
	if request.Order != nil {
		order = *request.Order
	} else {
		var maxOrder *int
		_ = i.db.WithContext(ctx).Model(&store.ChatGroupEntity{}).
			Where("account_id = ?", accountUUID).Select("MAX(\"order\")").Scan(&maxOrder).Error
		order = 0
		if maxOrder != nil {
			order = *maxOrder + 1
		}
	}
	group := &store.ChatGroupEntity{
		ID:        uuid.New(),
		AccountID: accountUUID,
		Name:      request.Name,
		Color:     request.Color,
		Icon:      request.Icon,
		Order:     order,
	}
	if err := i.db.WithContext(ctx).Create(group).Error; err != nil {
		failInternal(c, err)
		return
	}
	httpx.OK(c, store.GroupToModel(group))
}

func (i *instance) updateGroup(c *gin.Context) {
	ctx := c.Request.Context()
	groupID, ok := uuidParam(c, "groupId")
	if !ok {
		return
	}
	accountID := currentAccountID(c)
	accountUUID, _ := uuid.Parse(accountID)
	var request updateGroupRequest
	if !httpx.BindJSON(c, (&request)) {
		return
	}
	var group store.ChatGroupEntity
	if err := i.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", groupID, accountUUID).First(&group).Error; err != nil {
		failNotFound(c)
		return
	}
	if request.Name != nil {
		group.Name = *request.Name
	}
	if request.Color != nil {
		group.Color = request.Color
	}
	if request.Icon != nil {
		group.Icon = request.Icon
	}
	if request.Order != nil {
		group.Order = *request.Order
	}
	if err := i.db.WithContext(ctx).Save(&group).Error; err != nil {
		failInternal(c, err)
		return
	}
	modelGroup := store.GroupToModel(&group)
	modelGroup.RoomIDs = i.groupRoomIDs(ctx, group.ID)
	httpx.OK(c, modelGroup)
}

func (i *instance) groupRoomIDs(ctx context.Context, groupID uuid.UUID) []string {
	var roomIDs []string
	_ = i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).
		Where("chat_group_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", groupID).
		Pluck("chat_room_id", &roomIDs).Error
	if roomIDs == nil {
		roomIDs = []string{}
	}
	return roomIDs
}

func (i *instance) deleteGroup(c *gin.Context) {
	ctx := c.Request.Context()
	groupID, ok := uuidParam(c, "groupId")
	if !ok {
		return
	}
	accountUUID, _ := uuid.Parse(currentAccountID(c))
	var group store.ChatGroupEntity
	if err := i.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", groupID, accountUUID).First(&group).Error; err != nil {
		failNotFound(c)
		return
	}
	if err := i.db.WithContext(ctx).Delete(&group).Error; err != nil {
		failInternal(c, err)
		return
	}
	httpx.OKEmpty(c)
}

func (i *instance) moveToGroup(c *gin.Context) {
	ctx := c.Request.Context()
	roomID, ok := uuidParam(c, "roomId")
	if !ok {
		return
	}
	accountID := currentAccountID(c)
	var request moveToGroupRequest
	if !httpx.BindJSON(c, (&request)) {
		return
	}
	accountUUID, _ := uuid.Parse(accountID)
	member, err := i.activeMember(ctx, roomID, accountID)
	if err != nil {
		failBadRequest(c, "CHAT_GROUP_MOVE_FAILED", "Failed to move the room to the specified group.")
		return
	}
	if request.GroupID != nil && *request.GroupID != "" {
		groupID, err := uuid.Parse(*request.GroupID)
		if err != nil {
			failBadRequest(c, "CHAT_GROUP_MOVE_FAILED", "Failed to move the room to the specified group.")
			return
		}
		var count int64
		_ = i.db.WithContext(ctx).Model(&store.ChatGroupEntity{}).
			Where("id = ? AND account_id = ?", groupID, accountUUID).Count(&count).Error
		if count == 0 {
			failBadRequest(c, "CHAT_GROUP_MOVE_FAILED", "Failed to move the room to the specified group.")
			return
		}
		_ = i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).Where("id = ?", member.ID).
			Update("chat_group_id", groupID).Error
	} else {
		_ = i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).Where("id = ?", member.ID).
			Update("chat_group_id", nil).Error
	}
	httpx.OKEmpty(c)
}
