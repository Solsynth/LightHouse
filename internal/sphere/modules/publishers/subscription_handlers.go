package publishers

import (
	"context"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/filejson"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

func (i *instance) registerSubscriptionRoutes(api *gin.RouterGroup) {
	api.GET("/publishers/subscriptions", authn.RequireAuth(), i.handleSubscriptions)
	api.GET("/publishers/subscriptions/live", authn.RequireAuth(), i.handleSubscribedLiveStreams)
	api.PUT("/publishers/subscriptions/read-status", authn.RequireAuth(), i.requirePermission(permSubscriptions), i.handleMarkAllRead)
	api.GET("/publishers/blocked", authn.RequireAuth(), i.handleBlockedPublishers)
	api.GET("/publishers/muted", authn.RequireAuth(), i.handleMutedPublishers)
	api.GET("/publishers/:name/subscription", authn.RequireAuth(), i.handleSubscriptionStatus)
	api.GET("/publishers/:name/subscription/read-status", authn.RequireAuth(), i.handleSubscriptionReadStatus)
	api.POST("/publishers/:name/subscribe", authn.RequireAuth(), i.requirePermission(permSubscriptions), i.handleSubscribe)
	api.POST("/publishers/:name/unsubscribe", authn.RequireAuth(), i.requirePermission(permSubscriptions), i.handleUnsubscribe)
	api.PATCH("/publishers/:name/subscription/me/notify", authn.RequireAuth(), i.requirePermission(permSubscriptions), i.handleUpdateNotify)
	api.PUT("/publishers/:name/subscription/read-status", authn.RequireAuth(), i.requirePermission(permSubscriptions), i.handleUpdateReadStatus)
	api.GET("/publishers/:name/subscribers", authn.RequireAuth(), i.handleListSubscribers)
	api.POST("/publishers/:name/subscribers/:accountId", authn.RequireAuth(), i.requirePermission(permSubscriptions), i.handleAddSubscriber)
	api.DELETE("/publishers/:name/subscribers/:accountId", authn.RequireAuth(), i.requirePermission(permSubscriptions), i.handleRemoveSubscriber)
	api.GET("/publishers/:name/subscription/requests", authn.RequireAuth(), i.handleFollowRequests)
	api.POST("/publishers/:name/subscription/requests/:requestId/approve", authn.RequireAuth(), i.requirePermission(permSubscriptions), i.handleApproveFollowRequest)
	api.POST("/publishers/:name/subscription/requests/:requestId/reject", authn.RequireAuth(), i.requirePermission(permSubscriptions), i.handleRejectFollowRequest)
	api.GET("/publishers/:name/relationship", authn.RequireAuth(), i.handleRelationship)
	api.POST("/publishers/:name/block", authn.RequireAuth(), i.requirePermission(permSubscriptions), i.handleBlock)
	api.DELETE("/publishers/:name/block", authn.RequireAuth(), i.requirePermission(permSubscriptions), i.handleUnblock)
	api.POST("/publishers/:name/mute", authn.RequireAuth(), i.requirePermission(permSubscriptions), i.handleMute)
	api.DELETE("/publishers/:name/mute", authn.RequireAuth(), i.requirePermission(permSubscriptions), i.handleUnmute)
}

func (i *instance) publisherNotFound(c *gin.Context) {
	httpx.Fail(c, newAPIError("PUBLISHER_NOT_FOUND", "Publisher not found", 404))
}

func (i *instance) handleSubscriptionStatus(c *gin.Context) {
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
	ctx := c.Request.Context()
	requiresApproval := i.hasFollowRequiresApproval(ctx, publisher)
	response := subscriptionStatusResponse{RequiresApproval: requiresApproval, Notify: true}

	if requiresApproval {
		followRequest, err := i.getFollowRequest(ctx, publisher.ID, accountID)
		if err != nil {
			i.internal(c)
			return
		}
		if followRequest != nil {
			response.FollowRequest = i.subscriptionDTO(followRequest, publisher)
			response.Subscription = response.FollowRequest
			response.Notify = followRequest.Notify
			response.IsActive = followRequest.State == subscriptionAccepted && response.FollowRequest.IsActive
			response.IsPending = followRequest.State == subscriptionPending
			switch followRequest.State {
			case subscriptionPending:
				response.Status = "pending"
			case subscriptionAccepted:
				if response.IsActive {
					response.Status = "following"
				} else {
					response.Status = "ended"
				}
			case subscriptionRejected:
				response.Status = "rejected"
			default:
				response.Status = "none"
			}
			switch response.Status {
			case "pending":
				response.Message = "Follow request is pending approval"
			case "following":
				response.Message = "You are following this publisher"
			case "ended":
				response.Message = "Your follow has ended"
			case "rejected":
				response.Message = "Follow request was rejected"
			}
			httpx.OK(c, response)
			return
		}
	}

	subscription, err := i.getSubscriptionIncludingEnded(ctx, accountID, publisher.ID)
	if err != nil {
		i.internal(c)
		return
	}
	if subscription != nil {
		response.Subscription = i.subscriptionDTO(subscription, publisher)
		response.IsActive = response.Subscription.IsActive
		response.Notify = subscription.Notify
		if response.IsActive {
			response.Status = "subscribed"
			response.Message = "You are subscribed to this publisher"
		} else {
			response.Status = "ended"
			response.Message = "Your subscription has ended"
		}
	}
	httpx.OK(c, response)
}

func (i *instance) readStatus(c *gin.Context, accountID uuid.UUID, publisher *store.PublisherEntity) (*subscriptionReadStatus, bool) {
	subscription, err := i.getSubscription(c.Request.Context(), accountID, publisher.ID)
	if err != nil {
		i.internal(c)
		return nil, false
	}
	if subscription == nil {
		return nil, true
	}
	latest, err := i.store.LatestContentAt(c.Request.Context(), []uuid.UUID{publisher.ID})
	if err != nil {
		i.internal(c)
		return nil, false
	}
	value, ok := latest[publisher.ID]
	status := &subscriptionReadStatus{
		Subscription: i.subscriptionDTO(subscription, publisher),
	}
	if ok {
		status.LatestContentAt = model.NewTime(value)
		status.HasNewContent = subscription.LastReadAt == nil || value.After(*subscription.LastReadAt)
	}
	return status, true
}

func (i *instance) handleSubscriptionReadStatus(c *gin.Context) {
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

func (i *instance) handleSubscribe(c *gin.Context) {
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
	ctx := c.Request.Context()
	requiresApproval := i.hasFollowRequiresApproval(ctx, publisher)

	if requiresApproval {
		existing, err := i.getFollowRequest(ctx, publisher.ID, accountID)
		if err != nil {
			i.internal(c)
			return
		}
		if existing != nil {
			switch existing.State {
			case subscriptionPending:
				c.JSON(400, subscriptionStatusResponse{
					Status: "pending", Message: "Follow request already pending",
					RequiresApproval: true, FollowRequest: i.subscriptionDTO(existing, publisher),
				})
			case subscriptionAccepted:
				httpx.OK(c, subscriptionStatusResponse{
					Status: "following", Message: "Already following",
					RequiresApproval: true, FollowRequest: i.subscriptionDTO(existing, publisher),
				})
			case subscriptionRejected:
				c.JSON(400, subscriptionStatusResponse{
					Status: "rejected", Message: "Follow request was rejected. Please contact the publisher.",
					RequiresApproval: true, FollowRequest: i.subscriptionDTO(existing, publisher),
				})
			default:
				i.badRequest(c, "PUBLISHER_FOLLOW_INVALID_STATE", "Invalid follow request state")
			}
			return
		}
		followRequest, err := i.createFollowRequest(ctx, publisher, accountID)
		if err != nil {
			i.internal(c)
			return
		}
		i.notifyManagers(ctx, publisher, accountID)
		httpx.OK(c, subscriptionStatusResponse{
			Status: "pending", Message: "Follow request submitted and pending approval",
			RequiresApproval: true, FollowRequest: i.subscriptionDTO(followRequest, publisher),
		})
		return
	}

	existing, err := i.getSubscription(ctx, accountID, publisher.ID)
	if err != nil {
		i.internal(c)
		return
	}
	if existing != nil {
		c.JSON(409, subscriptionStatusResponse{
			Status: "subscribed", Message: "Already subscribed to this publisher",
			Subscription: i.subscriptionDTO(existing, publisher),
		})
		return
	}
	subscription, err := i.createSubscription(ctx, accountID, publisher.ID)
	if err != nil {
		i.log.Error("publisher subscribe failed", "publisher", publisher.ID, "error", err)
		httpx.Fail(c, newAPIError("PUBLISHER_SUBSCRIPTION_ERROR", "Failed to create subscription", 500))
		return
	}
	httpx.OK(c, subscriptionStatusResponse{
		Status: "subscribed", Message: "Successfully subscribed",
		Subscription: i.subscriptionDTO(subscription, publisher),
	})
}

func (i *instance) handleUnsubscribe(c *gin.Context) {
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
	ctx := c.Request.Context()
	requiresApproval := i.hasFollowRequiresApproval(ctx, publisher)
	if requiresApproval {
		existing, err := i.getFollowRequest(ctx, publisher.ID, accountID)
		if err != nil {
			i.internal(c)
			return
		}
		if existing != nil {
			if err := i.cancelFollowRequest(ctx, publisher.ID, accountID); err != nil {
				i.internal(c)
				return
			}
			_, _ = i.cancelSubscription(ctx, accountID, publisher.ID)
			httpx.OK(c, subscriptionStatusResponse{
				Status: "none", Message: "Follow request cancelled",
				RequiresApproval: true, IsActive: false, IsPending: false,
			})
			return
		}
		c.JSON(404, subscriptionStatusResponse{
			Status: "none", Message: "No follow request found", RequiresApproval: true,
		})
		return
	}
	success, err := i.cancelSubscription(ctx, accountID, publisher.ID)
	if err != nil {
		i.internal(c)
		return
	}
	if success {
		httpx.OK(c, subscriptionStatusResponse{
			Status: "none", Message: "Subscription cancelled successfully", IsActive: false,
		})
		return
	}
	c.JSON(404, subscriptionStatusResponse{Status: "none", Message: "No subscription found"})
}

func (i *instance) handleSubscriptions(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	page, ok := httpx.PageQuery(c, i.maxTake())
	if !ok {
		return
	}
	order := strings.ToLower(strings.TrimSpace(c.Query("order")))
	ctx := c.Request.Context()
	query := i.store.DB.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
		Where("account_id = ? AND ended_at IS NULL AND state = ? AND is_blocking = ?",
			accountID, subscriptionAccepted, false)
	switch order {
	case "", "created_at":
		query = query.Order("created_at DESC")
	case "latest_posted_at":
		query = query.
			Joins("LEFT JOIN (SELECT publisher_id, MAX(COALESCE(published_at, created_at)) AS latest FROM posts WHERE replied_post_id IS NULL AND visibility = 0 AND deleted_at IS NULL GROUP BY publisher_id) latest_posts ON latest_posts.publisher_id = publisher_subscriptions.publisher_id").
			Order("latest_posts.latest DESC NULLS LAST, publisher_subscriptions.created_at DESC")
	default:
		i.badRequest(c, "INVALID_ORDER", "Supported order values are created_at and latest_posted_at.")
		return
	}

	var total int64
	countQuery := i.store.DB.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
		Where("account_id = ? AND ended_at IS NULL AND state = ? AND is_blocking = ?",
			accountID, subscriptionAccepted, false)
	if err := countQuery.Count(&total).Error; err != nil {
		i.internal(c)
		return
	}
	var rows []store.PublisherSubscriptionEntity
	if err := query.Offset(page.Offset).Limit(page.Take).Find(&rows).Error; err != nil {
		i.internal(c)
		return
	}
	publisherIDs := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		publisherIDs = append(publisherIDs, row.PublisherID)
	}
	publishers := i.publishersByIDs(ctx, publisherIDs)
	latest, err := i.store.LatestContentAt(ctx, publisherIDs)
	if err != nil {
		i.internal(c)
		return
	}
	liveIDs, err := i.store.ActiveLivePublisherIDs(ctx, publisherIDs)
	if err != nil {
		i.internal(c)
		return
	}
	liveSet := map[uuid.UUID]bool{}
	for _, id := range liveIDs {
		liveSet[id] = true
	}
	items := make([]subscriptionWithLiveStatus, 0, len(rows))
	for index := range rows {
		row := &rows[index]
		item := subscriptionWithLiveStatus{
			Subscription: i.subscriptionDTO(row, publishers[row.PublisherID.String()]),
			IsLive:       liveSet[row.PublisherID],
		}
		if value, ok := latest[row.PublisherID]; ok {
			item.LatestContentAt = model.NewTime(value)
			item.HasNewContent = row.LastReadAt == nil || value.After(*row.LastReadAt)
		}
		items = append(items, item)
	}
	httpx.List(c, items, total)
}

func (i *instance) handleSubscribedLiveStreams(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	page, ok := httpx.PageQuery(c, i.maxTake())
	if !ok {
		return
	}
	ctx := c.Request.Context()
	var publisherIDs []uuid.UUID
	if err := i.store.DB.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
		Where("account_id = ? AND ended_at IS NULL AND state = ?", accountID, subscriptionAccepted).
		Pluck("publisher_id", &publisherIDs).Error; err != nil {
		i.internal(c)
		return
	}
	if len(publisherIDs) == 0 {
		httpx.OK(c, []liveStreamDTO{})
		return
	}
	var streams []store.LiveStreamEntity
	if err := i.store.DB.WithContext(ctx).
		Where("publisher_id IN ? AND status = ? AND visibility = ?", publisherIDs, 1, 0).
		Order("started_at DESC").
		Offset(page.Offset).Limit(page.Take).
		Find(&streams).Error; err != nil {
		i.internal(c)
		return
	}
	streamPublisherIDs := make([]uuid.UUID, 0, len(streams))
	for _, stream := range streams {
		if stream.PublisherID != nil {
			streamPublisherIDs = append(streamPublisherIDs, *stream.PublisherID)
		}
	}
	publishers := i.publishersByIDs(ctx, streamPublisherIDs)
	items := make([]liveStreamDTO, 0, len(streams))
	for index := range streams {
		items = append(items, liveStreamModel(&streams[index], publishers))
	}
	httpx.OK(c, items)
}

func (i *instance) handleMarkAllRead(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	updated, err := i.markAllSubscriptionsRead(c.Request.Context(), accountID)
	if err != nil {
		i.internal(c)
		return
	}
	httpx.OK(c, gin.H{"updated_count": updated})
}

func liveStreamModel(entity *store.LiveStreamEntity, publishers map[string]*store.PublisherEntity) liveStreamDTO {
	item := liveStreamDTO{
		ID:                   entity.ID.String(),
		Title:                entity.Title,
		Description:          entity.Description,
		Slug:                 entity.Slug,
		Type:                 entity.Type,
		Visibility:           entity.Visibility,
		Status:               entity.Status,
		RoomName:             entity.RoomName,
		IngressID:            entity.IngressID,
		IngressStreamKey:     entity.IngressStreamKey,
		EgressID:             entity.EgressID,
		HLSegressID:          entity.HLSEgressID,
		HLSPlaylistPath:      entity.HLSPlaylistPath,
		HLSStartedAt:         store.TimePtr(entity.HLSStartedAt),
		StartedAt:            store.TimePtr(entity.StartedAt),
		TotalDurationSeconds: entity.TotalDurationSeconds,
		ViewerCount:          entity.ViewerCount,
		PeakViewerCount:      entity.PeakViewerCount,
		TotalAwardScore:      entity.TotalAwardScore,
		DistributedAward:     entity.DistributedAwardAmount,
		Thumbnail:            filejson.Value(entity.Thumbnail),
		Metadata:             store.JSONValue(entity.Metadata),
		PublisherID:          store.UUIDPtrString(entity.PublisherID),
		CreatedAt:            model.NewTime(entity.CreatedAt),
		UpdatedAt:            model.NewTime(entity.UpdatedAt),
		DeletedAt:            store.DeletedPtr(entity.DeletedAt),
	}
	if entity.PublisherID != nil {
		if publisher, ok := publishers[entity.PublisherID.String()]; ok {
			mapped := store.PublisherToModel(publisher)
			item.Publisher = &mapped
		}
	}
	return item
}

func (i *instance) notifyManagers(ctx context.Context, publisher *store.PublisherEntity, requester uuid.UUID) {
	if i.notify == nil {
		return
	}
	rows, _, err := i.store.PublisherMemberRows(ctx, publisher.ID, true, 0, 1000)
	if err != nil {
		return
	}
	requesterAccount := i.getAccount(ctx, requester.String())
	name := "A user"
	if requesterAccount != nil {
		name = requesterAccount.GetNick()
	}
	title := "Follow request received"
	body := name + " wants to follow " + publisher.Nick
	for _, row := range rows {
		if row.Role < roleManager {
			continue
		}
		if err := i.notify.SendPush(ctx, row.AccountID.String(), title, body, map[string]string{
			"notification_type": "follow_request",
			"publisher_id":      publisher.ID.String(),
		}); err != nil {
			i.log.Debug("follow request notification failed", "account", row.AccountID, "error", err)
		}
	}
}
