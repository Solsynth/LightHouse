package posts

import (
	"context"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/go/pkg/errs"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

type createTagRequest struct {
	Slug        string  `json:"slug"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

type updateTagRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

type setProtectedRequest struct {
	IsProtected bool `json:"is_protected"`
}

type protectedTagQuotaRecord struct {
	ID          string      `json:"id"`
	Slug        string      `json:"slug"`
	Name        *string     `json:"name"`
	Description *string     `json:"description"`
	IsProtected bool        `json:"is_protected"`
	IsEvent     bool        `json:"is_event"`
	EventEndsAt *model.Time `json:"event_ends_at"`
}

type protectedTagQuotaResponse struct {
	Total     int                       `json:"total"`
	Used      int                       `json:"used"`
	Remaining int                       `json:"remaining"`
	Level     int                       `json:"level"`
	PerkLevel int                       `json:"perk_level"`
	Records   []protectedTagQuotaRecord `json:"records"`
}

func (h *handler) registerTaxonomyRoutes(host *module.Host[core.Services]) {
	api := host.API
	auth := host.Core.Auth

	api.GET("/posts/categories", h.listCategories)
	api.GET("/posts/tags", h.listTags)
	api.GET("/posts/categories/:slug", h.getCategory)
	api.POST("/posts/categories/:slug/subscribe", authn.RequireAuth(), auth.RequirePermission("post.categories.subscribe"), h.subscribeCategory)
	api.POST("/posts/categories/:slug/unsubscribe", authn.RequireAuth(), auth.RequirePermission("post.categories.subscribe"), h.unsubscribeCategory)
	api.GET("/posts/categories/:slug/subscription", authn.RequireAuth(), h.getCategorySubscription)
	api.POST("/posts/tags/:slug/subscribe", authn.RequireAuth(), auth.RequirePermission("post.categories.subscribe"), h.subscribeTag)
	api.POST("/posts/tags/:slug/unsubscribe", authn.RequireAuth(), auth.RequirePermission("post.categories.subscribe"), h.unsubscribeTag)
	api.GET("/posts/tags/:slug/subscription", authn.RequireAuth(), h.getTagSubscription)
	api.POST("/posts/tags", authn.RequireAuth(), h.createTag)
	api.GET("/posts/tags/quota", authn.RequireAuth(), h.getProtectedTagQuota)
	api.GET("/posts/tags/:slug/quota", authn.RequireAuth(), h.getProtectedTagQuota)
	api.GET("/posts/tags/:slug", h.getTag)
	api.PATCH("/posts/tags/:slug", authn.RequireAuth(), h.updateTag)
	api.POST("/posts/tags/:slug/claim", authn.RequireAuth(), auth.RequirePermission("posts.tags.claim"), h.claimTag)
	api.POST("/posts/tags/:slug/release", authn.RequireAuth(), h.releaseTag)
	api.PATCH("/posts/tags/:slug/protect", authn.RequireAuth(), h.setTagProtected)
	api.GET("/categories/subscriptions", authn.RequireAuth(), h.listSubscriptions)
}

// ---------------------------------------------------------------------------
// Listings
// ---------------------------------------------------------------------------

func (h *handler) listCategories(c *gin.Context) {
	ctx := c.Request.Context()
	offset, take := paginate(c, 20, h.maxTake())
	query := strings.TrimSpace(c.Query("query"))
	order := c.Query("order")
	now := time.Now().UTC()
	recent := now.Add(-7 * 24 * time.Hour)

	q := h.db.WithContext(ctx).Model(&store.PostCategoryEntity{})
	if query != "" {
		q = q.Where("slug ILIKE ?", "%"+query+"%")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	switch order {
	case "usage":
		q = q.Order("(SELECT count(*) FROM post_category_links l JOIN posts p ON p.id = l.posts_id AND p.deleted_at IS NULL WHERE l.categories_id = post_categories.id) DESC")
	case "popularity":
		q = q.Order(orderExpr("(SELECT count(*) FROM post_category_links l JOIN posts p ON p.id = l.posts_id AND p.deleted_at IS NULL WHERE l.categories_id = post_categories.id AND p.drafted_at IS NULL AND p.visibility = ? AND p.created_at >= ? AND p.created_at <= ?) DESC", int(model.VisibilityPublic), recent, now))
	case "":
		q = q.Order("COALESCE(name, slug) ASC")
	default:
		q = q.Order("created_at DESC")
	}
	var rows []store.PostCategoryEntity
	if err := q.Offset(offset).Limit(take).Find(&rows).Error; err != nil {
		httpx.Internal(c)
		return
	}
	usage, err := h.categoryUsage(ctx, rows, order, now)
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

func (h *handler) categoryUsage(ctx context.Context, rows []store.PostCategoryEntity, order string, now time.Time) (map[uuid.UUID]int, error) {
	out := map[uuid.UUID]int{}
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	type countRow struct {
		CategoryID uuid.UUID `gorm:"column:categories_id"`
		Count      int       `gorm:"column:count"`
	}
	var counts []countRow
	q := h.db.WithContext(ctx).Table("post_category_links l").
		Select("l.categories_id, count(*) AS count").
		Joins("JOIN posts p ON p.id = l.posts_id AND p.deleted_at IS NULL").
		Where("l.categories_id IN ?", ids)
	if order == "popularity" {
		q = q.Where("p.drafted_at IS NULL AND p.visibility = ? AND p.created_at >= ? AND p.created_at <= ?",
			int(model.VisibilityPublic), now.Add(-7*24*time.Hour), now)
	}
	if err := q.Group("l.categories_id").Scan(&counts).Error; err != nil {
		return out, err
	}
	for _, row := range counts {
		out[row.CategoryID] = row.Count
	}
	return out, nil
}

func (h *handler) listTags(c *gin.Context) {
	ctx := c.Request.Context()
	offset, take := paginate(c, 20, h.maxTake())
	query := strings.TrimSpace(c.Query("query"))
	order := c.Query("order")
	now := time.Now().UTC()
	recent := now.Add(-7 * 24 * time.Hour)

	q := h.db.WithContext(ctx).Model(&store.PostTagEntity{})
	if query != "" {
		q = q.Where("slug ILIKE ?", "%"+query+"%")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	switch order {
	case "usage":
		q = q.Order("(SELECT count(*) FROM post_tag_links l JOIN posts p ON p.id = l.posts_id AND p.deleted_at IS NULL WHERE l.tags_id = post_tags.id) DESC")
	case "popularity":
		q = q.Order(orderExpr("(SELECT count(*) FROM post_tag_links l JOIN posts p ON p.id = l.posts_id AND p.deleted_at IS NULL WHERE l.tags_id = post_tags.id AND p.drafted_at IS NULL AND p.visibility = ? AND p.created_at >= ? AND p.created_at <= ?) DESC", int(model.VisibilityPublic), recent, now))
	case "":
		q = q.Order("COALESCE(name, slug) ASC")
	default:
		q = q.Order("created_at DESC")
	}
	var rows []store.PostTagEntity
	if err := q.Offset(offset).Limit(take).Find(&rows).Error; err != nil {
		httpx.Internal(c)
		return
	}
	usage, err := h.tagUsage(ctx, rows, order, now)
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

func (h *handler) tagUsage(ctx context.Context, rows []store.PostTagEntity, order string, now time.Time) (map[uuid.UUID]int, error) {
	out := map[uuid.UUID]int{}
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	var counts []struct {
		TagID uuid.UUID `gorm:"column:tags_id"`
		Count int       `gorm:"column:count"`
	}
	q := h.db.WithContext(ctx).Table("post_tag_links l").
		Select("l.tags_id, count(*) AS count").
		Joins("JOIN posts p ON p.id = l.posts_id AND p.deleted_at IS NULL").
		Where("l.tags_id IN ?", ids)
	if order == "popularity" {
		q = q.Where("p.drafted_at IS NULL AND p.visibility = ? AND p.created_at >= ? AND p.created_at <= ?",
			int(model.VisibilityPublic), now.Add(-7*24*time.Hour), now)
	}
	if err := q.Group("l.tags_id").Scan(&counts).Error; err != nil {
		return out, err
	}
	for _, row := range counts {
		out[row.TagID] = row.Count
	}
	return out, nil
}

func (h *handler) getCategory(c *gin.Context) {
	var category store.PostCategoryEntity
	err := h.db.WithContext(c.Request.Context()).
		Where("lower(slug) = ?", strings.ToLower(c.Param("slug"))).First(&category).Error
	if err != nil {
		httpx.NotFound(c, "Category not found.")
		return
	}
	httpx.OK(c, postCategoryToModel(&category))
}

// ---------------------------------------------------------------------------
// Subscriptions
// ---------------------------------------------------------------------------

func (h *handler) subscribeCategory(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := h.viewerAccountID(c)
	if accountID == uuid.Nil {
		httpx.Unauthorized(c)
		return
	}
	var category store.PostCategoryEntity
	if err := h.db.WithContext(ctx).Where("lower(slug) = ?", strings.ToLower(c.Param("slug"))).
		First(&category).Error; err != nil {
		httpx.Fail(c, errs.New("CATEGORY_NOT_FOUND", "Category not found.", 404))
		return
	}
	var subscription store.PostCategorySubscriptionEntity
	err := h.db.WithContext(ctx).
		Where("category_id = ? AND account_id = ?", category.ID, accountID).First(&subscription).Error
	if err == nil {
		httpx.OK(c, postCategorySubscriptionToModel(&subscription))
		return
	}
	subscription = store.PostCategorySubscriptionEntity{
		ID:         uuid.New(),
		AccountID:  accountID,
		CategoryID: &category.ID,
	}
	if err := h.db.WithContext(ctx).Create(&subscription).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.Created(c, postCategorySubscriptionToModel(&subscription))
}

func (h *handler) unsubscribeCategory(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := h.viewerAccountID(c)
	if accountID == uuid.Nil {
		httpx.Unauthorized(c)
		return
	}
	var category store.PostCategoryEntity
	if err := h.db.WithContext(ctx).Where("lower(slug) = ?", strings.ToLower(c.Param("slug"))).
		First(&category).Error; err != nil {
		httpx.Fail(c, errs.New("CATEGORY_NOT_FOUND", "Category not found.", 404))
		return
	}
	var subscription store.PostCategorySubscriptionEntity
	if err := h.db.WithContext(ctx).Where("category_id = ? AND account_id = ?", category.ID, accountID).
		First(&subscription).Error; err != nil {
		httpx.NoContent(c)
		return
	}
	h.db.WithContext(ctx).Delete(&store.PostCategorySubscriptionEntity{}, "id = ?", subscription.ID)
	httpx.NoContent(c)
}

func (h *handler) getCategorySubscription(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := h.viewerAccountID(c)
	if accountID == uuid.Nil {
		httpx.Unauthorized(c)
		return
	}
	var category store.PostCategoryEntity
	if err := h.db.WithContext(ctx).Where("lower(slug) = ?", strings.ToLower(c.Param("slug"))).
		First(&category).Error; err != nil {
		httpx.Fail(c, errs.New("CATEGORY_NOT_FOUND", "Category not found.", 404))
		return
	}
	var subscription store.PostCategorySubscriptionEntity
	if err := h.db.WithContext(ctx).Where("category_id = ? AND account_id = ?", category.ID, accountID).
		First(&subscription).Error; err != nil {
		httpx.Fail(c, errs.New("CATEGORY_SUBSCRIPTION_NOT_FOUND", "Subscription not found.", 404))
		return
	}
	httpx.OK(c, postCategorySubscriptionToModel(&subscription))
}

func (h *handler) subscribeTag(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := h.viewerAccountID(c)
	if accountID == uuid.Nil {
		httpx.Unauthorized(c)
		return
	}
	var tag store.PostTagEntity
	if err := h.db.WithContext(ctx).Where("lower(slug) = ?", strings.ToLower(c.Param("slug"))).
		First(&tag).Error; err != nil {
		httpx.Fail(c, errs.New("POST_TAG_NOT_FOUND", "Tag not found.", 404))
		return
	}
	var subscription store.PostCategorySubscriptionEntity
	err := h.db.WithContext(ctx).Where("tag_id = ? AND account_id = ?", tag.ID, accountID).
		First(&subscription).Error
	if err == nil {
		httpx.OK(c, postCategorySubscriptionToModel(&subscription))
		return
	}
	subscription = store.PostCategorySubscriptionEntity{
		ID:        uuid.New(),
		AccountID: accountID,
		TagID:     &tag.ID,
	}
	if err := h.db.WithContext(ctx).Create(&subscription).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.Created(c, postCategorySubscriptionToModel(&subscription))
}

func (h *handler) unsubscribeTag(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := h.viewerAccountID(c)
	if accountID == uuid.Nil {
		httpx.Unauthorized(c)
		return
	}
	var tag store.PostTagEntity
	if err := h.db.WithContext(ctx).Where("lower(slug) = ?", strings.ToLower(c.Param("slug"))).
		First(&tag).Error; err != nil {
		httpx.Fail(c, errs.New("POST_TAG_NOT_FOUND", "Tag not found.", 404))
		return
	}
	var subscription store.PostCategorySubscriptionEntity
	if err := h.db.WithContext(ctx).Where("tag_id = ? AND account_id = ?", tag.ID, accountID).
		First(&subscription).Error; err != nil {
		httpx.NoContent(c)
		return
	}
	h.db.WithContext(ctx).Delete(&store.PostCategorySubscriptionEntity{}, "id = ?", subscription.ID)
	httpx.NoContent(c)
}

func (h *handler) getTagSubscription(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := h.viewerAccountID(c)
	if accountID == uuid.Nil {
		httpx.Unauthorized(c)
		return
	}
	var tag store.PostTagEntity
	if err := h.db.WithContext(ctx).Where("lower(slug) = ?", strings.ToLower(c.Param("slug"))).
		First(&tag).Error; err != nil {
		httpx.Fail(c, errs.New("POST_TAG_NOT_FOUND", "Tag not found.", 404))
		return
	}
	var subscription store.PostCategorySubscriptionEntity
	if err := h.db.WithContext(ctx).Where("tag_id = ? AND account_id = ?", tag.ID, accountID).
		First(&subscription).Error; err != nil {
		httpx.Fail(c, errs.New("TAG_SUBSCRIPTION_NOT_FOUND", "Subscription not found.", 404))
		return
	}
	httpx.OK(c, postCategorySubscriptionToModel(&subscription))
}

func (h *handler) listSubscriptions(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := h.viewerAccountID(c)
	if accountID == uuid.Nil {
		httpx.Unauthorized(c)
		return
	}
	page, ok := h.pageFrom(c)
	if !ok {
		return
	}
	query := h.db.WithContext(ctx).Model(&store.PostCategorySubscriptionEntity{}).
		Where("account_id = ?", accountID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	var rows []store.PostCategorySubscriptionEntity
	if err := query.Order("created_at DESC").Offset(page.Offset).Limit(page.Take).Find(&rows).Error; err != nil {
		httpx.Internal(c)
		return
	}
	out := make([]model.PostCategorySubscription, 0, len(rows))
	for i := range rows {
		out = append(out, postCategorySubscriptionToModel(&rows[i]))
	}
	httpx.List(c, out, total)
}

func (h *handler) viewerAccountID(c *gin.Context) uuid.UUID {
	account := authn.CurrentUser(c)
	if account == nil {
		return uuid.Nil
	}
	id, err := uuid.Parse(account.GetId())
	if err != nil {
		return uuid.Nil
	}
	return id
}

// ---------------------------------------------------------------------------
// Tag management (publisher scoped)
// ---------------------------------------------------------------------------

func (h *handler) resolveTaxonomyPublisher(ctx context.Context, accountID uuid.UUID, pubName string) *store.PublisherEntity {
	if pubName != "" {
		publisher, err := h.publisherByName(ctx, pubName)
		if err != nil {
			return nil
		}
		if h.isMemberWithRole(ctx, publisher.ID, accountID, int(model.PublisherRoleEditor)) {
			return publisher
		}
		return nil
	}
	var settings store.PublishingSettingsEntity
	if err := h.db.WithContext(ctx).Where("account_id = ?", accountID).First(&settings).Error; err == nil &&
		settings.DefaultPostingPublisherID != nil {
		publisher, err := h.publisherByID(ctx, *settings.DefaultPostingPublisherID)
		if err == nil && h.isMemberWithRole(ctx, publisher.ID, accountID, int(model.PublisherRoleEditor)) {
			return publisher
		}
	}
	var publisher store.PublisherEntity
	if err := h.db.WithContext(ctx).
		Where("account_id = ? AND type = ?", accountID, int(model.PublisherIndividual)).
		First(&publisher).Error; err != nil {
		return nil
	}
	return &publisher
}

func (h *handler) createTag(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := h.viewerAccountID(c)
	if accountID == uuid.Nil {
		httpx.Unauthorized(c)
		return
	}
	var request createTagRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	publisher := h.resolveTaxonomyPublisher(ctx, accountID, c.Query("pub"))
	if publisher == nil {
		httpx.BadRequest(c, "TAG_PUBLISHER_NOT_RESOLVED", "Cannot resolve publisher. Specify one via ?pub= or set a default.")
		return
	}
	slug := strings.ToLower(strings.TrimSpace(request.Slug))
	if slug == "" {
		httpx.BadRequest(c, "TAG_CREATE_FAILED", "A tag with this slug already exists.")
		return
	}
	var existing store.PostTagEntity
	if err := h.db.WithContext(ctx).Where("lower(slug) = ?", slug).First(&existing).Error; err == nil {
		httpx.BadRequest(c, "TAG_CREATE_FAILED", "A tag with this slug already exists.")
		return
	}
	tag := store.PostTagEntity{
		ID:               uuid.New(),
		Slug:             slug,
		Name:             request.Name,
		Description:      request.Description,
		OwnerPublisherID: &publisher.ID,
	}
	if err := h.db.WithContext(ctx).Create(&tag).Error; err != nil {
		httpx.BadRequest(c, "TAG_CREATE_FAILED", "Unable to create the tag.")
		return
	}
	httpx.Created(c, postTagToModel(&tag))
}

func (h *handler) getTag(c *gin.Context) {
	var tag store.PostTagEntity
	if err := h.db.WithContext(c.Request.Context()).
		Where("lower(slug) = ?", strings.ToLower(c.Param("slug"))).First(&tag).Error; err != nil {
		httpx.NotFound(c, "Tag not found.")
		return
	}
	httpx.OK(c, postTagToModel(&tag))
}

func (h *handler) updateTag(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := h.viewerAccountID(c)
	if accountID == uuid.Nil {
		httpx.Unauthorized(c)
		return
	}
	var request updateTagRequest
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
		httpx.Forbidden(c, "This tag has no owner. Only admin can edit it.")
		return
	}
	if !h.isMemberWithRole(ctx, *tag.OwnerPublisherID, accountID, int(model.PublisherRoleManager)) {
		httpx.Forbidden(c, "You must be a manager or above of the owning publisher to edit this tag.")
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

func (h *handler) claimTag(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := h.viewerAccountID(c)
	if accountID == uuid.Nil {
		httpx.Unauthorized(c)
		return
	}
	publisher := h.resolveTaxonomyPublisher(ctx, accountID, c.Query("pub"))
	if publisher == nil {
		httpx.BadRequest(c, "TAG_PUBLISHER_NOT_RESOLVED", "Cannot resolve publisher. Specify one via ?pub= or set a default.")
		return
	}
	var tag store.PostTagEntity
	if err := h.db.WithContext(ctx).Where("lower(slug) = ?", strings.ToLower(c.Param("slug"))).
		First(&tag).Error; err != nil {
		httpx.NotFound(c, "Tag not found.")
		return
	}
	if tag.OwnerPublisherID != nil {
		httpx.BadRequest(c, "TAG_CLAIM_FAILED", "This tag is already owned by a publisher.")
		return
	}
	tag.OwnerPublisherID = &publisher.ID
	if err := h.db.WithContext(ctx).Save(&tag).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, postTagToModel(&tag))
}

func (h *handler) releaseTag(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := h.viewerAccountID(c)
	if accountID == uuid.Nil {
		httpx.Unauthorized(c)
		return
	}
	publisher := h.resolveTaxonomyPublisher(ctx, accountID, c.Query("pub"))
	if publisher == nil {
		httpx.BadRequest(c, "TAG_PUBLISHER_NOT_RESOLVED", "Cannot resolve publisher. Specify one via ?pub= or set a default.")
		return
	}
	var tag store.PostTagEntity
	if err := h.db.WithContext(ctx).Where("lower(slug) = ?", strings.ToLower(c.Param("slug"))).
		First(&tag).Error; err != nil {
		httpx.NotFound(c, "Tag not found.")
		return
	}
	if tag.OwnerPublisherID == nil {
		httpx.BadRequest(c, "TAG_RELEASE_FAILED", "This tag has no owner.")
		return
	}
	if *tag.OwnerPublisherID != publisher.ID {
		httpx.Forbidden(c, "This tag is not owned by the specified publisher.")
		return
	}
	if !h.isMemberWithRole(ctx, publisher.ID, accountID, int(model.PublisherRoleManager)) {
		httpx.Forbidden(c, "You must be a manager or above of the owning publisher to release this tag.")
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

func (h *handler) setTagProtected(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := h.viewerAccountID(c)
	if accountID == uuid.Nil {
		httpx.Unauthorized(c)
		return
	}
	var request setProtectedRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	publisher := h.resolveTaxonomyPublisher(ctx, accountID, c.Query("pub"))
	if publisher == nil {
		httpx.BadRequest(c, "TAG_PUBLISHER_NOT_RESOLVED", "Cannot resolve publisher. Specify one via ?pub= or set a default.")
		return
	}
	if !h.isMemberWithRole(ctx, publisher.ID, accountID, int(model.PublisherRoleManager)) {
		httpx.Forbidden(c, "You must be a manager or above of the owning publisher.")
		return
	}
	var tag store.PostTagEntity
	if err := h.db.WithContext(ctx).Where("lower(slug) = ?", strings.ToLower(c.Param("slug"))).
		First(&tag).Error; err != nil {
		httpx.NotFound(c, "Tag not found.")
		return
	}
	if tag.OwnerPublisherID == nil || *tag.OwnerPublisherID != publisher.ID {
		httpx.Forbidden(c, "This tag is not owned by the specified publisher.")
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

func (h *handler) getProtectedTagQuota(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := h.viewerAccountID(c)
	if accountID == uuid.Nil {
		httpx.Unauthorized(c)
		return
	}
	publisher := h.resolveTaxonomyPublisher(ctx, accountID, c.Query("pub"))
	if publisher == nil {
		httpx.BadRequest(c, "TAG_PUBLISHER_NOT_RESOLVED", "Cannot resolve publisher. Specify one via ?pub= or set a default.")
		return
	}
	httpx.OK(c, h.protectedTagQuota(ctx, publisher))
}

func (h *handler) protectedTagQuota(ctx context.Context, publisher *store.PublisherEntity) protectedTagQuotaResponse {
	level, perkLevel := 0, 0
	var accountID uuid.UUID
	if publisher.AccountID != nil {
		accountID = *publisher.AccountID
	} else {
		var member store.PublisherMemberEntity
		if err := h.db.WithContext(ctx).Where("publisher_id = ? AND joined_at IS NOT NULL", publisher.ID).
			Order("role DESC").First(&member).Error; err == nil {
			accountID = member.AccountID
		}
	}
	if accountID != uuid.Nil && h.ports != nil {
		if accounts, ok := module.Get[ports.Accounts](h.ports, ports.PortAccounts); ok {
			if account, err := accounts.GetAccount(ctx, accountID.String()); err == nil {
				if account.GetProfile() != nil {
					level = int(account.GetProfile().GetLevel())
				}
				perkLevel = int(account.GetPerkLevel())
			}
		}
	}
	total := 3 + 3*perkLevel

	var rows []store.PostTagEntity
	h.db.WithContext(ctx).Where("owner_publisher_id = ?", publisher.ID).
		Order("is_protected DESC").Order("updated_at DESC").Find(&rows)
	records := make([]protectedTagQuotaRecord, 0, len(rows))
	used := 0
	for i := range rows {
		if rows[i].IsProtected {
			used++
		}
		records = append(records, protectedTagQuotaRecord{
			ID:          rows[i].ID.String(),
			Slug:        rows[i].Slug,
			Name:        rows[i].Name,
			Description: rows[i].Description,
			IsProtected: rows[i].IsProtected,
			IsEvent:     rows[i].IsEvent,
			EventEndsAt: store.TimePtr(rows[i].EventEndsAt),
		})
	}
	remaining := total - used
	if remaining < 0 {
		remaining = 0
	}
	return protectedTagQuotaResponse{
		Total:     total,
		Used:      used,
		Remaining: remaining,
		Level:     level,
		PerkLevel: perkLevel,
		Records:   records,
	}
}
