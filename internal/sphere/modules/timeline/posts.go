package timeline

import (
	"context"
	"html"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/filejson"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// visibilityFilter mirrors the arguments PostQueryExtensions.FilterWithVisibility
// receives from the timeline service.
type visibilityFilter struct {
	currentUser             *gen.DyAccount
	userFriends             []uuid.UUID
	publisherIDs            []uuid.UUID
	gatekeptPublisherIDs    []uuid.UUID
	followerPublisherIDs    []uuid.UUID
	blockedAccountIDs       []uuid.UUID
	mutedAccountIDs         []uuid.UUID
	closeFriendPublisherIDs []uuid.UUID
	blockedPublisherIDs     []uuid.UUID
	mutedPublisherIDs       []uuid.UUID
	showQuietPublic         bool
}

// feedQuery is the shared post query shape both feed entry points build.
type feedQuery struct {
	cursor               *time.Time
	filteredPublisherIDs []uuid.UUID
	realmIDs             []uuid.UUID
	scopedPublisherID    *uuid.UUID
	scopedCollectionID   *uuid.UUID
	limit                int
}

// buildPostsQuery mirrors TimelineService.BuildPostsQuery.
func (s *service) buildPostsQuery(ctx context.Context, q feedQuery) *gorm.DB {
	query := s.db.WithContext(ctx).Model(&store.PostEntity{}).
		Where("drafted_at IS NULL").
		Where("replied_post_id IS NULL").
		Where("chained_post_id IS NULL")
	if q.cursor != nil {
		query = query.Where("published_at < ?", *q.cursor)
	}
	query = query.Order("published_at DESC")
	if q.scopedPublisherID != nil {
		query = query.Where("publisher_id = ?", *q.scopedPublisherID)
	}
	if q.scopedCollectionID != nil {
		items := s.db.Model(&store.PostCollectionItemEntity{}).
			Select("post_id").
			Where("collection_id = ?", *q.scopedCollectionID)
		query = query.Where("id IN (?)", items)
	}
	if len(q.filteredPublisherIDs) > 0 {
		query = query.Where("publisher_id IN ?", q.filteredPublisherIDs)
	}
	if q.realmIDs == nil {
		query = query.Where("realm_id IS NULL")
	} else {
		query = query.Where("realm_id IS NULL OR realm_id IN ?", q.realmIDs)
	}
	if q.limit > 0 {
		query = query.Limit(q.limit)
	}
	return query
}

// publishersOfAccounts is the subquery used for account-scoped publisher
// predicates.
func (s *service) publishersOfAccounts(accountIDs []uuid.UUID) *gorm.DB {
	return s.db.Model(&store.PublisherEntity{}).Select("id").Where("account_id IN ?", accountIDs)
}

// applyVisibility mirrors PostQueryExtensions.FilterWithVisibility with
// isListing=true.
func (s *service) applyVisibility(query *gorm.DB, f visibilityFilter) *gorm.DB {
	now := time.Now().UTC()
	hasPublishers := len(f.publisherIDs) > 0

	// Listing always hides drafts (the base query already does, this keeps the
	// extension's own contract).
	query = query.Where("posts.drafted_at IS NULL")

	if f.currentUser != nil && hasPublishers {
		query = query.Where("posts.visibility != ? OR posts.publisher_id IN ?", model.VisibilityUnlisted, f.publisherIDs)
	} else {
		query = query.Where("posts.visibility != ?", model.VisibilityUnlisted)
	}

	if f.currentUser == nil {
		if len(f.gatekeptPublisherIDs) > 0 {
			query = query.Where("posts.publisher_id NOT IN ?", f.gatekeptPublisherIDs)
		}
		return query.
			Where("posts.published_at IS NOT NULL AND posts.published_at <= ?", now).
			Where("posts.visibility = ?", model.VisibilityPublic)
	}

	if hasPublishers {
		query = query.Where(
			"(posts.published_at IS NOT NULL AND posts.published_at <= ?) OR posts.publisher_id IN ?",
			now, f.publisherIDs,
		)
	} else {
		query = query.Where("posts.published_at IS NOT NULL AND posts.published_at <= ?", now)
	}

	if hasPublishers {
		query = query.Where("posts.visibility != ? OR posts.publisher_id IN ?", model.VisibilityPrivate, f.publisherIDs)
	} else {
		query = query.Where("posts.visibility != ?", model.VisibilityPrivate)
	}

	switch {
	case len(f.userFriends) > 0 && hasPublishers:
		query = query.Where(
			"posts.visibility != ? OR posts.publisher_id IN (?) OR posts.publisher_id IN ?",
			model.VisibilityFriends, s.publishersOfAccounts(f.userFriends), f.publisherIDs,
		)
	case len(f.userFriends) > 0:
		query = query.Where(
			"posts.visibility != ? OR posts.publisher_id IN (?)",
			model.VisibilityFriends, s.publishersOfAccounts(f.userFriends),
		)
	case hasPublishers:
		query = query.Where("posts.visibility != ? OR posts.publisher_id IN ?", model.VisibilityFriends, f.publisherIDs)
	default:
		query = query.Where("posts.visibility != ?", model.VisibilityFriends)
	}

	switch {
	case len(f.closeFriendPublisherIDs) > 0 && hasPublishers:
		query = query.Where(
			"posts.visibility != ? OR posts.publisher_id IN ? OR posts.publisher_id IN ?",
			model.VisibilityCloseFriendsOnly, f.publisherIDs, f.closeFriendPublisherIDs,
		)
	case len(f.closeFriendPublisherIDs) > 0:
		query = query.Where(
			"posts.visibility != ? OR posts.publisher_id IN ?",
			model.VisibilityCloseFriendsOnly, f.closeFriendPublisherIDs,
		)
	case hasPublishers:
		query = query.Where(
			"posts.visibility != ? OR posts.publisher_id IN ?",
			model.VisibilityCloseFriendsOnly, f.publisherIDs,
		)
	default:
		query = query.Where("posts.visibility != ?", model.VisibilityCloseFriendsOnly)
	}

	switch {
	case f.showQuietPublic:
		// No restriction.
	case len(f.followerPublisherIDs) > 0 && hasPublishers:
		query = query.Where(
			"posts.visibility != ? OR posts.publisher_id IN ? OR posts.publisher_id IN ?",
			model.VisibilityQuietPublic, f.publisherIDs, f.followerPublisherIDs,
		)
	case len(f.followerPublisherIDs) > 0:
		query = query.Where(
			"posts.visibility != ? OR posts.publisher_id IN ?",
			model.VisibilityQuietPublic, f.followerPublisherIDs,
		)
	case hasPublishers:
		query = query.Where("posts.visibility != ? OR posts.publisher_id IN ?", model.VisibilityQuietPublic, f.publisherIDs)
	default:
		query = query.Where("posts.visibility != ?", model.VisibilityQuietPublic)
	}

	if len(f.gatekeptPublisherIDs) > 0 && f.followerPublisherIDs != nil {
		query = query.Where(
			"posts.publisher_id NOT IN ? OR posts.publisher_id IN ? OR posts.publisher_id IN ?",
			f.gatekeptPublisherIDs, f.publisherIDs, f.followerPublisherIDs,
		)
	}

	if len(f.blockedAccountIDs) > 0 {
		query = query.Where("posts.publisher_id NOT IN (?)", s.publishersOfAccounts(f.blockedAccountIDs))
	}
	if len(f.mutedAccountIDs) > 0 {
		query = query.Where("posts.publisher_id NOT IN (?)", s.publishersOfAccounts(f.mutedAccountIDs))
	}
	if len(f.blockedPublisherIDs) > 0 {
		query = query.Where("posts.publisher_id NOT IN ?", f.blockedPublisherIDs)
	}
	if len(f.mutedPublisherIDs) > 0 {
		query = query.Where("posts.publisher_id NOT IN ?", f.mutedPublisherIDs)
	}
	return query
}

// canViewPost mirrors PostService.CanViewPost for embedded posts.
func canViewPost(post *model.Post, viewer *gen.DyAccount, publishers []uuid.UUID, userFriends []uuid.UUID, now time.Time) bool {
	if post == nil {
		return false
	}
	if viewer == nil {
		return post.DraftedAt == nil &&
			post.PublishedAt != nil && !post.PublishedAt.Time().After(now) &&
			post.Visibility == int(model.VisibilityPublic)
	}
	isPublished := post.DraftedAt == nil && post.PublishedAt != nil && !post.PublishedAt.Time().After(now)
	isMember := post.PublisherID != "" && containsUUID(publishers, post.PublisherID)
	if !isPublished && !isMember {
		return false
	}
	switch model.PostVisibility(post.Visibility) {
	case model.VisibilityPrivate:
		return isMember
	case model.VisibilityFriends:
		if isMember {
			return true
		}
		return post.Publisher != nil && post.Publisher.AccountID != nil &&
			containsUUID(userFriends, *post.Publisher.AccountID)
	}
	return true
}

func containsUUID(ids []uuid.UUID, raw string) bool {
	target, err := uuid.Parse(raw)
	if err != nil {
		return false
	}
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

// hydrateContext carries the viewer state post hydration needs.
type hydrateContext struct {
	viewerAccountID *uuid.UUID
	currentUser     *gen.DyAccount
	userFriends     []uuid.UUID
	userPublishers  []uuid.UUID
}

// hydratePosts maps post rows to the wire shape the C# LoadPostInfo produced:
// publisher, realm, tags, categories, collections, featured records, reaction
// maps, bookmark state, reply counters and the embedded replied/forwarded
// posts (gated by visibility).
func (s *service) hydratePosts(ctx context.Context, entities []store.PostEntity, hc hydrateContext) []model.Post {
	if len(entities) == 0 {
		return []model.Post{}
	}
	posts := make([]model.Post, 0, len(entities))
	for i := range entities {
		posts = append(posts, store.PostToModel(&entities[i]))
	}

	ids := make([]uuid.UUID, 0, len(entities))
	for i := range entities {
		ids = append(ids, entities[i].ID)
	}

	s.loadPublishers(ctx, posts)
	s.loadPostRealms(ctx, posts)
	s.loadTags(ctx, posts, ids)
	s.loadCategories(ctx, posts, ids)
	s.loadCollections(ctx, posts, ids)
	s.loadFeaturedRecords(ctx, posts, ids)
	s.loadInteractive(ctx, posts, ids, hc.viewerAccountID)
	s.loadEmbeddedPosts(ctx, posts, hc)
	return posts
}

// loadPublishers attaches the publisher snapshot, including the individual
// account when the profile service is configured.
func (s *service) loadPublishers(ctx context.Context, posts []model.Post) {
	ids := make([]uuid.UUID, 0, len(posts))
	seen := map[uuid.UUID]struct{}{}
	for i := range posts {
		id, err := uuid.Parse(posts[i].PublisherID)
		if err != nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return
	}
	var rows []store.PublisherEntity
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		s.log.Warn("timeline: load publishers", "error", err)
		return
	}
	byID := make(map[string]model.Publisher, len(rows))
	accountIDs := make([]string, 0, len(rows))
	for i := range rows {
		publisher := store.PublisherToModel(&rows[i])
		byID[publisher.ID] = publisher
		if rows[i].Type == int(model.PublisherIndividual) && rows[i].AccountID != nil {
			accountIDs = append(accountIDs, rows[i].AccountID.String())
		}
	}
	accounts := s.accountBatch(ctx, accountIDs)
	for i := range posts {
		publisher, ok := byID[posts[i].PublisherID]
		if !ok {
			continue
		}
		if publisher.AccountID != nil {
			if account, ok := accounts[*publisher.AccountID]; ok {
				publisher.Account = account
			}
		}
		posts[i].Publisher = &publisher
	}
}

// accountBatch fetches account references, degrading to an empty map when the
// profile service is not configured.
func (s *service) accountBatch(ctx context.Context, ids []string) map[string]any {
	out := map[string]any{}
	if len(ids) == 0 || s.clients == nil || s.clients.Profile == nil {
		return out
	}
	resp, err := s.clients.Profile.GetAccountBatch(ctx, &gen.DyGetAccountBatchRequest{Id: ids})
	if err != nil {
		s.log.Warn("timeline: account batch", "error", err)
		return out
	}
	for _, account := range resp.GetAccounts() {
		if account == nil {
			continue
		}
		out[account.GetId()] = accountRefFromProto(account)
	}
	return out
}

// loadPostRealms attaches the realm snapshot the C# service loaded through
// RemoteRealmService. The realms port answers first; the realm gRPC client
// fills in the ids the port could not resolve.
func (s *service) loadPostRealms(ctx context.Context, posts []model.Post) {
	ids := make([]string, 0, len(posts))
	seen := map[string]struct{}{}
	for i := range posts {
		if posts[i].RealmID == nil {
			continue
		}
		if _, ok := seen[*posts[i].RealmID]; ok {
			continue
		}
		seen[*posts[i].RealmID] = struct{}{}
		ids = append(ids, *posts[i].RealmID)
	}
	if len(ids) == 0 {
		return
	}
	realms := s.realmRefs(ctx, ids)
	for i := range posts {
		if posts[i].RealmID == nil {
			continue
		}
		if realm, ok := realms[*posts[i].RealmID]; ok {
			posts[i].Realm = realm
		}
	}
}

// realmBatch resolves realms by id, degrading to an empty map when the realm
// service is not configured.
func (s *service) realmBatch(ctx context.Context, ids []string) map[string]*gen.DyRealm {
	out := map[string]*gen.DyRealm{}
	if len(ids) == 0 || s.clients == nil || s.clients.Realm == nil {
		return out
	}
	resp, err := s.clients.Realm.GetRealmBatch(ctx, &gen.DyGetRealmBatchRequest{Ids: ids})
	if err != nil {
		s.log.Warn("timeline: realm batch", "error", err)
		return out
	}
	for _, realm := range resp.GetRealms() {
		if realm == nil {
			continue
		}
		out[realm.GetId()] = realm
	}
	return out
}

// loadTags attaches post tags through the legacy join table.
func (s *service) loadTags(ctx context.Context, posts []model.Post, ids []uuid.UUID) {
	var links []struct {
		PostID uuid.UUID
		TagID  uuid.UUID
	}
	if err := s.db.WithContext(ctx).Table("post_tag_links").
		Select("posts_id AS post_id, tags_id AS tag_id").
		Where("posts_id IN ?", ids).
		Scan(&links).Error; err != nil {
		s.log.Warn("timeline: load tag links", "error", err)
		return
	}
	if len(links) == 0 {
		return
	}
	tagIDs := make([]uuid.UUID, 0, len(links))
	byPost := make(map[uuid.UUID][]uuid.UUID, len(links))
	for _, link := range links {
		tagIDs = append(tagIDs, link.TagID)
		byPost[link.PostID] = append(byPost[link.PostID], link.TagID)
	}
	var rows []store.PostTagEntity
	if err := s.db.WithContext(ctx).Where("id IN ?", tagIDs).Find(&rows).Error; err != nil {
		s.log.Warn("timeline: load tags", "error", err)
		return
	}
	byID := make(map[uuid.UUID]model.PostTag, len(rows))
	for i := range rows {
		byID[rows[i].ID] = tagToModel(&rows[i])
	}
	for i := range posts {
		postID, err := uuid.Parse(posts[i].ID)
		if err != nil {
			continue
		}
		for _, tagID := range byPost[postID] {
			if tag, ok := byID[tagID]; ok {
				posts[i].Tags = append(posts[i].Tags, tag)
			}
		}
	}
}

// loadCategories attaches post categories through the legacy join table.
func (s *service) loadCategories(ctx context.Context, posts []model.Post, ids []uuid.UUID) {
	var links []struct {
		PostID     uuid.UUID
		CategoryID uuid.UUID
	}
	if err := s.db.WithContext(ctx).Table("post_category_links").
		Select("posts_id AS post_id, categories_id AS category_id").
		Where("posts_id IN ?", ids).
		Scan(&links).Error; err != nil {
		s.log.Warn("timeline: load category links", "error", err)
		return
	}
	if len(links) == 0 {
		return
	}
	categoryIDs := make([]uuid.UUID, 0, len(links))
	byPost := make(map[uuid.UUID][]uuid.UUID, len(links))
	for _, link := range links {
		categoryIDs = append(categoryIDs, link.CategoryID)
		byPost[link.PostID] = append(byPost[link.PostID], link.CategoryID)
	}
	var rows []store.PostCategoryEntity
	if err := s.db.WithContext(ctx).Where("id IN ?", categoryIDs).Find(&rows).Error; err != nil {
		s.log.Warn("timeline: load categories", "error", err)
		return
	}
	byID := make(map[uuid.UUID]model.PostCategory, len(rows))
	for i := range rows {
		byID[rows[i].ID] = categoryToModel(&rows[i])
	}
	for i := range posts {
		postID, err := uuid.Parse(posts[i].ID)
		if err != nil {
			continue
		}
		for _, categoryID := range byPost[postID] {
			if category, ok := byID[categoryID]; ok {
				posts[i].Categories = append(posts[i].Categories, category)
			}
		}
	}
}

// loadCollections attaches the publisher collections a post belongs to.
func (s *service) loadCollections(ctx context.Context, posts []model.Post, ids []uuid.UUID) {
	var items []store.PostCollectionItemEntity
	if err := s.db.WithContext(ctx).Where("post_id IN ?", ids).Find(&items).Error; err != nil {
		s.log.Warn("timeline: load collection items", "error", err)
		return
	}
	if len(items) == 0 {
		return
	}
	collectionIDs := make([]uuid.UUID, 0, len(items))
	byPost := make(map[uuid.UUID][]uuid.UUID, len(items))
	for _, item := range items {
		collectionIDs = append(collectionIDs, item.CollectionID)
		byPost[item.PostID] = append(byPost[item.PostID], item.CollectionID)
	}
	var rows []store.PostCollectionEntity
	if err := s.db.WithContext(ctx).Where("id IN ?", collectionIDs).Find(&rows).Error; err != nil {
		s.log.Warn("timeline: load collections", "error", err)
		return
	}
	byID := make(map[uuid.UUID]model.PostCollection, len(rows))
	for i := range rows {
		byID[rows[i].ID] = model.PostCollection{
			ID:          rows[i].ID.String(),
			Slug:        rows[i].Slug,
			Name:        rows[i].Name,
			Description: rows[i].Description,
			PublisherID: rows[i].PublisherID.String(),
			Background:  filejson.Value(rows[i].Background),
			Icon:        filejson.Value(rows[i].Icon),
			CreatedAt:   model.NewTime(rows[i].CreatedAt),
			UpdatedAt:   model.NewTime(rows[i].UpdatedAt),
			DeletedAt:   store.DeletedPtr(rows[i].DeletedAt),
		}
	}
	for i := range posts {
		postID, err := uuid.Parse(posts[i].ID)
		if err != nil {
			continue
		}
		for _, collectionID := range byPost[postID] {
			if collection, ok := byID[collectionID]; ok {
				posts[i].PublisherCollections = append(posts[i].PublisherCollections, collection)
			}
		}
	}
}

// loadFeaturedRecords attaches featured records.
func (s *service) loadFeaturedRecords(ctx context.Context, posts []model.Post, ids []uuid.UUID) {
	var rows []store.PostFeaturedRecordEntity
	if err := s.db.WithContext(ctx).Where("post_id IN ?", ids).Find(&rows).Error; err != nil {
		s.log.Warn("timeline: load featured records", "error", err)
		return
	}
	byPost := make(map[uuid.UUID][]model.PostFeaturedRecord, len(rows))
	for i := range rows {
		byPost[rows[i].PostID] = append(byPost[rows[i].PostID], model.PostFeaturedRecord{
			ID:            rows[i].ID.String(),
			PostID:        rows[i].PostID.String(),
			FeaturedAt:    store.TimePtr(rows[i].FeaturedAt),
			SocialCredits: rows[i].SocialCredits,
			CreatedAt:     model.NewTime(rows[i].CreatedAt),
			UpdatedAt:     model.NewTime(rows[i].UpdatedAt),
			DeletedAt:     store.DeletedPtr(rows[i].DeletedAt),
		})
	}
	for i := range posts {
		postID, err := uuid.Parse(posts[i].ID)
		if err != nil {
			continue
		}
		if records, ok := byPost[postID]; ok {
			posts[i].FeaturedRecords = records
		}
	}
}

// loadInteractive fills the reaction, bookmark and reply counters.
func (s *service) loadInteractive(ctx context.Context, posts []model.Post, ids []uuid.UUID, viewer *uuid.UUID) {
	var reactionRows []struct {
		PostID uuid.UUID
		Symbol string
		Count  int
	}
	if err := s.db.WithContext(ctx).Model(&store.PostReactionEntity{}).
		Select("post_id, symbol, count(*) AS count").
		Where("post_id IN ?", ids).
		Group("post_id, symbol").
		Scan(&reactionRows).Error; err != nil {
		s.log.Warn("timeline: load reactions", "error", err)
	}
	reactionCounts := make(map[uuid.UUID]map[string]int)
	for _, row := range reactionRows {
		if reactionCounts[row.PostID] == nil {
			reactionCounts[row.PostID] = map[string]int{}
		}
		reactionCounts[row.PostID][row.Symbol] = row.Count
	}

	reactionMade := make(map[uuid.UUID]map[string]bool)
	bookmarks := map[uuid.UUID]struct{}{}
	if viewer != nil {
		var madeRows []struct {
			PostID uuid.UUID
			Symbol string
		}
		if err := s.db.WithContext(ctx).Model(&store.PostReactionEntity{}).
			Select("post_id, symbol").
			Where("post_id IN ? AND account_id = ?", ids, *viewer).
			Scan(&madeRows).Error; err == nil {
			for _, row := range madeRows {
				if reactionMade[row.PostID] == nil {
					reactionMade[row.PostID] = map[string]bool{}
				}
				reactionMade[row.PostID][row.Symbol] = true
			}
		}
		var bookmarkIDs []uuid.UUID
		if err := s.db.WithContext(ctx).Model(&store.PostBookmarkEntity{}).
			Where("post_id IN ? AND account_id = ?", ids, *viewer).
			Pluck("post_id", &bookmarkIDs).Error; err == nil {
			for _, id := range bookmarkIDs {
				bookmarks[id] = struct{}{}
			}
		}
	}

	var replyRows []struct {
		RepliedPostID uuid.UUID
		Count         int
	}
	if err := s.db.WithContext(ctx).Model(&store.PostEntity{}).
		Select("replied_post_id, count(*) AS count").
		Where("replied_post_id IN ?", ids).
		Group("replied_post_id").
		Scan(&replyRows).Error; err != nil {
		s.log.Warn("timeline: load reply counts", "error", err)
	}
	replies := make(map[uuid.UUID]int, len(replyRows))
	for _, row := range replyRows {
		replies[row.RepliedPostID] = row.Count
	}

	threadReplies := make(map[uuid.UUID]int)
	var threadRows []struct {
		AncestorID uuid.UUID
		Count      int
	}
	if err := s.db.WithContext(ctx).Raw(`
		WITH RECURSIVE reply_tree AS (
			SELECT replied_post_id AS ancestor_id, id AS descendant_id
			FROM posts
			WHERE replied_post_id IN ? AND deleted_at IS NULL
			UNION ALL
			SELECT reply_tree.ancestor_id, posts.id AS descendant_id
			FROM posts
			INNER JOIN reply_tree ON posts.replied_post_id = reply_tree.descendant_id
			WHERE posts.deleted_at IS NULL
		)
		SELECT ancestor_id, COUNT(*)::int AS count
		FROM reply_tree
		GROUP BY ancestor_id`, ids).Scan(&threadRows).Error; err != nil {
		s.log.Warn("timeline: load thread reply counts", "error", err)
	} else {
		for _, row := range threadRows {
			threadReplies[row.AncestorID] = row.Count
		}
	}

	for i := range posts {
		postID, err := uuid.Parse(posts[i].ID)
		if err != nil {
			continue
		}
		if counts, ok := reactionCounts[postID]; ok {
			posts[i].ReactionsCount = counts
		}
		if made, ok := reactionMade[postID]; ok {
			posts[i].ReactionsMade = made
		}
		if _, ok := bookmarks[postID]; ok {
			posts[i].IsBookmarked = true
		}
		posts[i].RepliesCount = replies[postID]
		posts[i].ThreadRepliesCount = threadReplies[postID]
	}
}

// loadEmbeddedPosts attaches the replied and forwarded post snapshots, hiding
// the ones the viewer may not see.
func (s *service) loadEmbeddedPosts(ctx context.Context, posts []model.Post, hc hydrateContext) {
	ids := make([]uuid.UUID, 0, len(posts)*2)
	seen := map[uuid.UUID]struct{}{}
	add := func(raw *string) {
		if raw == nil {
			return
		}
		id, err := uuid.Parse(*raw)
		if err != nil {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for i := range posts {
		add(posts[i].RepliedPostID)
		add(posts[i].ForwardedPostID)
	}
	if len(ids) == 0 {
		return
	}
	var rows []store.PostEntity
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		s.log.Warn("timeline: load embedded posts", "error", err)
		return
	}
	embedded := make([]model.Post, 0, len(rows))
	for i := range rows {
		embedded = append(embedded, store.PostToModel(&rows[i]))
	}
	s.loadPublishers(ctx, embedded)
	s.loadPostRealms(ctx, embedded)

	byID := make(map[string]model.Post, len(embedded))
	for i := range embedded {
		byID[embedded[i].ID] = embedded[i]
	}
	now := time.Now().UTC()
	for i := range posts {
		if posts[i].RepliedPostID != nil {
			if child, ok := byID[*posts[i].RepliedPostID]; ok {
				if canViewPost(&child, hc.currentUser, hc.userPublishers, hc.userFriends, now) {
					posts[i].RepliedPost = &child
				} else {
					posts[i].RepliedGone = true
				}
			}
		}
		if posts[i].ForwardedPostID != nil {
			if child, ok := byID[*posts[i].ForwardedPostID]; ok {
				if canViewPost(&child, hc.currentUser, hc.userPublishers, hc.userFriends, now) {
					posts[i].ForwardedPost = &child
				} else {
					posts[i].ForwardedGone = true
				}
			}
		}
	}
}

// attachChainedPosts mirrors LoadChainedChildrenAsync: the thread below each
// head post, visibility gated and hydrated like a timeline item.
func (s *service) attachChainedPosts(ctx context.Context, posts []model.Post, hc hydrateContext) {
	headIDs := make([]uuid.UUID, 0, len(posts))
	for i := range posts {
		if posts[i].ChainedPostID != nil {
			continue
		}
		id, err := uuid.Parse(posts[i].ID)
		if err != nil {
			continue
		}
		headIDs = append(headIDs, id)
	}
	if len(headIDs) == 0 {
		return
	}
	var rows []store.PostEntity
	if err := s.db.WithContext(ctx).
		Where("chained_post_id IN ?", headIDs).
		Order("published_at ASC").
		Find(&rows).Error; err != nil {
		s.log.Warn("timeline: load chained posts", "error", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	children := s.hydratePosts(ctx, rows, hc)
	now := time.Now().UTC()
	byHead := make(map[string][]model.Post)
	for i := range rows {
		if rows[i].ChainedPostID == nil {
			continue
		}
		head := rows[i].ChainedPostID.String()
		child := children[i]
		if !canViewPost(&child, hc.currentUser, hc.userPublishers, hc.userFriends, now) {
			continue
		}
		byHead[head] = append(byHead[head], child)
	}
	for i := range posts {
		if posts[i].ChainedPostID != nil {
			continue
		}
		if list, ok := byHead[posts[i].ID]; ok {
			posts[i].ChainedPosts = list
			posts[i].ChainedCount = len(list)
		}
	}
}

// finalizePostContent mirrors TruncatePostContent + EscapeMarkdownHeadings.
func finalizePostContent(posts []model.Post) {
	const maxLength = 256
	const embedMaxLength = 80
	for i := range posts {
		truncateContent(&posts[i], maxLength)
		if posts[i].RepliedPost != nil && posts[i].Content != nil && len(*posts[i].Content) > embedMaxLength {
			truncateContent(posts[i].RepliedPost, embedMaxLength)
		}
		if posts[i].ForwardedPost != nil && posts[i].Content != nil && len(*posts[i].Content) > embedMaxLength {
			truncateContent(posts[i].ForwardedPost, embedMaxLength)
		}
		escapeMarkdownHeadings(&posts[i])
		for j := range posts[i].ChainedPosts {
			truncateContent(&posts[i].ChainedPosts[j], maxLength)
			escapeMarkdownHeadings(&posts[i].ChainedPosts[j])
		}
	}
}

func truncateContent(post *model.Post, limit int) {
	if post.Content == nil || len(*post.Content) <= limit {
		return
	}
	var plain string
	switch model.PostContentType(post.ContentType) {
	case model.ContentTypeMarkdown:
		plain = markdownToPlainText(*post.Content)
	case model.ContentTypeHTML:
		plain = htmlToPlainText(*post.Content)
	default:
		return
	}
	if len([]rune(plain)) <= limit {
		return
	}
	runes := []rune(plain)
	value := string(runes[:limit])
	post.Content = &value
	post.IsTruncated = true
}

var (
	htmlTagPattern     = regexp.MustCompile(`(?s)<[^>]*>`)
	markdownImageRegex = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	markdownLinkRegex  = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	markdownNoiseRegex = regexp.MustCompile("[*_~`>#]+")
	blankLinesRegex    = regexp.MustCompile(`\n{2,}`)
)

// markdownToPlainText is a light markdown stripper; it approximates
// Markdown.ToPlainText without pulling a parser dependency.
func markdownToPlainText(content string) string {
	text := markdownImageRegex.ReplaceAllString(content, "$1")
	text = markdownLinkRegex.ReplaceAllString(text, "$1")
	text = htmlTagPattern.ReplaceAllString(text, "")
	text = markdownNoiseRegex.ReplaceAllString(text, "")
	text = html.UnescapeString(text)
	text = blankLinesRegex.ReplaceAllString(text, "\n")
	return strings.TrimSpace(text)
}

func htmlToPlainText(content string) string {
	text := htmlTagPattern.ReplaceAllString(content, "")
	return strings.TrimSpace(html.UnescapeString(text))
}

// escapeMarkdownHeadings mirrors PostService.EscapeMarkdownHeadings.
func escapeMarkdownHeadings(post *model.Post) {
	if post.Type != int(model.PostTypeMoment) || post.ContentType != int(model.ContentTypeMarkdown) {
		return
	}
	if post.Content == nil || *post.Content == "" {
		return
	}
	lines := strings.Split(*post.Content, "\n")
	inFence := false
	fenceMarker := ""
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed != "" && (trimmed[0] == '`' || trimmed[0] == '~') {
			marker := trimmed[0]
			run := 0
			for run < len(trimmed) && trimmed[run] == marker {
				run++
			}
			if run >= 3 {
				if !inFence {
					inFence = true
					fenceMarker = strings.Repeat(string(marker), run)
				} else if strings.HasPrefix(trimmed, fenceMarker) &&
					strings.TrimSpace(trimmed[len(fenceMarker):]) == "" {
					inFence = false
					fenceMarker = ""
				}
			}
			continue
		}
		if inFence {
			continue
		}
		index := 0
		for index < len(line) && index < 3 && line[index] == ' ' {
			index++
		}
		hashStart := index
		for index < len(line) && line[index] == '#' && index-hashStart < 6 {
			index++
		}
		if index-hashStart >= 1 && index-hashStart <= 6 &&
			(index == len(line) || line[index] == ' ' || line[index] == '\t') {
			lines[i] = line[:hashStart] + "\\" + line[hashStart:]
		}
	}
	value := strings.Join(lines, "\n")
	post.Content = &value
}

// tagToModel maps a tag row to the wire type.
func tagToModel(entity *store.PostTagEntity) model.PostTag {
	return model.PostTag{
		ID:               entity.ID.String(),
		Slug:             entity.Slug,
		Name:             entity.Name,
		Description:      entity.Description,
		OwnerPublisherID: store.UUIDPtrString(entity.OwnerPublisherID),
		IsProtected:      entity.IsProtected,
		IsEvent:          entity.IsEvent,
		EventEndsAt:      store.TimePtr(entity.EventEndsAt),
		CreatedAt:        model.NewTime(entity.CreatedAt),
		UpdatedAt:        model.NewTime(entity.UpdatedAt),
		DeletedAt:        store.DeletedPtr(entity.DeletedAt),
	}
}

// categoryToModel maps a category row to the wire type.
func categoryToModel(entity *store.PostCategoryEntity) model.PostCategory {
	return model.PostCategory{
		ID:        entity.ID.String(),
		Slug:      entity.Slug,
		Name:      entity.Name,
		CreatedAt: model.NewTime(entity.CreatedAt),
		UpdatedAt: model.NewTime(entity.UpdatedAt),
		DeletedAt: store.DeletedPtr(entity.DeletedAt),
	}
}
