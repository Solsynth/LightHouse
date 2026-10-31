package posts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// postRequest mirrors PostActionController.PostRequest.
type postRequest struct {
	Title           *string          `json:"title"`
	Description     *string          `json:"description"`
	Slug            *string          `json:"slug"`
	Content         *string          `json:"content"`
	Visibility      *int             `json:"visibility"`
	Type            *int             `json:"type"`
	EmbedView       any              `json:"embed_view"`
	Tags            []string         `json:"tags"`
	Categories      []string         `json:"categories"`
	Attachments     []string         `json:"attachments"`
	Meta            map[string]any   `json:"meta"`
	DraftedAt       *model.Time      `json:"drafted_at"`
	PublishedAt     *model.Time      `json:"published_at"`
	RepliedPostID   *uuid.UUID       `json:"replied_post_id"`
	ForwardedPostID *uuid.UUID       `json:"forwarded_post_id"`
	ChainedPostID   *uuid.UUID       `json:"chained_post_id"`
	AutoChain       *bool            `json:"auto_chain"`
	RealmID         *uuid.UUID       `json:"realm_id"`
	SurveyID        *uuid.UUID       `json:"survey_id"`
	FundID          *uuid.UUID       `json:"fund_id"`
	MeetID          *uuid.UUID       `json:"meet_id"`
	LiveStreamID    *uuid.UUID       `json:"live_stream_id"`
	NotableDayID    *uuid.UUID       `json:"notable_day_id"`
	CalendarEventID *uuid.UUID       `json:"calendar_event_id"`
	LocationName    *string          `json:"location_name"`
	LocationAddress *string          `json:"location_address"`
	LocationWkt     *string          `json:"location_wkt"`
	ThumbnailID     *string          `json:"thumbnail_id"`
	Embeds          []map[string]any `json:"embeds"`
	Language        *string          `json:"language"`
	CollectionIDs   []uuid.UUID      `json:"collection_ids"`
}

type blogPermissionRequest struct {
	URL string `json:"url"`
}

type batchDeleteRequest struct {
	PostIDs []uuid.UUID `json:"post_ids"`
}

type batchVisibilityRequest struct {
	PostIDs     []uuid.UUID `json:"post_ids"`
	Visibility  *int        `json:"visibility"`
	DraftedAt   *model.Time `json:"drafted_at"`
	PublishedAt *model.Time `json:"published_at"`
}

type batchVisibilityResponse struct {
	Visibility  *int        `json:"visibility"`
	DraftedAt   *model.Time `json:"drafted_at"`
	PublishedAt *model.Time `json:"published_at"`
}

func (h *handler) registerPostWriteRoutes(host *module.Host[core.Services]) {
	api := host.API
	auth := host.Core.Auth
	api.POST("/posts/blog/check-permission", authn.RequireAuth(), h.checkBlogPermission)
	api.POST("/posts", host.Core.Auth.RequirePermission("posts.create"), h.createPost)
	api.PATCH("/posts/:id", h.updatePost)
	api.DELETE("/posts/:id", h.deletePost)
	api.POST("/posts/batch/delete", authn.RequireAuth(), auth.RequirePermission("posts.batch.delete"), h.batchDeletePosts)
	api.POST("/posts/batch/visibility", authn.RequireAuth(), auth.RequirePermission("posts.batch.delete"), h.batchVisibilityPosts)
	api.POST("/posts/:id/publish", authn.RequireAuth(), auth.RequirePermission("posts.create"), h.publishDraft)
	api.POST("/posts/:id/reactions", authn.RequireAuth(), auth.RequirePermission("posts.react"), h.reactPost)
	api.POST("/posts/:id/bookmark", authn.RequireAuth(), auth.RequirePermission("posts.bookmark"), h.bookmarkPost)
	api.DELETE("/posts/:id/bookmark", authn.RequireAuth(), auth.RequirePermission("posts.bookmark"), h.unbookmarkPost)
	api.GET("/posts/:id/bookmark", authn.RequireAuth(), h.getBookmarkStatus)
	api.GET("/posts/:id/awards", h.getPostAwards)
	api.GET("/posts/:id/awards/pending", h.getPendingAwards)
	api.POST("/posts/:id/awards", authn.RequireAuth(), auth.RequirePermission("posts.award"), h.awardPost)
	api.POST("/posts/:id/sponsor", authn.RequireAuth(), auth.RequirePermission("posts.sponsor"), h.sponsorPost)
	api.POST("/posts/:id/pin", authn.RequireAuth(), auth.RequirePermission("posts.pin"), h.pinPost)
	api.DELETE("/posts/:id/pin", authn.RequireAuth(), auth.RequirePermission("posts.pin"), h.unpinPost)
	api.POST("/posts/:id/boost", authn.RequireAuth(), auth.RequirePermission("posts.boost"), h.boostPost)
	api.DELETE("/posts/:id/boost", authn.RequireAuth(), auth.RequirePermission("posts.boost"), h.unboostPost)
	api.GET("/posts/:id/boosts", h.listBoosts)
	api.POST("/posts/:id/realm/moderate", authn.RequireAuth(), auth.RequirePermission("posts.moderate"), h.moderatePostInRealm)
}

// ---------------------------------------------------------------------------
// Publisher resolution and permissions
// ---------------------------------------------------------------------------

func (h *handler) resolvePublisherForRequest(ctx context.Context, accountID uuid.UUID, pubName string, isReply bool) *store.PublisherEntity {
	if strings.TrimSpace(pubName) != "" {
		publisher, err := h.publisherByName(ctx, pubName)
		if err != nil {
			return nil
		}
		return publisher
	}
	var settings store.PublishingSettingsEntity
	if err := h.db.WithContext(ctx).Where("account_id = ?", accountID).First(&settings).Error; err == nil {
		var defaultID *uuid.UUID
		if isReply {
			defaultID = settings.DefaultReplyPublisherID
		} else {
			defaultID = settings.DefaultPostingPublisherID
		}
		if defaultID != nil {
			publisher, err := h.publisherByID(ctx, *defaultID)
			if err == nil && h.isMemberWithRole(ctx, publisher.ID, accountID, int(model.PublisherRoleEditor)) {
				return publisher
			}
		}
	}
	var publisher store.PublisherEntity
	err := h.db.WithContext(ctx).
		Where("account_id = ? AND type = ?", accountID, int(model.PublisherIndividual)).
		First(&publisher).Error
	if err != nil {
		return nil
	}
	return &publisher
}

func (h *handler) permissionGranted(ctx context.Context, account *gen.DyAccount, key string) (bool, error) {
	if account == nil {
		return false, nil
	}
	if account.GetIsSuperuser() {
		return true, nil
	}
	if h.core.Clients == nil || h.core.Clients.Permission == nil {
		return false, ports.ErrUnavailable
	}
	resp, err := h.core.Clients.Permission.HasPermission(ctx, &gen.DyHasPermissionRequest{
		Actor: account.GetId(),
		Key:   key,
	})
	if err != nil {
		return false, err
	}
	return resp.GetHasPermission(), nil
}

func (h *handler) domainVerified(ctx context.Context, publisherID uuid.UUID, domain string) bool {
	var count int64
	h.db.WithContext(ctx).Model(&store.PublisherVerifiedDomainEntity{}).
		Where("publisher_id = ? AND lower(domain) = ? AND status = ?", publisherID, strings.ToLower(domain), 1).
		Count(&count)
	return count > 0
}

func (h *handler) checkBlogPermission(c *gin.Context) {
	ctx := c.Request.Context()
	account := authn.CurrentUser(c)
	if account == nil {
		httpx.Unauthorized(c)
		return
	}
	accountID, err := uuid.Parse(account.GetId())
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	var request blogPermissionRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	rawURL := strings.TrimSpace(request.URL)
	if rawURL == "" {
		httpx.BadRequest(c, "POST_URL_REQUIRED", "URL is required.")
		return
	}
	publisher := h.resolvePublisherForRequest(ctx, accountID, c.Query("pub"), false)
	if publisher == nil {
		httpx.BadRequest(c, "PUBLISHER_NOT_FOUND", "Publisher was not found.")
		return
	}
	if c.Query("pub") != "" && !h.isMemberWithRole(ctx, publisher.ID, accountID, int(model.PublisherRoleEditor)) {
		httpx.Forbidden(c, "You need at least be an editor to post as this publisher.")
		return
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		httpx.BadRequest(c, "POST_BLOG_PERMISSION_CHECK_FAILED", "Blog post content must be a valid URL.")
		return
	}
	granted, err := h.permissionGranted(ctx, account, "posts.create.blog")
	if err != nil {
		httpx.Fail(c, errs.New("POST_BLOG_PERMISSION_CHECK_FAILED", "Unable to check blog permission.", http.StatusBadRequest))
		return
	}
	host := strings.ToLower(parsed.Hostname())
	verified := h.domainVerified(ctx, publisher.ID, host)
	httpx.OK(c, gin.H{
		"can_create":              granted && verified,
		"permission_node_granted": granted,
		"permission_granted":      granted,
		"domain_verified":         verified,
		"domain":                  host,
		"publisher_id":            publisher.ID.String(),
		"publisher_name":          publisher.Name,
	})
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func (h *handler) createPost(c *gin.Context) {
	ctx := c.Request.Context()
	account := authn.CurrentUser(c)
	if account == nil {
		httpx.Unauthorized(c)
		return
	}
	var request postRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	request.Content = sanitizeText(request.Content)
	request.Title = normalizeOptionalText(request.Title)
	request.Description = normalizeOptionalText(request.Description)

	postType := int(model.PostTypeMoment)
	if request.Type != nil {
		postType = *request.Type
	}
	if postType != int(model.PostTypeBlog) {
		if strings.TrimSpace(stringValue(request.Content)) == "" && len(request.Attachments) == 0 && request.ForwardedPostID == nil {
			httpx.BadRequest(c, "POST_CONTENT_REQUIRED", "Content is required.")
			return
		}
	}
	if !h.validatePostShape(c, &request, postType) {
		return
	}

	accountID, err := uuid.Parse(account.GetId())
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	isReply := request.RepliedPostID != nil
	publisher := h.resolvePublisherForRequest(ctx, accountID, c.Query("pub"), isReply)
	if publisher == nil {
		httpx.BadRequest(c, "PUBLISHER_NOT_FOUND", "Publisher was not found.")
		return
	}
	if c.Query("pub") != "" && !h.isMemberWithRole(ctx, publisher.ID, accountID, int(model.PublisherRoleEditor)) {
		httpx.Forbidden(c, "You need at least be an editor to post as this publisher.")
		return
	}
	if request.RealmID != nil {
		if status, message := h.validatePostRealm(ctx, *request.RealmID, accountID); status != 0 {
			switch status {
			case 403:
				httpx.Forbidden(c, message)
			case 503:
				httpx.Fail(c, errs.New("SERVICE_UNAVAILABLE", "The realm service is unavailable.", 503))
			default:
				httpx.BadRequest(c, message, "The realm does not exist or is unavailable.")
			}
			return
		}
	}

	if postType == int(model.PostTypeBlog) {
		if strings.TrimSpace(stringValue(request.Content)) == "" {
			httpx.BadRequest(c, "POST_BLOG_INVALID_URL", "Blog post content must be a valid URL.")
			return
		}
		parsed, err := url.Parse(*request.Content)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			httpx.BadRequest(c, "POST_BLOG_INVALID_URL", "Blog post content must be a valid URL.")
			return
		}
		granted, err := h.permissionGranted(ctx, account, "posts.create.blog")
		if err != nil {
			httpx.Fail(c, errs.New("POST_BLOG_PERMISSION_CHECK_FAILED", "Unable to check blog permission.", http.StatusBadRequest))
			return
		}
		if !granted {
			httpx.Forbidden(c, "You do not have permission to create blog posts.")
			return
		}
		if !h.domainVerified(ctx, publisher.ID, parsed.Hostname()) {
			httpx.Forbidden(c, "This domain is not verified for your publisher. Add it via the domains endpoint first.")
			return
		}
	}

	now := time.Now().UTC()
	visibility := int(model.VisibilityPublic)
	if request.Visibility != nil {
		visibility = *request.Visibility
	}
	if visibility == int(model.VisibilityCloseFriendsOnly) || visibility == int(model.VisibilityFriends) {
		if publisher == nil {
			httpx.BadRequest(c, "POST_VISIBILITY_REQUIRES_PUBLISHER", "CloseFriendsOnly and Friends visibility require a publisher.")
			return
		}
	}
	if request.DraftedAt != nil && request.PublishedAt != nil {
		httpx.BadRequest(c, "POST_DRAFT_PUBLISH_CONFLICT", "Cannot set both draftedAt and publishedAt.")
		return
	}

	ent := store.PostEntity{
		ID:          uuid.New(),
		Title:       request.Title,
		Description: request.Description,
		Slug:        request.Slug,
		Content:     request.Content,
		Visibility:  visibility,
		ContentType: int(model.ContentTypeMarkdown),
		Type:        postType,
		Language:    request.Language,
		PublisherID: publisher.ID,
		Metadata:    store.ToJSON(request.Meta),
		EmbedView:   store.ToJSON(request.EmbedView),
		Mentions:    store.ToJSON([]model.ContentMention{}),
		Attachments: store.ToJSON([]model.CloudFileReferenceObject{}),
	}

	metadata := map[string]any{}
	if request.Meta != nil {
		for key, value := range request.Meta {
			metadata[key] = value
		}
	}

	if request.RepliedPostID != nil {
		var target store.PostEntity
		if err := h.db.WithContext(ctx).Where("id = ?", *request.RepliedPostID).First(&target).Error; err != nil {
			httpx.BadRequest(c, "POST_REPLY_TARGET_NOT_FOUND", "Post replying to was not found.")
			return
		}
		ent.RepliedPostID = &target.ID
	}
	if request.ForwardedPostID != nil {
		var target store.PostEntity
		if err := h.db.WithContext(ctx).Where("id = ?", *request.ForwardedPostID).First(&target).Error; err != nil {
			httpx.BadRequest(c, "POST_FORWARD_TARGET_NOT_FOUND", "Forwarded post was not found.")
			return
		}
		ent.ForwardedPostID = &target.ID
	}
	if request.ChainedPostID != nil && (request.RepliedPostID != nil || request.ForwardedPostID != nil) {
		httpx.BadRequest(c, "POST_CHAIN_CONFLICT", "A chained post cannot also be a reply or a forward.")
		return
	}
	if request.ChainedPostID != nil {
		var target store.PostEntity
		if err := h.db.WithContext(ctx).Where("id = ?", *request.ChainedPostID).First(&target).Error; err != nil {
			httpx.BadRequest(c, "POST_CHAIN_TARGET_NOT_FOUND", "Post to chain to was not found.")
			return
		}
		if target.PublisherID != publisher.ID {
			httpx.BadRequest(c, "POST_CHAIN_DIFFERENT_PUBLISHER", "You can only chain posts to your own posts.")
			return
		}
		headID := target.ID
		if target.ChainedPostID != nil {
			headID = *target.ChainedPostID
		}
		_ = headID
		ent.ChainedPostID = &headID
	}
	if request.RealmID != nil {
		ent.RealmID = request.RealmID
	}

	h.applyEmbeds(ctx, metadata, &request)
	ent.Metadata = store.ToJSON(metadata)

	if request.DraftedAt == nil && request.PublishedAt != nil && request.PublishedAt.Time().Before(now) {
		httpx.BadRequest(c, "POST_CREATE_FAILED", "Cannot create the post which published in the past.")
		return
	}
	applyPublicationTimes(&ent, request.DraftedAt, request.PublishedAt, now, true)

	if err := h.resolveAttachments(ctx, &ent, request.Attachments); err != nil {
		httpx.Fail(c, errs.New("POST_CREATE_FAILED", "Unable to resolve attachments.", http.StatusBadRequest))
		return
	}

	_, tagSlugs, err := h.resolveTags(ctx, request.Tags, publisher)
	if err != nil {
		httpx.BadRequest(c, "POST_CREATE_FAILED", err.Error())
		return
	}
	_, categorySlugs, err := h.resolveCategories(ctx, request.Categories)
	if err != nil {
		httpx.BadRequest(c, "POST_CREATE_FAILED", err.Error())
		return
	}

	isPublishedNow := ent.DraftedAt == nil && ent.PublishedAt != nil && !ent.PublishedAt.After(now)
	if isPublishedNow {
		ent.PublishNotifiedAt = &now
	}

	if err := h.db.WithContext(ctx).Create(&ent).Error; err != nil {
		httpx.Internal(c)
		return
	}
	h.linkTopics(ctx, ent.ID, tagSlugs, categorySlugs)

	autoChain := true
	if request.AutoChain != nil {
		autoChain = *request.AutoChain
	}
	if isPublishedNow && autoChain && ent.ChainedPostID == nil && ent.RepliedPostID == nil &&
		ent.ForwardedPostID == nil && ent.Type == int(model.PostTypeMoment) && ent.PublisherID != uuid.Nil {
		h.applyAutoChain(ctx, &ent)
	}

	if ent.RepliedPostID != nil {
		h.enqueueInterest(accountID, *ent.RepliedPostID, 1.5, "reply")
	}

	if len(request.CollectionIDs) > 0 {
		var collections []store.PostCollectionEntity
		h.db.WithContext(ctx).Where("publisher_id = ? AND id IN ?", publisher.ID, request.CollectionIDs).Find(&collections)
		for _, collection := range collections {
			if err := h.addPostToCollection(ctx, &collection, ent.ID, nil); err != nil {
				h.log.Debug("posts: add to collection failed", "collection", collection.ID, "error", err)
			}
		}
	}
	if err := h.writePostIndex(ctx, &ent); err != nil {
		h.log.Warn("posts: index created post failed", "post", ent.ID, "error", err)
	}

	post, err := h.hydrateOne(ctx, &ent, h.newViewer(c), false, false)
	if err != nil {
		httpx.Internal(c)
		return
	}
	h.afterPostCreated(ctx, &ent, &post, accountID)
	httpx.OK(c, post)
}

func (h *handler) validatePostShape(c *gin.Context, request *postRequest, postType int) bool {
	if request.Title != nil && len(*request.Title) > maxTitleLength {
		httpx.BadRequest(c, "POST_TITLE_TOO_LONG", "Title is too long.")
		return false
	}
	if request.Description != nil && len(*request.Description) > maxDescriptionLength {
		httpx.BadRequest(c, "POST_DESCRIPTION_TOO_LONG", "Description is too long.")
		return false
	}
	if request.Slug != nil && len(*request.Slug) > maxSlugLength {
		httpx.BadRequest(c, "POST_SLUG_TOO_LONG", "Slug is too long.")
		return false
	}
	if len(request.Tags) > maxTags {
		httpx.BadRequest(c, "POST_TAGS_TOO_MANY", "Too many tags.")
		return false
	}
	if len(request.Categories) > maxCategories {
		httpx.BadRequest(c, "POST_CATEGORIES_TOO_MANY", "Too many categories.")
		return false
	}
	if len(request.Attachments) > maxAttachments {
		httpx.BadRequest(c, "POST_ATTACHMENTS_TOO_MANY", "Too many attachments.")
		return false
	}
	if request.Language != nil && len(*request.Language) > maxLanguageLength {
		httpx.BadRequest(c, "POST_LANGUAGE_TOO_LONG", "Language is too long.")
		return false
	}
	if len(request.CollectionIDs) > maxCollectionIDs {
		httpx.BadRequest(c, "POST_COLLECTIONS_TOO_MANY", "Too many collections.")
		return false
	}
	if request.ThumbnailID != nil && strings.TrimSpace(*request.ThumbnailID) != "" {
		if postType != int(model.PostTypeArticle) {
			httpx.BadRequest(c, "POST_THUMBNAIL_NOT_SUPPORTED", "Thumbnail only supported in article.")
			return false
		}
		found := false
		for _, id := range request.Attachments {
			if id == *request.ThumbnailID {
				found = true
				break
			}
		}
		if !found {
			httpx.BadRequest(c, "POST_THUMBNAIL_NOT_IN_ATTACHMENTS", "Thumbnail must be presented in attachment list.")
			return false
		}
	}
	if request.LocationWkt != nil && strings.TrimSpace(*request.LocationWkt) != "" {
		if !strings.Contains(*request.LocationWkt, "(") {
			httpx.BadRequest(c, "POST_INVALID_LOCATION", "Invalid location WKT.")
			return false
		}
	}
	return true
}

// applyPublicationTimes implements the create/update publishedAt/draftedAt
// semantics.
func applyPublicationTimes(ent *store.PostEntity, draftedAt, publishedAt *model.Time, now time.Time, create bool) {
	if draftedAt != nil {
		drafted := draftedAt.Time()
		ent.DraftedAt = &drafted
		ent.PublishedAt = nil
		return
	}
	if publishedAt != nil {
		published := publishedAt.Time()
		ent.DraftedAt = nil
		ent.PublishedAt = &published
		return
	}
	if create {
		ent.PublishedAt = &now
	}
}

func (h *handler) resolveAttachments(ctx context.Context, ent *store.PostEntity, ids []string) error {
	if len(ids) == 0 {
		ent.Attachments = store.ToJSON([]model.CloudFileReferenceObject{})
		return nil
	}
	if h.ports == nil {
		return nil
	}
	files, ok := module.Get[ports.Files](h.ports, ports.PortFiles)
	if !ok {
		return nil
	}
	resolved, err := files.GetFiles(ctx, ids)
	if err != nil {
		return err
	}
	ordered := make([]model.CloudFileReferenceObject, 0, len(ids))
	for _, id := range ids {
		if file, ok := resolved[id]; ok {
			ordered = append(ordered, file)
		}
	}
	ent.Attachments = store.ToJSON(ordered)
	return nil
}

func normalizeSlugs(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		slug := strings.ToLower(strings.TrimSpace(value))
		if slug == "" {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		out = append(out, slug)
	}
	return out
}

func (h *handler) resolveTags(ctx context.Context, slugs []string, publisher *store.PublisherEntity) ([]store.PostTagEntity, []string, error) {
	normalized := normalizeSlugs(slugs)
	if len(normalized) == 0 {
		return nil, nil, nil
	}
	var existing []store.PostTagEntity
	if err := h.db.WithContext(ctx).Where("lower(slug) IN ?", normalized).Find(&existing).Error; err != nil {
		return nil, nil, err
	}
	known := map[string]struct{}{}
	var tags []store.PostTagEntity
	for _, tag := range existing {
		known[strings.ToLower(tag.Slug)] = struct{}{}
		tags = append(tags, tag)
	}
	now := time.Now().UTC()
	for _, slug := range normalized {
		if _, ok := known[slug]; ok {
			continue
		}
		tag := store.PostTagEntity{
			ID:        uuid.New(),
			Slug:      slug,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := h.db.WithContext(ctx).Create(&tag).Error; err != nil {
			return nil, nil, err
		}
		tags = append(tags, tag)
	}
	for i := range tags {
		if tags[i].IsEvent && tags[i].EventEndsAt != nil && !tags[i].EventEndsAt.After(now) {
			return nil, nil, fmt.Errorf("Tag '%s' is an event tag that has expired.", tags[i].Slug)
		}
		if tags[i].IsProtected && tags[i].OwnerPublisherID != nil && publisher != nil && *tags[i].OwnerPublisherID != publisher.ID {
			return nil, nil, fmt.Errorf("Tag '%s' is protected and can only be used by its owner.", tags[i].Slug)
		}
	}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		out = append(out, tag.Slug)
	}
	return tags, out, nil
}

func (h *handler) resolveCategories(ctx context.Context, slugs []string) ([]store.PostCategoryEntity, []string, error) {
	normalized := normalizeSlugs(slugs)
	if len(normalized) == 0 {
		return nil, nil, nil
	}
	var existing []store.PostCategoryEntity
	if err := h.db.WithContext(ctx).Where("lower(slug) IN ?", normalized).Find(&existing).Error; err != nil {
		return nil, nil, err
	}
	if len(existing) != len(normalized) {
		return nil, nil, fmt.Errorf("Categories contains one or more categories that wasn't exists.")
	}
	out := make([]string, 0, len(existing))
	for _, category := range existing {
		out = append(out, category.Slug)
	}
	return existing, out, nil
}

func (h *handler) linkTopics(ctx context.Context, postID uuid.UUID, tagSlugs, categorySlugs []string) {
	if len(tagSlugs) > 0 {
		var tags []store.PostTagEntity
		h.db.WithContext(ctx).Where("lower(slug) IN ?", tagSlugs).Find(&tags)
		for _, tag := range tags {
			link := store.PostTagLink{PostID: postID, TagID: tag.ID}
			h.db.WithContext(ctx).Create(&link)
		}
	}
	if len(categorySlugs) > 0 {
		var categories []store.PostCategoryEntity
		h.db.WithContext(ctx).Where("lower(slug) IN ?", categorySlugs).Find(&categories)
		for _, category := range categories {
			link := store.PostCategoryLink{PostID: postID, CategoryID: category.ID}
			h.db.WithContext(ctx).Create(&link)
		}
	}
}

func (h *handler) applyEmbeds(ctx context.Context, metadata map[string]any, request *postRequest) {
	if len(request.Embeds) > 0 {
		metadata["embeds"] = request.Embeds
	} else {
		embeds := embedList(metadata)
		filtered := embeds[:0]
		for _, embed := range embeds {
			kind, _ := embed["type"].(string)
			if kind == "survey" || kind == "poll" {
				continue
			}
			filtered = append(filtered, embed)
		}
		metadata["embeds"] = filtered
	}
	embeds := embedList(metadata)

	replaceEmbed := func(kind string, id uuid.UUID) {
		filtered := embeds[:0]
		for _, embed := range embeds {
			if existing, _ := embed["type"].(string); existing == kind {
				continue
			}
			filtered = append(filtered, embed)
		}
		embeds = append(filtered, map[string]any{"type": kind, "id": id.String()})
	}

	if request.SurveyID != nil {
		replaceEmbed("survey", *request.SurveyID)
	}
	if request.FundID != nil {
		replaceEmbed("fund", *request.FundID)
	}
	if request.MeetID != nil {
		replaceEmbed("meet", *request.MeetID)
	}
	if request.LiveStreamID != nil {
		replaceEmbed("livestream", *request.LiveStreamID)
	}
	if request.NotableDayID != nil {
		replaceEmbed("notable_day", *request.NotableDayID)
	}
	if request.CalendarEventID != nil {
		replaceEmbed("calendar_event", *request.CalendarEventID)
	}
	if hasLocationPayload(request) {
		filtered := embeds[:0]
		for _, embed := range embeds {
			if kind, _ := embed["type"].(string); kind == "location" {
				continue
			}
			filtered = append(filtered, embed)
		}
		embeds = filtered
		location := map[string]any{"type": "location"}
		if request.LocationName != nil {
			location["name"] = *request.LocationName
		}
		if request.LocationAddress != nil {
			location["address"] = *request.LocationAddress
		}
		if request.LocationWkt != nil {
			location["wkt"] = *request.LocationWkt
		}
		embeds = append(embeds, location)
	}
	metadata["embeds"] = embeds

	if request.ThumbnailID != nil {
		metadata["thumbnail"] = *request.ThumbnailID
	}
}

func embedList(metadata map[string]any) []map[string]any {
	raw, ok := metadata["embeds"].([]map[string]any)
	if ok {
		return raw
	}
	if generic, ok := metadata["embeds"].([]any); ok {
		out := make([]map[string]any, 0, len(generic))
		for _, item := range generic {
			if entry, ok := item.(map[string]any); ok {
				out = append(out, entry)
			}
		}
		return out
	}
	return []map[string]any{}
}

func hasLocationPayload(request *postRequest) bool {
	return strings.TrimSpace(stringValue(request.LocationName)) != "" ||
		strings.TrimSpace(stringValue(request.LocationAddress)) != "" ||
		strings.TrimSpace(stringValue(request.LocationWkt)) != ""
}

func (h *handler) applyAutoChain(ctx context.Context, ent *store.PostEntity) {
	window := 5
	if h.core.Config != nil && h.core.Config.Posts.AutoChainWindowMinutes > 0 {
		window = h.core.Config.Posts.AutoChainWindowMinutes
	}
	if ent.PublishedAt == nil {
		return
	}
	from := ent.PublishedAt.Add(-time.Duration(window) * time.Minute)
	var latest store.PostEntity
	err := h.db.WithContext(ctx).
		Where("publisher_id = ? AND id <> ?", ent.PublisherID, ent.ID).
		Where("drafted_at IS NULL AND published_at IS NOT NULL").
		Where("published_at >= ? AND published_at < ?", from, *ent.PublishedAt).
		Where("replied_post_id IS NULL AND forwarded_post_id IS NULL").
		Order("published_at DESC").First(&latest).Error
	if err != nil {
		return
	}
	headID := latest.ID
	if latest.ChainedPostID != nil {
		headID = *latest.ChainedPostID
	}
	ent.ChainedPostID = &headID
	h.db.WithContext(ctx).Model(&store.PostEntity{}).Where("id = ?", ent.ID).
		UpdateColumn("chained_post_id", headID)
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (h *handler) updatePost(c *gin.Context) {
	ctx := c.Request.Context()
	account := authn.CurrentUser(c)
	if account == nil {
		httpx.Unauthorized(c)
		return
	}
	accountID, err := uuid.Parse(account.GetId())
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	var request postRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	request.Content = sanitizeText(request.Content)
	request.Title = normalizeOptionalText(request.Title)
	request.Description = normalizeOptionalText(request.Description)
	postType := int(model.PostTypeMoment)
	if request.Type != nil {
		postType = *request.Type
	}
	if postType != int(model.PostTypeBlog) {
		if strings.TrimSpace(stringValue(request.Content)) == "" && len(request.Attachments) == 0 && request.ForwardedPostID == nil {
			httpx.BadRequest(c, "POST_CONTENT_REQUIRED", "Content is required.")
			return
		}
	}
	if !h.validatePostShape(c, &request, postType) {
		return
	}
	if request.DraftedAt != nil && request.PublishedAt != nil {
		httpx.BadRequest(c, "POST_DRAFT_PUBLISH_CONFLICT", "Cannot set both draftedAt and publishedAt.")
		return
	}

	var ent store.PostEntity
	if err := h.db.WithContext(ctx).Where("id = ?", id).First(&ent).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	if ent.LockedAt != nil {
		httpx.Fail(c, errs.New("POST_LOCKED", "This post is locked and cannot be edited.", 423))
		return
	}
	if !h.isMemberWithRole(ctx, ent.PublisherID, accountID, int(model.PublisherRoleEditor)) {
		httpx.Forbidden(c, "You need at least be an editor to edit this publisher's post.")
		return
	}
	if pubName := c.Query("pub"); pubName != "" {
		publisher, err := h.publisherByName(ctx, pubName)
		if err != nil {
			httpx.NotFound(c, "Publisher not found.")
			return
		}
		if !h.isMemberWithRole(ctx, publisher.ID, accountID, int(model.PublisherRoleEditor)) {
			httpx.Forbidden(c, "You need at least be an editor to transfer this post to this publisher.")
			return
		}
		ent.PublisherID = publisher.ID
	}
	if request.RealmID != nil {
		if status, message := h.validatePostRealm(ctx, *request.RealmID, accountID); status != 0 {
			switch status {
			case 403:
				httpx.Forbidden(c, message)
			case 503:
				httpx.Fail(c, errs.New("SERVICE_UNAVAILABLE", "The realm service is unavailable.", 503))
			default:
				httpx.BadRequest(c, message, "The realm does not exist or is unavailable.")
			}
			return
		}
	}

	if request.Title != nil {
		ent.Title = request.Title
	}
	if request.Description != nil {
		ent.Description = request.Description
	}
	if request.Slug != nil {
		ent.Slug = request.Slug
	}
	if request.Content != nil {
		ent.Content = request.Content
	}
	if request.Visibility != nil {
		ent.Visibility = *request.Visibility
	}
	if ent.Visibility == int(model.VisibilityCloseFriendsOnly) || ent.Visibility == int(model.VisibilityFriends) {
		if ent.PublisherID == uuid.Nil {
			httpx.BadRequest(c, "POST_VISIBILITY_REQUIRES_PUBLISHER", "CloseFriendsOnly and Friends visibility require a publisher.")
			return
		}
	}
	if request.Type != nil {
		ent.Type = *request.Type
	}
	if request.Language != nil {
		ent.Language = request.Language
	}
	if request.Meta != nil {
		ent.Metadata = store.ToJSON(request.Meta)
	}
	if request.DraftedAt != nil {
		drafted := request.DraftedAt.Time()
		ent.DraftedAt = &drafted
	}

	effectiveType := postType
	if request.Type == nil {
		effectiveType = ent.Type
	}
	if effectiveType == int(model.PostTypeBlog) {
		if ent.Content == nil || strings.TrimSpace(*ent.Content) == "" {
			httpx.BadRequest(c, "POST_BLOG_INVALID_URL", "Blog post content must be a valid URL.")
			return
		}
		parsed, err := url.Parse(*ent.Content)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			httpx.BadRequest(c, "POST_BLOG_INVALID_URL", "Blog post content must be a valid URL.")
			return
		}
		granted, err := h.permissionGranted(ctx, account, "posts.create.blog")
		if err != nil {
			httpx.Fail(c, errs.New("POST_BLOG_PERMISSION_CHECK_FAILED", "Unable to check blog permission.", 400))
			return
		}
		if !granted {
			httpx.Forbidden(c, "You do not have permission to create blog posts.")
			return
		}
		if !h.domainVerified(ctx, ent.PublisherID, parsed.Hostname()) {
			httpx.Forbidden(c, "This domain is not verified for your publisher.")
			return
		}
	}

	ent.EmbedView = store.ToJSON(request.EmbedView)
	metadata := map[string]any{}
	if ent.Metadata != nil {
		var decoded map[string]any
		if err := json.Unmarshal(ent.Metadata, &decoded); err == nil {
			metadata = decoded
		}
	}
	h.applyEmbeds(ctx, metadata, &request)
	if request.ThumbnailID == nil {
		delete(metadata, "thumbnail")
	}
	ent.Metadata = store.ToJSON(metadata)

	if request.RealmID != nil {
		ent.RealmID = request.RealmID
	} else {
		ent.RealmID = nil
	}

	if request.Attachments != nil {
		if err := h.resolveAttachments(ctx, &ent, request.Attachments); err != nil {
			httpx.BadRequest(c, "POST_PUBLISH_FAILED", "Unable to resolve attachments.")
			return
		}
	}
	if request.Tags != nil {
		publisher, _ := h.publisherByID(ctx, ent.PublisherID)
		_, tagSlugs, err := h.resolveTags(ctx, request.Tags, publisher)
		if err != nil {
			httpx.BadRequest(c, "POST_PUBLISH_FAILED", err.Error())
			return
		}
		if err := h.replaceTagLinks(ctx, ent.ID, tagSlugs); err != nil {
			httpx.Internal(c)
			return
		}
	}
	if request.Categories != nil {
		_, categorySlugs, err := h.resolveCategories(ctx, request.Categories)
		if err != nil {
			httpx.BadRequest(c, "POST_PUBLISH_FAILED", err.Error())
			return
		}
		if err := h.replaceCategoryLinks(ctx, ent.ID, categorySlugs); err != nil {
			httpx.Internal(c)
			return
		}
	}

	now := time.Now().UTC()
	ent.EditedAt = &now
	wasPublished := ent.DraftedAt == nil && ent.PublishedAt != nil && !ent.PublishedAt.After(now)

	if request.PublishedAt != nil {
		published := request.PublishedAt.Time()
		if published.Before(now) {
			wasScheduled := ent.PublishedAt != nil && ent.PublishedAt.After(now)
			if ent.DraftedAt != nil || wasScheduled {
				published = now
			} else {
				httpx.BadRequest(c, "POST_PUBLISH_FAILED", "Cannot set the published at to the past.")
				return
			}
		}
		ent.PublishedAt = &published
		ent.DraftedAt = nil
	}
	if request.DraftedAt != nil {
		drafted := request.DraftedAt.Time()
		ent.DraftedAt = &drafted
		ent.PublishedAt = nil
	}

	isPublished := ent.DraftedAt == nil && ent.PublishedAt != nil && !ent.PublishedAt.After(now)
	if isPublished && !wasPublished {
		ent.PublishNotifiedAt = &now
	} else if !isPublished {
		ent.PublishNotifiedAt = nil
	}
	if err := h.db.WithContext(ctx).Save(&ent).Error; err != nil {
		httpx.Internal(c)
		return
	}
	if err := h.writePostIndex(ctx, &ent); err != nil {
		h.log.Warn("posts: index updated post failed", "post", ent.ID, "error", err)
	}
	post, err := h.hydrateOne(ctx, &ent, h.newViewer(c), false, false)
	if err != nil {
		httpx.Internal(c)
		return
	}
	h.afterPostUpdated(ctx, &ent, &post, accountID)
	httpx.OK(c, post)
}

func (h *handler) replaceTagLinks(ctx context.Context, postID uuid.UUID, slugs []string) error {
	if err := h.db.WithContext(ctx).Where("posts_id = ?", postID).Delete(&store.PostTagLink{}).Error; err != nil {
		return err
	}
	h.linkTopics(ctx, postID, slugs, nil)
	return nil
}

func (h *handler) replaceCategoryLinks(ctx context.Context, postID uuid.UUID, slugs []string) error {
	if err := h.db.WithContext(ctx).Where("posts_id = ?", postID).Delete(&store.PostCategoryLink{}).Error; err != nil {
		return err
	}
	h.linkTopics(ctx, postID, nil, slugs)
	return nil
}

// ---------------------------------------------------------------------------
// Delete and batch operations
// ---------------------------------------------------------------------------

func (h *handler) deletePost(c *gin.Context) {
	ctx := c.Request.Context()
	account := authn.CurrentUser(c)
	if account == nil {
		httpx.Unauthorized(c)
		return
	}
	accountID, _ := uuid.Parse(account.GetId())
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
		httpx.Fail(c, errs.New("POST_LOCKED", "This post is locked and cannot be deleted.", 423))
		return
	}
	if !h.isMemberWithRole(ctx, ent.PublisherID, accountID, int(model.PublisherRoleEditor)) {
		httpx.Forbidden(c, "You need at least be an editor to delete the publisher's post.")
		return
	}
	if err := h.softDeletePost(ctx, &ent); err != nil {
		httpx.Internal(c)
		return
	}
	httpx.NoContent(c)
}

func (h *handler) softDeletePost(ctx context.Context, ent *store.PostEntity) error {
	now := time.Now().UTC()
	err := h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("post_id = ?", ent.ID).Delete(&store.PostReactionEntity{}).Error; err != nil {
			return err
		}
		if err := tx.Model(&store.PostEntity{}).Where("replied_post_id = ?", ent.ID).Update("replied_gone", true).Error; err != nil {
			return err
		}
		if err := tx.Model(&store.PostEntity{}).Where("forwarded_post_id = ?", ent.ID).Update("forwarded_gone", true).Error; err != nil {
			return err
		}
		if err := tx.Model(&store.PostEntity{}).Where("chained_post_id = ?", ent.ID).Update("chained_post_id", nil).Error; err != nil {
			return err
		}
		_ = now
		return tx.Delete(&store.PostEntity{}, "id = ?", ent.ID).Error
	})
	if err != nil {
		return err
	}
	h.afterPostDeleted(ctx, ent)
	return nil
}

func (h *handler) batchDeletePosts(c *gin.Context) {
	ctx := c.Request.Context()
	account := authn.CurrentUser(c)
	if account == nil {
		httpx.Unauthorized(c)
		return
	}
	var request batchDeleteRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	if len(request.PostIDs) == 0 {
		httpx.BadRequest(c, "POST_BATCH_EMPTY", "PostIds list is empty.")
		return
	}
	posts, errResp := h.loadBatchPosts(ctx, c, request.PostIDs, "POST_BATCH_NOT_FOUND")
	if errResp {
		return
	}
	for _, post := range posts {
		if err := h.softDeletePost(ctx, &post); err != nil {
			httpx.Internal(c)
			return
		}
	}
	httpx.NoContent(c)
}

func (h *handler) loadBatchPosts(ctx context.Context, c *gin.Context, ids []uuid.UUID, notFoundCode string) ([]store.PostEntity, bool) {
	var posts []store.PostEntity
	if err := h.db.WithContext(ctx).Where("id IN ?", ids).Find(&posts).Error; err != nil {
		httpx.Internal(c)
		return nil, true
	}
	if len(posts) != len(ids) {
		found := map[uuid.UUID]struct{}{}
		for _, post := range posts {
			found[post.ID] = struct{}{}
		}
		var missing []string
		for _, id := range ids {
			if _, ok := found[id]; !ok {
				missing = append(missing, id.String())
			}
		}
		httpx.BadRequest(c, notFoundCode, "Posts not found: "+strings.Join(missing, ", "))
		return nil, true
	}
	for _, post := range posts {
		if post.LockedAt != nil {
			httpx.Fail(c, errs.New("POST_LOCKED", fmt.Sprintf("Post %s is locked and cannot be edited.", post.ID), 423))
			return nil, true
		}
	}
	account := authn.CurrentUser(c)
	accountID := uuid.Nil
	if account != nil {
		accountID, _ = uuid.Parse(account.GetId())
	}
	seenPublishers := map[uuid.UUID]struct{}{}
	for _, post := range posts {
		if _, ok := seenPublishers[post.PublisherID]; ok {
			continue
		}
		seenPublishers[post.PublisherID] = struct{}{}
		if !h.isMemberWithRole(ctx, post.PublisherID, accountID, int(model.PublisherRoleEditor)) {
			httpx.Forbidden(c, "You need at least be an editor to edit posts from this publisher.")
			return nil, true
		}
	}
	return posts, false
}

func (h *handler) batchVisibilityPosts(c *gin.Context) {
	ctx := c.Request.Context()
	if authn.CurrentUser(c) == nil {
		httpx.Unauthorized(c)
		return
	}
	var request batchVisibilityRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	if len(request.PostIDs) == 0 {
		httpx.BadRequest(c, "POST_BATCH_EMPTY", "PostIds list is empty.")
		return
	}
	if request.DraftedAt != nil && request.PublishedAt != nil {
		httpx.BadRequest(c, "POST_DRAFT_PUBLISH_CONFLICT", "Cannot set both draftedAt and publishedAt.")
		return
	}
	posts, failed := h.loadBatchPosts(ctx, c, request.PostIDs, "POST_BATCH_NOT_FOUND")
	if failed {
		return
	}
	now := time.Now().UTC()
	for i := range posts {
		post := &posts[i]
		post.EditedAt = &now
		if request.Visibility != nil {
			post.Visibility = *request.Visibility
		}
		if request.PublishedAt != nil {
			published := request.PublishedAt.Time()
			if published.Before(now) && post.DraftedAt == nil {
				httpx.BadRequest(c, "POST_PUBLISH_PAST_NOT_ALLOWED", fmt.Sprintf("Cannot set publishedAt to the past for post %s.", post.ID))
				return
			}
			post.PublishedAt = &published
			post.DraftedAt = nil
			post.PublishNotifiedAt = nil
		}
		if request.DraftedAt != nil {
			drafted := request.DraftedAt.Time()
			post.DraftedAt = &drafted
			post.PublishedAt = nil
			post.PublishNotifiedAt = nil
		}
		if err := h.db.WithContext(ctx).Save(post).Error; err != nil {
			httpx.Internal(c)
			return
		}
	}
	httpx.NoContent(c)
}

func (h *handler) publishDraft(c *gin.Context) {
	ctx := c.Request.Context()
	account := authn.CurrentUser(c)
	if account == nil {
		httpx.Unauthorized(c)
		return
	}
	accountID, _ := uuid.Parse(account.GetId())
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
	if !h.isMemberWithRole(ctx, ent.PublisherID, accountID, int(model.PublisherRoleEditor)) {
		httpx.Forbidden(c, "You need at least be an editor to publish this post.")
		return
	}
	if ent.DraftedAt == nil {
		httpx.BadRequest(c, "POST_NOT_A_DRAFT", "This post is not a draft.")
		return
	}
	now := time.Now().UTC()
	ent.DraftedAt = nil
	ent.PublishedAt = &now
	ent.PublishNotifiedAt = &now
	ent.EditedAt = &now
	if err := h.db.WithContext(ctx).Save(&ent).Error; err != nil {
		httpx.Internal(c)
		return
	}
	post, err := h.hydrateOne(ctx, &ent, h.newViewer(c), false, false)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, post)
}
