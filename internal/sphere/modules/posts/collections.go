package posts

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/go/pkg/errs"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

type createCollectionRequest struct {
	Slug         string  `json:"slug"`
	Name         *string `json:"name"`
	Description  *string `json:"description"`
	BackgroundID *string `json:"background_id"`
	IconID       *string `json:"icon_id"`
}

type updateCollectionRequest struct {
	Name         *string `json:"name"`
	Description  *string `json:"description"`
	BackgroundID *string `json:"background_id"`
	IconID       *string `json:"icon_id"`
}

type addCollectionPostRequest struct {
	PostID uuid.UUID `json:"post_id"`
	Order  *int      `json:"order"`
}

type batchCollectionPostsRequest struct {
	PostIDs []uuid.UUID `json:"post_ids"`
}

type adminUpdateCollectionRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

type collectionOrderItem struct {
	id        uuid.UUID
	published *time.Time
	created   time.Time
}

type adminCollection struct {
	model.PostCollection
	ItemCount int `json:"item_count"`
}

func (h *handler) registerCollectionRoutes(host *module.Host[core.Services]) {
	api := host.API
	auth := host.Core.Auth
	manage := host.Core.Auth.RequirePermission("post.collections.posts.manage")

	api.GET("/collections", h.listAllCollections)
	api.GET("/publishers/:name/collections", h.listCollections)
	api.POST("/publishers/:name/collections", authn.RequireAuth(), h.createCollection)
	api.GET("/publishers/:name/collections/:slug", h.getCollection)
	api.PATCH("/publishers/:name/collections/:slug", authn.RequireAuth(), h.updateCollection)
	api.DELETE("/publishers/:name/collections/:slug", authn.RequireAuth(), h.deleteCollection)
	api.POST("/publishers/:name/collections/:slug/subscribe", authn.RequireAuth(), manage, h.subscribeCollection)
	api.POST("/publishers/:name/collections/:slug/unsubscribe", authn.RequireAuth(), manage, h.unsubscribeCollection)
	api.GET("/publishers/:name/collections/:slug/subscription", authn.RequireAuth(), h.getCollectionSubscription)
	api.GET("/publishers/:name/collections/:slug/posts", h.listCollectionPosts)
	api.POST("/publishers/:name/collections/:slug/posts", authn.RequireAuth(), manage, h.addCollectionPost)
	api.POST("/publishers/:name/collections/:slug/posts/batch", authn.RequireAuth(), manage, h.batchAddCollectionPosts)
	api.POST("/publishers/:name/collections/:slug/posts/batch/remove", authn.RequireAuth(), manage, h.batchRemoveCollectionPosts)
	api.DELETE("/publishers/:name/collections/:slug/posts/:postId", authn.RequireAuth(), manage, h.removeCollectionPost)
	api.PUT("/publishers/:name/collections/:slug/posts/reorder", authn.RequireAuth(), manage, h.reorderCollectionPosts)
	api.GET("/publishers/:name/collections/:slug/posts/:postId/prev", h.prevCollectionPost)
	api.GET("/publishers/:name/collections/:slug/posts/:postId/next", h.nextCollectionPost)

	api.GET("/admin/collections", authn.RequireAuth(), auth.RequirePermission("posts.moderate"), h.adminListCollections)
	api.GET("/admin/collections/:id", authn.RequireAuth(), auth.RequirePermission("posts.moderate"), h.adminGetCollection)
	api.PATCH("/admin/collections/:id", authn.RequireAuth(), auth.RequirePermission("post.collections.update"), h.adminUpdateCollection)
	api.DELETE("/admin/collections/:id", authn.RequireAuth(), auth.RequirePermission("post.collections.delete"), h.adminDeleteCollection)
}

func (h *handler) getCollectionBySlug(ctx context.Context, publisherName, slug string) (*store.PostCollectionEntity, error) {
	var collection store.PostCollectionEntity
	err := h.db.WithContext(ctx).
		Where("publisher_id IN (SELECT id FROM publishers WHERE lower(name) = ?)", strings.ToLower(publisherName)).
		Where("slug = ?", strings.ToLower(strings.TrimSpace(slug))).
		First(&collection).Error
	if err != nil {
		return nil, err
	}
	return &collection, nil
}

func (h *handler) listAllCollections(c *gin.Context) {
	ctx := c.Request.Context()
	offset := queryInt(c, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	take := queryInt(c, "take", 20)
	if take < 1 {
		take = 20
	}
	if take > h.maxTake() {
		take = h.maxTake()
	}
	search := strings.TrimSpace(c.Query("query"))
	query := h.db.WithContext(ctx).Model(&store.PostCollectionEntity{})
	if search != "" {
		pattern := "%" + search + "%"
		query = query.Where("(slug ILIKE ? OR name ILIKE ? OR description ILIKE ? OR publisher_id IN (SELECT id FROM publishers WHERE name ILIKE ? OR nick ILIKE ?))",
			pattern, pattern, pattern, pattern, pattern)
		if len([]rune(search)) >= 3 {
			query = query.Where("(similarity(slug, ?) > 0.3 OR name IS NULL OR similarity(name, ?) > 0.2)", search, search)
		}
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	if search != "" && len([]rune(search)) >= 3 {
		pattern := "%" + search + "%"
		query = query.Order(orderExpr("(slug ILIKE ? OR name ILIKE ? OR description ILIKE ?) DESC", pattern, pattern, pattern)).
			Order(orderExpr("similarity(slug, ?) DESC", search))
	} else {
		query = query.Order("COALESCE(name, slug) ASC").Order("slug ASC")
	}
	var rows []store.PostCollectionEntity
	if err := query.Offset(offset).Limit(take).Find(&rows).Error; err != nil {
		httpx.Internal(c)
		return
	}
	out := make([]model.PostCollection, 0, len(rows))
	for i := range rows {
		out = append(out, postCollectionToModel(&rows[i]))
	}
	httpx.List(c, out, total)
}

func (h *handler) listCollections(c *gin.Context) {
	ctx := c.Request.Context()
	var rows []store.PostCollectionEntity
	if err := h.db.WithContext(ctx).
		Where("publisher_id IN (SELECT id FROM publishers WHERE lower(name) = ?)", strings.ToLower(c.Param("name"))).
		Order("COALESCE(name, slug) ASC").Order("slug ASC").Find(&rows).Error; err != nil {
		httpx.Internal(c)
		return
	}
	out := make([]model.PostCollection, 0, len(rows))
	for i := range rows {
		out = append(out, postCollectionToModel(&rows[i]))
	}
	httpx.OK(c, out)
}

func (h *handler) createCollection(c *gin.Context) {
	ctx := c.Request.Context()
	publisher, ok := h.requirePublisherEditor(c, c.Param("name"))
	if !ok {
		return
	}
	var request createCollectionRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	slug := strings.ToLower(strings.TrimSpace(request.Slug))
	if slug == "" {
		httpx.BadRequest(c, "COLLECTION_CREATE_FAILED", "A collection with this slug already exists.")
		return
	}
	var count int64
	h.db.WithContext(ctx).Model(&store.PostCollectionEntity{}).
		Where("publisher_id = ? AND lower(slug) = ?", publisher.ID, slug).Count(&count)
	if count > 0 {
		httpx.BadRequest(c, "COLLECTION_CREATE_FAILED", "A collection with this slug already exists.")
		return
	}
	collection := store.PostCollectionEntity{
		ID:          uuid.New(),
		Slug:        slug,
		Name:        request.Name,
		Description: request.Description,
		PublisherID: publisher.ID,
		Background:  h.fileReferenceJSON(ctx, request.BackgroundID),
		Icon:        h.fileReferenceJSON(ctx, request.IconID),
	}
	if err := h.db.WithContext(ctx).Create(&collection).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.Created(c, postCollectionToModel(&collection))
}

func (h *handler) fileReferenceJSON(ctx context.Context, id *string) datatypes.JSON {
	if id == nil || strings.TrimSpace(*id) == "" {
		return nil
	}
	if h.ports == nil {
		return nil
	}
	files, ok := module.Get[ports.Files](h.ports, ports.PortFiles)
	if !ok {
		return nil
	}
	file, err := files.GetFile(ctx, *id)
	if err != nil {
		return nil
	}
	return store.ToJSON(file)
}

func (h *handler) getCollection(c *gin.Context) {
	collection, err := h.getCollectionBySlug(c.Request.Context(), c.Param("name"), c.Param("slug"))
	if err != nil {
		httpx.NotFound(c, "Collection not found.")
		return
	}
	httpx.OK(c, postCollectionToModel(collection))
}

func (h *handler) updateCollection(c *gin.Context) {
	ctx := c.Request.Context()
	collection, err := h.getCollectionBySlug(ctx, c.Param("name"), c.Param("slug"))
	if err != nil {
		httpx.NotFound(c, "Collection not found.")
		return
	}
	if _, ok := h.requirePublisherEditor(c, c.Param("name")); !ok {
		return
	}
	var request updateCollectionRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	collection.Name = request.Name
	collection.Description = request.Description
	if request.BackgroundID != nil {
		collection.Background = h.fileReferenceJSON(ctx, request.BackgroundID)
	}
	if request.IconID != nil {
		collection.Icon = h.fileReferenceJSON(ctx, request.IconID)
	}
	if err := h.db.WithContext(ctx).Save(collection).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, postCollectionToModel(collection))
}

func (h *handler) deleteCollection(c *gin.Context) {
	ctx := c.Request.Context()
	collection, err := h.getCollectionBySlug(ctx, c.Param("name"), c.Param("slug"))
	if err != nil {
		httpx.NotFound(c, "Collection not found.")
		return
	}
	if _, ok := h.requirePublisherEditor(c, c.Param("name")); !ok {
		return
	}
	if err := h.db.WithContext(ctx).Delete(&store.PostCollectionEntity{}, "id = ?", collection.ID).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.NoContent(c)
}

func (h *handler) requirePublisherEditor(c *gin.Context, publisherName string) (*store.PublisherEntity, bool) {
	account := authn.CurrentUser(c)
	if account == nil {
		httpx.Unauthorized(c)
		return nil, false
	}
	accountID, _ := uuid.Parse(account.GetId())
	publisher, err := h.publisherByName(c.Request.Context(), publisherName)
	if err != nil {
		httpx.NotFound(c, "Publisher not found.")
		return nil, false
	}
	if !h.isMemberWithRole(c.Request.Context(), publisher.ID, accountID, int(model.PublisherRoleEditor)) {
		httpx.Forbidden(c, "You need at least be an editor to manage this publisher collections.")
		return nil, false
	}
	return publisher, true
}

// ---------------------------------------------------------------------------
// Collection subscriptions
// ---------------------------------------------------------------------------

func (h *handler) subscribeCollection(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := h.viewerAccountID(c)
	if accountID == uuid.Nil {
		httpx.Unauthorized(c)
		return
	}
	collection, err := h.getCollectionBySlug(ctx, c.Param("name"), c.Param("slug"))
	if err != nil {
		httpx.Fail(c, errs.New("COLLECTION_NOT_FOUND", "Collection not found.", 404))
		return
	}
	var subscription store.PostCategorySubscriptionEntity
	if err := h.db.WithContext(ctx).Where("collection_id = ? AND account_id = ?", collection.ID, accountID).
		First(&subscription).Error; err == nil {
		httpx.OK(c, postCategorySubscriptionToModel(&subscription))
		return
	}
	subscription = store.PostCategorySubscriptionEntity{
		ID:           uuid.New(),
		AccountID:    accountID,
		CollectionID: &collection.ID,
	}
	if err := h.db.WithContext(ctx).Create(&subscription).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.Created(c, postCategorySubscriptionToModel(&subscription))
}

func (h *handler) unsubscribeCollection(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := h.viewerAccountID(c)
	if accountID == uuid.Nil {
		httpx.Unauthorized(c)
		return
	}
	collection, err := h.getCollectionBySlug(ctx, c.Param("name"), c.Param("slug"))
	if err != nil {
		httpx.Fail(c, errs.New("COLLECTION_NOT_FOUND", "Collection not found.", 404))
		return
	}
	var subscription store.PostCategorySubscriptionEntity
	if err := h.db.WithContext(ctx).Where("collection_id = ? AND account_id = ?", collection.ID, accountID).
		First(&subscription).Error; err != nil {
		httpx.NoContent(c)
		return
	}
	h.db.WithContext(ctx).Delete(&store.PostCategorySubscriptionEntity{}, "id = ?", subscription.ID)
	httpx.NoContent(c)
}

func (h *handler) getCollectionSubscription(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := h.viewerAccountID(c)
	if accountID == uuid.Nil {
		httpx.Unauthorized(c)
		return
	}
	collection, err := h.getCollectionBySlug(ctx, c.Param("name"), c.Param("slug"))
	if err != nil {
		httpx.Fail(c, errs.New("COLLECTION_NOT_FOUND", "Collection not found.", 404))
		return
	}
	var subscription store.PostCategorySubscriptionEntity
	if err := h.db.WithContext(ctx).Where("collection_id = ? AND account_id = ?", collection.ID, accountID).
		First(&subscription).Error; err != nil {
		httpx.Fail(c, errs.New("COLLECTION_SUBSCRIPTION_NOT_FOUND", "Subscription not found.", 404))
		return
	}
	httpx.OK(c, postCategorySubscriptionToModel(&subscription))
}

// ---------------------------------------------------------------------------
// Collection posts
// ---------------------------------------------------------------------------

func (h *handler) listCollectionPosts(c *gin.Context) {
	ctx := c.Request.Context()
	collection, err := h.getCollectionBySlug(ctx, c.Param("name"), c.Param("slug"))
	if err != nil {
		httpx.NotFound(c, "Collection not found.")
		return
	}
	page, ok := h.pageFrom(c)
	if !ok {
		return
	}
	v := h.newViewer(c)
	info := h.gatekeepInfo(ctx, v, []uuid.UUID{collection.PublisherID})

	countQuery := h.applyVisibility(h.db.WithContext(ctx).Model(&store.PostEntity{}).
		Where("id IN (SELECT post_id FROM post_collection_items WHERE collection_id = ? AND deleted_at IS NULL)", collection.ID),
		v, true, info, false)
	var total int64
	if err := countQuery.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	query := h.applyVisibility(h.db.WithContext(ctx).Model(&store.PostEntity{}).
		Where("id IN (SELECT post_id FROM post_collection_items WHERE collection_id = ? AND deleted_at IS NULL)", collection.ID),
		v, true, info, false)
	var rows []store.PostEntity
	if err := query.Order("COALESCE(published_at, created_at) DESC").Order("created_at DESC").Order("id DESC").
		Offset(page.Offset).Limit(page.Take).Find(&rows).Error; err != nil {
		httpx.Internal(c)
		return
	}
	posts, err := h.hydrate(ctx, rows, v, true, true)
	if err != nil {
		httpx.Internal(c)
		return
	}
	posts = h.filterVisible(ctx, posts, v, info, true)
	httpx.List(c, posts, total)
}

func (h *handler) addPostToCollection(ctx context.Context, collection *store.PostCollectionEntity, postID uuid.UUID, order *int) error {
	var count int64
	if err := h.db.WithContext(ctx).Model(&store.PostEntity{}).Where("id = ?", postID).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("Post not found.")
	}
	var existing store.PostCollectionItemEntity
	if err := h.db.WithContext(ctx).
		Where("collection_id = ? AND post_id = ?", collection.ID, postID).
		First(&existing).Error; err == nil {
		return fmt.Errorf("Post already exists in this collection.")
	}
	var maxOrder *int
	var row struct {
		Max *int `gorm:"column:max_order"`
	}
	h.db.WithContext(ctx).Model(&store.PostCollectionItemEntity{}).
		Select("max(\"order\") AS max_order").
		Where("collection_id = ?", collection.ID).Scan(&row)
	if row.Max != nil {
		maxOrder = row.Max
	}
	target := 0
	if maxOrder != nil {
		target = *maxOrder + 1
	}
	if order != nil {
		target = *order
		if err := h.db.WithContext(ctx).Model(&store.PostCollectionItemEntity{}).
			Where("collection_id = ? AND \"order\" >= ?", collection.ID, target).
			UpdateColumn("order", gorm.Expr("\"order\" + 1")).Error; err != nil {
			return err
		}
	}
	item := store.PostCollectionItemEntity{
		ID:           uuid.New(),
		CollectionID: collection.ID,
		PostID:       postID,
		Order:        target,
	}
	return h.db.WithContext(ctx).Create(&item).Error
}

func (h *handler) addCollectionPost(c *gin.Context) {
	ctx := c.Request.Context()
	collection, err := h.getCollectionBySlug(ctx, c.Param("name"), c.Param("slug"))
	if err != nil {
		httpx.NotFound(c, "Collection not found.")
		return
	}
	if _, ok := h.requirePublisherEditor(c, c.Param("name")); !ok {
		return
	}
	var request addCollectionPostRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	if request.Order != nil {
		httpx.BadRequest(c, "COLLECTION_ORDER_NOT_SUPPORTED", "Manual ordering is not supported. Collection posts are sorted automatically by published date descending.")
		return
	}
	if err := h.addPostToCollection(ctx, collection, request.PostID, nil); err != nil {
		httpx.BadRequest(c, "COLLECTION_ADD_POST_FAILED", err.Error())
		return
	}
	httpx.NoContent(c)
}

func (h *handler) batchAddCollectionPosts(c *gin.Context) {
	ctx := c.Request.Context()
	collection, err := h.getCollectionBySlug(ctx, c.Param("name"), c.Param("slug"))
	if err != nil {
		httpx.NotFound(c, "Collection not found.")
		return
	}
	if _, ok := h.requirePublisherEditor(c, c.Param("name")); !ok {
		return
	}
	var request batchCollectionPostsRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	var existing []uuid.UUID
	h.db.WithContext(ctx).Model(&store.PostCollectionItemEntity{}).
		Where("collection_id = ? AND post_id IN ?", collection.ID, request.PostIDs).
		Pluck("post_id", &existing)
	existingSet := map[uuid.UUID]struct{}{}
	for _, id := range existing {
		existingSet[id] = struct{}{}
	}
	var newIDs []uuid.UUID
	for _, id := range request.PostIDs {
		if _, ok := existingSet[id]; !ok {
			newIDs = append(newIDs, id)
		}
	}
	if len(newIDs) == 0 {
		httpx.NoContent(c)
		return
	}
	var found []uuid.UUID
	h.db.WithContext(ctx).Model(&store.PostEntity{}).Where("id IN ?", newIDs).Pluck("id", &found)
	foundSet := map[uuid.UUID]struct{}{}
	for _, id := range found {
		foundSet[id] = struct{}{}
	}
	var missing []string
	for _, id := range newIDs {
		if _, ok := foundSet[id]; !ok {
			missing = append(missing, id.String())
		}
	}
	if len(missing) > 0 {
		httpx.BadRequest(c, "COLLECTION_BATCH_ADD_FAILED", "Posts not found: "+strings.Join(missing, ", "))
		return
	}
	var maxRow struct {
		Max *int `gorm:"column:max_order"`
	}
	h.db.WithContext(ctx).Model(&store.PostCollectionItemEntity{}).
		Select("max(\"order\") AS max_order").Where("collection_id = ?", collection.ID).Scan(&maxRow)
	base := 0
	if maxRow.Max != nil {
		base = *maxRow.Max + 1
	}
	for index, id := range newIDs {
		item := store.PostCollectionItemEntity{
			ID:           uuid.New(),
			CollectionID: collection.ID,
			PostID:       id,
			Order:        base + index,
		}
		if err := h.db.WithContext(ctx).Create(&item).Error; err != nil {
			httpx.Internal(c)
			return
		}
	}
	httpx.NoContent(c)
}

func (h *handler) removeCollectionPost(c *gin.Context) {
	ctx := c.Request.Context()
	collection, err := h.getCollectionBySlug(ctx, c.Param("name"), c.Param("slug"))
	if err != nil {
		httpx.NotFound(c, "Collection not found.")
		return
	}
	if _, ok := h.requirePublisherEditor(c, c.Param("name")); !ok {
		return
	}
	postID, ok := parseUUID(c.Param("postId"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	h.removePostFromCollection(ctx, collection, postID)
	httpx.NoContent(c)
}

func (h *handler) batchRemoveCollectionPosts(c *gin.Context) {
	ctx := c.Request.Context()
	collection, err := h.getCollectionBySlug(ctx, c.Param("name"), c.Param("slug"))
	if err != nil {
		httpx.NotFound(c, "Collection not found.")
		return
	}
	if _, ok := h.requirePublisherEditor(c, c.Param("name")); !ok {
		return
	}
	var request batchCollectionPostsRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	for _, postID := range request.PostIDs {
		h.removePostFromCollection(ctx, collection, postID)
	}
	httpx.NoContent(c)
}

func (h *handler) removePostFromCollection(ctx context.Context, collection *store.PostCollectionEntity, postID uuid.UUID) {
	var item store.PostCollectionItemEntity
	if err := h.db.WithContext(ctx).Where("collection_id = ? AND post_id = ?", collection.ID, postID).
		First(&item).Error; err != nil {
		return
	}
	removedOrder := item.Order
	h.db.WithContext(ctx).Delete(&store.PostCollectionItemEntity{}, "id = ?", item.ID)
	h.db.WithContext(ctx).Model(&store.PostCollectionItemEntity{}).
		Where("collection_id = ? AND \"order\" > ?", collection.ID, removedOrder).
		UpdateColumn("order", gorm.Expr("\"order\" - 1"))
}

func (h *handler) reorderCollectionPosts(c *gin.Context) {
	collection, err := h.getCollectionBySlug(c.Request.Context(), c.Param("name"), c.Param("slug"))
	if err != nil {
		httpx.NotFound(c, "Collection not found.")
		return
	}
	if _, ok := h.requirePublisherEditor(c, c.Param("name")); !ok {
		return
	}
	httpx.BadRequest(c, "COLLECTION_REORDER_NOT_SUPPORTED", "Manual reordering is not supported. Collection posts are sorted automatically by published date descending.")
	_ = collection
}

func (h *handler) prevCollectionPost(c *gin.Context) { h.adjacentCollectionPost(c, false) }
func (h *handler) nextCollectionPost(c *gin.Context) { h.adjacentCollectionPost(c, true) }

func (h *handler) adjacentCollectionPost(c *gin.Context, next bool) {
	ctx := c.Request.Context()
	collection, err := h.getCollectionBySlug(ctx, c.Param("name"), c.Param("slug"))
	if err != nil {
		httpx.NotFound(c, "Collection not found.")
		return
	}
	postID, ok := parseUUID(c.Param("postId"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	v := h.newViewer(c)
	info := h.gatekeepInfo(ctx, v, []uuid.UUID{collection.PublisherID})

	type itemRow struct {
		PostID      uuid.UUID  `gorm:"column:post_id"`
		PublishedAt *time.Time `gorm:"column:published_at"`
		CreatedAt   time.Time  `gorm:"column:created_at"`
	}
	var items []itemRow
	if err := h.db.WithContext(ctx).Table("post_collection_items i").
		Select("i.post_id, p.published_at, p.created_at").
		Joins("JOIN posts p ON p.id = i.post_id AND p.deleted_at IS NULL").
		Where("i.collection_id = ? AND i.deleted_at IS NULL", collection.ID).
		Scan(&items).Error; err != nil {
		httpx.Internal(c)
		return
	}
	allIDs := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		allIDs = append(allIDs, item.PostID)
	}
	var visibleIDs []uuid.UUID
	if len(allIDs) > 0 {
		h.applyVisibility(h.db.WithContext(ctx).Model(&store.PostEntity{}).
			Where("id IN ?", allIDs), v, true, info, false).Pluck("id", &visibleIDs)
	}
	visible := map[uuid.UUID]struct{}{}
	for _, id := range visibleIDs {
		visible[id] = struct{}{}
	}
	orderedItems := make([]collectionOrderItem, 0, len(items))
	for _, item := range items {
		if _, ok := visible[item.PostID]; !ok {
			continue
		}
		orderedItems = append(orderedItems, collectionOrderItem{id: item.PostID, published: item.PublishedAt, created: item.CreatedAt})
	}
	sortCollectionItems(orderedItems)
	index := -1
	for i, item := range orderedItems {
		if item.id == postID {
			index = i
			break
		}
	}
	if index < 0 {
		httpx.NotFound(c, "Collection post not found.")
		return
	}
	target := index - 1
	if next {
		target = index + 1
	}
	if target < 0 || target >= len(orderedItems) {
		message := "No previous post found."
		if next {
			message = "No next post found."
		}
		httpx.Fail(c, errs.New("COLLECTION_POST_NOT_FOUND", message, 404))
		return
	}
	var ent store.PostEntity
	if err := h.db.WithContext(ctx).Where("id = ?", orderedItems[target].id).First(&ent).Error; err != nil {
		httpx.Fail(c, errs.New("COLLECTION_POST_NOT_FOUND", "Collection post not found.", 404))
		return
	}
	post, err := h.hydrateOne(ctx, &ent, v, true, false)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, post)
}

func sortCollectionItems(items []collectionOrderItem) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0; j-- {
			left, right := items[j-1], items[j]
			if collectionItemBefore(left.published, left.created, left.id, right.published, right.created, right.id) {
				break
			}
			items[j-1], items[j] = items[j], items[j-1]
		}
	}
}

func collectionItemBefore(ap *time.Time, ac time.Time, aid uuid.UUID, bp *time.Time, bc time.Time, bid uuid.UUID) bool {
	a, b := ac, bc
	if ap != nil {
		a = *ap
	}
	if bp != nil {
		b = *bp
	}
	if !a.Equal(b) {
		return a.After(b)
	}
	if !ac.Equal(bc) {
		return ac.After(bc)
	}
	return aid.String() > bid.String()
}

// ---------------------------------------------------------------------------
// Admin collections
// ---------------------------------------------------------------------------

func (h *handler) adminListCollections(c *gin.Context) {
	ctx := c.Request.Context()
	offset, take := paginate(c, 50, 200)
	query := h.db.WithContext(ctx).Model(&store.PostCollectionEntity{})
	if publisherID := c.Query("publisherId"); publisherID != "" {
		if id, ok := parseUUID(publisherID); ok {
			query = query.Where("publisher_id = ?", id)
		}
	}
	if search := strings.TrimSpace(c.Query("query")); search != "" {
		pattern := "%" + search + "%"
		query = query.Where("(slug ILIKE ? OR name ILIKE ? OR description ILIKE ? OR publisher_id IN (SELECT id FROM publishers WHERE name ILIKE ? OR nick ILIKE ?))",
			pattern, pattern, pattern, pattern, pattern)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	var rows []store.PostCollectionEntity
	if err := query.Order("updated_at DESC").Offset(offset).Limit(take).Find(&rows).Error; err != nil {
		httpx.Internal(c)
		return
	}
	counts := map[uuid.UUID]int{}
	if len(rows) > 0 {
		ids := make([]uuid.UUID, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		var countRows []struct {
			CollectionID uuid.UUID `gorm:"column:collection_id"`
			Count        int       `gorm:"column:count"`
		}
		h.db.WithContext(ctx).Model(&store.PostCollectionItemEntity{}).
			Select("collection_id, count(*) AS count").
			Where("collection_id IN ?", ids).Group("collection_id").Scan(&countRows)
		for _, row := range countRows {
			counts[row.CollectionID] = row.Count
		}
	}
	out := make([]adminCollection, 0, len(rows))
	for i := range rows {
		out = append(out, adminCollection{
			PostCollection: postCollectionToModel(&rows[i]),
			ItemCount:      counts[rows[i].ID],
		})
	}
	httpx.List(c, out, total)
}

func (h *handler) adminGetCollection(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Collection not found.")
		return
	}
	var collection store.PostCollectionEntity
	if err := h.db.WithContext(ctx).Where("id = ?", id).First(&collection).Error; err != nil {
		httpx.NotFound(c, "Collection not found.")
		return
	}
	var count int64
	h.db.WithContext(ctx).Model(&store.PostCollectionItemEntity{}).
		Where("collection_id = ?", collection.ID).Count(&count)
	httpx.OK(c, adminCollection{PostCollection: postCollectionToModel(&collection), ItemCount: int(count)})
}

func (h *handler) adminUpdateCollection(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Collection not found.")
		return
	}
	var request adminUpdateCollectionRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	var collection store.PostCollectionEntity
	if err := h.db.WithContext(ctx).Where("id = ?", id).First(&collection).Error; err != nil {
		httpx.NotFound(c, "Collection not found.")
		return
	}
	if request.Name != nil {
		collection.Name = request.Name
	}
	if request.Description != nil {
		collection.Description = request.Description
	}
	if err := h.db.WithContext(ctx).Save(&collection).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, postCollectionToModel(&collection))
}

func (h *handler) adminDeleteCollection(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Collection not found.")
		return
	}
	var collection store.PostCollectionEntity
	if err := h.db.WithContext(ctx).Where("id = ?", id).First(&collection).Error; err != nil {
		httpx.NotFound(c, "Collection not found.")
		return
	}
	if err := h.db.WithContext(ctx).Delete(&store.PostCollectionEntity{}, "id = ?", collection.ID).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.NoContent(c)
}
