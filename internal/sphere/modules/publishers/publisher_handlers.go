package publishers

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

func (i *instance) memberRowsForAccount(ctx context.Context, accountID uuid.UUID, joined bool) ([]store.PublisherMemberRow, error) {
	query := i.store.DB.WithContext(ctx).Model(&store.PublisherMemberRow{}).
		Where("account_id = ?", accountID).
		Where("deleted_at IS NULL")
	if joined {
		query = query.Where("joined_at IS NOT NULL")
	} else {
		query = query.Where("joined_at IS NULL")
	}
	var rows []store.PublisherMemberRow
	err := query.Order("created_at ASC").Find(&rows).Error
	return rows, err
}

func (i *instance) publishersByIDs(ctx context.Context, ids []uuid.UUID) map[string]*store.PublisherEntity {
	out := map[string]*store.PublisherEntity{}
	if len(ids) == 0 {
		return out
	}
	var entities []store.PublisherEntity
	if err := i.store.DB.WithContext(ctx).Where("id IN ?", ids).Find(&entities).Error; err != nil {
		return out
	}
	for index := range entities {
		out[entities[index].ID.String()] = &entities[index]
	}
	return out
}

func (i *instance) handleQuota(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	user := i.getAccount(c.Request.Context(), accountID.String())
	if user == nil {
		user = authn.CurrentUser(c)
	}
	quota, err := i.quota(c.Request.Context(), user)
	if err != nil {
		i.internal(c)
		return
	}
	httpx.OK(c, quota)
}

func (i *instance) handleListManagedPublishers(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	rows, err := i.memberRowsForAccount(ctx, accountID, true)
	if err != nil {
		i.internal(c)
		return
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.PublisherID)
	}
	entities := i.publishersByIDs(ctx, ids)
	ordered := make([]*store.PublisherEntity, 0, len(rows))
	for _, row := range rows {
		if entity, ok := entities[row.PublisherID.String()]; ok {
			ordered = append(ordered, entity)
		}
	}
	publishers := i.publisherModels(ctx, ordered, false, true)

	var settings store.PublishingSettingsEntity
	if err := i.store.DB.WithContext(ctx).Where("account_id = ?", accountID).First(&settings).Error; err == nil && settings.DefaultPostingPublisherID != nil {
		for index := range ordered {
			if ordered[index].ID == *settings.DefaultPostingPublisherID && index > 0 {
				item := publishers[index]
				copy(publishers[1:index+1], publishers[0:index])
				publishers[0] = item
				break
			}
		}
	}
	httpx.OK(c, publishers)
}

func (i *instance) handleListInvites(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	rows, err := i.memberRowsForAccount(ctx, accountID, false)
	if err != nil {
		i.internal(c)
		return
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.PublisherID)
	}
	entities := i.publishersByIDs(ctx, ids)
	publisherList := make([]*store.PublisherEntity, 0, len(rows))
	for _, row := range rows {
		if entity, ok := entities[row.PublisherID.String()]; ok {
			publisherList = append(publisherList, entity)
		}
	}
	mapped := i.publisherModels(ctx, publisherList, false, true)
	byID := map[string]model.Publisher{}
	for index, entity := range publisherList {
		byID[entity.ID.String()] = mapped[index]
	}
	members := i.memberModels(ctx, rows)
	for index := range members {
		if publisher, ok := byID[members[index].PublisherID]; ok {
			copyPublisher := publisher
			members[index].Publisher = &copyPublisher
		}
	}
	httpx.OK(c, members)
}

func (i *instance) handleInviteMember(c *gin.Context) {
	currentID, ok := i.accountID(c)
	if !ok {
		return
	}
	var request memberInviteRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	relatedID, err := uuid.Parse(request.RelatedUserID)
	if err != nil {
		i.badRequest(c, "PUBLISHER_RELATED_USER_NOT_FOUND", "Related user was not found")
		return
	}
	related := i.getAccount(c.Request.Context(), relatedID.String())
	if related == nil {
		i.badRequest(c, "PUBLISHER_RELATED_USER_NOT_FOUND", "Related user was not found")
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	if !i.isMemberWithRole(c.Request.Context(), publisher.ID, currentID, request.Role) {
		i.forbidden(c, "You cannot invite member has higher permission than yours.")
		return
	}
	if err := i.addMember(c.Request.Context(), publisher.ID, relatedID, request.Role, nil); err != nil {
		i.internal(c)
		return
	}
	httpx.OK(c, model.PublisherMember{
		PublisherID: publisher.ID.String(),
		AccountID:   relatedID.String(),
		Role:        request.Role,
	})
}

func (i *instance) handleAcceptInvite(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	row, err := i.memberRow(c.Request.Context(), publisher.ID, accountID, false)
	if err != nil {
		i.internal(c)
		return
	}
	if row == nil || row.JoinedAt != nil {
		i.notFound(c)
		return
	}
	now := time.Now().UTC()
	if err := i.store.DB.WithContext(c.Request.Context()).Model(&store.PublisherMemberRow{}).
		Where("publisher_id = ? AND account_id = ?", publisher.ID, accountID).
		Update("joined_at", now).Error; err != nil {
		i.internal(c)
		return
	}
	httpx.OK(c, i.publisherModelFor(c.Request.Context(), publisher, false))
}

func (i *instance) handleDeclineInvite(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	row, err := i.memberRow(c.Request.Context(), publisher.ID, accountID, false)
	if err != nil {
		i.internal(c)
		return
	}
	if row == nil || row.JoinedAt != nil {
		i.notFound(c)
		return
	}
	if err := i.deleteMember(c.Request.Context(), publisher.ID, accountID); err != nil {
		i.internal(c)
		return
	}
	httpx.NoContent(c)
}

func (i *instance) handleRemoveMember(c *gin.Context) {
	currentID, ok := i.accountID(c)
	if !ok {
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	memberID, err := uuid.Parse(c.Param("memberId"))
	if err != nil {
		i.notFound(c)
		return
	}
	row, err := i.memberRow(c.Request.Context(), publisher.ID, memberID, false)
	if err != nil {
		i.internal(c)
		return
	}
	if row == nil {
		i.badRequest(c, "PUBLISHER_MEMBER_NOT_FOUND", "Member was not found")
		return
	}
	if row.AccountID == currentID && row.Role >= roleOwner {
		i.badRequest(c, "PUBLISHER_CANNOT_REMOVE_OWNER", "You cannot remove yourself as the publisher owner.")
		return
	}
	if !i.isMemberWithRole(c.Request.Context(), publisher.ID, currentID, roleManager) {
		i.forbidden(c, "You need at least be a manager to remove members from this publisher.")
		return
	}
	if err := i.deleteMember(c.Request.Context(), publisher.ID, memberID); err != nil {
		i.internal(c)
		return
	}
	httpx.NoContent(c)
}

func (i *instance) handleLeavePublisher(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	row, err := i.memberRow(c.Request.Context(), publisher.ID, accountID, true)
	if err != nil {
		i.internal(c)
		return
	}
	if row == nil {
		i.notFound(c)
		return
	}
	if row.Role >= roleOwner {
		i.badRequest(c, "PUBLISHER_CANNOT_LEAVE_OWNER", "You cannot leave your own publisher.")
		return
	}
	if err := i.deleteMember(c.Request.Context(), publisher.ID, accountID); err != nil {
		i.internal(c)
		return
	}
	httpx.NoContent(c)
}

func (i *instance) handleUpdateMemberRole(c *gin.Context) {
	var newRole int
	if err := c.ShouldBindJSON(&newRole); err != nil {
		httpx.Fail(c, validationError("", err.Error()))
		return
	}
	if newRole >= roleOwner {
		i.badRequest(c, "PUBLISHER_ROLE_INVALID", "Unable to set publisher member to owner or greater role.")
		return
	}
	currentID, ok := i.accountID(c)
	if !ok {
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	memberID, err := uuid.Parse(c.Param("memberId"))
	if err != nil {
		i.notFound(c)
		return
	}
	row, err := i.memberRow(c.Request.Context(), publisher.ID, memberID, true)
	if err != nil {
		i.internal(c)
		return
	}
	if row == nil {
		i.notFound(c)
		return
	}
	required := roleManager
	if row.Role > required {
		required = row.Role
	}
	if newRole > required {
		required = newRole
	}
	if !i.isMemberWithRole(c.Request.Context(), publisher.ID, currentID, required) {
		i.forbidden(c, "You do not have permission to update member roles in this publisher.")
		return
	}
	if err := i.setMemberRole(c.Request.Context(), publisher.ID, memberID, newRole); err != nil {
		i.internal(c)
		return
	}
	member := i.memberModel(c.Request.Context(), &store.PublisherMemberRow{
		PublisherID: publisher.ID,
		AccountID:   memberID,
		Role:        newRole,
		JoinedAt:    row.JoinedAt,
	})
	httpx.OK(c, member)
}

func (i *instance) handleListMembers(c *gin.Context) {
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	page, ok := httpx.PageQuery(c, i.maxTake())
	if !ok {
		return
	}
	rows, total, err := i.store.PublisherMemberRows(c.Request.Context(), publisher.ID, true, page.Offset, page.Take)
	if err != nil {
		i.internal(c)
		return
	}
	members := i.memberModels(c.Request.Context(), rows)
	publisherModel := i.memberPublisher(c.Request.Context(), publisher)
	filtered := make([]model.PublisherMember, 0, len(members))
	for _, member := range members {
		if member.Account == nil {
			continue
		}
		member.Publisher = publisherModel
		filtered = append(filtered, member)
	}
	httpx.List(c, filtered, total)
}

func (i *instance) handleCurrentMember(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	row, err := i.memberRow(c.Request.Context(), publisher.ID, accountID, false)
	if err != nil {
		i.internal(c)
		return
	}
	if row == nil {
		i.notFound(c)
		return
	}
	member := i.memberModel(c.Request.Context(), row)
	member.Publisher = i.memberPublisher(c.Request.Context(), publisher)
	httpx.OK(c, member)
}
