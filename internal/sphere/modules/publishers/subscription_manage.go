package publishers

import (
	"errors"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/localization"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

func (i *instance) handleListSubscribers(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	publisher, err := i.publisherByName(c.Request.Context(), c.Param("name"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			i.publisherNotFound(c)
			return
		}
		i.internal(c)
		return
	}
	if !i.isMemberWithRole(c.Request.Context(), publisher.ID, accountID, roleManager) {
		i.forbidden(c, "You need to be a manager of this publisher to list subscribers.")
		return
	}
	page, ok := httpx.PageQuery(c, i.maxTake())
	if !ok {
		return
	}
	ctx := c.Request.Context()
	query := i.store.DB.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
		Where("publisher_id = ? AND ended_at IS NULL AND state = ? AND is_blocking = ?",
			publisher.ID, subscriptionAccepted, false)
	var total int64
	if err := i.store.DB.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
		Where("publisher_id = ? AND ended_at IS NULL AND state = ? AND is_blocking = ?",
			publisher.ID, subscriptionAccepted, false).
		Count(&total).Error; err != nil {
		i.internal(c)
		return
	}
	var rows []store.PublisherSubscriptionEntity
	if err := query.Order("created_at ASC").Offset(page.Offset).Limit(page.Take).Find(&rows).Error; err != nil {
		i.internal(c)
		return
	}
	accountIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.AccountID != nil {
			accountIDs = append(accountIDs, row.AccountID.String())
		}
	}
	accounts := i.getAccounts(ctx, accountIDs)
	result := make([]subscriberResponse, 0, len(rows))
	for index := range rows {
		row := &rows[index]
		item := subscriberResponse{Subscription: i.subscriptionDTO(row, nil)}
		if row.AccountID != nil {
			if account, ok := accounts[row.AccountID.String()]; ok {
				item.Account = i.accountRef(account)
			}
		}
		result = append(result, item)
	}
	httpx.List(c, result, total)
}

func (i *instance) handleAddSubscriber(c *gin.Context) {
	managerID, ok := i.accountID(c)
	if !ok {
		return
	}
	publisher, err := i.publisherByName(c.Request.Context(), c.Param("name"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			i.publisherNotFound(c)
			return
		}
		i.internal(c)
		return
	}
	if !i.isMemberWithRole(c.Request.Context(), publisher.ID, managerID, roleManager) {
		i.forbidden(c, "You need to be a manager of this publisher to add subscribers.")
		return
	}
	accountID, err := uuid.Parse(c.Param("accountId"))
	if err != nil {
		i.notFound(c)
		return
	}
	subscription, err := i.addSubscriber(c.Request.Context(), accountID, publisher.ID)
	if err != nil {
		i.badRequest(c, "PUBLISHER_SUBSCRIBER_ADD_FAILED", err.Error())
		return
	}
	account := i.getAccount(c.Request.Context(), accountID.String())
	if account != nil && i.notify != nil {
		lang := account.GetLanguage()
		title := localization.Localize(lang, "subscriberAddedTitle", map[string]string{
			"publisher": publisher.Nick,
		})
		body := localization.Localize(lang, "subscriberAddedBody", map[string]string{
			"publisher": publisher.Nick,
		})
		_ = i.notify.SendPush(c.Request.Context(), accountID.String(), title, body, map[string]string{
			"notification_type": "subscriber_added",
			"publisher_id":      publisher.ID.String(),
		})
	}
	result := subscriberResponse{Subscription: i.subscriptionDTO(subscription, nil)}
	if account != nil {
		result.Account = i.accountRef(account)
	}
	httpx.OK(c, result)
}

func (i *instance) handleRemoveSubscriber(c *gin.Context) {
	managerID, ok := i.accountID(c)
	if !ok {
		return
	}
	publisher, err := i.publisherByName(c.Request.Context(), c.Param("name"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			i.publisherNotFound(c)
			return
		}
		i.internal(c)
		return
	}
	if !i.isMemberWithRole(c.Request.Context(), publisher.ID, managerID, roleManager) {
		i.forbidden(c, "You need to be a manager of this publisher to remove subscribers.")
		return
	}
	accountID, err := uuid.Parse(c.Param("accountId"))
	if err != nil {
		i.notFound(c)
		return
	}
	removed, err := i.removeSubscriber(c.Request.Context(), accountID, publisher.ID, managerID)
	if err != nil {
		i.internal(c)
		return
	}
	if !removed {
		httpx.Fail(c, newAPIError("PUBLISHER_SUBSCRIBER_NOT_FOUND", "Subscriber not found", 404))
		return
	}
	httpx.NoContent(c)
}

func (i *instance) handleUpdateNotify(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	var request updateNotifyRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	publisher, err := i.publisherByName(c.Request.Context(), c.Param("name"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			i.publisherNotFound(c)
			return
		}
		i.internal(c)
		return
	}
	subscription, err := i.updateNotify(c.Request.Context(), accountID, publisher.ID, request.Notify)
	if err != nil {
		i.internal(c)
		return
	}
	if subscription == nil {
		httpx.Fail(c, newAPIError("PUBLISHER_SUBSCRIPTION_NOT_FOUND", "Subscription not found", 404))
		return
	}
	httpx.OK(c, i.subscriptionDTO(subscription, publisher))
}

func (i *instance) handleUpdateReadStatus(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	var request updateSubscriptionReadStateRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	publisher, err := i.publisherByName(c.Request.Context(), c.Param("name"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			i.publisherNotFound(c)
			return
		}
		i.internal(c)
		return
	}
	var at *time.Time
	if request.LastReadAt != nil {
		value := request.LastReadAt.Time()
		at = &value
	}
	subscription, err := i.updateLastReadAt(c.Request.Context(), accountID, publisher.ID, at)
	if err != nil {
		i.internal(c)
		return
	}
	if subscription == nil {
		httpx.Fail(c, newAPIError("PUBLISHER_SUBSCRIPTION_NOT_FOUND", "Subscription not found", 404))
		return
	}
	status, ok := i.readStatus(c, accountID, publisher)
	if !ok {
		return
	}
	if status == nil {
		httpx.Fail(c, newAPIError("PUBLISHER_SUBSCRIPTION_NOT_FOUND", "Subscription not found", 404))
		return
	}
	httpx.OK(c, status)
}

func (i *instance) handleFollowRequests(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	publisher, err := i.publisherByName(c.Request.Context(), c.Param("name"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			i.publisherNotFound(c)
			return
		}
		i.internal(c)
		return
	}
	if !i.isMemberWithRole(c.Request.Context(), publisher.ID, accountID, roleManager) {
		i.forbidden(c, "You need to be a manager of this publisher to view follow requests.")
		return
	}
	ctx := c.Request.Context()
	rows, err := i.pendingFollowRequests(ctx, publisher.ID)
	if err != nil {
		i.internal(c)
		return
	}
	accountIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.AccountID != nil {
			accountIDs = append(accountIDs, row.AccountID.String())
		}
	}
	accounts := i.getAccounts(ctx, accountIDs)
	items := make([]subscriptionDTO, 0, len(rows))
	for index := range rows {
		row := &rows[index]
		item := i.subscriptionDTO(row, publisher)
		if row.AccountID != nil {
			if account, ok := accounts[row.AccountID.String()]; ok {
				item.Account = i.accountRef(account)
			}
		}
		items = append(items, *item)
	}
	httpx.OK(c, items)
}

func (i *instance) handleApproveFollowRequest(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	publisher, err := i.publisherByName(c.Request.Context(), c.Param("name"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			i.publisherNotFound(c)
			return
		}
		i.internal(c)
		return
	}
	if !i.isMemberWithRole(c.Request.Context(), publisher.ID, accountID, roleManager) {
		i.forbidden(c, "You need to be a manager of this publisher to approve follow requests.")
		return
	}
	requestID, err := uuid.Parse(c.Param("requestId"))
	if err != nil {
		i.notFound(c)
		return
	}
	request, err := i.approveFollowRequest(c.Request.Context(), requestID, accountID)
	if err != nil {
		i.badRequest(c, "PUBLISHER_FOLLOW_APPROVE_FAILED", err.Error())
		return
	}
	i.notifyFollowDecision(c, publisher, request, true, nil)
	httpx.OK(c, i.subscriptionDTO(request, publisher))
}

func (i *instance) handleRejectFollowRequest(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	var body rejectFollowRequestBody
	if !httpx.BindJSON(c, &body) {
		return
	}
	publisher, err := i.publisherByName(c.Request.Context(), c.Param("name"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			i.publisherNotFound(c)
			return
		}
		i.internal(c)
		return
	}
	if !i.isMemberWithRole(c.Request.Context(), publisher.ID, accountID, roleManager) {
		i.forbidden(c, "You need to be a manager of this publisher to reject follow requests.")
		return
	}
	requestID, err := uuid.Parse(c.Param("requestId"))
	if err != nil {
		i.notFound(c)
		return
	}
	request, err := i.rejectFollowRequest(c.Request.Context(), requestID, accountID, body.Reason)
	if err != nil {
		i.badRequest(c, "PUBLISHER_FOLLOW_REJECT_FAILED", err.Error())
		return
	}
	i.notifyFollowDecision(c, publisher, request, false, body.Reason)
	httpx.OK(c, i.subscriptionDTO(request, publisher))
}

func (i *instance) notifyFollowDecision(c *gin.Context, publisher *store.PublisherEntity, request *store.PublisherSubscriptionEntity, approved bool, reason *string) {
	if i.notify == nil || request == nil || request.AccountID == nil {
		return
	}
	lang := "en"
	if acc := i.getAccount(c.Request.Context(), request.AccountID.String()); acc != nil && acc.GetLanguage() != "" {
		lang = acc.GetLanguage()
	}
	var title, body string
	if approved {
		title = localization.Localize(lang, "followRequestApprovedTitle", map[string]string{
			"publisher": publisher.Nick,
		})
		body = localization.Localize(lang, "followRequestApprovedBody", map[string]string{
			"publisher": publisher.Nick,
		})
	} else {
		title = localization.Localize(lang, "followRequestRejectedTitle", map[string]string{
			"publisher": publisher.Nick,
		})
		body = localization.Localize(lang, "followRequestRejectedBody", map[string]string{
			"publisher": publisher.Nick,
		})
	}
	if reason != nil && *reason != "" {
		body += ": " + *reason
	}
	_ = i.notify.SendPush(c.Request.Context(), request.AccountID.String(), title, body, map[string]string{
		"notification_type": "follow_decision",
		"publisher_id":      publisher.ID.String(),
	})
}

func (i *instance) handleBlockedPublishers(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	rows, err := i.blockedPublishers(c.Request.Context(), accountID)
	if err != nil {
		i.internal(c)
		return
	}
	httpx.OK(c, i.subscriptionsWithPublishers(c, rows))
}

func (i *instance) handleMutedPublishers(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	rows, err := i.mutedPublishers(c.Request.Context(), accountID)
	if err != nil {
		i.internal(c)
		return
	}
	httpx.OK(c, i.subscriptionsWithPublishers(c, rows))
}

func (i *instance) subscriptionsWithPublishers(c *gin.Context, rows []store.PublisherSubscriptionEntity) []subscriptionDTO {
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.PublisherID)
	}
	publishers := i.publishersByIDs(c.Request.Context(), ids)
	items := make([]subscriptionDTO, 0, len(rows))
	for index := range rows {
		row := &rows[index]
		items = append(items, *i.subscriptionDTO(row, publishers[row.PublisherID.String()]))
	}
	return items
}

func (i *instance) relationship(c *gin.Context, publisher *store.PublisherEntity, accountID uuid.UUID) (relationshipResponse, error) {
	subscription, err := i.getSubscriptionIncludingEnded(c.Request.Context(), accountID, publisher.ID)
	if err != nil {
		return relationshipResponse{}, err
	}
	active, err := i.getSubscription(c.Request.Context(), accountID, publisher.ID)
	if err != nil {
		return relationshipResponse{}, err
	}
	response := relationshipResponse{IsSubscribed: active != nil}
	if subscription != nil {
		response.Subscription = i.subscriptionDTO(subscription, publisher)
		response.IsBlocking = subscription.IsBlocking
		response.IsMuting = subscription.IsMuting
	}
	return response, nil
}

func (i *instance) handleRelationship(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	publisher, err := i.publisherByName(c.Request.Context(), c.Param("name"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			i.publisherNotFound(c)
			return
		}
		i.internal(c)
		return
	}
	response, err := i.relationship(c, publisher, accountID)
	if err != nil {
		i.internal(c)
		return
	}
	httpx.OK(c, response)
}

func (i *instance) handleBlock(c *gin.Context) {
	i.setRelationshipFlag(c, true, false)
}

func (i *instance) handleUnblock(c *gin.Context) {
	i.setRelationshipFlag(c, false, false)
}

func (i *instance) handleMute(c *gin.Context) {
	i.setRelationshipFlag(c, true, true)
}

func (i *instance) handleUnmute(c *gin.Context) {
	i.setRelationshipFlag(c, false, true)
}

func (i *instance) setRelationshipFlag(c *gin.Context, enabled, muting bool) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	publisher, err := i.publisherByName(c.Request.Context(), c.Param("name"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			i.publisherNotFound(c)
			return
		}
		i.internal(c)
		return
	}
	var subscription *store.PublisherSubscriptionEntity
	if muting {
		subscription, err = i.setPublisherMuting(c.Request.Context(), accountID, publisher.ID, enabled)
	} else {
		subscription, err = i.setPublisherBlocking(c.Request.Context(), accountID, publisher.ID, enabled)
	}
	if err != nil {
		i.internal(c)
		return
	}
	active, err := i.getSubscription(c.Request.Context(), accountID, publisher.ID)
	if err != nil {
		i.internal(c)
		return
	}
	response := relationshipResponse{
		IsSubscribed: active != nil,
		IsBlocking:   enabled && !muting,
		IsMuting:     enabled && muting,
	}
	if subscription != nil {
		response.Subscription = i.subscriptionDTO(subscription, publisher)
		if muting {
			response.IsBlocking = subscription.IsBlocking
		} else {
			response.IsMuting = subscription.IsMuting
		}
	}
	httpx.OK(c, response)
}
