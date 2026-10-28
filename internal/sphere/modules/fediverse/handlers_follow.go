package fediverse

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// actorWithFollowStatus is SnPublisher plus the [NotMapped] IsFollowing flag
// the C# controller serialized alongside the publisher fields.
type actorWithFollowStatus struct {
	model.Publisher
	IsFollowing bool `json:"is_following"`
}

func (s *server) primaryPublisher(c *gin.Context, accountID uuid.UUID) (*store.PublisherEntity, error) {
	var publisher store.PublisherEntity
	err := s.db.WithContext(c.Request.Context()).
		Where("id IN (?)", s.db.Model(&store.PublisherMemberEntity{}).
			Select("publisher_id").Where("account_id = ?", accountID)).
		Order("created_at ASC").
		First(&publisher).Error
	if err != nil {
		return nil, err
	}
	return &publisher, nil
}

func (s *server) handleFollowFollowing(c *gin.Context) {
	accountID, ok := s.currentAccount(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	publisher, err := s.primaryPublisher(c, accountID)
	if err != nil {
		httpx.List(c, []actorWithFollowStatus{}, 0)
		return
	}
	offset, take := parseOffsetTake(c, 50)
	base := s.db.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
		Where("follower_publisher_id = ? AND state = ? AND ended_at IS NULL AND is_blocking = ?",
			publisher.ID, int(model.SubscriptionAccepted), false)
	var total int64
	if err := base.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	var subscriptions []store.PublisherSubscriptionEntity
	if err := base.Order("followed_at DESC NULLS LAST, created_at DESC").
		Limit(take).Offset(offset).Find(&subscriptions).Error; err != nil {
		httpx.Internal(c)
		return
	}
	ids := make([]uuid.UUID, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		ids = append(ids, subscription.PublisherID)
	}
	rows := s.publishersByIDs(ctx, ids)
	items := make([]actorWithFollowStatus, 0, len(rows))
	for _, row := range rows {
		mapped := store.PublisherToModel(&row)
		items = append(items, actorWithFollowStatus{Publisher: mapped, IsFollowing: true})
	}
	httpx.List(c, items, total)
}

func (s *server) handleFollowFollowers(c *gin.Context) {
	accountID, ok := s.currentAccount(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	publisher, err := s.primaryPublisher(c, accountID)
	if err != nil {
		httpx.List(c, []actorWithFollowStatus{}, 0)
		return
	}
	offset, take := parseOffsetTake(c, 50)
	base := s.db.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
		Where("publisher_id = ? AND state = ? AND ended_at IS NULL AND is_blocking = ?",
			publisher.ID, int(model.SubscriptionAccepted), false)
	var total int64
	if err := base.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	var subscriptions []store.PublisherSubscriptionEntity
	if err := base.Order("followed_at DESC NULLS LAST, created_at DESC").
		Limit(take).Offset(offset).Find(&subscriptions).Error; err != nil {
		httpx.Internal(c)
		return
	}
	ids := make([]uuid.UUID, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		if subscription.FollowerPublisherID != nil {
			ids = append(ids, *subscription.FollowerPublisherID)
		}
	}
	items := s.actorsWithFollowStatus(c, ids, publisher.ID)
	httpx.List(c, items, total)
}

func (s *server) handleFollowSearch(c *gin.Context) {
	query := strings.TrimSpace(c.Query("query"))
	if query == "" {
		httpx.BadRequest(c, "FEDIVERSE_SEARCH_QUERY_REQUIRED", "Query is required")
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit < 1 {
		limit = 1
	}
	if limit > 50 {
		limit = 50
	}
	actors, err := s.discovery.searchActors(c.Request.Context(), query, limit, true)
	if err != nil {
		httpx.Internal(c)
		return
	}
	ids := make([]uuid.UUID, 0, len(actors))
	for i := range actors {
		ids = append(ids, actors[i].ID)
	}
	accountID, ok := s.currentAccount(c)
	if !ok {
		return
	}
	publisher, err := s.primaryPublisher(c, accountID)
	if err != nil {
		items := make([]actorWithFollowStatus, 0, len(actors))
		for i := range actors {
			mapped := store.PublisherToModel(&actors[i])
			items = append(items, actorWithFollowStatus{Publisher: mapped})
		}
		httpx.OK(c, items)
		return
	}
	httpx.OK(c, s.actorsWithFollowStatus(c, ids, publisher.ID))
}

func (s *server) actorsWithFollowStatus(c *gin.Context, ids []uuid.UUID, publisherID uuid.UUID) []actorWithFollowStatus {
	rows := s.publishersByIDs(c.Request.Context(), ids)
	if len(rows) == 0 {
		return []actorWithFollowStatus{}
	}
	following := make(map[uuid.UUID]bool)
	if publisherID != uuid.Nil {
		var followed []uuid.UUID
		_ = s.db.WithContext(c.Request.Context()).Model(&store.PublisherSubscriptionEntity{}).
			Where("follower_publisher_id = ? AND publisher_id IN ? AND state = ? AND ended_at IS NULL AND is_blocking = ?",
				publisherID, ids, int(model.SubscriptionAccepted), false).
			Pluck("publisher_id", &followed).Error
		for _, id := range followed {
			following[id] = true
		}
	}
	items := make([]actorWithFollowStatus, 0, len(rows))
	for i := range rows {
		mapped := store.PublisherToModel(&rows[i])
		items = append(items, actorWithFollowStatus{Publisher: mapped, IsFollowing: following[rows[i].ID]})
	}
	return items
}

func (s *server) publishersByIDs(ctx context.Context, ids []uuid.UUID) []store.PublisherEntity {
	if len(ids) == 0 {
		return nil
	}
	var rows []store.PublisherEntity
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil
	}
	byID := make(map[uuid.UUID]store.PublisherEntity, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	ordered := make([]store.PublisherEntity, 0, len(ids))
	for _, id := range ids {
		if row, ok := byID[id]; ok {
			ordered = append(ordered, row)
		}
	}
	return ordered
}

func (s *server) handleFollowRelationships(c *gin.Context) {
	accountID, ok := s.currentAccount(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	publisher, err := s.primaryPublisher(c, accountID)
	if err != nil {
		httpx.NotFound(c, "Publisher not found")
		return
	}
	actorURL := s.actorURL(publisher.Name)

	var followingCount, followersCount, pendingCount int64
	_ = s.db.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
		Where("follower_publisher_id = ? AND state = ? AND ended_at IS NULL AND is_blocking = ?",
			publisher.ID, int(model.SubscriptionAccepted), false).Count(&followingCount).Error
	_ = s.db.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
		Where("publisher_id = ? AND state = ? AND ended_at IS NULL AND is_blocking = ?",
			publisher.ID, int(model.SubscriptionAccepted), false).Count(&followersCount).Error
	_ = s.db.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
		Where("follower_publisher_id = ? AND state = ?", publisher.ID, int(model.SubscriptionPending)).
		Count(&pendingCount).Error

	var relationships []store.PublisherSubscriptionEntity
	_ = s.db.WithContext(ctx).
		Where("follower_publisher_id = ?", publisher.ID).
		Order("followed_at DESC NULLS LAST, created_at DESC").
		Limit(20).
		Find(&relationships).Error

	ids := make([]uuid.UUID, 0, len(relationships))
	for _, relationship := range relationships {
		ids = append(ids, relationship.PublisherID)
	}
	rows := s.publishersByIDs(ctx, ids)
	byID := make(map[uuid.UUID]store.PublisherEntity, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	items := make([]gin.H, 0, len(relationships))
	for _, relationship := range relationships {
		row, ok := byID[relationship.PublisherID]
		if !ok {
			continue
		}
		items = append(items, gin.H{
			"actor":                store.PublisherToModel(&row),
			"state":                relationship.State,
			"is_following":         relationship.State == int(model.SubscriptionAccepted),
			"followed_at":          store.TimePtr(relationship.FollowedAt),
			"target_publisher_uri": deref(row.URI),
			"username":             deref(row.Username),
			"display_name":         row.Nick,
		})
	}
	httpx.OK(c, gin.H{
		"actor_uri":       actorURL,
		"following_count": followingCount,
		"followers_count": followersCount,
		"pending_count":   pendingCount,
		"relationships":   items,
	})
}

func (s *server) handleFollowCheckActor(c *gin.Context) {
	username := c.Param("username")
	actorURL := s.actorURL(username)
	ctx := c.Request.Context()

	var existing store.PublisherEntity
	if err := s.db.WithContext(ctx).Where("uri = ?", actorURL).First(&existing).Error; err == nil {
		httpx.OK(c, gin.H{
			"exists":            true,
			"actor":             store.PublisherToModel(&existing),
			"actor_uri":         actorURL,
			"username":          deref(existing.Username),
			"display_name":      existing.Nick,
			"bio":               deref(existing.Bio),
			"avatar_url":        fileReferenceURL(existing.Picture, s.fileBase),
			"instance_domain":   deref(existing.InstanceDomain),
			"public_key_exists": existing.PublicKey != nil && *existing.PublicKey != "",
			"is_local":          false,
			"last_activity_at":  store.TimePtr(existing.LastActivityAt),
		})
		return
	}
	document, err := s.discovery.fetchDocument(ctx, actorURL, nil)
	if err != nil {
		httpx.OK(c, gin.H{
			"exists":    false,
			"actor_uri": actorURL,
			"error":     err.Error(),
		})
		return
	}
	domain := hostOfURL(actorURL)
	instance, err := s.discovery.ensureInstance(ctx, domain)
	if err != nil {
		httpx.OK(c, gin.H{"exists": false, "actor_uri": actorURL, "error": err.Error()})
		return
	}
	actor, err := s.discovery.getOrCreateActor(ctx, actorURL, username, &instance.ID)
	if err != nil {
		httpx.OK(c, gin.H{"exists": false, "actor_uri": actorURL, "error": err.Error()})
		return
	}
	if displayName := getString(document, "name"); displayName != "" {
		actor.Nick = displayName
	}
	if bio := getString(document, "summary"); bio != "" {
		actor.Bio = &bio
	}
	if icon := getObject(document, "icon"); icon != nil {
		if raw := getString(icon, "url"); raw != "" {
			actor.Picture = store.ToJSON(externalFileReference(raw))
		}
	}
	if publicKey := getObject(document, "publicKey"); publicKey != nil {
		if pem := getString(publicKey, "publicKeyPem"); pem != "" {
			actor.PublicKey = &pem
		}
	}
	now := time.Now().UTC()
	actor.LastActivityAt = &now
	_ = s.db.WithContext(ctx).Save(actor).Error
	httpx.OK(c, gin.H{
		"exists":            true,
		"actor":             store.PublisherToModel(actor),
		"actor_uri":         actorURL,
		"username":          username,
		"display_name":      actor.Nick,
		"bio":               deref(actor.Bio),
		"avatar_url":        fileReferenceURL(actor.Picture, s.fileBase),
		"instance_domain":   domain,
		"public_key_exists": actor.PublicKey != nil && *actor.PublicKey != "",
		"is_discoverable":   true,
		"is_local":          false,
		"last_activity_at":  activityPubTime(now),
	})
}

func (s *server) currentAccount(c *gin.Context) (uuid.UUID, bool) {
	user := authn.CurrentUser(c)
	if user == nil {
		httpx.Unauthorized(c)
		return uuid.Nil, false
	}
	id, err := uuid.Parse(user.GetId())
	if err != nil {
		httpx.Unauthorized(c)
		return uuid.Nil, false
	}
	return id, true
}

