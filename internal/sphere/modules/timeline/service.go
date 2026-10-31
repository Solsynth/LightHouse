package timeline

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"math/rand/v2"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/app"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/grpcclient"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// Ranking constants, copied from TimelineService.
const (
	adInsertionIndex                  = 4
	articleTypeBoost                  = 1.5
	realmBoostLevelRankBonus          = 0.6
	publisherRepeatPenalty            = 1.35
	explicitPositiveFeedbackScore     = 4.0
	explicitNegativeFeedbackScore     = -30.0
	personalizedLowRankThresholdFloor = 0.35
	personalizedLowRankThresholdRatio = 0.18
	discoveryCandidatePostTake        = 48
	timelineCandidateMultiplier       = 2
	automatedPostPenalty              = 2.5
	subscriptionBoostBonus            = 1.5
	personalizationBoostMultiplier    = 3.0
	discoveryLookbackDays             = 45
)

const (
	publicRealmsCacheTTL = time.Minute
	realmCacheTTL        = 5 * time.Minute
	automatedStatusTTL   = 5 * time.Minute
	discoveryProfileTTL  = 3 * time.Minute
	friendIDsTTL         = 2 * time.Minute
	blockedIDsTTL        = 2 * time.Minute
	mutedIDsTTL          = 2 * time.Minute
	friendPresenceTTL    = 30 * time.Second
	friendStatusTTL      = 30 * time.Second
	userRealmsTTL        = 2 * time.Minute
	softCursorTTL        = 5 * time.Minute
	fediverseVisibleTTL  = 60 * time.Second
	boostedPostsTTL      = 30 * time.Second
	closeFriendsTTL      = 5 * time.Minute
	userPublishersTTL    = 5 * time.Minute
)

// service is the timeline module's runtime.
type service struct {
	db      *gorm.DB
	cache   app.CacheService
	clients *grpcclient.Clients
	log     *slog.Logger
	maxTake int
	signals ports.PostSignals
	realms  ports.Realms
}

func newService(c *core.Services, log *slog.Logger) *service {
	if log == nil {
		log = slog.Default()
	}
	s := &service{log: log, maxTake: 100}
	if c == nil {
		return s
	}
	s.db = c.DB
	s.cache = c.Redis
	s.clients = c.Clients
	if c.Config != nil && c.Config.Security.MaxTake > 0 {
		s.maxTake = c.Config.Security.MaxTake
	}
	return s
}

// cacheGet loads a cached value, reporting whether it was present.
func (s *service) cacheGet(ctx context.Context, key string, dest any) bool {
	if s.cache == nil {
		return false
	}
	found, err := s.cache.Get(ctx, key, dest)
	if err != nil {
		s.log.Warn("timeline: cache get", "key", key, "error", err)
		return false
	}
	return found
}

func (s *service) cacheSet(ctx context.Context, key string, value any, ttl time.Duration) {
	if s.cache == nil {
		return
	}
	if err := s.cache.Set(ctx, key, value, ttl); err != nil {
		s.log.Warn("timeline: cache set", "key", key, "error", err)
	}
}

func (s *service) cacheRemove(ctx context.Context, key string) {
	if s.cache == nil {
		return
	}
	if err := s.cache.Remove(ctx, key); err != nil {
		s.log.Warn("timeline: cache remove", "key", key, "error", err)
	}
}

// ---------------------------------------------------------------------------
// Feed scope
// ---------------------------------------------------------------------------

// resolveFeedScope mirrors TimelineService.ResolveFeedScopeAsync.
func (s *service) resolveFeedScope(ctx context.Context, publisherName, collectionSlug string) (timelineFeedScope, error) {
	normalizedPublisher := normalizeScopeValue(publisherName)
	normalizedCollection := normalizeScopeValue(collectionSlug)
	if normalizedCollection != nil && normalizedPublisher == nil {
		return timelineFeedScope{}, errors.New("Collection timeline scope requires ?pub={publisherName}.")
	}
	scope := timelineFeedScope{
		requestedPublisherName:  normalizedPublisher,
		requestedCollectionSlug: normalizedCollection,
	}
	if normalizedPublisher == nil {
		return scope, nil
	}
	var publisher store.PublisherEntity
	if err := s.db.WithContext(ctx).Where("lower(name) = ?", normalizeSlug(*normalizedPublisher)).First(&publisher).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return scope, err
		}
		return scope, nil
	}
	scope.publisherID = &publisher.ID
	if normalizedCollection == nil {
		return scope, nil
	}
	slug := normalizeSlug(*normalizedCollection)
	var collection store.PostCollectionEntity
	if err := s.db.WithContext(ctx).
		Where("publisher_id = ? AND lower(slug) = ?", publisher.ID, slug).
		First(&collection).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return scope, err
		}
		return scope, nil
	}
	scope.collectionID = &collection.ID
	return scope, nil
}

func normalizeScopeValue(raw string) *string {
	trimmed := trimSpace(raw)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func trimSpace(raw string) string {
	start, end := 0, len(raw)
	for start < end && (raw[start] == ' ' || raw[start] == '\t' || raw[start] == '\n' || raw[start] == '\r') {
		start++
	}
	for end > start && (raw[end-1] == ' ' || raw[end-1] == '\t' || raw[end-1] == '\n' || raw[end-1] == '\r') {
		end--
	}
	return raw[start:end]
}

func normalizeSlug(raw string) string {
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out = append(out, c)
	}
	return string(out)
}

// ---------------------------------------------------------------------------
// Cached identity lookups
// ---------------------------------------------------------------------------

// cachedFriendIDs resolves the viewer's friends through the profile service.
func (s *service) cachedFriendIDs(ctx context.Context, accountID uuid.UUID) []uuid.UUID {
	key := "timeline:friends:" + accountID.String()
	var cached []uuid.UUID
	if s.cacheGet(ctx, key, &cached) {
		return cached
	}
	ids := s.relationshipIDs(ctx, accountID, "friends")
	s.cacheSet(ctx, key, ids, friendIDsTTL)
	return ids
}

func (s *service) relationshipIDs(ctx context.Context, accountID uuid.UUID, kind string) []uuid.UUID {
	if s.clients == nil || s.clients.Profile == nil {
		return nil
	}
	var (
		resp *gen.DyListRelationshipSimpleResponse
		err  error
	)
	switch kind {
	case "blocked":
		// Query both directions so blocked accounts cannot see each other's posts.
		accountReq := &gen.DyListRelationshipSimpleRequest{
			RelationIdentifier: &gen.DyListRelationshipSimpleRequest_AccountId{AccountId: accountID.String()},
		}
		resp, err = s.clients.Profile.ListBlocked(ctx, accountReq)
		if err == nil && resp != nil {
			relatedReq := &gen.DyListRelationshipSimpleRequest{
				RelationIdentifier: &gen.DyListRelationshipSimpleRequest_RelatedId{RelatedId: accountID.String()},
			}
			if relatedResp, relErr := s.clients.Profile.ListBlocked(ctx, relatedReq); relErr == nil && relatedResp != nil {
				resp.AccountsId = append(resp.AccountsId, relatedResp.GetAccountsId()...)
			}
		}
	case "muted":
		// Muted accounts: the viewer only queries accounts they have muted.
		request := &gen.DyListRelationshipSimpleRequest{
			RelationIdentifier: &gen.DyListRelationshipSimpleRequest_AccountId{AccountId: accountID.String()},
		}
		resp, err = s.clients.Profile.ListMuted(ctx, request)
	case "close_friends":
		// Close friends: timeline needs publishers of authors who added the viewer as a close friend.
		request := &gen.DyListRelationshipSimpleRequest{
			RelationIdentifier: &gen.DyListRelationshipSimpleRequest_RelatedId{RelatedId: accountID.String()},
		}
		resp, err = s.clients.Profile.ListCloseFriends(ctx, request)
	default:
		request := &gen.DyListRelationshipSimpleRequest{
			RelationIdentifier: &gen.DyListRelationshipSimpleRequest_AccountId{AccountId: accountID.String()},
		}
		resp, err = s.clients.Profile.ListFriends(ctx, request)
	}
	if err != nil {
		s.log.Warn("timeline: list relationships", "kind", kind, "error", err)
		return nil
	}
	out := make([]uuid.UUID, 0, len(resp.GetAccountsId()))
	seen := make(map[uuid.UUID]struct{}, len(resp.GetAccountsId()))
	for _, raw := range resp.GetAccountsId() {
		if id, err := uuid.Parse(raw); err == nil {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				out = append(out, id)
			}
		}
	}
	return out
}

func (s *service) cachedBlockedAccountIDs(ctx context.Context, accountID uuid.UUID) []uuid.UUID {
	key := "timeline:blocked:" + accountID.String()
	var cached []uuid.UUID
	if s.cacheGet(ctx, key, &cached) {
		return cached
	}
	ids := s.relationshipIDs(ctx, accountID, "blocked")
	s.cacheSet(ctx, key, ids, blockedIDsTTL)
	return ids
}

func (s *service) cachedMutedAccountIDs(ctx context.Context, accountID uuid.UUID) []uuid.UUID {
	key := "timeline:muted:" + accountID.String()
	var cached []uuid.UUID
	if s.cacheGet(ctx, key, &cached) {
		return cached
	}
	ids := s.relationshipIDs(ctx, accountID, "muted")
	s.cacheSet(ctx, key, ids, mutedIDsTTL)
	return ids
}

func (s *service) cachedCloseFriendPublisherIDs(ctx context.Context, accountID uuid.UUID) ([]uuid.UUID, bool) {
	key := "timeline:close_friends:" + accountID.String()
	var cached []uuid.UUID
	if s.cacheGet(ctx, key, &cached) {
		return cached, true
	}
	accountIDs := s.relationshipIDs(ctx, accountID, "close_friends")
	if len(accountIDs) == 0 {
		return []uuid.UUID{}, true
	}
	var publisherIDs []uuid.UUID
	if err := s.db.WithContext(ctx).Model(&store.PublisherEntity{}).
		Where("account_id IN ?", accountIDs).
		Pluck("id", &publisherIDs).Error; err != nil {
		s.log.Warn("timeline: close friend publishers", "error", err)
		return []uuid.UUID{}, false
	}
	s.cacheSet(ctx, key, publisherIDs, closeFriendsTTL)
	return publisherIDs, true
}

// cachedUserRealms resolves the realms the account belongs to.
func (s *service) cachedUserRealms(ctx context.Context, accountID uuid.UUID) []uuid.UUID {
	key := "timeline:realms:" + accountID.String()
	var cached []uuid.UUID
	if s.cacheGet(ctx, key, &cached) {
		return cached
	}
	var ids []uuid.UUID
	if s.clients != nil && s.clients.Realm != nil {
		resp, err := s.clients.Realm.GetUserRealms(ctx, &gen.DyGetUserRealmsRequest{AccountId: accountID.String()})
		if err != nil {
			s.log.Warn("timeline: user realms", "error", err)
		} else {
			for _, raw := range resp.GetRealmIds() {
				if id, err := uuid.Parse(raw); err == nil {
					ids = append(ids, id)
				}
			}
		}
	}
	s.cacheSet(ctx, key, ids, userRealmsTTL)
	return ids
}

// userPublishers resolves the publishers an account is a member of.
func (s *service) userPublishers(ctx context.Context, accountID uuid.UUID) []uuid.UUID {
	key := "timeline:user-publishers:" + accountID.String()
	var cached []uuid.UUID
	if s.cacheGet(ctx, key, &cached) {
		return cached
	}
	var publisherIDs []uuid.UUID
	if err := s.db.WithContext(ctx).Model(&store.PublisherMemberEntity{}).
		Where("account_id = ?", accountID).
		Pluck("publisher_id", &publisherIDs).Error; err != nil {
		s.log.Warn("timeline: user publishers", "error", err)
		return nil
	}
	s.cacheSet(ctx, key, publisherIDs, userPublishersTTL)
	return publisherIDs
}

// subscribedPublishers mirrors PublisherService.GetSubscribedPublishers:
// accepted, live and not blocking.
func (s *service) subscribedPublishers(ctx context.Context, accountID uuid.UUID) []uuid.UUID {
	var ids []uuid.UUID
	if err := s.db.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
		Where("account_id = ?", accountID).
		Where("ended_at IS NULL").
		Where("state = ? AND is_blocking = ?", model.SubscriptionAccepted, false).
		Pluck("publisher_id", &ids).Error; err != nil {
		s.log.Warn("timeline: subscribed publishers", "error", err)
	}
	return ids
}

// gatekeptPublisherIDs mirrors GetGatekeptPublisherIds.
func (s *service) gatekeptPublisherIDs(ctx context.Context, realmIDs []uuid.UUID) []uuid.UUID {
	query := s.db.WithContext(ctx).Model(&store.PublisherEntity{}).
		Where("gatekept_follows = ?", true)
	if len(realmIDs) > 0 {
		query = query.Where("realm_id IS NULL OR realm_id IN ?", realmIDs)
	} else {
		query = query.Where("realm_id IS NULL")
	}
	var ids []uuid.UUID
	if err := query.Pluck("id", &ids).Error; err != nil {
		s.log.Warn("timeline: gatekept publishers", "error", err)
	}
	return ids
}

// cachedPublicRealms mirrors GetCachedPublicRealmsAsync.
func (s *service) cachedPublicRealms(ctx context.Context, order string, take int) []*gen.DyRealm {
	if order == "random" {
		return s.fetchPublicRealms(ctx, order, take)
	}
	key := "timeline:public-realms:" + order + ":" + itoa(take)
	var cached []*gen.DyRealm
	if s.cacheGet(ctx, key, &cached) {
		return cached
	}
	realms := s.fetchPublicRealms(ctx, order, take)
	s.cacheSet(ctx, key, realms, publicRealmsCacheTTL)
	return realms
}

func (s *service) fetchPublicRealms(ctx context.Context, order string, take int) []*gen.DyRealm {
	if s.clients == nil || s.clients.Realm == nil {
		return nil
	}
	request := &gen.DyGetPublicRealmsRequest{Take: int32(take)}
	if order != "" {
		request.OrderBy = &order
	}
	resp, err := s.clients.Realm.GetPublicRealms(ctx, request)
	if err != nil {
		s.log.Warn("timeline: public realms", "error", err)
		return nil
	}
	return resp.GetRealms()
}

func itoa(value int) string { return strconv.Itoa(value) }

// boostedPostIDs mirrors GetBoostedPostIdsForTimelineAsync.
func (s *service) boostedPostIDs(ctx context.Context, accountID uuid.UUID, cursor *time.Time) []uuid.UUID {
	cacheKey := "timeline:boosted-posts:" + accountID.String()
	if cursor == nil {
		var cached []uuid.UUID
		if s.cacheGet(ctx, cacheKey, &cached) {
			return cached
		}
	}
	followed := s.subscribedPublishers(ctx, accountID)
	if len(followed) == 0 {
		return nil
	}
	query := s.db.WithContext(ctx).Model(&store.BoostEntity{}).
		Where("publisher_id IN ?", followed)
	if cursor != nil {
		query = query.Where("boosted_at < ?", *cursor)
	}
	var rows []uuid.UUID
	if err := query.Order("boosted_at DESC").Pluck("post_id", &rows).Error; err != nil {
		s.log.Warn("timeline: boosted posts", "error", err)
		return nil
	}
	seen := make(map[uuid.UUID]struct{}, len(rows))
	ids := make([]uuid.UUID, 0, len(rows))
	for _, id := range rows {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if cursor == nil {
		s.cacheSet(ctx, cacheKey, ids, boostedPostsTTL)
	}
	return ids
}

// visibleFediverseActorIDs mirrors GetVisibleFediverseActorIdsAsync.
func (s *service) visibleFediverseActorIDs(ctx context.Context, accountID uuid.UUID, friends []uuid.UUID) []uuid.UUID {
	key := "timeline:visible-fediverse-actors:" + accountID.String()
	var cached []uuid.UUID
	if s.cacheGet(ctx, key, &cached) {
		return cached
	}
	visible := map[uuid.UUID]struct{}{}
	for _, id := range s.subscribedPublishers(ctx, accountID) {
		visible[id] = struct{}{}
	}
	if len(friends) > 0 {
		var friendPublisherIDs []uuid.UUID
		if err := s.db.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
			Where("account_id IN ?", friends).
			Where("state = ? AND ended_at IS NULL AND is_blocking = ?", model.SubscriptionAccepted, false).
			Pluck("publisher_id", &friendPublisherIDs).Error; err == nil {
			for _, id := range friendPublisherIDs {
				visible[id] = struct{}{}
			}
		}
	}
	out := make([]uuid.UUID, 0, len(visible))
	for id := range visible {
		out = append(out, id)
	}
	s.cacheSet(ctx, key, out, fediverseVisibleTTL)
	return out
}

// ---------------------------------------------------------------------------
// Anonymous feed
// ---------------------------------------------------------------------------

// listEventsForAnyone mirrors TimelineService.ListEventsForAnyone.
func (s *service) listEventsForAnyone(ctx context.Context, take int, cursor *time.Time, mode timelineMode, scope timelineFeedScope) (model.TimelinePage, error) {
	realms := s.cachedPublicRealms(ctx, "", 0)
	realmIDs := make([]uuid.UUID, 0, len(realms))
	for _, realm := range realms {
		if id, err := uuid.Parse(realm.GetId()); err == nil {
			realmIDs = append(realmIDs, id)
		}
	}
	gatekept := s.gatekeptPublisherIDs(ctx, realmIDs)

	query := s.buildPostsQuery(ctx, feedQuery{
		cursor:             cursor,
		realmIDs:           realmIDs,
		scopedPublisherID:  scope.publisherID,
		scopedCollectionID: scope.collectionID,
		limit:              take * timelineCandidateMultiplier,
	})
	query = s.applyVisibility(query, visibilityFilter{gatekeptPublisherIDs: gatekept})

	entities, err := s.fetchPosts(ctx, query)
	if err != nil {
		return model.TimelinePage{}, err
	}
	hc := hydrateContext{}
	posts := s.hydratePosts(ctx, entities, hc)
	s.attachChainedPosts(ctx, posts, hc)
	posts = s.rankPosts(ctx, posts, take, nil, mode, true)
	finalizePostContent(posts)

	now := time.Now().UTC()
	activities := make([]model.TimelineEvent, 0, len(posts))
	if scope.hasExplicitScope() {
		for i := range posts {
			activities = append(activities, postTimelineEvent(posts[i], postInstant(&posts[i])))
		}
	} else {
		posts = s.insertSponsored(ctx, posts)
		for i := range posts {
			if rand.Float64() < 0.15 {
				if discovery := s.maybeGetAnonymousDiscoveryActivity(ctx); discovery != nil {
					activities = append(activities, *discovery)
				}
			}
			activities = append(activities, postTimelineEvent(posts[i], postInstant(&posts[i])))
		}
	}
	if len(activities) == 0 {
		activities = append(activities, emptyTimelineEvent(now))
	}
	page := buildTimelinePage(activities, mode)
	s.attachEventRealms(ctx, page)
	s.recordPageViews(ctx, page, "")
	return page, nil
}

func postInstant(post *model.Post) time.Time {
	if post.PublishedAt != nil {
		return post.PublishedAt.Time()
	}
	if post.CreatedAt != nil {
		return post.CreatedAt.Time()
	}
	return time.Time{}
}

// ---------------------------------------------------------------------------
// Authenticated feed
// ---------------------------------------------------------------------------

// listEvents mirrors TimelineService.ListEvents.
func (s *service) listEvents(
	ctx context.Context,
	take int,
	cursor *time.Time,
	user *gen.DyAccount,
	mode timelineMode,
	filter string,
	scope timelineFeedScope,
	aggressive bool,
) (model.TimelinePage, error) {
	accountID, err := uuid.Parse(user.GetId())
	if err != nil {
		return model.TimelinePage{}, err
	}

	friends := s.cachedFriendIDs(ctx, accountID)
	userPublisherIDs := s.userPublishers(ctx, accountID)
	blockedAccountIDs := s.cachedBlockedAccountIDs(ctx, accountID)
	mutedAccountIDs := s.cachedMutedAccountIDs(ctx, accountID)
	blockedPublisherIDs := s.subscriptionPublisherIDs(ctx, accountID, "is_blocking")
	mutedPublisherIDs := s.subscriptionPublisherIDs(ctx, accountID, "is_muting")
	closeFriendPublisherIDs, _ := s.cachedCloseFriendPublisherIDs(ctx, accountID)

	var filteredPublisherIDs []uuid.UUID
	switch normalizeKind(filter) {
	case "subscriptions":
		filteredPublisherIDs = s.subscribedPublishers(ctx, accountID)
	case "friends":
		filteredPublisherIDs = s.friendPublisherIDs(ctx, friends)
	}

	userRealmIDs := s.cachedUserRealms(ctx, accountID)
	boostedIDs := s.boostedPostIDs(ctx, accountID, cursor)
	visibleFediverse := s.visibleFediverseActorIDs(ctx, accountID, friends)

	query := s.buildPostsQuery(ctx, feedQuery{
		cursor:               cursor,
		filteredPublisherIDs: filteredPublisherIDs,
		realmIDs:             userRealmIDs,
		scopedPublisherID:    scope.publisherID,
		scopedCollectionID:   scope.collectionID,
	})
	if len(visibleFediverse) > 0 {
		query = query.Where("posts.fediverse_uri IS NULL OR posts.publisher_id IN ?", visibleFediverse)
	} else {
		query = query.Where("posts.fediverse_uri IS NULL")
	}
	query = query.Where("posts.drafted_at IS NULL")

	var timelinePublisherIDs []uuid.UUID
	if trimSpace(filter) == "" {
		timelinePublisherIDs = userPublisherIDs
	}

	gatekept := s.gatekeptPublisherIDs(ctx, userRealmIDs)
	var followerPublisherIDs []uuid.UUID
	if len(gatekept) > 0 {
		followerPublisherIDs = s.subscribedPublishers(ctx, accountID)
	}

	query = s.applyVisibility(query, visibilityFilter{
		currentUser:             user,
		userFriends:             friends,
		publisherIDs:            timelinePublisherIDs,
		gatekeptPublisherIDs:    gatekept,
		followerPublisherIDs:    followerPublisherIDs,
		blockedAccountIDs:       blockedAccountIDs,
		mutedAccountIDs:         mutedAccountIDs,
		closeFriendPublisherIDs: closeFriendPublisherIDs,
		blockedPublisherIDs:     blockedPublisherIDs,
		mutedPublisherIDs:       mutedPublisherIDs,
	})
	query = query.Limit(take * timelineCandidateMultiplier)

	entities, err := s.fetchPosts(ctx, query)
	if err != nil {
		return model.TimelinePage{}, err
	}

	hc := hydrateContext{
		viewerAccountID: &accountID,
		currentUser:     user,
		userFriends:     friends,
		userPublishers:  userPublisherIDs,
	}
	posts := s.hydratePosts(ctx, entities, hc)

	existing := make(map[string]struct{}, len(posts))
	for i := range posts {
		existing[posts[i].ID] = struct{}{}
	}
	extraIDs := make([]uuid.UUID, 0, len(boostedIDs))
	for _, id := range boostedIDs {
		if _, ok := existing[id.String()]; ok {
			continue
		}
		extraIDs = append(extraIDs, id)
	}
	if len(extraIDs) > 0 {
		boostedQuery := s.db.WithContext(ctx).Model(&store.PostEntity{}).
			Where("id IN ?", extraIDs).
			Where("drafted_at IS NULL")
		if cursor != nil {
			boostedQuery = boostedQuery.Where("published_at < ?", *cursor)
		}
		boostedEntities, err := s.fetchPosts(ctx, boostedQuery)
		if err != nil {
			return model.TimelinePage{}, err
		}
		posts = append(posts, s.hydratePosts(ctx, boostedEntities, hc)...)
	}

	s.attachChainedPosts(ctx, posts, hc)
	posts = s.rankPosts(ctx, posts, take, user, mode, aggressive)
	if err := s.updateLastReadAt(ctx, accountID, posts); err != nil {
		s.log.Warn("timeline: update last read at", "error", err)
	}
	s.cacheSet(ctx, "timeline:soft-cursor:"+accountID.String(), time.Now().UTC(), softCursorTTL)
	finalizePostContent(posts)

	if scope.hasExplicitScope() {
		activities := make([]model.TimelineEvent, 0, len(posts))
		for i := range posts {
			activities = append(activities, postTimelineEvent(posts[i], postInstant(&posts[i])))
		}
		if len(activities) == 0 {
			activities = append(activities, emptyTimelineEvent(time.Now().UTC()))
		}
		page := buildTimelinePage(activities, mode)
		s.attachEventRealms(ctx, page)
		s.recordPageViews(ctx, page, accountID.String())
		return page, nil
	}

	posts = s.insertSponsored(ctx, posts)
	postEvents := make([]model.TimelineEvent, 0, len(posts))
	for i := range posts {
		postEvents = append(postEvents, postTimelineEvent(posts[i], postInstant(&posts[i])))
	}
	statusEvents := s.cachedFriendsStatuses(ctx, accountID, friends)
	presenceAccountIDs := append(append([]uuid.UUID{}, friends...), accountID)
	presenceEvents := s.cachedFriendsPresence(ctx, accountID, presenceAccountIDs, cursor, take)
	merged := mergeEventsByTime(postEvents, presenceEvents, statusEvents, take)

	discoveryIndex := -1
	if cursor == nil && len(merged) >= 4 {
		discoveryIndex = 4
	}
	var personalized *model.TimelineEvent
	activities := make([]model.TimelineEvent, 0, len(merged)+1)
	for i := range merged {
		if i == discoveryIndex {
			if personalized == nil {
				personalized = s.maybeGetDiscoveryActivity(ctx, user, friends, userPublisherIDs, userRealmIDs)
			}
			if personalized != nil {
				activities = append(activities, *personalized)
			}
		}
		activities = append(activities, merged[i])
	}
	if len(activities) == 0 {
		activities = append(activities, emptyTimelineEvent(time.Now().UTC()))
	}
	page := buildTimelinePage(activities, mode)
	s.attachEventRealms(ctx, page)
	s.recordPageViews(ctx, page, accountID.String())
	return page, nil
}

// subscriptionPublisherIDs lists blocked or muted publisher subscriptions.
func (s *service) subscriptionPublisherIDs(ctx context.Context, accountID uuid.UUID, flag string) []uuid.UUID {
	if flag != "is_blocking" && flag != "is_muting" {
		return nil
	}
	var ids []uuid.UUID
	if err := s.db.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
		Where("account_id = ? AND ended_at IS NULL AND "+flag+" = ?", accountID, true).
		Pluck("publisher_id", &ids).Error; err != nil {
		s.log.Warn("timeline: subscription flags", "flag", flag, "error", err)
	}
	return ids
}

// friendPublisherIDs mirrors PublisherService.GetUserPublishersBatch for the
// user's friends, flattened.
func (s *service) friendPublisherIDs(ctx context.Context, friendIDs []uuid.UUID) []uuid.UUID {
	if len(friendIDs) == 0 {
		return nil
	}
	var ids []uuid.UUID
	if err := s.db.WithContext(ctx).Model(&store.PublisherMemberEntity{}).
		Where("account_id IN ?", friendIDs).
		Distinct("publisher_id").
		Pluck("publisher_id", &ids).Error; err != nil {
		s.log.Warn("timeline: friend publishers", "error", err)
	}
	return ids
}

// updateLastReadAt mirrors PublisherSubscriptionService.UpdateLastReadAtForPostsAsync.
func (s *service) updateLastReadAt(ctx context.Context, accountID uuid.UUID, posts []model.Post) error {
	latest := map[string]time.Time{}
	for i := range posts {
		instant := postInstant(&posts[i])
		if instant.IsZero() {
			continue
		}
		if current, ok := latest[posts[i].PublisherID]; !ok || instant.After(current) {
			latest[posts[i].PublisherID] = instant
		}
	}
	if len(latest) == 0 {
		return nil
	}
	publisherIDs := make([]uuid.UUID, 0, len(latest))
	for raw := range latest {
		if id, err := uuid.Parse(raw); err == nil {
			publisherIDs = append(publisherIDs, id)
		}
	}
	var subscriptions []store.PublisherSubscriptionEntity
	if err := s.db.WithContext(ctx).
		Where("account_id = ? AND ended_at IS NULL AND state = ?", accountID, model.SubscriptionAccepted).
		Where("publisher_id IN ?", publisherIDs).
		Find(&subscriptions).Error; err != nil {
		return err
	}
	changed := false
	for i := range subscriptions {
		value := latest[subscriptions[i].PublisherID.String()]
		if subscriptions[i].LastReadAt == nil || subscriptions[i].LastReadAt.Before(value) {
			readAt := value
			subscriptions[i].LastReadAt = &readAt
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.db.WithContext(ctx).Save(&subscriptions).Error
}

// fetchPosts runs a post query.
func (s *service) fetchPosts(ctx context.Context, query *gorm.DB) ([]store.PostEntity, error) {
	var entities []store.PostEntity
	if err := query.Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// ---------------------------------------------------------------------------
// Friend presence and status events
// ---------------------------------------------------------------------------

// cachedFriendsPresence mirrors GetCachedFriendsPresence.
func (s *service) cachedFriendsPresence(ctx context.Context, accountID uuid.UUID, accountIDs []uuid.UUID, cursor *time.Time, take int) []model.TimelineEvent {
	if len(accountIDs) == 0 {
		return nil
	}
	cursorKey := "0"
	if cursor != nil {
		cursorKey = itoa(int(cursor.UnixNano()))
	}
	cacheKey := "timeline:presence:" + accountID.String() + ":" + cursorKey + ":" + itoa(take)
	var cached []model.TimelineEvent
	if s.cacheGet(ctx, cacheKey, &cached) {
		return cached
	}
	if s.clients == nil || s.clients.Presence == nil {
		return nil
	}
	request := &gen.DyListFriendsActivitiesRequest{
		MaxPerType: 1,
		Take:       int32(take),
	}
	for _, id := range accountIDs {
		request.AccountIds = append(request.AccountIds, id.String())
	}
	if cursor != nil {
		request.Cursor = timestamppb.New(*cursor)
	}
	resp, err := s.clients.Presence.ListFriendsActivities(ctx, request)
	if err != nil {
		s.log.Warn("timeline: friend presence", "error", err)
		return nil
	}
	activities := make([]presenceActivity, 0, len(resp.GetActivities()))
	accountSet := make([]string, 0, len(resp.GetActivities()))
	seen := map[string]struct{}{}
	for _, raw := range resp.GetActivities() {
		if raw == nil {
			continue
		}
		activity := presenceActivityFromProto(raw)
		activities = append(activities, activity)
		if _, ok := seen[activity.AccountID]; !ok {
			seen[activity.AccountID] = struct{}{}
			accountSet = append(accountSet, activity.AccountID)
		}
	}
	accounts := s.accountBatch(ctx, accountSet)
	events := make([]model.TimelineEvent, 0, len(activities))
	for i := range activities {
		if account, ok := accounts[activities[i].AccountID]; ok {
			activities[i].Account = account
		}
		created := time.Time{}
		if activities[i].UpdatedAt != nil {
			created = activities[i].UpdatedAt.Time()
		}
		now := time.Now().UTC()
		events = append(events, newTimelineEvent(
			activities[i].ID,
			"presence.friend",
			"presence:"+activities[i].AccountID+":"+activities[i].ID,
			friendPresenceEvent{Activity: activities[i]},
			created,
			now,
		))
	}
	s.cacheSet(ctx, cacheKey, events, friendPresenceTTL)
	return events
}

// cachedFriendsStatuses mirrors GetCachedFriendsStatuses.
func (s *service) cachedFriendsStatuses(ctx context.Context, accountID uuid.UUID, friendIDs []uuid.UUID) []model.TimelineEvent {
	if len(friendIDs) == 0 {
		return nil
	}
	cacheKey := "timeline:status:" + accountID.String()
	var cached []model.TimelineEvent
	if s.cacheGet(ctx, cacheKey, &cached) {
		return cached
	}
	if s.clients == nil || s.clients.Profile == nil {
		return nil
	}
	ids := make([]string, 0, len(friendIDs))
	for _, id := range friendIDs {
		ids = append(ids, id.String())
	}
	resp, err := s.clients.Profile.GetAccountStatusBatch(ctx, &gen.DyGetAccountBatchRequest{Id: ids})
	if err != nil {
		s.log.Warn("timeline: friend statuses", "error", err)
		return nil
	}
	statuses := make([]accountStatus, 0, len(resp.GetStatuses()))
	accountSet := make([]string, 0, len(resp.GetStatuses()))
	seen := map[string]struct{}{}
	for _, raw := range resp.GetStatuses() {
		if raw == nil {
			continue
		}
		status := accountStatusFromProto(raw)
		statuses = append(statuses, status)
		if _, ok := seen[status.AccountID]; !ok {
			seen[status.AccountID] = struct{}{}
			accountSet = append(accountSet, status.AccountID)
		}
	}
	accounts := s.accountBatch(ctx, accountSet)
	now := time.Now().UTC()
	events := make([]model.TimelineEvent, 0, len(statuses))
	for i := range statuses {
		if account, ok := accounts[statuses[i].AccountID]; ok {
			statuses[i].Account = account
		}
		if !shouldIncludeFriendStatus(&statuses[i]) {
			continue
		}
		created := now
		if statuses[i].UpdatedAt != nil {
			created = statuses[i].UpdatedAt.Time()
		} else if statuses[i].CreatedAt != nil {
			created = statuses[i].CreatedAt.Time()
		}
		events = append(events, newTimelineEvent(
			statuses[i].ID,
			"status.friend",
			"status:"+statuses[i].AccountID+":"+statuses[i].ID,
			friendStatusEvent{Status: statuses[i]},
			created,
			now,
		))
	}
	sort.Slice(events, func(i, j int) bool {
		return eventInstant(events[j]).Before(eventInstant(events[i]))
	})
	s.cacheSet(ctx, cacheKey, events, friendStatusTTL)
	return events
}

// shouldIncludeFriendStatus mirrors TimelineService.ShouldIncludeFriendStatus.
func shouldIncludeFriendStatus(status *accountStatus) bool {
	const statusTypeInvisible = 3
	return status.IsCustomized &&
		status.Type != statusTypeInvisible &&
		status.DeletedAt == nil &&
		status.ClearedAt == nil
}

// mergeEventsByTime mirrors MergeEventsByTime.
func mergeEventsByTime(postEvents, presenceEvents, statusEvents []model.TimelineEvent, take int) []model.TimelineEvent {
	merged := make([]model.TimelineEvent, 0, len(postEvents)+len(presenceEvents)+len(statusEvents))
	merged = append(merged, postEvents...)
	merged = append(merged, presenceEvents...)
	merged = append(merged, statusEvents...)
	sort.SliceStable(merged, func(i, j int) bool {
		return eventInstant(merged[j]).Before(eventInstant(merged[i]))
	})
	if take > 0 && len(merged) > take {
		merged = merged[:take]
	}
	return merged
}

func eventInstant(event model.TimelineEvent) time.Time {
	if event.CreatedAt == nil {
		return time.Time{}
	}
	return event.CreatedAt.Time()
}

// ---------------------------------------------------------------------------
// Discovery activities
// ---------------------------------------------------------------------------

// discoveryItem mirrors DiscoveryItem.
type discoveryItem struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// discoveryEvent mirrors TimelineDiscoveryEvent.
type discoveryEvent struct {
	Items []discoveryItem `json:"items"`
}

// maybeGetAnonymousDiscoveryActivity mirrors MaybeGetDiscoveryActivity().
func (s *service) maybeGetAnonymousDiscoveryActivity(ctx context.Context) *model.TimelineEvent {
	options := make([]func(context.Context) *model.TimelineEvent, 0, 3)
	if rand.Float64() < 0.5 {
		options = append(options, s.realmDiscoveryActivity)
	}
	if rand.Float64() < 0.5 {
		options = append(options, s.publisherDiscoveryActivity)
	}
	if rand.Float64() < 0.5 {
		options = append(options, s.shuffledPostsActivity)
	}
	if len(options) == 0 {
		return nil
	}
	return options[rand.IntN(len(options))](ctx)
}

// maybeGetDiscoveryActivity mirrors the personalized MaybeGetDiscoveryActivity.
func (s *service) maybeGetDiscoveryActivity(
	ctx context.Context,
	user *gen.DyAccount,
	friends []uuid.UUID,
	userPublisherIDs []uuid.UUID,
	userRealmIDs []uuid.UUID,
) *model.TimelineEvent {
	profile, err := s.buildDiscoveryProfile(ctx, user, friends, userPublisherIDs, userRealmIDs)
	if err != nil {
		s.log.Warn("timeline: discovery profile", "error", err)
		return nil
	}
	type option struct {
		kind  string
		title string
		items []any
	}
	options := make([]option, 0, 3)
	if len(profile.SuggestedPublishers) > 0 {
		picked := pickSuggestion(profile.SuggestedPublishers)
		options = append(options, option{"publisher", "Suggested publisher", []any{picked}})
	}
	if len(profile.SuggestedAccounts) > 0 {
		picked := pickSuggestion(profile.SuggestedAccounts)
		options = append(options, option{"account", "People you may know", []any{picked}})
	}
	if len(profile.SuggestedRealms) > 0 {
		picked := pickSuggestion(profile.SuggestedRealms)
		options = append(options, option{"realm", "Suggested realm", []any{picked}})
	}
	if len(options) == 0 {
		return nil
	}
	picked := options[rand.IntN(len(options))]
	now := time.Now().UTC()
	event := newTimelineEvent(
		uuid.NewString(),
		"discovery.v2",
		"discovery:"+picked.kind,
		personalizedDiscoveryEvent{Kind: picked.kind, Title: picked.title, Items: picked.items},
		now,
		now,
	)
	return &event
}

func pickSuggestion[T any](items []T) T {
	count := len(items)
	if count > 3 {
		count = 3
	}
	return items[rand.IntN(count)]
}

// realmDiscoveryActivity mirrors GetRealmDiscoveryActivity.
func (s *service) realmDiscoveryActivity(ctx context.Context) *model.TimelineEvent {
	realms := s.cachedPublicRealms(ctx, "random", 5)
	if len(realms) == 0 {
		return nil
	}
	items := make([]discoveryItem, 0, len(realms))
	for _, realm := range realms {
		items = append(items, discoveryItem{Type: "realm", Data: realmRefFromProto(realm)})
	}
	now := time.Now().UTC()
	event := newTimelineEvent(uuid.NewString(), "discovery", "discovery", discoveryEvent{Items: items}, now, now)
	return &event
}

// publisherDiscoveryActivity mirrors GetPublisherDiscoveryActivity.
func (s *service) publisherDiscoveryActivity(ctx context.Context) *model.TimelineEvent {
	publishers := s.popularPublishers(ctx, 5)
	if len(publishers) == 0 {
		return nil
	}
	items := make([]discoveryItem, 0, len(publishers))
	for i := range publishers {
		items = append(items, discoveryItem{Type: "publisher", Data: publishers[i]})
	}
	now := time.Now().UTC()
	event := newTimelineEvent(uuid.NewString(), "discovery", "discovery", discoveryEvent{Items: items}, now, now)
	return &event
}

// popularPublishers mirrors GetPopularPublishers.
func (s *service) popularPublishers(ctx context.Context, take int) []model.Publisher {
	recent := time.Now().UTC().AddDate(0, 0, -7)
	var posts []store.PostEntity
	if err := s.db.WithContext(ctx).
		Where("drafted_at IS NULL AND published_at > ?", recent).
		Find(&posts).Error; err != nil {
		s.log.Warn("timeline: popular publishers", "error", err)
		return nil
	}
	if len(posts) == 0 {
		return nil
	}
	byPublisher := map[uuid.UUID][]store.PostEntity{}
	for i := range posts {
		byPublisher[posts[i].PublisherID] = append(byPublisher[posts[i].PublisherID], posts[i])
	}
	ids := make([]uuid.UUID, 0, len(byPublisher))
	for id := range byPublisher {
		ids = append(ids, id)
	}
	var rows []store.PublisherEntity
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil
	}
	type ranked struct {
		publisher model.Publisher
		rank      float64
	}
	rankedPublishers := make([]ranked, 0, len(rows))
	for i := range rows {
		rank := 0.0
		for _, post := range byPublisher[rows[i].ID] {
			rank += float64(post.Upvotes - post.Downvotes)
		}
		rank += float64(len(byPublisher[rows[i].ID]))
		rankedPublishers = append(rankedPublishers, ranked{store.PublisherToModel(&rows[i]), rank})
	}
	sort.SliceStable(rankedPublishers, func(i, j int) bool {
		return rankedPublishers[j].rank < rankedPublishers[i].rank
	})
	out := make([]model.Publisher, 0, take)
	for i := 0; i < len(rankedPublishers) && i < take; i++ {
		out = append(out, rankedPublishers[i].publisher)
	}
	return out
}

// shuffledPostsActivity mirrors GetShuffledPostsActivity.
func (s *service) shuffledPostsActivity(ctx context.Context) *model.TimelineEvent {
	realms := s.cachedPublicRealms(ctx, "", 0)
	realmIDs := make([]uuid.UUID, 0, len(realms))
	for _, realm := range realms {
		if id, err := uuid.Parse(realm.GetId()); err == nil {
			realmIDs = append(realmIDs, id)
		}
	}
	query := s.db.WithContext(ctx).Model(&store.PostEntity{}).
		Where("drafted_at IS NULL AND visibility = ?", model.VisibilityPublic).
		Where("replied_post_id IS NULL AND chained_post_id IS NULL")
	if len(realmIDs) > 0 {
		query = query.Where("realm_id IS NULL OR realm_id IN ?", realmIDs)
	} else {
		query = query.Where("realm_id IS NULL")
	}
	query = query.Order("random()").Limit(5)
	entities, err := s.fetchPosts(ctx, query)
	if err != nil || len(entities) == 0 {
		return nil
	}
	posts := s.hydratePosts(ctx, entities, hydrateContext{})
	finalizePostContent(posts)
	items := make([]discoveryItem, 0, len(posts))
	for i := range posts {
		items = append(items, discoveryItem{Type: "post", Data: posts[i]})
	}
	now := time.Now().UTC()
	event := newTimelineEvent(uuid.NewString(), "discovery", "discovery", discoveryEvent{Items: items}, now, now)
	return &event
}

// ---------------------------------------------------------------------------
// Ranking
// ---------------------------------------------------------------------------

// postRankMeta carries the entity-only ranking inputs the wire type lacks.
type postRankMeta struct {
	reactionScore   int
	realmBoostLevel int
}

// rankPosts mirrors TimelineService.RankPosts.
func (s *service) rankPosts(
	ctx context.Context,
	posts []model.Post,
	take int,
	user *gen.DyAccount,
	mode timelineMode,
	aggressive bool,
) []model.Post {
	if len(posts) == 0 {
		return posts
	}
	if mode == modeLatest {
		return sortLatestPosts(posts, take)
	}
	now := time.Now().UTC()
	meta := s.rankMeta(ctx, posts)

	var (
		personalization map[string]float64
		subscriptions   map[string]struct{}
	)
	if mode == modePersonalized && user != nil {
		if accountID, err := uuid.Parse(user.GetId()); err == nil {
			personalization = s.personalizationBonus(ctx, posts, accountID, now)
		}
	}
	if user != nil {
		if accountID, err := uuid.Parse(user.GetId()); err == nil {
			subscriptions = map[string]struct{}{}
			for _, id := range s.subscribedPublishers(ctx, accountID) {
				subscriptions[id.String()] = struct{}{}
			}
		}
	}
	automated := s.automatedPublisherAccounts(ctx, posts)

	type candidate struct {
		post       model.Post
		rank       float64
		shouldHide bool
	}
	candidates := make([]candidate, 0, len(posts))
	for i := range posts {
		info := meta[posts[i].ID]
		baseRank := calculateBaseRank(&posts[i], info, now)
		bonus := personalization[posts[i].ID] * personalizationBoostMultiplier
		ratingBonus := 0.0
		if posts[i].Publisher != nil {
			ratingBonus = math.Min(3, float64(posts[i].Publisher.RatingLevel)*0.05)
		}
		penalty := 0.0
		if posts[i].Publisher != nil && posts[i].Publisher.AccountID != nil {
			if automated[*posts[i].Publisher.AccountID] {
				penalty = automatedPostPenalty
			}
		}
		boost := 0.0
		if _, ok := subscriptions[posts[i].PublisherID]; ok {
			boost = subscriptionBoostBonus
		}
		isShadowbanned := posts[i].IsShadowbanned ||
			(posts[i].Publisher != nil && posts[i].Publisher.IsShadowbanned)
		candidates = append(candidates, candidate{
			post:       posts[i],
			rank:       baseRank + bonus + ratingBonus - penalty + boost,
			shouldHide: isShadowbanned,
		})
	}

	visible := make([]candidate, 0, len(candidates))
	for _, item := range candidates {
		if !item.shouldHide {
			visible = append(visible, item)
		}
	}
	sort.SliceStable(visible, func(i, j int) bool { return visible[j].rank < visible[i].rank })

	if mode == modePersonalized && user != nil && aggressive && len(visible) > take {
		cutoff := math.Max(personalizedLowRankThresholdFloor, visible[0].rank*personalizedLowRankThresholdRatio)
		filtered := make([]candidate, 0, len(visible))
		for _, item := range visible {
			if item.rank >= cutoff {
				filtered = append(filtered, item)
			}
		}
		minimum := take
		if minimum < 3 {
			minimum = 3
		}
		switch {
		case len(filtered) >= minimum:
			visible = filtered
		case minimum > len(visible):
			visible = visible[:len(visible)]
		default:
			visible = visible[:minimum]
		}
	}

	selected := make([]model.Post, 0, take)
	publisherCounts := map[string]int{}
	for _, item := range visible {
		if len(selected) >= take {
			break
		}
		penalty := 0.0
		if item.post.PublisherID != "" {
			penalty = float64(publisherCounts[item.post.PublisherID]) * publisherRepeatPenalty
		}
		item.post.DebugRank = item.rank - penalty
		selected = append(selected, item.post)
		if item.post.PublisherID != "" {
			publisherCounts[item.post.PublisherID]++
		}
	}
	return selected
}

// calculateBaseRank mirrors TimelineService.CalculateBaseRank.
func calculateBaseRank(post *model.Post, info postRankMeta, now time.Time) float64 {
	performance := float64(info.reactionScore)*1.4 + float64(post.ThreadRepliesCount)*0.8 + post.AwardedScore/10
	if post.Type == int(model.PostTypeArticle) || post.Type == int(model.PostTypeBlog) {
		performance += articleTypeBoost
	}
	if post.RealmID != nil && info.realmBoostLevel > 0 {
		performance += float64(info.realmBoostLevel) * realmBoostLevelRankBonus
	}
	postTime := postInstant(post)
	elapsedMinutes := now.Sub(postTime).Minutes()
	performanceWeight := performance + 5
	normalizedTime := elapsedMinutes / 60
	return performanceWeight / math.Pow(normalizedTime+1, 1.2)
}

// rankMeta resolves the entity-only ranking inputs.
func (s *service) rankMeta(ctx context.Context, posts []model.Post) map[string]postRankMeta {
	out := make(map[string]postRankMeta, len(posts))
	ids := make([]uuid.UUID, 0, len(posts))
	realmIDs := make([]string, 0, len(posts))
	seenRealm := map[string]struct{}{}
	for i := range posts {
		if id, err := uuid.Parse(posts[i].ID); err == nil {
			ids = append(ids, id)
		}
		if posts[i].RealmID != nil {
			if _, ok := seenRealm[*posts[i].RealmID]; !ok {
				seenRealm[*posts[i].RealmID] = struct{}{}
				realmIDs = append(realmIDs, *posts[i].RealmID)
			}
		}
	}
	scores := map[uuid.UUID]int{}
	if len(ids) > 0 {
		var rows []struct {
			ID            uuid.UUID
			ReactionScore int
		}
		if err := s.db.WithContext(ctx).Model(&store.PostEntity{}).
			Select("id, reaction_score").
			Where("id IN ?", ids).
			Scan(&rows).Error; err == nil {
			for _, row := range rows {
				scores[row.ID] = row.ReactionScore
			}
		}
	}
	boosts := map[string]int{}
	if len(realmIDs) > 0 {
		for id, realm := range s.realmBatch(ctx, realmIDs) {
			boosts[id] = int(realm.GetBoostLevel())
		}
	}
	for i := range posts {
		info := postRankMeta{}
		if id, err := uuid.Parse(posts[i].ID); err == nil {
			info.reactionScore = scores[id]
		}
		if posts[i].RealmID != nil {
			info.realmBoostLevel = boosts[*posts[i].RealmID]
		}
		out[posts[i].ID] = info
	}
	return out
}

// personalizationBonus mirrors GetPersonalizationBonusMap.
func (s *service) personalizationBonus(ctx context.Context, posts []model.Post, accountID uuid.UUID, now time.Time) map[string]float64 {
	out := make(map[string]float64, len(posts))
	if len(posts) == 0 {
		return out
	}
	tagIDs := make([]uuid.UUID, 0)
	categoryIDs := make([]uuid.UUID, 0)
	publisherIDs := make([]uuid.UUID, 0)
	collectionIDs := make([]uuid.UUID, 0)
	collectionByPost := map[string][]uuid.UUID{}
	collect := func(target *[]uuid.UUID, set map[uuid.UUID]struct{}, raw string) {
		id, err := uuid.Parse(raw)
		if err != nil {
			return
		}
		if _, ok := set[id]; ok {
			return
		}
		set[id] = struct{}{}
		*target = append(*target, id)
	}
	tagSeen := map[uuid.UUID]struct{}{}
	categorySeen := map[uuid.UUID]struct{}{}
	publisherSeen := map[uuid.UUID]struct{}{}
	collectionSeen := map[uuid.UUID]struct{}{}
	for i := range posts {
		for _, tag := range posts[i].Tags {
			collect(&tagIDs, tagSeen, tag.ID)
		}
		for _, category := range posts[i].Categories {
			collect(&categoryIDs, categorySeen, category.ID)
		}
		if posts[i].PublisherID != "" {
			collect(&publisherIDs, publisherSeen, posts[i].PublisherID)
		}
		for _, collection := range posts[i].PublisherCollections {
			id, err := uuid.Parse(collection.ID)
			if err != nil {
				continue
			}
			collectionByPost[posts[i].ID] = append(collectionByPost[posts[i].ID], id)
			collect(&collectionIDs, collectionSeen, collection.ID)
		}
	}

	conditions := make([]string, 0, 4)
	args := make([]any, 0, 8)
	addCondition := func(kind postInterestKind, ids []uuid.UUID) {
		if len(ids) == 0 {
			return
		}
		conditions = append(conditions, "(kind = ? AND reference_id IN ?)")
		args = append(args, int(kind), ids)
	}
	addCondition(interestTag, tagIDs)
	addCondition(interestCategory, categoryIDs)
	addCondition(interestPublisher, publisherIDs)
	addCondition(interestCollection, collectionIDs)

	var profiles []store.PostInterestProfileEntity
	if len(conditions) > 0 {
		if err := s.db.WithContext(ctx).
			Where("account_id = ?", accountID).
			Where("("+joinWithOr(conditions)+")", args...).
			Find(&profiles).Error; err != nil {
			s.log.Warn("timeline: personalization profiles", "error", err)
		}
	}
	tagInterest := s.decayedInterest(profiles, interestTag, now)
	categoryInterest := s.decayedInterest(profiles, interestCategory, now)
	publisherInterest := s.decayedInterest(profiles, interestPublisher, now)
	collectionInterest := s.decayedInterest(profiles, interestCollection, now)

	subscribedTags := map[uuid.UUID]struct{}{}
	subscribedCategories := map[uuid.UUID]struct{}{}
	subscriptionConditions := make([]string, 0, 2)
	subscriptionArgs := make([]any, 0, 4)
	if len(tagIDs) > 0 {
		subscriptionConditions = append(subscriptionConditions, "tag_id IN ?")
		subscriptionArgs = append(subscriptionArgs, tagIDs)
	}
	if len(categoryIDs) > 0 {
		subscriptionConditions = append(subscriptionConditions, "category_id IN ?")
		subscriptionArgs = append(subscriptionArgs, categoryIDs)
	}
	if len(subscriptionConditions) > 0 {
		var subscriptions []store.PostCategorySubscriptionEntity
		if err := s.db.WithContext(ctx).
			Where("account_id = ?", accountID).
			Where("("+joinWithOr(subscriptionConditions)+")", subscriptionArgs...).
			Find(&subscriptions).Error; err == nil {
			for i := range subscriptions {
				if subscriptions[i].TagID != nil {
					subscribedTags[*subscriptions[i].TagID] = struct{}{}
				}
				if subscriptions[i].CategoryID != nil {
					subscribedCategories[*subscriptions[i].CategoryID] = struct{}{}
				}
			}
		}
	}

	for i := range posts {
		bonus := 0.0
		for _, tag := range posts[i].Tags {
			if id, err := uuid.Parse(tag.ID); err == nil {
				bonus += tagInterest[id] * 0.8
				if _, ok := subscribedTags[id]; ok {
					bonus += 1.25
				}
			}
		}
		for _, category := range posts[i].Categories {
			if id, err := uuid.Parse(category.ID); err == nil {
				bonus += categoryInterest[id] * 0.75
				if _, ok := subscribedCategories[id]; ok {
					bonus += 1.5
				}
			}
		}
		if posts[i].PublisherID != "" {
			if id, err := uuid.Parse(posts[i].PublisherID); err == nil {
				score := publisherInterest[id]
				if score >= 0 {
					bonus += math.Min(2, score*0.2)
				} else {
					bonus += math.Max(-6, score*0.5)
				}
			}
		}
		for _, collectionID := range collectionByPost[posts[i].ID] {
			bonus += collectionInterest[collectionID] * 0.6
		}
		out[posts[i].ID] = bonus
	}
	return out
}

// automatedPublisherAccounts resolves which post publishers belong to
// automated accounts, caching the status per account.
func (s *service) automatedPublisherAccounts(ctx context.Context, posts []model.Post) map[string]bool {
	out := map[string]bool{}
	accountIDs := make([]string, 0, len(posts))
	seen := map[string]struct{}{}
	for i := range posts {
		if posts[i].Publisher == nil || posts[i].Publisher.AccountID == nil {
			continue
		}
		id := *posts[i].Publisher.AccountID
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		accountIDs = append(accountIDs, id)
	}
	if len(accountIDs) == 0 {
		return out
	}
	missing := make([]string, 0, len(accountIDs))
	for _, id := range accountIDs {
		var cached bool
		if s.cacheGet(ctx, "timeline:automated:"+id, &cached) {
			if cached {
				out[id] = true
			}
			continue
		}
		missing = append(missing, id)
	}
	if len(missing) == 0 || s.clients == nil || s.clients.Profile == nil {
		return out
	}
	resp, err := s.clients.Profile.GetAccountStatusBatch(ctx, &gen.DyGetAccountBatchRequest{Id: missing})
	if err != nil {
		s.log.Warn("timeline: automated statuses", "error", err)
		return out
	}
	statuses := map[string]bool{}
	for _, status := range resp.GetStatuses() {
		statuses[status.GetAccountId()] = status.GetIsAutomated()
	}
	for _, id := range missing {
		isAutomated := statuses[id]
		if isAutomated {
			out[id] = true
		}
		s.cacheSet(ctx, "timeline:automated:"+id, isAutomated, automatedStatusTTL)
	}
	return out
}

// sortLatestPosts mirrors SortLatestPosts.
func sortLatestPosts(posts []model.Post, take int) []model.Post {
	filtered := make([]model.Post, 0, len(posts))
	for i := range posts {
		if posts[i].IsShadowbanned {
			continue
		}
		if posts[i].Publisher != nil && posts[i].Publisher.IsShadowbanned {
			continue
		}
		filtered = append(filtered, posts[i])
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		return postInstant(&filtered[j]).Before(postInstant(&filtered[i]))
	})
	if len(filtered) > take {
		filtered = filtered[:take]
	}
	for i := range filtered {
		filtered[i].DebugRank = 0
	}
	return filtered
}

// buildTimelinePage mirrors BuildTimelinePage.
func buildTimelinePage(activities []model.TimelineEvent, mode timelineMode) model.TimelinePage {
	return model.TimelinePage{
		Items:      activities,
		NextCursor: nextCursor(activities),
		Mode:       mode.String(),
	}
}

func nextCursor(activities []model.TimelineEvent) *string {
	var oldest time.Time
	found := false
	for i := range activities {
		if activities[i].Type == "empty" {
			continue
		}
		instant := eventInstant(activities[i])
		if instant.IsZero() {
			continue
		}
		if !found || instant.Before(oldest) {
			oldest = instant
			found = true
		}
	}
	if !found {
		return nil
	}
	value := oldest.UTC().Format(time.RFC3339Nano)
	return &value
}
