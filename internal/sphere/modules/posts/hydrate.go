package posts

import (
	"context"
	"html"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/accountjson"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/filejson"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

// postRelations is the batch-loaded relation set used to hydrate posts.
type postRelations struct {
	publishers        map[uuid.UUID]*store.PublisherEntity
	publisherAccounts map[uuid.UUID]any
	accounts          map[uuid.UUID]any
	tags              map[uuid.UUID][]model.PostTag
	categories        map[uuid.UUID][]model.PostCategory
	featured          map[uuid.UUID][]model.PostFeaturedRecord
	reactionCounts    map[uuid.UUID]map[string]int
	reactionsMade     map[uuid.UUID]map[string]bool
	bookmarked        map[uuid.UUID]bool
	replies           map[uuid.UUID]int
	threadReplies     map[uuid.UUID]int
	collections       map[uuid.UUID][]model.PostCollection
	realms            map[uuid.UUID]*model.RealmRef
}

// hydrate converts post rows into wire posts with every relation the C#
// LoadPostInfo path fills. trackViews enqueues the buffered view counters.
func (h *handler) hydrate(ctx context.Context, ents []store.PostEntity, v *viewer, truncate bool, trackViews bool) ([]model.Post, error) {
	if len(ents) == 0 {
		return []model.Post{}, nil
	}
	ids := make([]uuid.UUID, 0, len(ents))
	for i := range ents {
		ids = append(ids, ents[i].ID)
	}

	relatedEnts, err := h.loadRelatedPosts(ctx, ents)
	if err != nil {
		return nil, err
	}
	childEnts, err := h.loadChainedChildren(ctx, ents)
	if err != nil {
		return nil, err
	}

	allEnts := make([]store.PostEntity, 0, len(ents)+len(relatedEnts)+len(childEnts))
	allEnts = append(allEnts, ents...)
	allEnts = append(allEnts, relatedEnts...)
	allEnts = append(allEnts, childEnts...)

	allIDs := make([]uuid.UUID, 0, len(allEnts))
	publisherIDs := make([]uuid.UUID, 0, len(allEnts))
	accountIDs := make([]uuid.UUID, 0, len(allEnts))
	realmIDs := make([]uuid.UUID, 0, len(allEnts))
	for i := range allEnts {
		allIDs = append(allIDs, allEnts[i].ID)
		if allEnts[i].PublisherID != uuid.Nil {
			publisherIDs = append(publisherIDs, allEnts[i].PublisherID)
		}
		if allEnts[i].AccountID != nil && *allEnts[i].AccountID != uuid.Nil {
			accountIDs = append(accountIDs, *allEnts[i].AccountID)
		}
		if allEnts[i].RealmID != nil {
			realmIDs = append(realmIDs, *allEnts[i].RealmID)
		}
	}

	rel, err := h.loadRelations(ctx, allIDs, publisherIDs, accountIDs, v)
	if err != nil {
		return nil, err
	}
	rel.realms = h.loadRealmRefs(ctx, realmIDs)

	base := make(map[uuid.UUID]model.Post, len(allEnts))
	for i := range allEnts {
		base[allEnts[i].ID] = h.buildPost(&allEnts[i], rel)
	}

	childrenByHead := map[uuid.UUID][]model.Post{}
	for i := range childEnts {
		child := childEnts[i]
		if child.ChainedPostID == nil {
			continue
		}
		post := base[child.ID]
		var publisher *store.PublisherEntity
		if post.Publisher != nil {
			publisher = rel.publishers[child.PublisherID]
		}
		if !h.canViewPost(ctx, &post, v, accountIDOf(publisher)) {
			continue
		}
		childrenByHead[*child.ChainedPostID] = append(childrenByHead[*child.ChainedPostID], post)
	}

	out := make([]model.Post, 0, len(ents))
	for i := range ents {
		ent := &ents[i]
		post := base[ent.ID]

		if ent.RepliedPostID != nil {
			if replied, ok := base[*ent.RepliedPostID]; ok {
				var publisher *store.PublisherEntity
				if id, err := uuid.Parse(replied.PublisherID); err == nil {
					publisher = rel.publishers[id]
				}
				if h.canViewPost(ctx, &replied, v, accountIDOf(publisher)) {
					post.RepliedPost = &replied
				} else {
					post.RepliedGone = true
				}
			} else {
				post.RepliedGone = true
			}
		}
		if ent.ForwardedPostID != nil {
			if forwarded, ok := base[*ent.ForwardedPostID]; ok {
				var publisher *store.PublisherEntity
				if id, err := uuid.Parse(forwarded.PublisherID); err == nil {
					publisher = rel.publishers[id]
				}
				if h.canViewPost(ctx, &forwarded, v, accountIDOf(publisher)) {
					post.ForwardedPost = &forwarded
				} else {
					post.ForwardedGone = true
				}
			} else {
				post.ForwardedGone = true
			}
		}
		if ent.ChainedPostID == nil {
			post.ChainedPosts = childrenByHead[ent.ID]
			if post.ChainedPosts == nil {
				post.ChainedPosts = []model.Post{}
			}
			post.ChainedCount = len(post.ChainedPosts)
		}
		post.PublisherCollections = rel.collections[ent.ID]
		if post.PublisherCollections == nil {
			post.PublisherCollections = []model.PostCollection{}
		}

		escapeMarkdownHeadings(&post)
		if truncate {
			truncatePost(&post)
		}
		if trackViews {
			h.enqueueView(ent.ID, v)
		}
		out = append(out, post)
	}
	return out, nil
}

func (h *handler) hydrateOne(ctx context.Context, ent *store.PostEntity, v *viewer, truncate bool, trackViews bool) (model.Post, error) {
	posts, err := h.hydrate(ctx, []store.PostEntity{*ent}, v, truncate, trackViews)
	if err != nil {
		return model.Post{}, err
	}
	if len(posts) == 0 {
		return store.PostToModel(ent), nil
	}
	return posts[0], nil
}

func (h *handler) loadRelatedPosts(ctx context.Context, ents []store.PostEntity) ([]store.PostEntity, error) {
	var ids []uuid.UUID
	for i := range ents {
		if ents[i].RepliedPostID != nil {
			ids = append(ids, *ents[i].RepliedPostID)
		}
		if ents[i].ForwardedPostID != nil {
			ids = append(ids, *ents[i].ForwardedPostID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []store.PostEntity
	if err := h.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (h *handler) loadChainedChildren(ctx context.Context, ents []store.PostEntity) ([]store.PostEntity, error) {
	var heads []uuid.UUID
	for i := range ents {
		if ents[i].ChainedPostID == nil {
			heads = append(heads, ents[i].ID)
		}
	}
	if len(heads) == 0 {
		return nil, nil
	}
	var rows []store.PostEntity
	if err := h.db.WithContext(ctx).
		Where("chained_post_id IN ?", heads).
		Order("published_at ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (h *handler) loadRelations(ctx context.Context, postIDs, publisherIDs, accountIDs []uuid.UUID, v *viewer) (*postRelations, error) {
	rel := &postRelations{
		publishers:        map[uuid.UUID]*store.PublisherEntity{},
		publisherAccounts: map[uuid.UUID]any{},
		accounts:          map[uuid.UUID]any{},
		tags:              map[uuid.UUID][]model.PostTag{},
		categories:        map[uuid.UUID][]model.PostCategory{},
		featured:          map[uuid.UUID][]model.PostFeaturedRecord{},
		reactionCounts:    map[uuid.UUID]map[string]int{},
		reactionsMade:     map[uuid.UUID]map[string]bool{},
		bookmarked:        map[uuid.UUID]bool{},
		replies:           map[uuid.UUID]int{},
		threadReplies:     map[uuid.UUID]int{},
		collections:       map[uuid.UUID][]model.PostCollection{},
	}
	publishers, err := h.loadPublishers(ctx, dedupeUUID(publisherIDs))
	if err != nil {
		return nil, err
	}
	rel.publishers = publishers
	rel.publisherAccounts = h.loadPublisherAccounts(ctx, publishers)
	rel.accounts = h.loadAccounts(ctx, dedupeUUID(accountIDs))

	if err := h.loadTags(ctx, postIDs, rel); err != nil {
		return nil, err
	}
	if err := h.loadCategories(ctx, postIDs, rel); err != nil {
		return nil, err
	}

	var featured []store.PostFeaturedRecordEntity
	if err := h.db.WithContext(ctx).Where("post_id IN ?", postIDs).
		Order("created_at ASC").Find(&featured).Error; err == nil {
		for i := range featured {
			rel.featured[featured[i].PostID] = append(rel.featured[featured[i].PostID], postFeaturedRecordToModel(&featured[i]))
		}
	}

	var reactionRows []struct {
		PostID uuid.UUID `gorm:"column:post_id"`
		Symbol string    `gorm:"column:symbol"`
		Count  int       `gorm:"column:count"`
	}
	if err := h.db.WithContext(ctx).Model(&store.PostReactionEntity{}).
		Select("post_id, symbol, count(*) AS count").
		Where("post_id IN ?", postIDs).
		Group("post_id, symbol").Scan(&reactionRows).Error; err == nil {
		for _, row := range reactionRows {
			if rel.reactionCounts[row.PostID] == nil {
				rel.reactionCounts[row.PostID] = map[string]int{}
			}
			rel.reactionCounts[row.PostID][row.Symbol] = row.Count
		}
	}

	if v != nil && v.authed() {
		var made []struct {
			PostID uuid.UUID `gorm:"column:post_id"`
			Symbol string    `gorm:"column:symbol"`
		}
		if err := h.db.WithContext(ctx).Model(&store.PostReactionEntity{}).
			Select("post_id, symbol").
			Where("post_id IN ? AND account_id = ?", postIDs, v.accountID).
			Scan(&made).Error; err == nil {
			for _, row := range made {
				if rel.reactionsMade[row.PostID] == nil {
					rel.reactionsMade[row.PostID] = map[string]bool{}
				}
				rel.reactionsMade[row.PostID][row.Symbol] = true
			}
		}

		var bookmarked []uuid.UUID
		if err := h.db.WithContext(ctx).Model(&store.PostBookmarkEntity{}).
			Where("post_id IN ? AND account_id = ?", postIDs, v.accountID).
			Pluck("post_id", &bookmarked).Error; err == nil {
			for _, id := range bookmarked {
				rel.bookmarked[id] = true
			}
		}
	}

	var replyRows []struct {
		RepliedPostID uuid.UUID `gorm:"column:replied_post_id"`
		Count         int       `gorm:"column:count"`
	}
	if err := h.db.WithContext(ctx).Model(&store.PostEntity{}).
		Select("replied_post_id, count(*) AS count").
		Where("replied_post_id IN ?", postIDs).
		Group("replied_post_id").Scan(&replyRows).Error; err == nil {
		for _, row := range replyRows {
			rel.replies[row.RepliedPostID] = row.Count
		}
	}

	rel.threadReplies = h.threadReplyCounts(ctx, postIDs)

	if err := h.loadCollections(ctx, postIDs, rel); err != nil {
		return nil, err
	}
	return rel, nil
}

func (h *handler) threadReplyCounts(ctx context.Context, postIDs []uuid.UUID) map[uuid.UUID]int {
	out := map[uuid.UUID]int{}
	if len(postIDs) == 0 {
		return out
	}
	var rows []struct {
		AncestorID uuid.UUID `gorm:"column:ancestor_id"`
		Count      int       `gorm:"column:count"`
	}
	query := `WITH RECURSIVE reply_tree AS (
		SELECT replied_post_id AS ancestor_id, id AS descendant_id
		FROM posts WHERE replied_post_id IN ? AND deleted_at IS NULL
		UNION ALL
		SELECT rt.ancestor_id, p.id
		FROM posts p INNER JOIN reply_tree rt ON p.replied_post_id = rt.descendant_id
		WHERE p.deleted_at IS NULL
	)
	SELECT ancestor_id, count(*) AS count FROM reply_tree GROUP BY ancestor_id`
	if err := h.db.WithContext(ctx).Raw(query, postIDs).Scan(&rows).Error; err != nil {
		h.log.Debug("posts: thread reply count failed", "error", err)
		return out
	}
	for _, row := range rows {
		out[row.AncestorID] = row.Count
	}
	return out
}

func (h *handler) loadTags(ctx context.Context, postIDs []uuid.UUID, rel *postRelations) error {
	var links []store.PostTagLink
	if err := h.db.WithContext(ctx).Where("posts_id IN ?", postIDs).Find(&links).Error; err != nil {
		return err
	}
	if len(links) == 0 {
		return nil
	}
	tagIDs := make([]uuid.UUID, 0, len(links))
	for _, link := range links {
		tagIDs = append(tagIDs, link.TagID)
	}
	var tags []store.PostTagEntity
	if err := h.db.WithContext(ctx).Where("id IN ?", tagIDs).Find(&tags).Error; err != nil {
		return err
	}
	byID := map[uuid.UUID]model.PostTag{}
	for i := range tags {
		byID[tags[i].ID] = postTagToModel(&tags[i])
	}
	for _, link := range links {
		if tag, ok := byID[link.TagID]; ok {
			rel.tags[link.PostID] = append(rel.tags[link.PostID], tag)
		}
	}
	return nil
}

func (h *handler) loadCategories(ctx context.Context, postIDs []uuid.UUID, rel *postRelations) error {
	var links []store.PostCategoryLink
	if err := h.db.WithContext(ctx).Where("posts_id IN ?", postIDs).Find(&links).Error; err != nil {
		return err
	}
	if len(links) == 0 {
		return nil
	}
	categoryIDs := make([]uuid.UUID, 0, len(links))
	for _, link := range links {
		categoryIDs = append(categoryIDs, link.CategoryID)
	}
	var categories []store.PostCategoryEntity
	if err := h.db.WithContext(ctx).Where("id IN ?", categoryIDs).Find(&categories).Error; err != nil {
		return err
	}
	byID := map[uuid.UUID]model.PostCategory{}
	for i := range categories {
		byID[categories[i].ID] = postCategoryToModel(&categories[i])
	}
	for _, link := range links {
		if category, ok := byID[link.CategoryID]; ok {
			rel.categories[link.PostID] = append(rel.categories[link.PostID], category)
		}
	}
	return nil
}

func (h *handler) loadCollections(ctx context.Context, postIDs []uuid.UUID, rel *postRelations) error {
	var items []store.PostCollectionItemEntity
	if err := h.db.WithContext(ctx).Where("post_id IN ?", postIDs).Find(&items).Error; err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	collectionIDs := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		collectionIDs = append(collectionIDs, item.CollectionID)
	}
	var collections []store.PostCollectionEntity
	if err := h.db.WithContext(ctx).Where("id IN ?", collectionIDs).Find(&collections).Error; err != nil {
		return err
	}
	byID := map[uuid.UUID]model.PostCollection{}
	for i := range collections {
		byID[collections[i].ID] = postCollectionToModel(&collections[i])
	}
	for _, item := range items {
		collection, ok := byID[item.CollectionID]
		if !ok {
			continue
		}
		rel.collections[item.PostID] = append(rel.collections[item.PostID], collection)
	}
	return nil
}

func (h *handler) buildPost(ent *store.PostEntity, rel *postRelations) model.Post {
	post := store.PostToModel(ent)
	var pubAccount any
	var publisherEnt *store.PublisherEntity
	if publisher, ok := rel.publishers[ent.PublisherID]; ok {
		publisherEnt = publisher
		mapped := store.PublisherToModel(publisher)
		if account, ok := rel.publisherAccounts[ent.PublisherID]; ok {
			mapped.Account = account
			pubAccount = account
		}
		post.Publisher = &mapped
	}
	if ent.AccountID != nil {
		if account, ok := rel.accounts[*ent.AccountID]; ok {
			post.Account = account
		} else if pubAccount != nil && publisherEnt != nil && publisherEnt.AccountID != nil && *ent.AccountID == *publisherEnt.AccountID {
			post.Account = pubAccount
		}
	} else if publisherEnt != nil && publisherEnt.Type == int(model.PublisherIndividual) && publisherEnt.AccountID != nil {
		post.AccountID = store.UUIDPtrString(publisherEnt.AccountID)
		post.Account = pubAccount
	}
	if tags, ok := rel.tags[ent.ID]; ok {
		post.Tags = tags
	}
	if categories, ok := rel.categories[ent.ID]; ok {
		post.Categories = categories
	}
	if featured, ok := rel.featured[ent.ID]; ok {
		post.FeaturedRecords = featured
	}
	if counts, ok := rel.reactionCounts[ent.ID]; ok {
		post.ReactionsCount = counts
	}
	if made, ok := rel.reactionsMade[ent.ID]; ok {
		post.ReactionsMade = made
	}
	post.IsBookmarked = rel.bookmarked[ent.ID]
	post.RepliesCount = rel.replies[ent.ID]
	post.ThreadRepliesCount = rel.threadReplies[ent.ID]
	if ent.RealmID != nil {
		if ref, ok := rel.realms[*ent.RealmID]; ok {
			post.Realm = ref
		}
	}
	return post
}

func accountIDOf(publisher *store.PublisherEntity) uuid.UUID {
	if publisher == nil || publisher.AccountID == nil {
		return uuid.Nil
	}
	return *publisher.AccountID
}

// loadAccounts batch-resolves account snapshots for post creators.
func (h *handler) loadAccounts(ctx context.Context, accountIDs []uuid.UUID) map[uuid.UUID]any {
	accountsByID := map[uuid.UUID]any{}
	if len(accountIDs) == 0 || h.ports == nil {
		return accountsByID
	}
	rawIDs := make([]string, 0, len(accountIDs))
	for _, id := range accountIDs {
		rawIDs = append(rawIDs, id.String())
	}
	accounts, ok := module.Get[ports.Accounts](h.ports, ports.PortAccounts)
	if !ok {
		return accountsByID
	}
	list, err := accounts.GetAccounts(ctx, rawIDs)
	if err != nil {
		h.log.Warn("posts: load accounts", "error", err)
		return accountsByID
	}
	for _, account := range list {
		if account == nil {
			continue
		}
		if id, err := uuid.Parse(account.GetId()); err == nil {
			accountsByID[id] = accountjson.FromProto(account)
		}
	}
	return accountsByID
}

// loadPublisherAccounts embeds the account snapshot on individual publishers,
// matching LoadIndividualPublisherAccounts. Timeline listings hydrate through
// their own loader; every other post listing goes through loadRelations, so
// the snapshot has to be attached here or the client renders a bare account_id.
func (h *handler) loadPublisherAccounts(ctx context.Context, publishers map[uuid.UUID]*store.PublisherEntity) map[uuid.UUID]any {
	accountsByPublisher := map[uuid.UUID]any{}
	if len(publishers) == 0 || h.ports == nil {
		return accountsByPublisher
	}
	accountIDs := make([]string, 0, len(publishers))
	for _, publisher := range publishers {
		if publisher.AccountID != nil && publisher.Type == int(model.PublisherIndividual) {
			accountIDs = append(accountIDs, publisher.AccountID.String())
		}
	}
	if len(accountIDs) == 0 {
		return accountsByPublisher
	}
	accounts, ok := module.Get[ports.Accounts](h.ports, ports.PortAccounts)
	if !ok {
		return accountsByPublisher
	}
	list, err := accounts.GetAccounts(ctx, accountIDs)
	if err != nil {
		h.log.Warn("posts: load publisher accounts", "error", err)
		return accountsByPublisher
	}
	byID := make(map[string]any, len(list))
	for _, account := range list {
		if account == nil {
			continue
		}
		byID[account.GetId()] = accountjson.FromProto(account)
	}
	for id, publisher := range publishers {
		if publisher.AccountID == nil {
			continue
		}
		if account, ok := byID[publisher.AccountID.String()]; ok {
			accountsByPublisher[id] = account
		}
	}
	return accountsByPublisher
}

func dedupeUUID(ids []uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]struct{}{}
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id == uuid.Nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// ---------------------------------------------------------------------------
// Wire mappers
// ---------------------------------------------------------------------------

func postTagToModel(e *store.PostTagEntity) model.PostTag {
	return model.PostTag{
		ID:               e.ID.String(),
		Slug:             e.Slug,
		Name:             e.Name,
		Description:      e.Description,
		OwnerPublisherID: store.UUIDPtrString(e.OwnerPublisherID),
		IsProtected:      e.IsProtected,
		IsEvent:          e.IsEvent,
		EventEndsAt:      store.TimePtr(e.EventEndsAt),
		CreatedAt:        model.NewTime(e.CreatedAt),
		UpdatedAt:        model.NewTime(e.UpdatedAt),
		DeletedAt:        store.DeletedPtr(e.DeletedAt),
	}
}

func postCategoryToModel(e *store.PostCategoryEntity) model.PostCategory {
	return model.PostCategory{
		ID:        e.ID.String(),
		Slug:      e.Slug,
		Name:      e.Name,
		CreatedAt: model.NewTime(e.CreatedAt),
		UpdatedAt: model.NewTime(e.UpdatedAt),
		DeletedAt: store.DeletedPtr(e.DeletedAt),
	}
}

func postCollectionToModel(e *store.PostCollectionEntity) model.PostCollection {
	return model.PostCollection{
		ID:          e.ID.String(),
		Slug:        e.Slug,
		Name:        e.Name,
		Description: e.Description,
		PublisherID: e.PublisherID.String(),
		Background:  filejson.Value(e.Background),
		Icon:        filejson.Value(e.Icon),
		CreatedAt:   model.NewTime(e.CreatedAt),
		UpdatedAt:   model.NewTime(e.UpdatedAt),
		DeletedAt:   store.DeletedPtr(e.DeletedAt),
	}
}

func postCollectionItemToModel(e *store.PostCollectionItemEntity) model.PostCollectionItem {
	return model.PostCollectionItem{
		ID:           e.ID.String(),
		CollectionID: e.CollectionID.String(),
		PostID:       e.PostID.String(),
		Order:        e.Order,
		CreatedAt:    model.NewTime(e.CreatedAt),
		UpdatedAt:    model.NewTime(e.UpdatedAt),
		DeletedAt:    store.DeletedPtr(e.DeletedAt),
	}
}

func postFeaturedRecordToModel(e *store.PostFeaturedRecordEntity) model.PostFeaturedRecord {
	return model.PostFeaturedRecord{
		ID:            e.ID.String(),
		PostID:        e.PostID.String(),
		FeaturedAt:    store.TimePtr(e.FeaturedAt),
		SocialCredits: e.SocialCredits,
		CreatedAt:     model.NewTime(e.CreatedAt),
		UpdatedAt:     model.NewTime(e.UpdatedAt),
		DeletedAt:     store.DeletedPtr(e.DeletedAt),
	}
}

func postReactionToModel(e *store.PostReactionEntity) model.PostReaction {
	return model.PostReaction{
		ID:           e.ID.String(),
		Symbol:       e.Symbol,
		Attitude:     e.Attitude,
		PostID:       e.PostID.String(),
		AccountID:    store.UUIDPtrString(e.AccountID),
		FediverseURI: e.FediverseURI,
		PublisherID:  store.UUIDPtrString(e.PublisherID),
		IsLocal:      e.IsLocal,
		CreatedAt:    model.NewTime(e.CreatedAt),
		UpdatedAt:    model.NewTime(e.UpdatedAt),
		DeletedAt:    store.DeletedPtr(e.DeletedAt),
	}
}

// hydrateReactionActors embeds the account or fediverse publisher snapshot on
// reactions. Clients render the actor from reaction.account/react.publisher, so
// a bare account_id shows up as "unknown".
func (h *handler) hydrateReactionActors(ctx context.Context, reactions []model.PostReaction) {
	if len(reactions) == 0 {
		return
	}
	accountIDs := make([]string, 0, len(reactions))
	publisherIDs := make([]uuid.UUID, 0, len(reactions))
	for i := range reactions {
		if value := reactions[i].AccountID; value != nil && *value != "" {
			accountIDs = append(accountIDs, *value)
		}
		if value := reactions[i].PublisherID; value != nil && *value != "" {
			if id, err := uuid.Parse(*value); err == nil {
				publisherIDs = append(publisherIDs, id)
			}
		}
	}
	accountsByID := map[string]any{}
	if len(accountIDs) > 0 && h.ports != nil {
		if accounts, ok := module.Get[ports.Accounts](h.ports, ports.PortAccounts); ok {
			if list, err := accounts.GetAccounts(ctx, accountIDs); err == nil {
				for _, account := range list {
					if account == nil {
						continue
					}
					accountsByID[account.GetId()] = accountjson.FromProto(account)
				}
			}
		}
	}
	publishersByID := map[uuid.UUID]*model.Publisher{}
	if len(publisherIDs) > 0 {
		if rows, err := h.loadPublishers(ctx, dedupeUUID(publisherIDs)); err == nil {
			for id, row := range rows {
				mapped := store.PublisherToModel(row)
				publishersByID[id] = &mapped
			}
		}
	}
	for i := range reactions {
		if value := reactions[i].AccountID; value != nil {
			if account, ok := accountsByID[*value]; ok {
				reactions[i].Account = account
			}
		}
		if value := reactions[i].PublisherID; value != nil {
			if id, err := uuid.Parse(*value); err == nil {
				if publisher, ok := publishersByID[id]; ok {
					reactions[i].Publisher = publisher
				}
			}
		}
	}
}

func postBookmarkToModel(e *store.PostBookmarkEntity) model.PostBookmark {
	return model.PostBookmark{
		ID:        e.ID.String(),
		PostID:    e.PostID.String(),
		AccountID: e.AccountID.String(),
		CreatedAt: model.NewTime(e.CreatedAt),
		UpdatedAt: model.NewTime(e.UpdatedAt),
		DeletedAt: store.DeletedPtr(e.DeletedAt),
	}
}

func postAwardToModel(e *store.PostAwardEntity) model.PostAward {
	return model.PostAward{
		ID:        e.ID.String(),
		Amount:    e.Amount,
		Attitude:  e.Attitude,
		Message:   e.Message,
		PostID:    e.PostID.String(),
		AccountID: e.AccountID.String(),
		SettledAt: store.TimePtr(e.SettledAt),
		CreatedAt: model.NewTime(e.CreatedAt),
		UpdatedAt: model.NewTime(e.UpdatedAt),
		DeletedAt: store.DeletedPtr(e.DeletedAt),
	}
}

func postSponsorBidToModel(e *store.PostSponsorBidEntity) model.PostSponsorBid {
	return model.PostSponsorBid{
		ID:        e.ID.String(),
		PostID:    e.PostID.String(),
		AccountID: e.AccountID.String(),
		Amount:    e.Amount,
		ExpiresAt: store.TimePtr(&e.ExpiresAt),
		CreatedAt: model.NewTime(e.CreatedAt),
		UpdatedAt: model.NewTime(e.UpdatedAt),
		DeletedAt: store.DeletedPtr(e.DeletedAt),
	}
}

func boostToModel(e *store.BoostEntity) model.Boost {
	return model.Boost{
		ID:             e.ID.String(),
		PostID:         e.PostID.String(),
		PublisherID:    e.PublisherID.String(),
		ActivityPubURI: e.ActivityPubURI,
		WebURL:         e.WebURL,
		Content:        e.Content,
		BoostedAt:      store.TimePtr(e.BoostedAt),
		CreatedAt:      model.NewTime(e.CreatedAt),
		UpdatedAt:      model.NewTime(e.UpdatedAt),
		DeletedAt:      store.DeletedPtr(e.DeletedAt),
	}
}

func postCategorySubscriptionToModel(e *store.PostCategorySubscriptionEntity) model.PostCategorySubscription {
	return model.PostCategorySubscription{
		ID:           e.ID.String(),
		AccountID:    e.AccountID.String(),
		CategoryID:   store.UUIDPtrString(e.CategoryID),
		TagID:        store.UUIDPtrString(e.TagID),
		CollectionID: store.UUIDPtrString(e.CollectionID),
		CreatedAt:    model.NewTime(e.CreatedAt),
		UpdatedAt:    model.NewTime(e.UpdatedAt),
		DeletedAt:    store.DeletedPtr(e.DeletedAt),
	}
}

func postWatchPreferenceToModel(e *store.PostWatchPreferenceEntity) model.PostWatchPreference {
	return model.PostWatchPreference{
		ID:              e.ID.String(),
		AccountID:       e.AccountID.String(),
		Source:          e.Source,
		NotifyReactions: e.NotifyReactions,
		NotifyReplies:   e.NotifyReplies,
		NotifyChains:    e.NotifyChains,
		NotifyForwards:  e.NotifyForwards,
		NotifyEdits:     e.NotifyEdits,
		CreatedAt:       model.NewTime(e.CreatedAt),
		UpdatedAt:       model.NewTime(e.UpdatedAt),
		DeletedAt:       store.DeletedPtr(e.DeletedAt),
	}
}

func postAggregatedStatsToModel(e *store.PostAggregatedStatsEntity) model.PostAggregatedStats {
	return model.PostAggregatedStats{
		ID:                e.ID.String(),
		PostID:            e.PostID.String(),
		ActiveBidTotal:    e.ActiveBidTotal,
		ActiveBidCount:    e.ActiveBidCount,
		IsCurrentlyPlaced: e.IsCurrentlyPlaced,
		ShownCount:        e.ShownCount,
		LastShownAt:       store.TimePtr(e.LastShownAt),
	}
}

// ---------------------------------------------------------------------------
// Content truncation
// ---------------------------------------------------------------------------

const (
	truncateMaxLength = 256
	truncateEmbedMax  = 80
)

var htmlTagPattern = regexp.MustCompile(`<[^>]*>`)

func truncatePost(post *model.Post) {
	if post.Content != nil && len(*post.Content) > truncateMaxLength {
		if plain, ok := plainText(*post.Content, post.ContentType); ok && len([]rune(plain)) > truncateMaxLength {
			cut := truncateRunes(plain, truncateMaxLength)
			post.Content = &cut
			post.IsTruncated = true
		}
	}
	truncateEmbedded(post.RepliedPost, post.ContentType)
	truncateEmbedded(post.ForwardedPost, post.ContentType)
}

func truncateEmbedded(embedded *model.Post, contentType int) {
	if embedded == nil || embedded.Content == nil || len(*embedded.Content) <= truncateEmbedMax {
		return
	}
	if plain, ok := plainText(*embedded.Content, contentType); ok && len([]rune(plain)) > truncateEmbedMax {
		cut := truncateRunes(plain, truncateEmbedMax)
		embedded.Content = &cut
		embedded.IsTruncated = true
	}
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func plainText(content string, contentType int) (string, bool) {
	switch contentType {
	case int(model.ContentTypeHTML):
		return strings.TrimSpace(html.UnescapeString(htmlTagPattern.ReplaceAllString(content, ""))), true
	case int(model.ContentTypeMarkdown):
		return strings.TrimSpace(stripMarkdown(content)), true
	default:
		return "", false
	}
}

func stripMarkdown(content string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			lines[i] = ""
			continue
		}
		trimmed = strings.TrimLeft(trimmed, "#> ")
		trimmed = strings.TrimPrefix(trimmed, "- ")
		trimmed = strings.TrimPrefix(trimmed, "* ")
		lines[i] = trimmed
	}
	return strings.Join(lines, " ")
}

// escapeMarkdownHeadings mirrors PostService.EscapeMarkdownHeadings: headings
// inside moment posts would otherwise be promoted by the client renderer.
func escapeMarkdownHeadings(post *model.Post) {
	escapeHeadings(post)
	if post.RepliedPost != nil {
		escapeHeadings(post.RepliedPost)
	}
	if post.ForwardedPost != nil {
		escapeHeadings(post.ForwardedPost)
	}
}

func escapeHeadings(post *model.Post) {
	if post.Type != int(model.PostTypeMoment) || post.ContentType != int(model.ContentTypeMarkdown) {
		return
	}
	if post.Content == nil || *post.Content == "" {
		return
	}
	lines := strings.Split(*post.Content, "\n")
	inFence := false
	fence := ""
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
					fence = strings.Repeat(string(marker), run)
				} else if strings.HasPrefix(trimmed, fence) && strings.TrimSpace(trimmed[len(fence):]) == "" {
					inFence = false
					fence = ""
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
	joined := strings.Join(lines, "\n")
	post.Content = &joined
}

// ---------------------------------------------------------------------------
// View buffering
// ---------------------------------------------------------------------------

func (h *handler) enqueueView(postID uuid.UUID, v *viewer) {
	h.viewMu.Lock()
	defer h.viewMu.Unlock()
	bucket, ok := h.views[postID]
	if !ok {
		bucket = &viewBucket{unique: map[string]struct{}{}}
		h.views[postID] = bucket
	}
	bucket.total++
	if v != nil && v.authed() {
		bucket.unique[v.accountID.String()] = struct{}{}
	}
}

func (h *handler) flushViews(ctx context.Context) error {
	h.viewMu.Lock()
	pending := h.views
	h.views = map[uuid.UUID]*viewBucket{}
	h.viewMu.Unlock()
	for postID, bucket := range pending {
		if bucket.total == 0 {
			continue
		}
		if err := h.db.WithContext(ctx).Model(&store.PostEntity{}).
			Where("id = ?", postID).
			UpdateColumns(map[string]any{
				"views_total":  gorm.Expr("views_total + ?", bucket.total),
				"views_unique": gorm.Expr("views_unique + ?", len(bucket.unique)),
			}).Error; err != nil {
			h.log.Warn("posts: flush views failed", "post", postID, "error", err)
		}
	}
	return nil
}

// enqueueInterest records an interest signal for the periodic flush.
func (h *handler) enqueueInterest(accountID, postID uuid.UUID, delta float64, kind string) {
	if accountID == uuid.Nil || postID == uuid.Nil || delta == 0 {
		return
	}
	h.interestMu.Lock()
	defer h.interestMu.Unlock()
	h.interest = append(h.interest, interestSignal{
		accountID: accountID,
		postID:    postID,
		delta:     delta,
		kind:      kind,
		at:        time.Now().UTC(),
	})
}

func (h *handler) flushInterest(ctx context.Context) error {
	h.interestMu.Lock()
	pending := h.interest
	h.interest = nil
	h.interestMu.Unlock()
	for _, signal := range pending {
		var profile store.PostInterestProfileEntity
		err := h.db.WithContext(ctx).
			Where("account_id = ? AND kind = 0 AND reference_id = ?", signal.accountID, signal.postID).
			First(&profile).Error
		if isNotFound(err) {
			profile = store.PostInterestProfileEntity{
				ID:               uuid.New(),
				AccountID:        signal.accountID,
				Kind:             0,
				ReferenceID:      signal.postID,
				Score:            clampInterest(signal.delta),
				InteractionCount: 1,
				LastInteractedAt: &signal.at,
				LastSignalType:   &signal.kind,
			}
			if err := h.db.WithContext(ctx).Create(&profile).Error; err != nil {
				h.log.Warn("posts: create interest profile failed", "error", err)
			}
			continue
		}
		if err != nil {
			continue
		}
		profile.Score = clampInterest(profile.Score + signal.delta)
		profile.InteractionCount++
		profile.LastInteractedAt = &signal.at
		profile.LastSignalType = &signal.kind
		if err := h.db.WithContext(ctx).Save(&profile).Error; err != nil {
			h.log.Warn("posts: update interest profile failed", "error", err)
		}
	}
	return nil
}

func clampInterest(score float64) float64 {
	if score > 100 {
		return 100
	}
	if score < -100 {
		return -100
	}
	return score
}
