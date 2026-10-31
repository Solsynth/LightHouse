package posts

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

type setVisibilityRequest struct {
	Visibility int `json:"visibility"`
}

type setShadowbanRequest struct {
	Reason int `json:"reason"`
}

type removeFromRealmRequest struct {
	Reason *string `json:"reason"`
}

func (h *handler) registerAdminRoutes(host *module.Host[core.Services]) {
	api := host.API
	auth := host.Core.Auth

	api.GET("/admin/posts", authn.RequireAuth(), auth.RequirePermission("posts.moderate"), h.adminListPosts)
	api.GET("/admin/posts/:id", authn.RequireAuth(), auth.RequirePermission("posts.moderate"), h.adminGetPost)
	api.GET("/admin/posts/:id/lock", authn.RequireAuth(), h.adminGetLock)
	api.POST("/admin/posts/:id/lock", authn.RequireAuth(), auth.RequirePermission("posts.lock"), h.adminLockPost)
	api.DELETE("/admin/posts/:id/lock", authn.RequireAuth(), auth.RequirePermission("posts.lock"), h.adminUnlockPost)
	api.POST("/admin/posts/:id/visibility", authn.RequireAuth(), auth.RequirePermission("posts.moderate"), h.adminSetVisibility)
	api.POST("/admin/posts/:id/shadowban", authn.RequireAuth(), auth.RequirePermission("posts.moderate"), h.adminShadowban)
	api.DELETE("/admin/posts/:id/shadowban", authn.RequireAuth(), auth.RequirePermission("posts.moderate"), h.adminUnshadowban)
	api.POST("/admin/posts/:id/realm/remove", authn.RequireAuth(), auth.RequirePermission("posts.moderate"), h.adminRemoveFromRealm)
	api.DELETE("/admin/posts/:id", authn.RequireAuth(), auth.RequirePermission("posts.delete"), h.adminDeletePost)
	api.POST("/admin/posts/:id/lock/batch", authn.RequireAuth(), auth.RequirePermission("posts.lock"), h.adminLockBatch)
	api.DELETE("/admin/posts/lock/batch", authn.RequireAuth(), auth.RequirePermission("posts.lock"), h.adminUnlockBatch)
	api.GET("/admin/stats", authn.RequireAuth(), auth.RequirePermission("posts.moderate"), h.adminStats)

	h.registerAdminTaxonomyRoutes(host)
}

func (h *handler) adminListPosts(c *gin.Context) {
	ctx := c.Request.Context()
	offset, take := paginate(c, 50, 200)
	query := h.db.WithContext(ctx).Model(&store.PostEntity{})
	if search := strings.TrimSpace(c.Query("query")); search != "" {
		pattern := "%" + search + "%"
		query = query.Where("(title ILIKE ? OR description ILIKE ? OR content ILIKE ?)", pattern, pattern, pattern)
	}
	if raw := c.Query("publisherId"); raw != "" {
		if id, ok := parseUUID(raw); ok {
			query = query.Where("publisher_id = ?", id)
		}
	}
	if raw := c.Query("realmId"); raw != "" {
		if id, ok := parseUUID(raw); ok {
			query = query.Where("realm_id = ?", id)
		}
	}
	if visibility := queryIntPtr(c, "visibility"); visibility != nil {
		query = query.Where("visibility = ?", *visibility)
	}
	if reason := queryIntPtr(c, "shadowbanReason"); reason != nil {
		query = query.Where("shadowban_reason = ?", *reason)
	}
	if locked := queryBoolPtr(c, "locked"); locked != nil {
		if *locked {
			query = query.Where("locked_at IS NOT NULL")
		} else {
			query = query.Where("locked_at IS NULL")
		}
	}
	if drafted := queryBoolPtr(c, "drafted"); drafted != nil {
		if *drafted {
			query = query.Where("drafted_at IS NOT NULL")
		} else {
			query = query.Where("drafted_at IS NULL")
		}
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	var rows []store.PostEntity
	if err := query.Order("COALESCE(published_at, created_at) DESC").
		Offset(offset).Limit(take).Find(&rows).Error; err != nil {
		httpx.Internal(c)
		return
	}
	posts, err := h.hydrate(ctx, rows, nil, false, false)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.List(c, posts, total)
}

func (h *handler) adminGetPost(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	var ent store.PostEntity
	if err := h.db.WithContext(ctx).Where("id = ?", id).First(&ent).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	post, err := h.hydrateOne(ctx, &ent, nil, false, false)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, post)
}

func (h *handler) adminGetLock(c *gin.Context) {
	var ent store.PostEntity
	if err := h.db.WithContext(c.Request.Context()).Where("id = ?", c.Param("id")).First(&ent).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	httpx.OK(c, gin.H{"locked": ent.LockedAt != nil, "lockedAt": store.TimePtr(ent.LockedAt)})
}

func (h *handler) adminLockPost(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	var ent store.PostEntity
	if err := h.db.WithContext(ctx).Where("id = ?", id).First(&ent).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	if ent.LockedAt != nil {
		httpx.BadRequest(c, "POST_ALREADY_LOCKED", "Post is already locked.")
		return
	}
	now := time.Now().UTC()
	ent.LockedAt = &now
	if err := h.db.WithContext(ctx).Model(&store.PostEntity{}).Where("id = ?", id).
		UpdateColumn("locked_at", now).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, gin.H{"locked": true, "lockedAt": now})
}

func (h *handler) adminUnlockPost(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	var ent store.PostEntity
	if err := h.db.WithContext(ctx).Where("id = ?", id).First(&ent).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	if ent.LockedAt == nil {
		httpx.BadRequest(c, "POST_NOT_LOCKED", "Post is not locked.")
		return
	}
	if err := h.db.WithContext(ctx).Model(&store.PostEntity{}).Where("id = ?", id).
		UpdateColumn("locked_at", nil).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, gin.H{"locked": false})
}

func (h *handler) adminSetVisibility(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	var request setVisibilityRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	var ent store.PostEntity
	if err := h.db.WithContext(ctx).Where("id = ?", id).First(&ent).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	now := time.Now().UTC()
	ent.Visibility = request.Visibility
	ent.EditedAt = &now
	if err := h.db.WithContext(ctx).Save(&ent).Error; err != nil {
		httpx.BadRequest(c, "POST_VISIBILITY_UPDATE_FAILED", "Unable to update post visibility.")
		return
	}
	post, err := h.hydrateOne(ctx, &ent, nil, false, false)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, post)
}

func (h *handler) adminShadowban(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	var request setShadowbanRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	if request.Reason == int(model.ShadowbanNone) {
		httpx.BadRequest(c, "POST_SHADOWBAN_USE_DELETE", "Use DELETE to clear a shadowban.")
		return
	}
	var ent store.PostEntity
	if err := h.db.WithContext(ctx).Where("id = ?", id).First(&ent).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	now := time.Now().UTC()
	ent.ShadowbanReason = &request.Reason
	ent.ShadowbannedAt = &now
	if err := h.db.WithContext(ctx).Save(&ent).Error; err != nil {
		httpx.Internal(c)
		return
	}
	post, err := h.hydrateOne(ctx, &ent, nil, false, false)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, post)
}

func (h *handler) adminUnshadowban(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	var ent store.PostEntity
	if err := h.db.WithContext(ctx).Where("id = ?", id).First(&ent).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	none := int(model.ShadowbanNone)
	ent.ShadowbanReason = &none
	ent.ShadowbannedAt = nil
	if err := h.db.WithContext(ctx).Save(&ent).Error; err != nil {
		httpx.Internal(c)
		return
	}
	post, err := h.hydrateOne(ctx, &ent, nil, false, false)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, post)
}

func (h *handler) adminRemoveFromRealm(c *gin.Context) {
	ctx := c.Request.Context()
	account := authn.CurrentUser(c)
	if account == nil {
		httpx.Unauthorized(c)
		return
	}
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	var request removeFromRealmRequest
	if c.Request.ContentLength > 0 {
		if !httpx.BindJSON(c, &request) {
			return
		}
	}
	var ent store.PostEntity
	if err := h.db.WithContext(ctx).Where("id = ?", id).First(&ent).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	if ent.RealmID == nil {
		httpx.BadRequest(c, "POST_NOT_IN_REALM", "This post is not linked to a realm.")
		return
	}
	realmID := *ent.RealmID
	var count int64
	h.db.WithContext(ctx).Model(&store.RealmPostModerationLogEntity{}).
		Where("post_id = ? AND realm_id = ?", ent.ID, realmID).Count(&count)
	if count > 0 {
		httpx.BadRequest(c, "POST_ALREADY_REMOVED_FROM_REALM", "This post has already been removed from the realm.")
		return
	}
	accountID, _ := uuid.Parse(account.GetId())
	now := time.Now().UTC()
	log := store.RealmPostModerationLogEntity{
		ID:                 uuid.New(),
		RealmID:            realmID,
		PostID:             ent.ID,
		ModeratorAccountID: accountID,
		Reason:             request.Reason,
		ModeratedAt:        &now,
	}
	if err := h.db.WithContext(ctx).Create(&log).Error; err != nil {
		httpx.Internal(c)
		return
	}
	ent.Visibility = int(model.VisibilityPrivate)
	ent.RealmID = nil
	if err := h.db.WithContext(ctx).Save(&ent).Error; err != nil {
		httpx.Internal(c)
		return
	}
	post, err := h.hydrateOne(ctx, &ent, nil, false, false)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, post)
}

func (h *handler) adminDeletePost(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	var ent store.PostEntity
	if err := h.db.WithContext(ctx).Where("id = ?", id).First(&ent).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	if err := h.softDeletePost(ctx, &ent); err != nil {
		httpx.Internal(c)
		return
	}
	httpx.NoContent(c)
}

func (h *handler) adminLockBatch(c *gin.Context) {
	ctx := c.Request.Context()
	var ids []uuid.UUID
	if !httpx.BindJSON(c, &ids) {
		return
	}
	now := time.Now().UTC()
	var posts []store.PostEntity
	h.db.WithContext(ctx).Where("id IN ?", ids).Find(&posts)
	for _, post := range posts {
		if post.LockedAt != nil {
			continue
		}
		h.db.WithContext(ctx).Model(&store.PostEntity{}).Where("id = ?", post.ID).
			UpdateColumn("locked_at", now)
	}
	httpx.OK(c, gin.H{"locked": len(posts)})
}

func (h *handler) adminUnlockBatch(c *gin.Context) {
	ctx := c.Request.Context()
	var ids []uuid.UUID
	if !httpx.BindJSON(c, &ids) {
		return
	}
	var posts []store.PostEntity
	h.db.WithContext(ctx).Where("id IN ?", ids).Find(&posts)
	if len(posts) > 0 {
		postIDs := make([]uuid.UUID, 0, len(posts))
		for _, post := range posts {
			postIDs = append(postIDs, post.ID)
		}
		h.db.WithContext(ctx).Model(&store.PostEntity{}).Where("id IN ?", postIDs).
			UpdateColumn("locked_at", nil)
	}
	httpx.OK(c, gin.H{"unlocked": len(posts)})
}

func (h *handler) adminStats(c *gin.Context) {
	ctx := c.Request.Context()
	now := time.Now().UTC()
	posts := h.db.WithContext(ctx).Model(&store.PostEntity{})
	var total, published, drafts, lastDay, lastWeek, lastMonth int64
	posts.Count(&total)
	posts.Where("published_at IS NOT NULL").Count(&published)
	posts.Where("drafted_at IS NOT NULL").Count(&drafts)
	posts.Where("created_at >= ?", now.Add(-24*time.Hour)).Count(&lastDay)
	posts.Where("created_at >= ?", now.Add(-7*24*time.Hour)).Count(&lastWeek)
	posts.Where("created_at >= ?", now.Add(-30*24*time.Hour)).Count(&lastMonth)
	var publishers, reactions, bookmarks int64
	h.db.WithContext(ctx).Model(&store.PublisherEntity{}).Count(&publishers)
	h.db.WithContext(ctx).Model(&store.PostReactionEntity{}).Count(&reactions)
	h.db.WithContext(ctx).Model(&store.PostBookmarkEntity{}).Count(&bookmarks)
	httpx.OK(c, model.PostStatsResponse{
		CalculatedAt:    model.NewTime(now),
		TotalPosts:      total,
		PublishedPosts:  published,
		DraftPosts:      drafts,
		PostsLastDay:    lastDay,
		PostsLastWeek:   lastWeek,
		PostsLastMonth:  lastMonth,
		TotalPublishers: publishers,
		TotalReactions:  reactions,
		TotalBookmarks:  bookmarks,
	})
}
