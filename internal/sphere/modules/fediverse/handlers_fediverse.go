package fediverse

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// ---- actor browsing -------------------------------------------------------

func (s *server) handleActorLookup(c *gin.Context) {
	identifier := c.Param("id")
	ctx := c.Request.Context()
	if strings.Contains(identifier, "@") {
		username, domain, ok := splitHandle(identifier)
		if !ok {
			httpx.NotFound(c, "Actor not found")
			return
		}
		var actor store.PublisherEntity
		err := s.db.WithContext(ctx).
			Where("lower(username) = ? AND lower(instance_domain) = ?", strings.ToLower(username), strings.ToLower(domain)).
			First(&actor).Error
		if err != nil {
			uri := s.discovery.resolveWebFinger(ctx, username, domain)
			if uri == "" {
				httpx.Fail(c, errsActorNotFound())
				return
			}
			discovered, discoverErr := s.discovery.getOrCreateActor(ctx, uri, username, nil)
			if discoverErr != nil {
				httpx.Fail(c, errsActorNotFound())
				return
			}
			actor = *discovered
		}
		httpx.OK(c, store.PublisherToModel(&actor))
		return
	}
	id, err := uuid.Parse(identifier)
	if err != nil {
		httpx.Fail(c, errsActorNotFound())
		return
	}
	var actor store.PublisherEntity
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&actor).Error; err != nil {
		httpx.Fail(c, errsActorNotFound())
		return
	}
	httpx.OK(c, store.PublisherToModel(&actor))
}

func (s *server) handleActorPosts(c *gin.Context) {
	actor, ok := s.requireRemoteActor(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	publisher := store.PublisherToModel(actor)

	var posts []store.PostEntity
	if err := s.db.WithContext(ctx).
		Where("publisher_id = ? AND drafted_at IS NULL AND visibility = ?", actor.ID, int(model.VisibilityPublic)).
		Order("published_at DESC NULLS LAST, created_at DESC").
		Find(&posts).Error; err != nil {
		httpx.Internal(c)
		return
	}
	var boosts []store.BoostEntity
	_ = s.db.WithContext(ctx).Where("publisher_id = ?", actor.ID).Find(&boosts).Error

	// The C# action also pulled the actor's remote outbox and merged it in.
	// Lighthouse only returns locally mirrored posts; remote-only entries are
	// not synthesised here.
	items := make([]model.Post, 0, len(posts)+len(boosts))
	for i := range posts {
		post := store.PostToModel(&posts[i])
		post.Publisher = &publisher
		items = append(items, post)
	}
	for i := range boosts {
		var boosted store.PostEntity
		if err := s.db.WithContext(ctx).
			Where("id = ? AND drafted_at IS NULL AND visibility = ?", boosts[i].PostID, int(model.VisibilityPublic)).
			First(&boosted).Error; err != nil {
			continue
		}
		post := store.PostToModel(&boosted)
		post.Publisher = &publisher
		items = append(items, post)
	}
	total := int64(len(items))
	offset, take := parseOffsetTake(c, 20)
	start := offset
	if start > len(items) {
		start = len(items)
	}
	end := start + take
	if end > len(items) {
		end = len(items)
	}
	httpx.List(c, items[start:end], total)
}

func (s *server) handleActorFollowerList(c *gin.Context) {
	actor, ok := s.requireRemoteActor(c)
	if !ok {
		return
	}
	s.listActorSubscriptions(c, actor, true)
}

func (s *server) handleActorFollowingList(c *gin.Context) {
	actor, ok := s.requireRemoteActor(c)
	if !ok {
		return
	}
	s.listActorSubscriptions(c, actor, false)
}

func (s *server) listActorSubscriptions(c *gin.Context, actor *store.PublisherEntity, followers bool) {
	ctx := c.Request.Context()
	query := s.db.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
		Where("state = ? AND ended_at IS NULL AND is_blocking = ?", int(model.SubscriptionAccepted), false)
	if followers {
		query = query.Where("publisher_id = ?", actor.ID)
	} else {
		query = query.Where("follower_publisher_id = ?", actor.ID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	offset, take := parseOffsetTake(c, 40)
	var subscriptions []store.PublisherSubscriptionEntity
	if err := query.Order("followed_at DESC NULLS LAST, created_at DESC").
		Limit(take).Offset(offset).Find(&subscriptions).Error; err != nil {
		httpx.Internal(c)
		return
	}
	ids := make([]uuid.UUID, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		if followers {
			if subscription.FollowerPublisherID != nil {
				ids = append(ids, *subscription.FollowerPublisherID)
			}
		} else {
			ids = append(ids, subscription.PublisherID)
		}
	}
	models := make([]model.Publisher, 0, len(ids))
	if len(ids) > 0 {
		var rows []store.PublisherEntity
		if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err == nil {
			byID := make(map[uuid.UUID]store.PublisherEntity, len(rows))
			for _, row := range rows {
				byID[row.ID] = row
			}
			for _, id := range ids {
				if row, ok := byID[id]; ok {
					models = append(models, store.PublisherToModel(&row))
				}
			}
		}
	}
	httpx.List(c, models, total)
}

func (s *server) handleActorRelationship(c *gin.Context) {
	actor, ok := s.requireRemoteActor(c)
	if !ok {
		return
	}
	user := authn.CurrentUser(c)
	if user == nil {
		httpx.Unauthorized(c)
		return
	}
	accountID, err := uuid.Parse(user.GetId())
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	ctx := c.Request.Context()

	var localActorIDs []uuid.UUID
	_ = s.db.WithContext(ctx).Model(&store.PublisherEntity{}).
		Where("account_id = ? AND uri IS NOT NULL", accountID).
		Pluck("id", &localActorIDs).Error

	response := gin.H{
		"actor_id":       actor.ID.String(),
		"actor_username": deref(actor.Username),
		"actor_instance": deref(actor.InstanceDomain),
		"actor_handle":   deref(actor.Username) + "@" + deref(actor.InstanceDomain),
		"is_following":   false,
		"is_pending":     false,
		"is_followed_by": false,
	}
	if len(localActorIDs) == 0 {
		httpx.OK(c, response)
		return
	}
	var relationships []store.PublisherSubscriptionEntity
	_ = s.db.WithContext(ctx).
		Where("follower_publisher_id IN ? AND publisher_id = ?", localActorIDs, actor.ID).
		Find(&relationships).Error
	isFollowing := false
	isPending := false
	for _, relationship := range relationships {
		if relationship.EndedAt != nil {
			continue
		}
		switch relationship.State {
		case int(model.SubscriptionAccepted):
			isFollowing = true
		case int(model.SubscriptionPending):
			isPending = true
		}
	}
	var followedBy int64
	_ = s.db.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
		Where("follower_publisher_id = ? AND publisher_id IN ? AND state = ? AND ended_at IS NULL",
			actor.ID, localActorIDs, int(model.SubscriptionAccepted)).
		Count(&followedBy).Error
	response["is_following"] = isFollowing
	response["is_pending"] = isPending
	response["is_followed_by"] = followedBy > 0
	httpx.OK(c, response)
}

func (s *server) handleActorSearch(c *gin.Context) {
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
	models := make([]model.Publisher, 0, len(actors))
	for i := range actors {
		models = append(models, store.PublisherToModel(&actors[i]))
	}
	httpx.OK(c, models)
}

func (s *server) handleActorAvailability(c *gin.Context) {
	user := authn.CurrentUser(c)
	if user == nil {
		httpx.Unauthorized(c)
		return
	}
	accountID, err := uuid.Parse(user.GetId())
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	ctx := c.Request.Context()
	var owned []store.PublisherEntity
	if err := s.db.WithContext(ctx).Where("account_id = ?", accountID).Find(&owned).Error; err != nil {
		httpx.Internal(c)
		return
	}
	publishers := make([]gin.H, 0)
	for i := range owned {
		if owned[i].URI == nil || *owned[i].URI == "" {
			continue
		}
		publishers = append(publishers, gin.H{
			"publisher_id":     owned[i].ID.String(),
			"publisher_name":   owned[i].Name,
			"fediverse_handle": deref(owned[i].Username) + "@" + deref(owned[i].InstanceDomain),
			"fediverse_uri":    *owned[i].URI,
			"avatar_url":       nil,
			"is_enabled":       true,
			"followers_count":  0,
			"following_count":  0,
			"posts_count":      0,
		})
	}
	httpx.OK(c, gin.H{
		"is_enabled": len(publishers) > 0,
		"publishers": publishers,
	})
}

func (s *server) handleActorFollow(c *gin.Context) {
	s.changeActorFollow(c, true)
}

func (s *server) handleActorUnfollow(c *gin.Context) {
	s.changeActorFollow(c, false)
}

func (s *server) changeActorFollow(c *gin.Context, follow bool) {
	user := authn.CurrentUser(c)
	if user == nil {
		httpx.Unauthorized(c)
		return
	}
	accountID, err := uuid.Parse(user.GetId())
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	ctx := c.Request.Context()
	publisherID := s.effectiveFediverseIdentity(c, accountID)
	if publisherID == nil {
		httpx.BadRequest(c, "ACTIVITYPUB_NO_FEDIVERSE_PUBLISHER", "No Fediverse publisher available")
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errsActorNotFound())
		return
	}
	var target store.PublisherEntity
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&target).Error; err != nil || target.URI == nil {
		httpx.Fail(c, errsActorNotFound())
		return
	}
	var success bool
	if follow {
		success = s.delivery.sendFollow(ctx, *publisherID, *target.URI)
	} else {
		success = s.delivery.sendUnfollow(ctx, *publisherID, *target.URI)
	}
	if success {
		if follow {
			httpx.OK(c, gin.H{
				"success":         true,
				"message":         "Follow request sent. Waiting for acceptance.",
				"target_actor_uri": *target.URI,
			})
		} else {
			httpx.OK(c, gin.H{"success": true, "message": "Unfollowed successfully"})
		}
		return
	}
	if follow {
		httpx.BadRequest(c, "FEDIVERSE_FOLLOW_REQUEST_FAILED", "Failed to send follow request")
		return
	}
	httpx.BadRequest(c, "FEDIVERSE_UNFOLLOW_FAILED", "Failed to unfollow")
}

// effectiveFediverseIdentity mirrors GetEffectiveFediverseIdentityAsync.
func (s *server) effectiveFediverseIdentity(c *gin.Context, accountID uuid.UUID) *uuid.UUID {
	ctx := c.Request.Context()
	var settings store.PublishingSettingsEntity
	if err := s.db.WithContext(ctx).Where("account_id = ?", accountID).First(&settings).Error; err == nil &&
		settings.DefaultFediversePublisherID != nil {
		var memberCount int64
		_ = s.db.WithContext(ctx).Model(&store.PublisherMemberEntity{}).
			Where("publisher_id = ? AND account_id = ?", *settings.DefaultFediversePublisherID, accountID).
			Count(&memberCount).Error
		var actorCount int64
		_ = s.db.WithContext(ctx).Model(&store.PublisherEntity{}).
			Where("id = ? AND uri IS NOT NULL", *settings.DefaultFediversePublisherID).
			Count(&actorCount).Error
		if memberCount > 0 && actorCount > 0 {
			return settings.DefaultFediversePublisherID
		}
	}
	var publisher store.PublisherEntity
	err := s.db.WithContext(ctx).
		Where("uri IS NOT NULL AND id IN (?)", s.db.Model(&store.PublisherMemberEntity{}).
			Select("publisher_id").Where("account_id = ?", accountID)).
		Order("created_at ASC").
		First(&publisher).Error
	if err != nil {
		return nil
	}
	return &publisher.ID
}

func (s *server) requireRemoteActor(c *gin.Context) (*store.PublisherEntity, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errsActorNotFound())
		return nil, false
	}
	var actor store.PublisherEntity
	if err := s.db.WithContext(c.Request.Context()).Where("id = ?", id).First(&actor).Error; err != nil {
		httpx.Fail(c, errsActorNotFound())
		return nil, false
	}
	return &actor, true
}

// ---- moderation -----------------------------------------------------------

type createRuleRequest struct {
	Name            string  `json:"name"`
	Description     *string `json:"description"`
	Type            int     `json:"type"`
	Action          int     `json:"action"`
	Domain          *string `json:"domain"`
	KeywordPattern  *string `json:"keyword_pattern"`
	IsRegex         bool    `json:"is_regex"`
	IsEnabled       bool    `json:"is_enabled"`
	ReportThreshold *int    `json:"report_threshold"`
	Priority        int     `json:"priority"`
}

type updateRuleRequest struct {
	Name            *string `json:"name"`
	Description     *string `json:"description"`
	Type            *int    `json:"type"`
	Action          *int    `json:"action"`
	Domain          *string `json:"domain"`
	KeywordPattern  *string `json:"keyword_pattern"`
	IsRegex         *bool   `json:"is_regex"`
	IsEnabled       *bool   `json:"is_enabled"`
	ReportThreshold *int    `json:"report_threshold"`
	Priority        *int    `json:"priority"`
}

type toggleRuleRequest struct {
	Enabled bool `json:"enabled"`
}

type checkDomainRequest struct {
	Domain string `json:"domain"`
}

type checkActorRequest struct {
	ActorURI    *string `json:"actor_uri"`
	Content     *string `json:"content"`
	ActorDomain *string `json:"actor_domain"`
}

type suspendActorRequest struct {
	ActorURI string  `json:"actor_uri"`
	Name     *string `json:"name"`
	Reason   *string `json:"reason"`
	Priority *int    `json:"priority"`
}

func (s *server) handleRuleList(c *gin.Context) {
	ctx := c.Request.Context()
	query := s.db.WithContext(ctx).Model(&store.FediverseModerationRuleEntity{})
	if enabledOnly, _ := strconv.ParseBool(c.Query("enabledOnly")); enabledOnly {
		query = query.Where("is_enabled = ? AND (expires_at IS NULL OR expires_at > ?)", true, time.Now().UTC())
	}
	var rules []store.FediverseModerationRuleEntity
	if err := query.Order("priority ASC").Find(&rules).Error; err != nil {
		httpx.Internal(c)
		return
	}
	models := make([]model.FediverseModerationRule, 0, len(rules))
	for i := range rules {
		models = append(models, ruleToModel(&rules[i]))
	}
	httpx.OK(c, models)
}

func (s *server) handleRuleGet(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errsRuleNotFound())
		return
	}
	var rule store.FediverseModerationRuleEntity
	if err := s.db.WithContext(c.Request.Context()).Where("id = ?", id).First(&rule).Error; err != nil {
		httpx.Fail(c, errsRuleNotFound())
		return
	}
	httpx.OK(c, ruleToModel(&rule))
}

func (s *server) handleRuleCreate(c *gin.Context) {
	var request createRuleRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	now := time.Now().UTC()
	rule := &store.FediverseModerationRuleEntity{
		ID:              uuid.New(),
		Name:            request.Name,
		Description:     request.Description,
		Type:            request.Type,
		Action:          request.Action,
		Domain:          request.Domain,
		KeywordPattern:  request.KeywordPattern,
		IsRegex:         request.IsRegex,
		IsEnabled:       request.IsEnabled,
		ReportThreshold: request.ReportThreshold,
		Priority:        request.Priority,
		IsSystemRule:    false,
		EntityBase:      store.EntityBase{CreatedAt: now, UpdatedAt: now},
	}
	if err := s.db.WithContext(c.Request.Context()).Create(rule).Error; err != nil {
		httpx.Internal(c)
		return
	}
	s.moderation.invalidate()
	httpx.Created(c, ruleToModel(rule))
}

func (s *server) handleRuleUpdate(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errsRuleNotFound())
		return
	}
	var request updateRuleRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	var rule store.FediverseModerationRuleEntity
	if err := s.db.WithContext(c.Request.Context()).Where("id = ?", id).First(&rule).Error; err != nil {
		httpx.Fail(c, errsRuleNotFound())
		return
	}
	if rule.IsSystemRule {
		httpx.BadRequest(c, "FEDIVERSE_RULE_SYSTEM_NOT_MODIFIABLE", "Cannot modify system rules.")
		return
	}
	if request.Name != nil && *request.Name != "" {
		rule.Name = *request.Name
	}
	if request.Description != nil {
		rule.Description = request.Description
	}
	if request.Type != nil {
		rule.Type = *request.Type
	}
	if request.Action != nil {
		rule.Action = *request.Action
	}
	if request.Domain != nil {
		rule.Domain = request.Domain
	}
	if request.KeywordPattern != nil {
		rule.KeywordPattern = request.KeywordPattern
	}
	if request.IsRegex != nil {
		rule.IsRegex = *request.IsRegex
	}
	if request.IsEnabled != nil {
		rule.IsEnabled = *request.IsEnabled
	}
	if request.ReportThreshold != nil {
		rule.ReportThreshold = request.ReportThreshold
	}
	if request.Priority != nil {
		rule.Priority = *request.Priority
	}
	rule.UpdatedAt = time.Now().UTC()
	if err := s.db.WithContext(c.Request.Context()).Save(&rule).Error; err != nil {
		httpx.Internal(c)
		return
	}
	s.moderation.invalidate()
	httpx.OK(c, ruleToModel(&rule))
}

func (s *server) handleRuleDelete(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errsRuleNotFound())
		return
	}
	var rule store.FediverseModerationRuleEntity
	if err := s.db.WithContext(c.Request.Context()).Where("id = ?", id).First(&rule).Error; err != nil {
		httpx.Fail(c, errsRuleNotFound())
		return
	}
	if rule.IsSystemRule {
		httpx.BadRequest(c, "FEDIVERSE_RULE_SYSTEM_NOT_DELETABLE", "Cannot delete system rules.")
		return
	}
	if err := s.db.WithContext(c.Request.Context()).Delete(&rule).Error; err != nil {
		httpx.Internal(c)
		return
	}
	s.moderation.invalidate()
	httpx.NoContent(c)
}

func (s *server) handleRuleToggle(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errsRuleNotFound())
		return
	}
	var request toggleRuleRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	var rule store.FediverseModerationRuleEntity
	if err := s.db.WithContext(c.Request.Context()).Where("id = ?", id).First(&rule).Error; err != nil {
		httpx.Fail(c, errsRuleNotFound())
		return
	}
	if rule.IsSystemRule {
		httpx.BadRequest(c, "FEDIVERSE_RULE_SYSTEM_NOT_TOGGLABLE", "Cannot toggle system rules.")
		return
	}
	rule.IsEnabled = request.Enabled
	rule.UpdatedAt = time.Now().UTC()
	if err := s.db.WithContext(c.Request.Context()).Save(&rule).Error; err != nil {
		httpx.Internal(c)
		return
	}
	s.moderation.invalidate()
	httpx.OK(c, ruleToModel(&rule))
}

func (s *server) handleCheckDomain(c *gin.Context) {
	var request checkDomainRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	httpx.OK(c, s.moderation.checkDomain(c.Request.Context(), request.Domain))
}

func (s *server) handleCheckActor(c *gin.Context) {
	var request checkActorRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	httpx.OK(c, s.moderation.checkActor(c.Request.Context(), deref(request.ActorURI), deref(request.Content), deref(request.ActorDomain)))
}

func (s *server) handleCheckActorRule(c *gin.Context) {
	var request checkActorRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	httpx.OK(c, s.moderation.checkActorRule(c.Request.Context(), deref(request.ActorURI)))
}

func (s *server) handleActorRuleList(c *gin.Context) {
	ctx := c.Request.Context()
	query := s.db.WithContext(ctx).Model(&store.FediverseModerationRuleEntity{}).
		Where("type IN ?", []int{ruleActorSuspend, ruleActorAllow})
	if enabledOnly, _ := strconv.ParseBool(c.Query("enabledOnly")); enabledOnly {
		query = query.Where("is_enabled = ? AND (expires_at IS NULL OR expires_at > ?)", true, time.Now().UTC())
	}
	var rules []store.FediverseModerationRuleEntity
	if err := query.Order("priority ASC").Find(&rules).Error; err != nil {
		httpx.Internal(c)
		return
	}
	models := make([]model.FediverseModerationRule, 0, len(rules))
	for i := range rules {
		models = append(models, ruleToModel(&rules[i]))
	}
	httpx.OK(c, models)
}

func (s *server) handleSuspendActor(c *gin.Context) {
	var request suspendActorRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	if strings.TrimSpace(request.ActorURI) == "" {
		httpx.BadRequest(c, "FEDIVERSE_ACTOR_URI_REQUIRED", "Actor URI is required.")
		return
	}
	parsed, err := url.Parse(request.ActorURI)
	if err != nil || !parsed.IsAbs() || parsed.Scheme != "https" {
		httpx.BadRequest(c, "FEDIVERSE_ACTOR_URI_INVALID", "Actor URI must be an absolute https URI.")
		return
	}
	actorURI := strings.TrimRight(request.ActorURI, "/")
	now := time.Now().UTC()
	ctx := c.Request.Context()

	var rule store.FediverseModerationRuleEntity
	err = s.db.WithContext(ctx).
		Where("type = ? AND domain = ?", ruleActorSuspend, actorURI).
		First(&rule).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		name := "Suspend " + actorURI
		if request.Name != nil && strings.TrimSpace(*request.Name) != "" {
			name = *request.Name
		}
		priority := 0
		if request.Priority != nil {
			priority = *request.Priority
		}
		rule = store.FediverseModerationRuleEntity{
			ID:          uuid.New(),
			Name:        name,
			Description: request.Reason,
			Type:        ruleActorSuspend,
			Action:      actionSuspend,
			Domain:      &actorURI,
			IsEnabled:   true,
			Priority:    priority,
			IsSystemRule: false,
			EntityBase:  store.EntityBase{CreatedAt: now, UpdatedAt: now},
		}
		if createErr := s.db.WithContext(ctx).Create(&rule).Error; createErr != nil {
			httpx.Internal(c)
			return
		}
	} else if err != nil {
		httpx.Internal(c)
		return
	} else {
		if rule.IsSystemRule {
			httpx.BadRequest(c, "FEDIVERSE_RULE_SYSTEM_NOT_MODIFIABLE", "Cannot modify system rules.")
			return
		}
		if request.Name != nil && strings.TrimSpace(*request.Name) != "" {
			rule.Name = *request.Name
		}
		if request.Reason != nil {
			rule.Description = request.Reason
		}
		if request.Priority != nil {
			rule.Priority = *request.Priority
		}
		rule.IsEnabled = true
		rule.ExpiresAt = nil
		rule.UpdatedAt = now
		if saveErr := s.db.WithContext(ctx).Save(&rule).Error; saveErr != nil {
			httpx.Internal(c)
			return
		}
	}
	s.moderation.invalidate()
	httpx.OK(c, ruleToModel(&rule))
}

func (s *server) handleUnsuspendActor(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errsRuleNotFound())
		return
	}
	var rule store.FediverseModerationRuleEntity
	if err := s.db.WithContext(c.Request.Context()).Where("id = ?", id).First(&rule).Error; err != nil ||
		rule.Type != ruleActorSuspend {
		httpx.NotFound(c, "Actor suspension rule not found.")
		return
	}
	if rule.IsSystemRule {
		httpx.BadRequest(c, "FEDIVERSE_RULE_SYSTEM_NOT_DELETABLE", "Cannot delete system rules.")
		return
	}
	if err := s.db.WithContext(c.Request.Context()).Delete(&rule).Error; err != nil {
		httpx.Internal(c)
		return
	}
	s.moderation.invalidate()
	httpx.NoContent(c)
}

// ---- helpers --------------------------------------------------------------

func parseOffsetTake(c *gin.Context, defaultTake int) (int, int) {
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 {
		offset = 0
	}
	take, err := strconv.Atoi(c.DefaultQuery("take", strconv.Itoa(defaultTake)))
	if err != nil || take <= 0 {
		take = defaultTake
	}
	return offset, take
}
