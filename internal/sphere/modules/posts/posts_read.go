package posts

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
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

func (h *handler) registerPostReadRoutes(host *module.Host[core.Services]) {
	api := host.API
	api.GET("/posts/featured", h.listFeaturedPosts)
	api.GET("/posts/sponsor/current", h.getCurrentSponsoredPost)
	api.GET("/posts/sponsor/leaderboard", h.getSponsorLeaderboard)
	api.GET("/posts/:id/sponsor", h.getPostSponsorship)
	api.GET("/posts/:id/sponsor/history", authn.RequireAuth(), h.getPostSponsorHistory)
	api.GET("/posts/drafts", authn.RequireAuth(), host.Core.Auth.RequirePermission("posts.view"), h.listDrafts)
	api.GET("/posts/bookmarks", authn.RequireAuth(), host.Core.Auth.RequirePermission("posts.bookmark"), h.listBookmarks)
	api.GET("/posts", h.listPosts)
	api.GET("/posts/reactions/users/:name", h.listUserReactions)
	api.GET("/posts/:id/:slug", h.getPostByPublisherAndSlug)
	api.GET("/posts/:id", h.getPostByID)
	api.GET("/posts/:id/prev", h.getPrevPost)
	api.GET("/posts/:id/next", h.getNextPost)
	api.GET("/posts/:id/reactions", h.getReactions)
	api.GET("/posts/:id/replies/featured", h.getFeaturedReply)
	api.GET("/posts/:id/replies/pinned", h.listPinnedReplies)
	api.GET("/posts/:id/replies/threaded", h.listThreadedReplies)
	api.GET("/posts/:id/replies", h.listReplies)
	api.GET("/posts/:id/thread", h.getThread)
	api.GET("/posts/:id/chain", h.getChain)
	api.GET("/posts/:id/forwards", h.listForwards)
}

// ---------------------------------------------------------------------------
// Featured and sponsors
// ---------------------------------------------------------------------------

const featuredCacheKey = "posts:featured"

func (h *handler) listFeaturedPosts(c *gin.Context) {
	ctx := c.Request.Context()
	v := h.newViewer(c)

	featuredIDs, err := h.featuredPostIDs(ctx)
	if err != nil {
		httpx.Internal(c)
		return
	}
	if len(featuredIDs) == 0 {
		posts := []model.Post{}
		if sponsored, err := h.currentSponsoredPost(ctx, v); err == nil && sponsored != nil {
			sponsored.Sponsored = true
			posts = append(posts, *sponsored)
		}
		httpx.List(c, posts, int64(len(posts)))
		return
	}

	var rows []store.PostEntity
	if err := h.db.WithContext(ctx).
		Where("id IN ?", featuredIDs).
		Where("visibility = ?", int(model.VisibilityPublic)).
		Where("(shadowban_reason IS NULL OR shadowban_reason = ?)", int(model.ShadowbanNone)).
		Find(&rows).Error; err != nil {
		httpx.Internal(c)
		return
	}
	byID := map[uuid.UUID]store.PostEntity{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	ordered := make([]store.PostEntity, 0, len(rows))
	for _, id := range featuredIDs {
		if row, ok := byID[id]; ok {
			ordered = append(ordered, row)
		}
	}

	posts, err := h.hydrate(ctx, ordered, v, true, true)
	if err != nil {
		httpx.Internal(c)
		return
	}

	sponsored, err := h.currentSponsoredPost(ctx, v)
	if err == nil && sponsored != nil {
		sponsored.Sponsored = true
		posts = append([]model.Post{*sponsored}, posts...)
	}
	httpx.List(c, posts, int64(len(posts)))
}

func (h *handler) featuredPostIDs(ctx context.Context) ([]uuid.UUID, error) {
	if h.core.Redis != nil {
		var cached []string
		if found, err := h.core.Redis.Get(ctx, featuredCacheKey, &cached); err == nil && found && len(cached) > 0 {
			ids := make([]uuid.UUID, 0, len(cached))
			for _, raw := range cached {
				if id, ok := parseUUID(raw); ok {
					ids = append(ids, id)
				}
			}
			if len(ids) > 0 {
				return ids, nil
			}
		}
	}

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	periodStart := today.Add(-24 * time.Hour)
	periodEnd := today

	var candidateIDs []uuid.UUID
	if err := h.db.WithContext(ctx).Model(&store.PostEntity{}).
		Where("visibility = ?", int(model.VisibilityPublic)).
		Where("created_at >= ? AND created_at < ?", periodStart, periodEnd).
		Where("fediverse_uri IS NULL").
		Where("(shadowban_reason IS NULL OR shadowban_reason = ?)", int(model.ShadowbanNone)).
		Where("publisher_id NOT IN (SELECT id FROM publishers WHERE (gatekept_follows = true OR (shadowban_reason IS NOT NULL AND shadowban_reason <> 0)))").
		Pluck("id", &candidateIDs).Error; err != nil {
		return nil, err
	}
	if len(candidateIDs) == 0 {
		return nil, nil
	}

	reactionScores := map[uuid.UUID]int{}
	var reactionRows []struct {
		PostID uuid.UUID `gorm:"column:post_id"`
		Score  int       `gorm:"column:score"`
	}
	h.db.WithContext(ctx).Model(&store.PostReactionEntity{}).
		Select("post_id, sum(CASE WHEN attitude = ? THEN 1 ELSE -1 END) AS score", attitudePositive).
		Where("post_id IN ?", candidateIDs).
		Group("post_id").Scan(&reactionRows)
	for _, row := range reactionRows {
		reactionScores[row.PostID] = row.Score
	}

	replyCounts := map[uuid.UUID]int{}
	var replyRows []struct {
		RepliedPostID uuid.UUID `gorm:"column:replied_post_id"`
		Count         int       `gorm:"column:count"`
	}
	h.db.WithContext(ctx).Model(&store.PostEntity{}).
		Select("replied_post_id, count(*) AS count").
		Where("replied_post_id IN ?", candidateIDs).
		Group("replied_post_id").Scan(&replyRows)
	for _, row := range replyRows {
		replyCounts[row.RepliedPostID] = row.Count
	}

	awardScores := map[uuid.UUID]float64{}
	var awardRows []struct {
		ID           uuid.UUID `gorm:"column:id"`
		AwardedScore float64   `gorm:"column:awarded_score"`
	}
	h.db.WithContext(ctx).Model(&store.PostEntity{}).
		Select("id, awarded_score").Where("id IN ?", candidateIDs).Scan(&awardRows)
	for _, row := range awardRows {
		awardScores[row.ID] = row.AwardedScore
	}

	type scored struct {
		id    uuid.UUID
		score int
	}
	scores := make([]scored, 0, len(candidateIDs))
	for _, id := range candidateIDs {
		scores = append(scores, scored{
			id:    id,
			score: reactionScores[id] + replyCounts[id] + int(awardScores[id]/10),
		})
	}
	sort.SliceStable(scores, func(i, j int) bool {
		if scores[i].score == scores[j].score {
			return scores[i].id.String() < scores[j].id.String()
		}
		return scores[i].score > scores[j].score
	})
	if len(scores) > 5 {
		scores = scores[:5]
	}

	ids := make([]uuid.UUID, 0, len(scores))
	cacheable := make([]string, 0, len(scores))
	for _, entry := range scores {
		ids = append(ids, entry.id)
		cacheable = append(cacheable, entry.id.String())
	}

	if h.core.Redis != nil && len(cacheable) > 0 {
		if err := h.core.Redis.Set(ctx, featuredCacheKey, cacheable, 4*time.Hour); err != nil {
			h.log.Debug("posts: cache featured ids failed", "error", err)
		}
	}

	var existing []uuid.UUID
	h.db.WithContext(ctx).Model(&store.PostFeaturedRecordEntity{}).
		Where("post_id IN ?", ids).Pluck("post_id", &existing)
	existingSet := map[uuid.UUID]struct{}{}
	for _, id := range existing {
		existingSet[id] = struct{}{}
	}
	for _, entry := range scores {
		if _, ok := existingSet[entry.id]; ok {
			continue
		}
		record := store.PostFeaturedRecordEntity{
			ID:            uuid.New(),
			PostID:        entry.id,
			FeaturedAt:    &now,
			SocialCredits: entry.score,
		}
		if err := h.db.WithContext(ctx).Create(&record).Error; err != nil {
			h.log.Debug("posts: create featured record failed", "error", err)
		}
	}
	return ids, nil
}

func (h *handler) getCurrentSponsoredPost(c *gin.Context) {
	ctx := c.Request.Context()
	v := h.newViewer(c)
	post, err := h.currentSponsoredPost(ctx, v)
	if err != nil {
		httpx.Internal(c)
		return
	}
	if post == nil {
		httpx.OK(c, gin.H{"sponsored": false, "post": nil})
		return
	}
	httpx.OK(c, gin.H{"sponsored": true, "post": post})
}

func (h *handler) currentSponsoredPost(ctx context.Context, v *viewer) (*model.Post, error) {
	now := time.Now().UTC()
	var placement store.PostSponsorPlacementEntity
	err := h.db.WithContext(ctx).
		Where("valid_from <= ? AND valid_until > ?", now, now).
		Order("valid_from DESC").First(&placement).Error
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ent store.PostEntity
	err = h.db.WithContext(ctx).
		Where("id = ?", placement.PostID).
		Where("visibility = ?", int(model.VisibilityPublic)).
		Where("(shadowban_reason IS NULL OR shadowban_reason = ?)", int(model.ShadowbanNone)).
		Where("publisher_id NOT IN (SELECT id FROM publishers WHERE shadowban_reason IS NOT NULL AND shadowban_reason <> 0)").
		First(&ent).Error
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	post, err := h.hydrateOne(ctx, &ent, v, true, false)
	if err != nil {
		return nil, err
	}
	post.Sponsored = true
	return &post, nil
}

func (h *handler) getSponsorLeaderboard(c *gin.Context) {
	ctx := c.Request.Context()
	take := queryInt(c, "take", 20)
	if take <= 0 {
		take = 20
	}
	now := time.Now().UTC()
	var rows []struct {
		PostID      uuid.UUID `gorm:"column:post_id"`
		TotalAmount float64   `gorm:"column:total_amount"`
		BidCount    int       `gorm:"column:bid_count"`
	}
	if err := h.db.WithContext(ctx).Model(&store.PostSponsorBidEntity{}).
		Select("post_id, sum(amount) AS total_amount, count(*) AS bid_count").
		Where("expires_at > ?", now).
		Group("post_id").
		Order("total_amount DESC").
		Limit(take).Scan(&rows).Error; err != nil {
		httpx.Internal(c)
		return
	}
	entries := make([]model.SponsorLeaderboardEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, model.SponsorLeaderboardEntry{
			PostID:      row.PostID.String(),
			TotalAmount: row.TotalAmount,
			BidCount:    row.BidCount,
		})
	}
	httpx.OK(c, entries)
}

func (h *handler) getPostSponsorship(c *gin.Context) {
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	now := time.Now().UTC()
	var total float64
	if err := h.db.WithContext(c.Request.Context()).Model(&store.PostSponsorBidEntity{}).
		Where("post_id = ? AND expires_at > ?", id, now).
		Select("COALESCE(sum(amount), 0)").Scan(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, gin.H{"total_amount": total})
}

func (h *handler) getPostSponsorHistory(c *gin.Context) {
	ctx := c.Request.Context()
	v := h.newViewer(c)
	if !v.authed() {
		httpx.Unauthorized(c)
		return
	}
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	var post store.PostEntity
	if err := h.db.WithContext(ctx).Where("id = ?", id).First(&post).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	var authorAccountID uuid.UUID
	var publisher store.PublisherEntity
	if err := h.db.WithContext(ctx).Where("id = ?", post.PublisherID).First(&publisher).Error; err == nil && publisher.AccountID != nil {
		authorAccountID = *publisher.AccountID
	}

	query := h.db.WithContext(ctx).Where("post_id = ?", id)
	if authorAccountID == uuid.Nil || authorAccountID != v.accountID {
		query = query.Where("account_id = ?", v.accountID)
	}
	var rows []store.PostSponsorBidEntity
	if err := query.Order("created_at DESC").Find(&rows).Error; err != nil {
		httpx.Internal(c)
		return
	}
	bids := make([]model.PostSponsorBid, 0, len(rows))
	for i := range rows {
		bids = append(bids, postSponsorBidToModel(&rows[i]))
	}
	httpx.OK(c, bids)
}

// ---------------------------------------------------------------------------
// Drafts and bookmarks
// ---------------------------------------------------------------------------

func (h *handler) listDrafts(c *gin.Context) {
	ctx := c.Request.Context()
	v := h.newViewer(c)
	if !v.authed() {
		httpx.Unauthorized(c)
		return
	}
	page, ok := h.pageFrom(c)
	if !ok {
		return
	}
	publisherIDs := v.memberIDs()
	requested, missing := h.resolvePublisherNames(ctx, parsePublisherNames(c.Query("pub")))
	if len(missing) > 0 {
		httpx.Fail(c, publisherNotFoundError("PUBLISHER_NOT_FOUND", missing))
		return
	}
	if len(requested) > 0 {
		for _, publisher := range requested {
			if !h.isMemberWithRole(ctx, publisher.ID, v.accountID, int(model.PublisherRoleEditor)) {
				httpx.Forbidden(c, "You need at least be an editor to view drafts.")
				return
			}
		}
		publisherIDs = publisherIDs[:0]
		for _, publisher := range requested {
			publisherIDs = append(publisherIDs, publisher.ID)
		}
	}
	includeScheduled := c.Query("include_scheduled") == "true" || c.Query("include_scheduled") == "1"
	now := time.Now().UTC()

	query := h.db.WithContext(ctx).Model(&store.PostEntity{}).
		Where("publisher_id <> ?", uuid.Nil).
		Where("publisher_id IN ?", publisherIDs)
	if includeScheduled {
		query = query.Where("(drafted_at IS NOT NULL OR (drafted_at IS NULL AND published_at IS NOT NULL AND published_at > ?))", now).
			Order("updated_at DESC")
	} else {
		query = query.Where("drafted_at IS NOT NULL").
			Order("COALESCE(drafted_at, updated_at) DESC")
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	var rows []store.PostEntity
	if err := query.Offset(page.Offset).Limit(page.Take).Find(&rows).Error; err != nil {
		httpx.Internal(c)
		return
	}
	posts, err := h.hydrate(ctx, rows, v, true, true)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.List(c, posts, total)
}

func (h *handler) listBookmarks(c *gin.Context) {
	ctx := c.Request.Context()
	v := h.newViewer(c)
	if !v.authed() {
		httpx.Unauthorized(c)
		return
	}
	page, ok := h.pageFrom(c)
	if !ok {
		return
	}
	var publisherIDs []uuid.UUID
	if err := h.db.WithContext(ctx).Model(&store.PostBookmarkEntity{}).
		Joins("JOIN posts ON posts.id = post_bookmarks.post_id AND posts.deleted_at IS NULL").
		Where("post_bookmarks.account_id = ?", v.accountID).
		Where("post_bookmarks.deleted_at IS NULL").
		Pluck("posts.publisher_id", &publisherIDs).Error; err != nil {
		httpx.Internal(c)
		return
	}
	info := h.gatekeepInfo(ctx, v, dedupeUUID(publisherIDs))

	query := h.db.WithContext(ctx).Model(&store.PostEntity{}).
		Where("id IN (SELECT post_id FROM post_bookmarks WHERE account_id = ? AND deleted_at IS NULL)", v.accountID)
	query = h.applyVisibility(query, v, true, info, false)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	var rows []store.PostEntity
	if err := query.Order("COALESCE(published_at, created_at) DESC").
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

// ---------------------------------------------------------------------------
// Post listing
// ---------------------------------------------------------------------------

func (h *handler) listPosts(c *gin.Context) {
	ctx := c.Request.Context()
	v := h.newViewer(c)
	page, ok := h.pageFrom(c)
	if !ok {
		return
	}

	requested, missing := h.resolvePublisherNames(ctx, parsePublisherNames(c.Query("pub")))
	if len(missing) > 0 {
		httpx.Fail(c, publisherNotFoundError("PUBLISHER_NOT_FOUND", missing))
		return
	}
	publisherIDs := make([]uuid.UUID, 0, len(requested))
	for _, publisher := range requested {
		publisherIDs = append(publisherIDs, publisher.ID)
	}
	hasPublisherFilter := len(publisherIDs) > 0

	query := h.db.WithContext(ctx).Model(&store.PostEntity{}).
		Where("fediverse_uri IS NULL").
		Where("chained_post_id IS NULL")

	if hasPublisherFilter {
		query = query.Where("publisher_id IN ?", publisherIDs)
	}
	if typ := queryIntPtr(c, "type"); typ != nil {
		query = query.Where("type = ?", *typ)
	}
	if categories := queryList(c, "categories"); len(categories) > 0 {
		query = query.Where("id IN (SELECT posts_id FROM post_category_links l JOIN post_categories c ON c.id = l.categories_id WHERE lower(c.slug) IN ? AND c.deleted_at IS NULL)", lowerAll(categories))
	}
	if tags := queryList(c, "tags"); len(tags) > 0 {
		query = query.Where("id IN (SELECT posts_id FROM post_tag_links l JOIN post_tags t ON t.id = l.tags_id WHERE lower(t.slug) IN ? AND t.deleted_at IS NULL)", lowerAll(tags))
	}
	if onlyMedia := c.Query("media"); onlyMedia == "true" || onlyMedia == "1" {
		query = query.Where("COALESCE(jsonb_array_length(attachments), 0) > 0")
	}
	if start := queryInt64Ptr(c, "periodStart"); start != nil {
		query = query.Where("COALESCE(published_at, created_at) >= ?", time.Unix(*start, 0).UTC())
	}
	if end := queryInt64Ptr(c, "periodEnd"); end != nil {
		query = query.Where("COALESCE(published_at, created_at) <= ?", time.Unix(*end, 0).UTC())
	}

	pinned := queryBoolPtr(c, "pinned")
	realm := strings.TrimSpace(c.Query("realm"))
	switch {
	case pinned != nil && *pinned && realm != "":
		query = query.Where("pin_mode = ?", int(model.PinModeRealmPage))
	case pinned != nil && *pinned && hasPublisherFilter:
		query = query.Where("pin_mode = ?", int(model.PinModePublisherPage))
	case pinned != nil && *pinned:
		httpx.Fail(c, errs.BadRequest("POST_PINNED_FILTER_REQUIRES_PARAM",
			"You need pass extra realm or publisher params in order to filter with pinned posts."))
		return
	case pinned != nil && !*pinned:
		query = query.Where("pin_mode IS NULL")
	}
	if realm != "" {
		switch id, ok := parseUUID(realm); {
		case ok:
			query = query.Where("realm_id = ?", id)
		default:
			resolved, err := h.realmIDBySlug(ctx, realm)
			switch {
			case err == nil:
				query = query.Where("realm_id = ?", resolved)
			case errors.Is(err, errRealmUnavailable):
				h.log.Debug("posts: realm filter ignored, realm port unavailable", "realm", realm)
			default:
				h.log.Debug("posts: realm filter not found", "realm", realm, "error", err)
				query = query.Where("1 = 0")
			}
		}
	}

	if includeReplies := queryBoolPtr(c, "replies"); includeReplies != nil {
		if *includeReplies {
			query = query.Where("replied_post_id IS NOT NULL")
		} else {
			query = query.Where("replied_post_id IS NULL")
		}
	}

	if mentioned := strings.TrimSpace(c.Query("mentioned")); mentioned != "" {
		pattern := "%@" + mentioned + "%"
		query = query.Where("(content ILIKE ? OR mentions::text ILIKE ?)", pattern, "%"+strings.ToLower(mentioned)+"%")
	}

	info := h.gatekeepInfo(ctx, v, publisherIDs)
	query = h.applyVisibility(query, v, true, info, hasPublisherFilter)

	search := strings.TrimSpace(c.Query("query"))
	query = applyTextSearch(query, search)
	shuffle := c.Query("shuffle") == "true" || c.Query("shuffle") == "1"
	orderDesc := true
	if raw, exists := c.GetQuery("orderDesc"); exists {
		orderDesc = raw == "true" || raw == "1"
	}
	query = applyPostOrdering(query, c.Query("order"), orderDesc, shuffle, search)

	var total int64
	if err := query.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}

	if hasPublisherFilter && total == 0 && len(requested) == 1 {
		publisher := requested[0]
		gatekept := publisher.GatekeptFollows != nil && *publisher.GatekeptFollows
		if gatekept && !v.isMember(publisher.ID) && !h.isSubscriber(ctx, v.accountID, publisher.ID) {
			if !v.authed() {
				httpx.Fail(c, errs.New("AUTHENTICATION_REQUIRED", "Authentication is required to view this publisher's posts.", http.StatusUnauthorized))
				return
			}
			apiErr := errs.New("PUBLISHER_GATEKEPT", fmt.Sprintf("%s's posts are only available to subscribers.", publisher.Name), http.StatusForbidden)
			apiErr.Meta = gin.H{"publisher": publisher.Name, "is_gatekept": true, "requires_subscription": true}
			httpx.Fail(c, apiErr)
			return
		}
	}

	var rows []store.PostEntity
	if err := query.Offset(page.Offset).Limit(page.Take).Find(&rows).Error; err != nil {
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

func lowerAll(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strings.ToLower(strings.TrimSpace(value)))
	}
	return out
}

// ---------------------------------------------------------------------------
// Single post reads
// ---------------------------------------------------------------------------

func (h *handler) getPostByID(c *gin.Context) {
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	h.respondDetail(c, id)
}

func (h *handler) getPostByPublisherAndSlug(c *gin.Context) {
	publisherName := c.Param("id")
	slug := c.Param("slug")
	var ent store.PostEntity
	err := h.db.WithContext(c.Request.Context()).
		Where("lower(slug) = ?", strings.ToLower(slug)).
		Where("publisher_id IN (SELECT id FROM publishers WHERE lower(name) = ?)", strings.ToLower(publisherName)).
		First(&ent).Error
	if err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	h.respondDetail(c, ent.ID)
}

func (h *handler) respondDetail(c *gin.Context, id uuid.UUID) {
	ctx := c.Request.Context()
	v := h.newViewer(c)

	var ent store.PostEntity
	query := h.applyDetailScope(h.db.WithContext(ctx).Model(&store.PostEntity{}), v)
	if err := query.Where("id = ?", id).First(&ent).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	publisher, _ := h.publisherByID(ctx, ent.PublisherID)
	post, err := h.hydrateOne(ctx, &ent, v, false, false)
	if err != nil {
		httpx.Internal(c)
		return
	}
	if status, message := h.ensurePostVisible(ctx, &post, v, publisher); status != 0 {
		if status == 404 {
			httpx.NotFound(c, "Post not found.")
			return
		}
		apiErr := errs.New("FORBIDDEN", message, status)
		httpx.Fail(c, apiErr)
		return
	}
	h.enqueueView(ent.ID, v)
	if v.authed() {
		h.enqueueInterest(v.accountID, ent.ID, 0.2, "view")
	}
	httpx.OK(c, post)
}

func (h *handler) getPrevPost(c *gin.Context) { h.adjacentPost(c, false) }
func (h *handler) getNextPost(c *gin.Context) { h.adjacentPost(c, true) }

func (h *handler) adjacentPost(c *gin.Context, next bool) {
	ctx := c.Request.Context()
	v := h.newViewer(c)
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	var current store.PostEntity
	if err := h.db.WithContext(ctx).Where("id = ?", id).First(&current).Error; err != nil {
		httpx.Fail(c, errs.BadRequest("POST_CURRENT_NOT_FOUND", "Current post not found."))
		return
	}
	currentTime := current.CreatedAt
	if current.PublishedAt != nil {
		currentTime = *current.PublishedAt
	}

	requested, missing := h.resolvePublisherNames(ctx, parsePublisherNames(c.Query("pub")))
	if len(missing) > 0 {
		httpx.Fail(c, publisherNotFoundError("POST_PUBLISHER_NOT_FOUND", missing))
		return
	}
	publisherIDs := make([]uuid.UUID, 0, len(requested))
	for _, publisher := range requested {
		publisherIDs = append(publisherIDs, publisher.ID)
	}
	hasPublisherFilter := len(publisherIDs) > 0

	query := h.db.WithContext(ctx).Model(&store.PostEntity{}).
		Where("fediverse_uri IS NULL").
		Where("chained_post_id IS NULL")
	if hasPublisherFilter {
		query = query.Where("publisher_id IN ?", publisherIDs)
	}
	if typ := queryIntPtr(c, "type"); typ != nil {
		query = query.Where("type = ?", *typ)
	}
	if categories := queryList(c, "categories"); len(categories) > 0 {
		query = query.Where("id IN (SELECT posts_id FROM post_category_links l JOIN post_categories c ON c.id = l.categories_id WHERE lower(c.slug) IN ? AND c.deleted_at IS NULL)", lowerAll(categories))
	}
	if tags := queryList(c, "tags"); len(tags) > 0 {
		query = query.Where("id IN (SELECT posts_id FROM post_tag_links l JOIN post_tags t ON t.id = l.tags_id WHERE lower(t.slug) IN ? AND t.deleted_at IS NULL)", lowerAll(tags))
	}
	if onlyMedia := c.Query("media"); onlyMedia == "true" || onlyMedia == "1" {
		query = query.Where("COALESCE(jsonb_array_length(attachments), 0) > 0")
	}
	if start := queryInt64Ptr(c, "periodStart"); start != nil {
		query = query.Where("COALESCE(published_at, created_at) >= ?", time.Unix(*start, 0).UTC())
	}
	if end := queryInt64Ptr(c, "periodEnd"); end != nil {
		query = query.Where("COALESCE(published_at, created_at) <= ?", time.Unix(*end, 0).UTC())
	}
	pinned := queryBoolPtr(c, "pinned")
	realm := strings.TrimSpace(c.Query("realm"))
	switch {
	case pinned != nil && *pinned && realm != "":
		query = query.Where("pin_mode = ?", int(model.PinModeRealmPage))
	case pinned != nil && *pinned && hasPublisherFilter:
		query = query.Where("pin_mode = ?", int(model.PinModePublisherPage))
	case pinned != nil && *pinned:
		httpx.Fail(c, errs.BadRequest("POST_PINNED_FILTER_REQUIRES_PARAM",
			"You need pass extra realm or publisher params in order to filter with pinned posts."))
		return
	case pinned != nil && !*pinned:
		query = query.Where("pin_mode IS NULL")
	}
	if includeReplies := queryBoolPtr(c, "replies"); includeReplies != nil {
		if *includeReplies {
			query = query.Where("replied_post_id IS NOT NULL")
		} else {
			query = query.Where("replied_post_id IS NULL")
		}
	}
	query = applyTextSearch(query, c.Query("query"))
	info := h.gatekeepInfo(ctx, v, publisherIDs)
	query = h.applyVisibility(query, v, true, info, false)

	var ent store.PostEntity
	var err error
	if next {
		err = query.Where("COALESCE(published_at, created_at) > ?", currentTime).
			Order("COALESCE(published_at, created_at) ASC").First(&ent).Error
	} else {
		err = query.Where("COALESCE(published_at, created_at) < ?", currentTime).
			Order("COALESCE(published_at, created_at) DESC").First(&ent).Error
	}
	if err != nil {
		code := "POST_PREV_NOT_FOUND"
		message := "No previous post found"
		if next {
			code = "POST_NEXT_NOT_FOUND"
			message = "No next post found"
		}
		httpx.Fail(c, errs.New(code, message, http.StatusNotFound))
		return
	}
	publisher, _ := h.publisherByID(ctx, ent.PublisherID)
	post, err := h.hydrateOne(ctx, &ent, v, false, false)
	if err != nil {
		httpx.Internal(c)
		return
	}
	if status, message := h.ensurePostVisible(ctx, &post, v, publisher); status != 0 {
		if status == 404 {
			httpx.NotFound(c, "Post not found.")
			return
		}
		httpx.Fail(c, errs.New("FORBIDDEN", message, status))
		return
	}
	httpx.OK(c, post)
}

// ---------------------------------------------------------------------------
// Reactions
// ---------------------------------------------------------------------------

func (h *handler) getReactions(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	page, ok := h.pageFrom(c)
	if !ok {
		return
	}
	query := h.db.WithContext(ctx).Model(&store.PostReactionEntity{}).Where("post_id = ?", id)
	if symbol := c.Query("symbol"); symbol != "" {
		query = query.Where("symbol = ?", symbol)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	if strings.ToLower(c.Query("order")) == "created" {
		query = query.Order("created_at DESC")
	} else {
		query = query.Order("symbol ASC").Order("created_at DESC")
	}
	var rows []store.PostReactionEntity
	if err := query.Offset(page.Offset).Limit(page.Take).Find(&rows).Error; err != nil {
		httpx.Internal(c)
		return
	}
	reactions := make([]model.PostReaction, 0, len(rows))
	for i := range rows {
		reactions = append(reactions, postReactionToModel(&rows[i]))
	}
	httpx.List(c, reactions, total)
}

func (h *handler) listUserReactions(c *gin.Context) {
	ctx := c.Request.Context()
	name := c.Param("name")
	accountID := h.resolveAccountByName(ctx, name)
	if accountID == uuid.Nil {
		httpx.NotFound(c, "Account not found.")
		return
	}
	v := h.newViewer(c)
	page, ok := h.pageFrom(c)
	if !ok {
		return
	}

	visibleIDs, err := h.visiblePostIDsForReactions(ctx, accountID, v)
	if err != nil {
		httpx.Internal(c)
		return
	}
	query := h.db.WithContext(ctx).Model(&store.PostReactionEntity{}).
		Where("account_id = ?", accountID).
		Where("fediverse_uri IS NULL").
		Where("post_id IN ?", visibleIDs)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	var reactions []store.PostReactionEntity
	if err := query.Order("created_at DESC").Offset(page.Offset).Limit(page.Take).Find(&reactions).Error; err != nil {
		httpx.Internal(c)
		return
	}
	postIDs := make([]uuid.UUID, 0, len(reactions))
	for _, reaction := range reactions {
		postIDs = append(postIDs, reaction.PostID)
	}
	var ents []store.PostEntity
	if len(postIDs) > 0 {
		if err := h.db.WithContext(ctx).Where("id IN ?", postIDs).Find(&ents).Error; err != nil {
			httpx.Internal(c)
			return
		}
	}
	posts, err := h.hydrate(ctx, ents, v, true, true)
	if err != nil {
		httpx.Internal(c)
		return
	}
	byID := map[string]model.Post{}
	for _, post := range posts {
		byID[post.ID] = post
	}
	items := make([]model.UserReactionListingItem, 0, len(reactions))
	for i := range reactions {
		post, ok := byID[reactions[i].PostID.String()]
		if !ok {
			continue
		}
		items = append(items, model.UserReactionListingItem{
			Reaction: postReactionToModel(&reactions[i]),
			Post:     post,
		})
	}
	httpx.List(c, items, total)
}

func (h *handler) visiblePostIDsForReactions(ctx context.Context, accountID uuid.UUID, v *viewer) ([]uuid.UUID, error) {
	var publisherIDs []uuid.UUID
	if err := h.db.WithContext(ctx).Model(&store.PostReactionEntity{}).
		Joins("JOIN posts ON posts.id = post_reactions.post_id AND posts.deleted_at IS NULL").
		Where("post_reactions.account_id = ? AND post_reactions.fediverse_uri IS NULL AND post_reactions.deleted_at IS NULL", accountID).
		Distinct().Pluck("posts.publisher_id", &publisherIDs).Error; err != nil {
		return nil, err
	}
	info := h.gatekeepInfo(ctx, v, dedupeUUID(publisherIDs))
	query := h.db.WithContext(ctx).Model(&store.PostEntity{}).
		Where("id IN (SELECT post_id FROM post_reactions WHERE account_id = ? AND fediverse_uri IS NULL AND deleted_at IS NULL)", accountID)
	query = h.applyVisibility(query, v, true, info, false)
	var ids []uuid.UUID
	if err := query.Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

func (h *handler) resolveAccountByName(ctx context.Context, name string) uuid.UUID {
	if h.ports == nil {
		return uuid.Nil
	}
	accounts, ok := module.Get[ports.Accounts](h.ports, ports.PortAccounts)
	if !ok {
		return uuid.Nil
	}
	found, err := accounts.SearchAccounts(ctx, name, 10)
	if err != nil {
		return uuid.Nil
	}
	for _, account := range found {
		if strings.EqualFold(account.GetName(), name) {
			id, err := uuid.Parse(account.GetId())
			if err == nil {
				return id
			}
		}
	}
	return uuid.Nil
}

// ---------------------------------------------------------------------------
// Reply listings
// ---------------------------------------------------------------------------

func (h *handler) getFeaturedReply(c *gin.Context) {
	ctx := c.Request.Context()
	v := h.newViewer(c)
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	var rows []store.PostEntity
	if err := h.db.WithContext(ctx).Where("replied_post_id = ?", id).Find(&rows).Error; err != nil {
		httpx.Internal(c)
		return
	}
	publisherIDs := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		publisherIDs = append(publisherIDs, row.PublisherID)
	}
	info := h.gatekeepInfo(ctx, v, dedupeUUID(publisherIDs))
	now := time.Now().UTC()
	sort.SliceStable(rows, func(i, j int) bool {
		scoreI := rows[i].Upvotes*2 - rows[i].Downvotes
		if now.Sub(rows[i].CreatedAt) < time.Hour {
			scoreI += 5
		}
		scoreJ := rows[j].Upvotes*2 - rows[j].Downvotes
		if now.Sub(rows[j].CreatedAt) < time.Hour {
			scoreJ += 5
		}
		return scoreI > scoreJ
	})
	for i := range rows {
		post, err := h.hydrateOne(ctx, &rows[i], v, true, false)
		if err != nil {
			httpx.Internal(c)
			return
		}
		publisher, _ := h.publisherByID(ctx, rows[i].PublisherID)
		if !h.canViewPost(ctx, &post, v, accountIDOf(publisher)) {
			continue
		}
		if _, gatekept := info.gatekept[rows[i].PublisherID]; gatekept {
			if !v.isMember(rows[i].PublisherID) {
				if _, subscribed := info.subscribed[rows[i].PublisherID]; !subscribed {
					continue
				}
			}
		}
		httpx.OK(c, post)
		return
	}
	httpx.NotFound(c, "Post not found.")
}

func (h *handler) listPinnedReplies(c *gin.Context) {
	ctx := c.Request.Context()
	v := h.newViewer(c)
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	var rows []store.PostEntity
	if err := h.db.WithContext(ctx).
		Where("replied_post_id = ? AND pin_mode = ?", id, int(model.PinModeReplyPage)).
		Order("created_at DESC").Find(&rows).Error; err != nil {
		httpx.Internal(c)
		return
	}
	publisherIDs := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		publisherIDs = append(publisherIDs, row.PublisherID)
	}
	info := h.gatekeepInfo(ctx, v, dedupeUUID(publisherIDs))
	posts, err := h.hydrate(ctx, rows, v, false, true)
	if err != nil {
		httpx.Internal(c)
		return
	}
	posts = h.filterVisible(ctx, posts, v, info, false)
	httpx.OK(c, posts)
}

func (h *handler) listReplies(c *gin.Context) {
	ctx := c.Request.Context()
	v := h.newViewer(c)
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	page, ok := h.pageFrom(c)
	if !ok {
		return
	}
	var parent store.PostEntity
	if err := h.db.WithContext(ctx).Where("id = ?", id).First(&parent).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	rows, info, total, err := h.replyPage(ctx, id, v, page.Offset, page.Take, c.Query("order"), boolQuery(c, "orderDesc", true))
	if err != nil {
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

func boolQuery(c *gin.Context, name string, fallback bool) bool {
	raw, exists := c.GetQuery(name)
	if !exists {
		return fallback
	}
	return raw == "true" || raw == "1"
}

func (h *handler) replyPage(ctx context.Context, parentID uuid.UUID, v *viewer, offset, take int, order string, orderDesc bool) ([]store.PostEntity, gatekeep, int64, error) {
	var publisherIDs []uuid.UUID
	if err := h.db.WithContext(ctx).Model(&store.PostEntity{}).
		Where("replied_post_id = ?", parentID).Pluck("publisher_id", &publisherIDs).Error; err != nil {
		return nil, gatekeep{}, 0, err
	}
	info := h.gatekeepInfo(ctx, v, dedupeUUID(publisherIDs))

	countQuery := h.applyVisibility(h.db.WithContext(ctx).Model(&store.PostEntity{}).
		Where("replied_post_id = ?", parentID), v, true, info, false)
	var total int64
	if err := countQuery.Count(&total).Error; err != nil {
		return nil, info, 0, err
	}

	query := h.applyVisibility(h.db.WithContext(ctx).Model(&store.PostEntity{}).
		Where("replied_post_id = ?", parentID), v, true, info, false)
	query = applyPostOrdering(query, order, orderDesc, false, "")
	var rows []store.PostEntity
	if err := query.Offset(offset).Limit(take).Find(&rows).Error; err != nil {
		return nil, info, 0, err
	}
	return rows, info, total, nil
}

func (h *handler) listThreadedReplies(c *gin.Context) {
	ctx := c.Request.Context()
	v := h.newViewer(c)
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	page, ok := h.pageFrom(c)
	if !ok {
		return
	}
	var parent store.PostEntity
	if err := h.db.WithContext(ctx).Where("id = ?", id).First(&parent).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	rows, info, total, err := h.replyPage(ctx, id, v, page.Offset, page.Take, c.Query("order"), boolQuery(c, "orderDesc", true))
	if err != nil {
		httpx.Internal(c)
		return
	}
	c.Header("X-Total", strconv.FormatInt(total, 10))
	if len(rows) == 0 {
		httpx.OK(c, []model.ThreadedReplyNode{})
		return
	}

	posts, err := h.hydrate(ctx, rows, v, true, true)
	if err != nil {
		httpx.Internal(c)
		return
	}
	posts = h.filterVisible(ctx, posts, v, info, true)

	byParent := map[uuid.UUID][]model.Post{}
	visited := map[uuid.UUID]bool{}
	frontier := make([]uuid.UUID, 0, len(posts))

	// Build the tree level by level.
	nodes := map[uuid.UUID]model.Post{}
	for _, post := range posts {
		pid, _ := uuid.Parse(post.ID)
		nodes[pid] = post
		visited[pid] = true
	}
	frontier = frontier[:0]
	for _, post := range posts {
		frontier = append(frontier, uuid.MustParse(post.ID))
	}

	for len(frontier) > 0 {
		var children []store.PostEntity
		if err := h.applyVisibility(h.db.WithContext(ctx).Model(&store.PostEntity{}).
			Where("replied_post_id IN ?", frontier), v, true, info, false).
			Find(&children).Error; err != nil {
			httpx.Internal(c)
			return
		}
		children = filterNewEntities(children, visited)
		if len(children) == 0 {
			break
		}
		childPosts, err := h.hydrate(ctx, children, v, true, true)
		if err != nil {
			httpx.Internal(c)
			return
		}
		childPosts = h.filterVisible(ctx, childPosts, v, info, true)
		next := make([]uuid.UUID, 0, len(childPosts))
		for _, child := range childPosts {
			pid, _ := uuid.Parse(child.ID)
			visited[pid] = true
			if child.RepliedPostID != nil {
				parentID, err := uuid.Parse(*child.RepliedPostID)
				if err == nil {
					byParent[parentID] = append(byParent[parentID], child)
				}
			}
			nodes[pid] = child
			next = append(next, pid)
		}
		frontier = next
	}

	tree := make([]model.ThreadedReplyNode, 0)
	for i := range posts {
		pid, _ := uuid.Parse(posts[i].ID)
		root, ok := nodes[pid]
		if !ok {
			continue
		}
		flattenThreaded(root, byParent, 0, &tree)
	}
	httpx.OK(c, tree)
}

func filterNewEntities(rows []store.PostEntity, visited map[uuid.UUID]bool) []store.PostEntity {
	out := make([]store.PostEntity, 0, len(rows))
	for _, row := range rows {
		if visited[row.ID] {
			continue
		}
		out = append(out, row)
	}
	return out
}

func flattenThreaded(post model.Post, byParent map[uuid.UUID][]model.Post, depth int, out *[]model.ThreadedReplyNode) {
	post.RepliedPost = nil
	post.ForwardedPost = nil
	*out = append(*out, model.ThreadedReplyNode{
		Post:     post,
		Depth:    depth,
		ParentID: post.RepliedPostID,
	})
	id, err := uuid.Parse(post.ID)
	if err != nil {
		return
	}
	for _, reply := range byParent[id] {
		flattenThreaded(reply, byParent, depth+1, out)
	}
}

func (h *handler) getThread(c *gin.Context) {
	ctx := c.Request.Context()
	v := h.newViewer(c)
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	includeAncestors := boolQuery(c, "ancestors", true)
	ancestorLimit := queryInt(c, "ancestorLimit", 50)
	take := queryInt(c, "take", 20)
	if take <= 0 {
		take = 20
	}

	var ent store.PostEntity
	if err := h.applyDetailScope(h.db.WithContext(ctx).Model(&store.PostEntity{}), v).
		Where("id = ?", id).First(&ent).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	publisher, _ := h.publisherByID(ctx, ent.PublisherID)
	current, err := h.hydrateOne(ctx, &ent, v, false, false)
	if err != nil {
		httpx.Internal(c)
		return
	}
	if status, message := h.ensurePostVisible(ctx, &current, v, publisher); status != 0 {
		if status == 404 {
			httpx.NotFound(c, "Post not found.")
			return
		}
		httpx.Fail(c, errs.New("FORBIDDEN", message, status))
		return
	}

	var ancestorNodes []model.ThreadedReplyNode
	if includeAncestors {
		ancestorNodes = h.loadAncestors(ctx, v, &ent, ancestorLimit)
	}

	info := h.gatekeepInfo(ctx, v, nil)
	var rootRows []store.PostEntity
	if err := h.applyVisibility(h.db.WithContext(ctx).Model(&store.PostEntity{}).
		Where("replied_post_id = ?", id), v, true, info, false).
		Order("COALESCE(published_at, created_at) DESC").Find(&rootRows).Error; err != nil {
		httpx.Internal(c)
		return
	}
	rootPublisherIDs := make([]uuid.UUID, 0, len(rootRows))
	for _, row := range rootRows {
		rootPublisherIDs = append(rootPublisherIDs, row.PublisherID)
	}
	info = h.gatekeepInfo(ctx, v, dedupeUUID(rootPublisherIDs))

	rootPosts, err := h.hydrate(ctx, rootRows, v, true, true)
	if err != nil {
		httpx.Internal(c)
		return
	}
	rootPosts = h.filterVisible(ctx, rootPosts, v, info, true)

	byParent := map[uuid.UUID][]model.Post{}
	nodes := map[uuid.UUID]model.Post{}
	visited := map[uuid.UUID]bool{}
	frontier := make([]uuid.UUID, 0, len(rootPosts))
	for _, post := range rootPosts {
		pid, _ := uuid.Parse(post.ID)
		nodes[pid] = post
		visited[pid] = true
		frontier = append(frontier, pid)
	}
	for len(frontier) > 0 {
		var children []store.PostEntity
		if err := h.applyVisibility(h.db.WithContext(ctx).Model(&store.PostEntity{}).
			Where("replied_post_id IN ?", frontier), v, true, info, false).
			Order("COALESCE(published_at, created_at) DESC").Find(&children).Error; err != nil {
			httpx.Internal(c)
			return
		}
		children = filterNewEntities(children, visited)
		if len(children) == 0 {
			break
		}
		childPosts, err := h.hydrate(ctx, children, v, true, true)
		if err != nil {
			httpx.Internal(c)
			return
		}
		childPosts = h.filterVisible(ctx, childPosts, v, info, true)
		next := make([]uuid.UUID, 0, len(childPosts))
		for _, child := range childPosts {
			pid, _ := uuid.Parse(child.ID)
			visited[pid] = true
			if child.RepliedPostID != nil {
				parentID, err := uuid.Parse(*child.RepliedPostID)
				if err == nil {
					byParent[parentID] = append(byParent[parentID], child)
				}
			}
			nodes[pid] = child
			next = append(next, pid)
		}
		frontier = next
	}

	descendants := make([]model.ThreadedReplyNode, 0)
	for _, root := range rootPosts {
		flattenThreaded(root, byParent, 0, &descendants)
	}
	if len(descendants) > take {
		descendants = descendants[:take]
	}

	h.enqueueView(ent.ID, v)
	if v.authed() {
		h.enqueueInterest(v.accountID, ent.ID, 0.2, "view")
	}

	depth := len(ancestorNodes)
	httpx.OK(c, model.PostThreadResponse{
		Ancestors: ancestorNodes,
		Current: model.ThreadedReplyNode{
			Post:     current,
			Depth:    depth,
			ParentID: current.RepliedPostID,
		},
		Replies: descendants,
	})
}

func (h *handler) loadAncestors(ctx context.Context, v *viewer, ent *store.PostEntity, limit int) []model.ThreadedReplyNode {
	if ent.RepliedPostID == nil {
		return nil
	}
	var ids []uuid.UUID
	type row struct {
		ID uuid.UUID `gorm:"column:id"`
	}
	var rows []row
	query := `WITH RECURSIVE ancestor_chain AS (
		SELECT id, replied_post_id, 0 AS depth FROM posts WHERE id = ? AND replied_post_id IS NOT NULL
		UNION ALL
		SELECT p.id, p.replied_post_id, ac.depth + 1 FROM posts p
		INNER JOIN ancestor_chain ac ON p.id = ac.replied_post_id
		WHERE p.deleted_at IS NULL
	)
	SELECT id FROM ancestor_chain WHERE depth > 0 ORDER BY depth DESC LIMIT ?`
	if err := h.db.WithContext(ctx).Raw(query, ent.ID, limit).Scan(&rows).Error; err != nil {
		return nil
	}
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	var ents []store.PostEntity
	if err := h.db.WithContext(ctx).Where("id IN ?", ids).Find(&ents).Error; err != nil {
		return nil
	}
	posts, err := h.hydrate(ctx, ents, v, false, false)
	if err != nil {
		return nil
	}
	byID := map[uuid.UUID]model.Post{}
	for _, post := range posts {
		id, _ := uuid.Parse(post.ID)
		byID[id] = post
	}
	nodes := make([]model.ThreadedReplyNode, 0, len(ids))
	index := 0
	for _, id := range ids {
		post, ok := byID[id]
		if !ok {
			continue
		}
		nodes = append(nodes, model.ThreadedReplyNode{
			Post:     post,
			Depth:    index,
			ParentID: post.RepliedPostID,
		})
		index++
	}
	return nodes
}

// ---------------------------------------------------------------------------
// Chain and forwards
// ---------------------------------------------------------------------------

func (h *handler) getChain(c *gin.Context) {
	ctx := c.Request.Context()
	v := h.newViewer(c)
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
	headID := ent.ID
	if ent.ChainedPostID != nil {
		headID = *ent.ChainedPostID
	}
	var head store.PostEntity
	if err := h.applyDetailScope(h.db.WithContext(ctx).Model(&store.PostEntity{}), v).
		Where("id = ?", headID).First(&head).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	publisher, _ := h.publisherByID(ctx, head.PublisherID)
	post, err := h.hydrateOne(ctx, &head, v, false, false)
	if err != nil {
		httpx.Internal(c)
		return
	}
	if status, message := h.ensurePostVisible(ctx, &post, v, publisher); status != 0 {
		if status == 404 {
			httpx.NotFound(c, "Post not found.")
			return
		}
		httpx.Fail(c, errs.New("FORBIDDEN", message, status))
		return
	}
	chain := make([]model.Post, 0, len(post.ChainedPosts)+1)
	chain = append(chain, post)
	chain = append(chain, post.ChainedPosts...)
	chain[0].ChainedPosts = []model.Post{}
	chain[0].ChainedCount = 0
	httpx.OK(c, chain)
}

func (h *handler) listForwards(c *gin.Context) {
	ctx := c.Request.Context()
	v := h.newViewer(c)
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	page, ok := h.pageFrom(c)
	if !ok {
		return
	}
	var parent store.PostEntity
	if err := h.db.WithContext(ctx).Where("id = ?", id).First(&parent).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	var publisherIDs []uuid.UUID
	if err := h.db.WithContext(ctx).Model(&store.PostEntity{}).
		Where("forwarded_post_id = ?", id).Pluck("publisher_id", &publisherIDs).Error; err != nil {
		httpx.Internal(c)
		return
	}
	info := h.gatekeepInfo(ctx, v, dedupeUUID(publisherIDs))
	query := h.applyVisibility(h.db.WithContext(ctx).Model(&store.PostEntity{}).
		Where("forwarded_post_id = ?", id), v, true, info, false)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	var rows []store.PostEntity
	if err := query.Order("COALESCE(published_at, created_at) DESC").
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

