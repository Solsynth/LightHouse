package chat

import (
	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/store"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
)

func (i *instance) listRealmChat(c *gin.Context) {
	ctx := c.Request.Context()
	realm, err := i.realmBySlug(ctx, c.Param("slug"))
	if err != nil {
		failNotFound(c)
		return
	}
	accountID := currentAccountID(c)
	if !realm.GetIsPublic() {
		if accountID == "" {
			httpx.Unauthorized(c)
			return
		}
		if !i.isRealmMember(ctx, realm.GetId(), accountID) {
			failForbidden(c, "You need at least one member to view the realm's chat.")
			return
		}
	}
	var rooms []*store.ChatRoomEntity
	if err := i.db.WithContext(ctx).Where("realm_id = ?", realm.GetId()).Find(&rooms).Error; err != nil {
		failInternal(c, err)
		return
	}
	httpx.OK(c, i.hydrateRooms(ctx, rooms, accountID))
}
