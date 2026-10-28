package posts

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

type adminCreateTagRequest struct {
	Slug            string  `json:"slug"`
	Name            *string `json:"name"`
	Description     *string `json:"description"`
	OwnerPublisherID *uuid.UUID `json:"ownerPublisherId"`
}

type assignTagRequest struct {
	PublisherID uuid.UUID `json:"publisherId"`
}

type adminProtectedRequest struct {
	IsProtected bool `json:"isProtected"`
}

type setEventRequest struct {
	IsEvent bool        `json:"isEvent"`
	EndsAt  *model.Time `json:"endsAt"`
}

type adminUpdateTagRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

type createCategoryRequest struct {
	Slug string  `json:"slug"`
	Name *string `json:"name"`
}

type updateCategoryRequest struct {
	Slug *string `json:"slug"`
	Name *string `json:"name"`
}

func (h *handler) registerAdminTaxonomyRoutes(host *module.Host[core.Services]) {
	api := host.API
	auth := host.Core.Auth
	moderate := auth.RequirePermission("posts.moderate")

	for _, base := range []string{"/admin/tags", "/admin/posts/tags"} {
		api.GET(base, moderate, h.adminListTags)
		api.GET(base+"/:slug", moderate, h.adminGetTag)
		api.POST(base, auth.RequirePermission("posts.tags.create"), h.adminCreateTag)
		api.POST(base+"/:slug/assign", auth.RequirePermission("posts.tags.assign"), h.adminAssignTag)
		api.DELETE(base+"/:slug/assign", auth.RequirePermission("posts.tags.assign"), h.adminUnassignTag)
		api.PATCH(base+"/:slug/protect", auth.RequirePermission("posts.tags.protect"), h.adminSetTagProtected)
		api.PATCH(base+"/:slug/event", auth.RequirePermission("posts.tags.event"), h.adminSetTagEvent)
		api.PATCH(base+"/:slug", auth.RequirePermission("posts.tags.update"), h.adminUpdateTag)
		api.DELETE(base+"/:slug", auth.RequirePermission("posts.tags.delete"), h.adminDeleteTag)
	}

	api.GET("/admin/categories", moderate, h.adminListCategories)
	api.GET("/admin/categories/:slug", moderate, h.adminGetCategory)
	api.POST("/admin/categories", auth.RequirePermission("post.categories.manage"), h.adminCreateCategory)
	api.PATCH("/admin/categories/:slug", auth.RequirePermission("post.categories.manage"), h.adminUpdateCategory)
	api.DELETE("/admin/categories/:slug", auth.RequirePermission("post.categories.manage"), h.adminDeleteCategory)
}

// ---------------------------------------------------------------------------
// Tags
// ---------------------------------------------------------------------------

func (h *handler) adminListTags(c *gin.Context) {
	ctx := c.Request.Context()
	offset, take := paginate(c, 50, 200)
	query := h.db.WithContext(ctx).Model(&store.PostTagEntity{})
	if search := strings.TrimSpace(c.Query("query")); search != "" {
		pattern := "%" + search + "%"
		query = query.Where("(slug ILIKE ? OR name ILIKE ? OR description ILIKE ?)", pattern, pattern, pattern)
	}
	if raw := c.Query("ownerPublisherId"); raw != "" {
		if id, ok := parseUUID(raw); ok {
			query = query.Where("owner_publisher_id = ?", id)
		}
	}
	if unowned := queryBoolPtr(c, "unowned"); unowned != nil && *unowned {
		query = query.Where("owner_publisher_id IS NULL")
	}
	if protected := queryBoolPtr(c, "isProtected"); protected != nil {
		query = query.Where("is_protected = ?", *protected)
	}
	if event := queryBoolPtr(c, "isEvent"); event != nil {
		query = query.Where("is_event = ?", *event)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	switch c.Query("order") {
	case "name":
		query = query.Order("COALESCE(name, slug) ASC")
	case "created":
		query = query.Order("created_at DESC")
	case "usage":
		query = query.Order("(SELECT count(*) FROM post_tag_links l JOIN posts p ON p.id = l.posts_id AND p.deleted_at IS NULL WHERE l.tags_id = post_tags.id) DESC").Order("slug ASC")
	default:
		query = query.Order("updated_at DESC")
	}
	var rows []store.PostTagEntity
	if err := query.Offset(offset).Limit(take).Find(&rows).Error; err != nil {
		httpx.Internal(c)
		return
	}
	usage, err := h.tagUsage(ctx, rows, "", time.Now().UTC())
	if err != nil {
		httpx.Internal(c)
		return
	}
	out := make([]model.PostTag, 0, len(rows))
	for i := range rows {
		tag := postTagToModel(&rows[i])
		value := usage[rows[i].ID]
		tag.Usage = &value
		out = append(out, tag)
	}
	httpx.List(c, out, total)
}

func (h *handler) adminGetTag(c *gin.Context) {
	ctx := c.Request.Context()
	var tag store.PostTagEntity
	if err := h.db.WithContext(ctx).Where("lower(slug) = ?", strings.ToLower(c.Param("slug"))).
		First(&tag).Error; err != nil {
		httpx.NotFound(c, "Tag not found.")
		return
	}
	var count int64
	h.db.WithContext(ctx).Table("post_tag_links l").
		Joins("JOIN posts p ON p.id = l.posts_id AND p.deleted_at IS NULL").
		Where("l.tags_id = ?", tag.ID).Count(&count)
	out := postTagToModel(&tag)
	usage := int(count)
	out.Usage = &usage
	httpx.OK(c, out)
}

func (h *handler) adminCreateTag(c *gin.Context) {
	ctx := c.Request.Context()
	var request adminCreateTagRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	var owner *store.PublisherEntity
	if request.OwnerPublisherID != nil {
		publisher, err := h.publisherByID(ctx, *request.OwnerPublisherID)
		if err != nil {
			httpx.BadRequest(c, "TAG_OWNER_PUBLISHER_NOT_FOUND", "Owner publisher not found.")
			return
		}
		owner = publisher
	}
	slug := strings.ToLower(strings.TrimSpace(request.Slug))
	if slug == "" {
		httpx.BadRequest(c, "TAG_CREATE_FAILED", "A tag with this slug already exists.")
		return
	}
	var count int64
	h.db.WithContext(ctx).Model(&store.PostTagEntity{}).Where("lower(slug) = ?", slug).Count(&count)
	if count > 0 {
		httpx.BadRequest(c, "TAG_CREATE_FAILED", "A tag with this slug already exists.")
		return
	}
	tag := store.PostTagEntity{
		ID:          uuid.New(),
		Slug:        slug,
		Name:        request.Name,
		Description: request.Description,
	}
	if owner != nil {
		tag.OwnerPublisherID = &owner.ID
	}
	if err := h.db.WithContext(ctx).Create(&tag).Error; err != nil {
		httpx.BadRequest(c, "TAG_CREATE_FAILED", "Unable to create the tag.")
		return
	}
	httpx.Created(c, postTagToModel(&tag))
}

func (h *handler) adminAssignTag(c *gin.Context) {
	ctx := c.Request.Context()
	var request assignTagRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	var tag store.PostTagEntity
	if err := h.db.WithContext(ctx).Where("lower(slug) = ?", strings.ToLower(c.Param("slug"))).
		First(&tag).Error; err != nil {
		httpx.NotFound(c, "Tag not found.")
		return
	}
	publisher, err := h.publisherByID(ctx, request.PublisherID)
	if err != nil {
		httpx.BadRequest(c, "TAG_ASSIGN_FAILED", "Publisher not found.")
		return
	}
	tag.OwnerPublisherID = &publisher.ID
	if err := h.db.WithContext(ctx).Save(&tag).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, postTagToModel(&tag))
}

func (h *handler) adminUnassignTag(c *gin.Context) {
	ctx := c.Request.Context()
	var tag store.PostTagEntity
	if err := h.db.WithContext(ctx).Where("lower(slug) = ?", strings.ToLower(c.Param("slug"))).
		First(&tag).Error; err != nil {
		httpx.NotFound(c, "Tag not found.")
		return
	}
	tag.OwnerPublisherID = nil
	tag.IsProtected = false
	if err := h.db.WithContext(ctx).Save(&tag).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, postTagToModel(&tag))
}

func (h *handler) adminSetTagProtected(c *gin.Context) {
	ctx := c.Request.Context()
	var request adminProtectedRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	var tag store.PostTagEntity
	if err := h.db.WithContext(ctx).Where("lower(slug) = ?", strings.ToLower(c.Param("slug"))).
		First(&tag).Error; err != nil {
		httpx.NotFound(c, "Tag not found.")
		return
	}
	if tag.OwnerPublisherID == nil {
		httpx.BadRequest(c, "TAG_NO_OWNER", "Tag has no owner. Assign ownership first.")
		return
	}
	publisher, err := h.publisherByID(ctx, *tag.OwnerPublisherID)
	if err != nil {
		httpx.BadRequest(c, "TAG_OWNER_PUBLISHER_NOT_FOUND", "Owner publisher not found.")
		return
	}
	if request.IsProtected && !tag.IsProtected {
		quota := h.protectedTagQuota(ctx, publisher)
		if quota.Used >= quota.Total {
			httpx.BadRequest(c, "TAG_PROTECT_FAILED", "Protected tag quota exceeded.")
			return
		}
	}
	tag.IsProtected = request.IsProtected
	if err := h.db.WithContext(ctx).Save(&tag).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, postTagToModel(&tag))
}

func (h *handler) adminSetTagEvent(c *gin.Context) {
	ctx := c.Request.Context()
	var request setEventRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	var tag store.PostTagEntity
	if err := h.db.WithContext(ctx).Where("lower(slug) = ?", strings.ToLower(c.Param("slug"))).
		First(&tag).Error; err != nil {
		httpx.NotFound(c, "Tag not found.")
		return
	}
	if request.IsEvent {
		if request.EndsAt == nil {
			httpx.BadRequest(c, "TAG_EVENT_FAILED", "Event tags must have an end time.")
			return
		}
		endsAt := request.EndsAt.Time()
		if !endsAt.After(time.Now().UTC()) {
			httpx.BadRequest(c, "TAG_EVENT_FAILED", "Event end time must be in the future.")
			return
		}
		tag.EventEndsAt = &endsAt
	} else {
		tag.EventEndsAt = nil
	}
	tag.IsEvent = request.IsEvent
	if err := h.db.WithContext(ctx).Save(&tag).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, postTagToModel(&tag))
}

func (h *handler) adminUpdateTag(c *gin.Context) {
	ctx := c.Request.Context()
	var request adminUpdateTagRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	var tag store.PostTagEntity
	if err := h.db.WithContext(ctx).Where("lower(slug) = ?", strings.ToLower(c.Param("slug"))).
		First(&tag).Error; err != nil {
		httpx.NotFound(c, "Tag not found.")
		return
	}
	if request.Name != nil {
		tag.Name = request.Name
	}
	if request.Description != nil {
		tag.Description = request.Description
	}
	if err := h.db.WithContext(ctx).Save(&tag).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, postTagToModel(&tag))
}

func (h *handler) adminDeleteTag(c *gin.Context) {
	ctx := c.Request.Context()
	var tag store.PostTagEntity
	if err := h.db.WithContext(ctx).Where("lower(slug) = ?", strings.ToLower(c.Param("slug"))).
		First(&tag).Error; err != nil {
		httpx.NotFound(c, "Tag not found.")
		return
	}
	if err := h.db.WithContext(ctx).Where("tags_id = ?", tag.ID).Delete(&store.PostTagLink{}).Error; err != nil {
		httpx.Internal(c)
		return
	}
	if err := h.db.WithContext(ctx).Delete(&store.PostTagEntity{}, "id = ?", tag.ID).Error; err != nil {
		httpx.BadRequest(c, "TAG_DELETE_FAILED", "Unable to delete the tag.")
		return
	}
	httpx.NoContent(c)
}

// ---------------------------------------------------------------------------
// Categories
// ---------------------------------------------------------------------------

func (h *handler) adminListCategories(c *gin.Context) {
	ctx := c.Request.Context()
	offset, take := paginate(c, 50, 200)
	query := h.db.WithContext(ctx).Model(&store.PostCategoryEntity{})
	if search := strings.TrimSpace(c.Query("query")); search != "" {
		pattern := "%" + search + "%"
		query = query.Where("(slug ILIKE ? OR name ILIKE ?)", pattern, pattern)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	switch c.Query("order") {
	case "name":
		query = query.Order("COALESCE(name, slug) ASC")
	case "created":
		query = query.Order("created_at DESC")
	case "usage":
		query = query.Order("(SELECT count(*) FROM post_category_links l JOIN posts p ON p.id = l.posts_id AND p.deleted_at IS NULL WHERE l.categories_id = post_categories.id) DESC").Order("slug ASC")
	default:
		query = query.Order("updated_at DESC")
	}
	var rows []store.PostCategoryEntity
	if err := query.Offset(offset).Limit(take).Find(&rows).Error; err != nil {
		httpx.Internal(c)
		return
	}
	usage, err := h.categoryUsage(ctx, rows, "", time.Now().UTC())
	if err != nil {
		httpx.Internal(c)
		return
	}
	out := make([]model.PostCategory, 0, len(rows))
	for i := range rows {
		category := postCategoryToModel(&rows[i])
		value := usage[rows[i].ID]
		category.Usage = &value
		out = append(out, category)
	}
	httpx.List(c, out, total)
}

func (h *handler) adminGetCategory(c *gin.Context) {
	ctx := c.Request.Context()
	var category store.PostCategoryEntity
	if err := h.db.WithContext(ctx).Where("lower(slug) = ?", strings.ToLower(c.Param("slug"))).
		First(&category).Error; err != nil {
		httpx.NotFound(c, "Category not found.")
		return
	}
	var count int64
	h.db.WithContext(ctx).Table("post_category_links l").
		Joins("JOIN posts p ON p.id = l.posts_id AND p.deleted_at IS NULL").
		Where("l.categories_id = ?", category.ID).Count(&count)
	out := postCategoryToModel(&category)
	usage := int(count)
	out.Usage = &usage
	httpx.OK(c, out)
}

func (h *handler) adminCreateCategory(c *gin.Context) {
	ctx := c.Request.Context()
	var request createCategoryRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	slug := strings.ToLower(strings.TrimSpace(request.Slug))
	if slug == "" {
		httpx.BadRequest(c, "CATEGORY_SLUG_REQUIRED", "Slug is required.")
		return
	}
	var count int64
	h.db.WithContext(ctx).Model(&store.PostCategoryEntity{}).Where("lower(slug) = ?", slug).Count(&count)
	if count > 0 {
		httpx.BadRequest(c, "CATEGORY_SLUG_EXISTS", "A category with this slug already exists.")
		return
	}
	category := store.PostCategoryEntity{
		ID:   uuid.New(),
		Slug: slug,
		Name: request.Name,
	}
	if err := h.db.WithContext(ctx).Create(&category).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.Created(c, postCategoryToModel(&category))
}

func (h *handler) adminUpdateCategory(c *gin.Context) {
	ctx := c.Request.Context()
	var request updateCategoryRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	var category store.PostCategoryEntity
	if err := h.db.WithContext(ctx).Where("lower(slug) = ?", strings.ToLower(c.Param("slug"))).
		First(&category).Error; err != nil {
		httpx.NotFound(c, "Category not found.")
		return
	}
	if request.Slug != nil {
		slug := strings.ToLower(strings.TrimSpace(*request.Slug))
		if slug == "" {
			httpx.BadRequest(c, "CATEGORY_SLUG_REQUIRED", "Slug cannot be empty.")
			return
		}
		if slug != category.Slug {
			var count int64
			h.db.WithContext(ctx).Model(&store.PostCategoryEntity{}).
				Where("lower(slug) = ? AND id <> ?", slug, category.ID).Count(&count)
			if count > 0 {
				httpx.BadRequest(c, "CATEGORY_SLUG_EXISTS", "A category with this slug already exists.")
				return
			}
			category.Slug = slug
		}
	}
	if request.Name != nil {
		category.Name = request.Name
	}
	if err := h.db.WithContext(ctx).Save(&category).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, postCategoryToModel(&category))
}

func (h *handler) adminDeleteCategory(c *gin.Context) {
	ctx := c.Request.Context()
	var category store.PostCategoryEntity
	if err := h.db.WithContext(ctx).Where("lower(slug) = ?", strings.ToLower(c.Param("slug"))).
		First(&category).Error; err != nil {
		httpx.NotFound(c, "Category not found.")
		return
	}
	if err := h.db.WithContext(ctx).Where("categories_id = ?", category.ID).Delete(&store.PostCategoryLink{}).Error; err != nil {
		httpx.Internal(c)
		return
	}
	if err := h.db.WithContext(ctx).Where("category_id = ?", category.ID).
		Delete(&store.PostCategorySubscriptionEntity{}).Error; err != nil {
		httpx.Internal(c)
		return
	}
	if err := h.db.WithContext(ctx).Delete(&store.PostCategoryEntity{}, "id = ?", category.ID).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.NoContent(c)
}
