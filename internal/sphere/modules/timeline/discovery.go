package timeline

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// interestAdjustment is one weight change before aggregation.
type interestAdjustment struct {
	kind        postInterestKind
	referenceID uuid.UUID
	delta       float64
}

// discoveryCandidate is the projection the suggestion scoring needs.
type discoveryCandidate struct {
	id            uuid.UUID
	publisherID   uuid.UUID
	realmID       *uuid.UUID
	reactionScore int
	awardedScore  float64
	repliesCount  int
	postType      int
	tagIDs        []uuid.UUID
	categoryIDs   []uuid.UUID
	instant       time.Time
}

// rankedDiscoveryTarget mirrors RankedDiscoveryTarget.
type rankedDiscoveryTarget struct {
	referenceID uuid.UUID
	score       float64
	reasons     []string
}

// discoveryProfileCacheKey mirrors the C# cache key.
func discoveryProfileCacheKey(accountID uuid.UUID) string {
	return "timeline:discovery-profile:" + accountID.String()
}

// discoveryProfile returns the cached profile for a viewer.
func (s *service) discoveryProfile(ctx context.Context, user *gen.DyAccount) (model.DiscoveryProfile, error) {
	accountID, err := uuid.Parse(user.GetId())
	if err != nil {
		return model.DiscoveryProfile{}, err
	}
	cacheKey := discoveryProfileCacheKey(accountID)
	var cached model.DiscoveryProfile
	if s.cacheGet(ctx, cacheKey, &cached) {
		return cached, nil
	}
	friends := s.cachedFriendIDs(ctx, accountID)
	publishers := s.userPublishers(ctx, accountID)
	realms := s.cachedUserRealms(ctx, accountID)
	profile, err := s.buildDiscoveryProfile(ctx, user, friends, publishers, realms)
	if err != nil {
		return model.DiscoveryProfile{}, err
	}
	s.cacheSet(ctx, cacheKey, profile, discoveryProfileTTL)
	return profile, nil
}

// buildDiscoveryProfile mirrors the private TimelineService.GetDiscoveryProfile.
func (s *service) buildDiscoveryProfile(
	ctx context.Context,
	user *gen.DyAccount,
	friends []uuid.UUID,
	userPublisherIDs []uuid.UUID,
	userRealmIDs []uuid.UUID,
) (model.DiscoveryProfile, error) {
	accountID, err := uuid.Parse(user.GetId())
	if err != nil {
		return model.DiscoveryProfile{}, err
	}
	now := time.Now().UTC()

	var interestProfiles []store.PostInterestProfileEntity
	if err := s.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("score DESC").
		Limit(128).
		Find(&interestProfiles).Error; err != nil {
		return model.DiscoveryProfile{}, err
	}
	var preferences []store.DiscoveryPreferenceEntity
	if err := s.db.WithContext(ctx).
		Where("account_id = ? AND state = ?", accountID, discoveryPreferenceUninterested).
		Find(&preferences).Error; err != nil {
		return model.DiscoveryProfile{}, err
	}

	tagInterest := s.decayedInterest(interestProfiles, interestTag, now)
	categoryInterest := s.decayedInterest(interestProfiles, interestCategory, now)
	publisherInterest := s.decayedInterest(interestProfiles, interestPublisher, now)
	collectionInterest := s.decayedInterest(interestProfiles, interestCollection, now)

	publicRealms := s.cachedPublicRealms(ctx, "date", 40)
	visibleRealmIDs := make([]uuid.UUID, 0, len(publicRealms)+len(userRealmIDs))
	seenRealm := map[uuid.UUID]struct{}{}
	for _, realm := range publicRealms {
		if id, err := uuid.Parse(realm.GetId()); err == nil {
			if _, ok := seenRealm[id]; !ok {
				seenRealm[id] = struct{}{}
				visibleRealmIDs = append(visibleRealmIDs, id)
			}
		}
	}
	for _, id := range userRealmIDs {
		if _, ok := seenRealm[id]; !ok {
			seenRealm[id] = struct{}{}
			visibleRealmIDs = append(visibleRealmIDs, id)
		}
	}

	candidates, err := s.discoveryCandidatePosts(ctx, now, visibleRealmIDs)
	if err != nil {
		return model.DiscoveryProfile{}, err
	}
	collectionMap := s.postCollectionMap(ctx, candidateIDs(candidates))

	interests := s.buildDiscoveryInterestEntries(ctx, interestProfiles, now)
	suggestionContext := s.buildDiscoverySuggestionContext(
		ctx, user, friends, userPublisherIDs, userRealmIDs, preferences,
		candidates, publicRealms, tagInterest, categoryInterest,
		publisherInterest, collectionInterest, collectionMap, now,
	)

	publishers := make([]model.Publisher, 0, len(suggestionContext.publishers))
	for _, candidate := range suggestionContext.publishers {
		publishers = append(publishers, candidate.publisher)
	}
	return model.DiscoveryProfile{
		GeneratedAt:         model.NewTime(now),
		Interests:           interests,
		SuggestedPublishers: publishers,
		SuggestedAccounts:   suggestionContext.accounts,
		SuggestedRealms:     suggestionContext.realms,
		Suppressed:          suggestionContext.suppressed,
	}, nil
}

// decayedInterest builds the decayed score map for one interest kind.
func (s *service) decayedInterest(profiles []store.PostInterestProfileEntity, kind postInterestKind, now time.Time) map[uuid.UUID]float64 {
	out := map[uuid.UUID]float64{}
	for i := range profiles {
		if postInterestKind(profiles[i].Kind) != kind {
			continue
		}
		out[profiles[i].ReferenceID] = decayedInterestScore(&profiles[i], now)
	}
	return out
}

// decayedInterestScore mirrors GetDecayedInterestScore.
func decayedInterestScore(profile *store.PostInterestProfileEntity, now time.Time) float64 {
	if profile.LastInteractedAt == nil {
		return profile.Score
	}
	ageDays := now.Sub(*profile.LastInteractedAt).Hours() / 24
	if ageDays < 0 {
		ageDays = 0
	}
	decay := math.Exp(-ageDays / 30)
	return profile.Score * decay
}

// discoveryCandidatePosts mirrors GetDiscoveryCandidatePosts.
func (s *service) discoveryCandidatePosts(ctx context.Context, now time.Time, visibleRealmIDs []uuid.UUID) ([]discoveryCandidate, error) {
	recent := now.AddDate(0, 0, -discoveryLookbackDays)
	query := s.db.WithContext(ctx).Model(&store.PostEntity{}).
		Where("drafted_at IS NULL AND replied_post_id IS NULL AND chained_post_id IS NULL").
		Where("visibility = ?", model.VisibilityPublic).
		Where("(published_at IS NOT NULL AND published_at >= ?) OR (published_at IS NULL AND created_at >= ?)", recent, recent)
	if len(visibleRealmIDs) > 0 {
		query = query.Where("realm_id IS NULL OR realm_id IN ?", visibleRealmIDs)
	} else {
		query = query.Where("realm_id IS NULL")
	}
	var rows []store.PostEntity
	if err := query.Order("published_at DESC").Limit(discoveryCandidatePostTake).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	ids := make([]uuid.UUID, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].ID)
	}
	tagsByPost := s.tagIDsByPost(ctx, ids)
	categoriesByPost := s.categoryIDsByPost(ctx, ids)

	out := make([]discoveryCandidate, 0, len(rows))
	for i := range rows {
		instant := rows[i].CreatedAt
		if rows[i].PublishedAt != nil {
			instant = *rows[i].PublishedAt
		}
		out = append(out, discoveryCandidate{
			id:            rows[i].ID,
			publisherID:   rows[i].PublisherID,
			realmID:       rows[i].RealmID,
			reactionScore: rows[i].ReactionScore,
			awardedScore:  rows[i].AwardedScore,
			repliesCount:  rows[i].RepliesCount,
			postType:      rows[i].Type,
			tagIDs:        tagsByPost[rows[i].ID],
			categoryIDs:   categoriesByPost[rows[i].ID],
			instant:       instant,
		})
	}
	return out, nil
}

func candidateIDs(candidates []discoveryCandidate) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(candidates))
	for i := range candidates {
		ids = append(ids, candidates[i].id)
	}
	return ids
}

func (s *service) tagIDsByPost(ctx context.Context, postIDs []uuid.UUID) map[uuid.UUID][]uuid.UUID {
	out := map[uuid.UUID][]uuid.UUID{}
	if len(postIDs) == 0 {
		return out
	}
	var links []struct {
		PostID uuid.UUID
		TagID  uuid.UUID
	}
	if err := s.db.WithContext(ctx).Table("post_tag_links").
		Select("posts_id AS post_id, tags_id AS tag_id").
		Where("posts_id IN ?", postIDs).
		Scan(&links).Error; err != nil {
		s.log.Warn("timeline: discovery tag links", "error", err)
		return out
	}
	for _, link := range links {
		out[link.PostID] = append(out[link.PostID], link.TagID)
	}
	return out
}

func (s *service) categoryIDsByPost(ctx context.Context, postIDs []uuid.UUID) map[uuid.UUID][]uuid.UUID {
	out := map[uuid.UUID][]uuid.UUID{}
	if len(postIDs) == 0 {
		return out
	}
	var links []struct {
		PostID     uuid.UUID
		CategoryID uuid.UUID
	}
	if err := s.db.WithContext(ctx).Table("post_category_links").
		Select("posts_id AS post_id, categories_id AS category_id").
		Where("posts_id IN ?", postIDs).
		Scan(&links).Error; err != nil {
		s.log.Warn("timeline: discovery category links", "error", err)
		return out
	}
	for _, link := range links {
		out[link.PostID] = append(out[link.PostID], link.CategoryID)
	}
	return out
}

// postCollectionMap mirrors GetPostCollectionMapAsync.
func (s *service) postCollectionMap(ctx context.Context, postIDs []uuid.UUID) map[uuid.UUID][]uuid.UUID {
	out := map[uuid.UUID][]uuid.UUID{}
	if len(postIDs) == 0 {
		return out
	}
	var items []store.PostCollectionItemEntity
	if err := s.db.WithContext(ctx).Where("post_id IN ?", postIDs).Find(&items).Error; err != nil {
		s.log.Warn("timeline: discovery collection items", "error", err)
		return out
	}
	for _, item := range items {
		out[item.PostID] = append(out[item.PostID], item.CollectionID)
	}
	return out
}

// buildDiscoveryInterestEntries mirrors BuildDiscoveryInterestEntries.
func (s *service) buildDiscoveryInterestEntries(ctx context.Context, profiles []store.PostInterestProfileEntity, now time.Time) []model.InterestEntry {
	entries := make([]model.InterestEntry, 0, len(profiles))
	for i := range profiles {
		entries = append(entries, model.InterestEntry{
			Kind:             profiles[i].Kind,
			ReferenceID:      profiles[i].ReferenceID.String(),
			Score:            decayedInterestScore(&profiles[i], now),
			InteractionCount: profiles[i].InteractionCount,
			LastInteractedAt: store.TimePtr(profiles[i].LastInteractedAt),
			LastSignalType:   profiles[i].LastSignalType,
		})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[j].Score < entries[i].Score })
	if len(entries) > 20 {
		entries = entries[:20]
	}
	return entries
}

// discoverySuggestionContext mirrors DiscoverySuggestionContext.
type discoverySuggestionContext struct {
	publishers []publisherCandidate
	accounts   []model.AccountRef
	realms     []model.RealmRef
	suppressed []model.DiscoveryPreference
}

// buildDiscoverySuggestionContext mirrors BuildDiscoverySuggestionContext.
func (s *service) buildDiscoverySuggestionContext(
	ctx context.Context,
	user *gen.DyAccount,
	friends []uuid.UUID,
	userPublisherIDs []uuid.UUID,
	userRealmIDs []uuid.UUID,
	preferences []store.DiscoveryPreferenceEntity,
	candidates []discoveryCandidate,
	publicRealms []*gen.DyRealm,
	tagInterest map[uuid.UUID]float64,
	categoryInterest map[uuid.UUID]float64,
	publisherInterest map[uuid.UUID]float64,
	collectionInterest map[uuid.UUID]float64,
	collectionMap map[uuid.UUID][]uuid.UUID,
	now time.Time,
) discoverySuggestionContext {
	hiddenByKind := map[discoveryTargetKind]map[uuid.UUID]struct{}{}
	for i := range preferences {
		kind := discoveryTargetKind(preferences[i].Kind)
		if hiddenByKind[kind] == nil {
			hiddenByKind[kind] = map[uuid.UUID]struct{}{}
		}
		hiddenByKind[kind][preferences[i].ReferenceID] = struct{}{}
	}
	accountID, err := uuid.Parse(user.GetId())
	if err != nil {
		return discoverySuggestionContext{}
	}
	hiddenRelationship := map[uuid.UUID]struct{}{}
	for _, id := range s.cachedBlockedAccountIDs(ctx, accountID) {
		hiddenRelationship[id] = struct{}{}
	}
	for _, id := range s.cachedMutedAccountIDs(ctx, accountID) {
		hiddenRelationship[id] = struct{}{}
	}

	subscribed := map[uuid.UUID]struct{}{}
	for _, id := range s.subscribedPublishers(ctx, accountID) {
		subscribed[id] = struct{}{}
	}
	publicRealmMap := map[uuid.UUID]*gen.DyRealm{}
	for _, realm := range publicRealms {
		if id, err := uuid.Parse(realm.GetId()); err == nil {
			publicRealmMap[id] = realm
		}
	}

	publisherCandidates := s.buildPublisherSuggestions(
		ctx, userPublisherIDs, subscribed, hiddenByKind[discoveryTargetPublisher],
		hiddenRelationship, candidates, tagInterest, categoryInterest,
		publisherInterest, collectionInterest, collectionMap, now,
	)
	accountCandidates := s.buildAccountSuggestions(
		ctx, accountID, friends, hiddenByKind[discoveryTargetAccount],
		hiddenRelationship, publisherCandidates,
	)
	realmCandidates := s.buildRealmSuggestions(
		userRealmIDs, hiddenByKind[discoveryTargetRealm], candidates, publicRealmMap,
		tagInterest, categoryInterest, collectionInterest, collectionMap, now,
	)
	suppressed := s.buildSuppressedSuggestions(preferences, publicRealmMap)

	return discoverySuggestionContext{
		publishers: truncate3(publisherCandidates),
		accounts:   truncate3(accountCandidates),
		realms:     truncate3(realmCandidates),
		suppressed: suppressed,
	}
}

// publisherCandidate carries the publisher snapshot together with its score so
// the account suggestions can inherit the score.
type publisherCandidate struct {
	publisher  model.Publisher
	accountID  *uuid.UUID
	score      float64
	reasons    []string
	individual bool
}

// buildPublisherSuggestions mirrors BuildPublisherSuggestions.
func (s *service) buildPublisherSuggestions(
	ctx context.Context,
	userPublisherIDs []uuid.UUID,
	subscribed map[uuid.UUID]struct{},
	hiddenPublishers map[uuid.UUID]struct{},
	hiddenRelationship map[uuid.UUID]struct{},
	candidates []discoveryCandidate,
	tagInterest map[uuid.UUID]float64,
	categoryInterest map[uuid.UUID]float64,
	publisherInterest map[uuid.UUID]float64,
	collectionInterest map[uuid.UUID]float64,
	collectionMap map[uuid.UUID][]uuid.UUID,
	now time.Time,
) []publisherCandidate {
	owned := map[uuid.UUID]struct{}{}
	for _, id := range userPublisherIDs {
		owned[id] = struct{}{}
	}
	grouped := map[uuid.UUID][]discoveryCandidate{}
	for i := range candidates {
		if candidates[i].publisherID == uuid.Nil {
			continue
		}
		grouped[candidates[i].publisherID] = append(grouped[candidates[i].publisherID], candidates[i])
	}

	tagNames := s.tagLabels(ctx, candidateTagIDs(candidates))
	categoryNames := s.categoryLabels(ctx, candidateCategoryIDs(candidates))

	ranked := make([]rankedDiscoveryTarget, 0, len(grouped))
	for publisherID, group := range grouped {
		scores := make([]float64, 0, len(group))
		for i := range group {
			scores = append(scores, calculateDiscoveryPostScore(
				&group[i], tagInterest, categoryInterest, collectionInterest,
				collectionMap[group[i].id], now,
			))
		}
		sort.Sort(sort.Reverse(sort.Float64Slice(scores)))
		score := 0.0
		for i := 0; i < len(scores) && i < 3; i++ {
			score += scores[i]
		}
		score += publisherInterest[publisherID] * 0.4
		if score <= 0.2 {
			continue
		}
		if _, ok := owned[publisherID]; ok {
			continue
		}
		if _, ok := subscribed[publisherID]; ok {
			continue
		}
		if _, ok := hiddenPublishers[publisherID]; ok {
			continue
		}
		ranked = append(ranked, rankedDiscoveryTarget{
			referenceID: publisherID,
			score:       score,
			reasons:     buildReasonLabels(group, tagNames, categoryNames),
		})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[j].score < ranked[i].score })
	if len(ranked) > 8 {
		ranked = ranked[:8]
	}
	if len(ranked) == 0 {
		return nil
	}

	ids := make([]uuid.UUID, 0, len(ranked))
	for _, item := range ranked {
		ids = append(ids, item.referenceID)
	}
	var rows []store.PublisherEntity
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		s.log.Warn("timeline: discovery publishers", "error", err)
		return nil
	}
	byID := map[uuid.UUID]store.PublisherEntity{}
	for i := range rows {
		byID[rows[i].ID] = rows[i]
	}
	accountIDs := make([]string, 0, len(rows))
	for i := range rows {
		if rows[i].AccountID != nil {
			accountIDs = append(accountIDs, rows[i].AccountID.String())
		}
	}
	accounts := s.accountBatch(ctx, accountIDs)

	out := make([]publisherCandidate, 0, len(ranked))
	for _, item := range ranked {
		row, ok := byID[item.referenceID]
		if !ok {
			continue
		}
		if row.AccountID != nil {
			if _, hidden := hiddenRelationship[*row.AccountID]; hidden {
				continue
			}
		}
		publisher := store.PublisherToModel(&row)
		if publisher.AccountID != nil {
			if account, ok := accounts[*publisher.AccountID]; ok {
				publisher.Account = account
			}
		}
		out = append(out, publisherCandidate{
			publisher:  publisher,
			accountID:  row.AccountID,
			score:      item.score,
			reasons:    item.reasons,
			individual: row.Type == int(model.PublisherIndividual),
		})
	}
	return out
}

// buildAccountSuggestions mirrors BuildAccountSuggestions.
func (s *service) buildAccountSuggestions(
	ctx context.Context,
	accountID uuid.UUID,
	friends []uuid.UUID,
	hiddenAccounts map[uuid.UUID]struct{},
	hiddenRelationship map[uuid.UUID]struct{},
	publishers []publisherCandidate,
) []model.AccountRef {
	friendSet := map[uuid.UUID]struct{}{}
	for _, id := range friends {
		friendSet[id] = struct{}{}
	}
	candidates := make([]publisherCandidate, 0, len(publishers))
	seenAccounts := map[uuid.UUID]struct{}{}
	for _, item := range publishers {
		if !item.individual || item.accountID == nil {
			continue
		}
		id := *item.accountID
		if id == accountID {
			continue
		}
		if _, ok := friendSet[id]; ok {
			continue
		}
		if _, ok := hiddenAccounts[id]; ok {
			continue
		}
		if _, ok := hiddenRelationship[id]; ok {
			continue
		}
		if _, ok := seenAccounts[id]; ok {
			continue
		}
		seenAccounts[id] = struct{}{}
		candidates = append(candidates, item)
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[j].score < candidates[i].score })

	ids := make([]string, 0, len(candidates))
	for _, item := range candidates {
		ids = append(ids, item.accountID.String())
	}
	accounts := s.accountBatch(ctx, ids)
	out := make([]model.AccountRef, 0, len(candidates))
	for _, item := range candidates {
		account, ok := accounts[item.accountID.String()]
		if !ok || account == nil {
			continue
		}
		out = append(out, *account)
	}
	return out
}

// buildRealmSuggestions mirrors BuildRealmSuggestions.
func (s *service) buildRealmSuggestions(
	userRealmIDs []uuid.UUID,
	hiddenRealms map[uuid.UUID]struct{},
	candidates []discoveryCandidate,
	publicRealmMap map[uuid.UUID]*gen.DyRealm,
	tagInterest map[uuid.UUID]float64,
	categoryInterest map[uuid.UUID]float64,
	collectionInterest map[uuid.UUID]float64,
	collectionMap map[uuid.UUID][]uuid.UUID,
	now time.Time,
) []model.RealmRef {
	owned := map[uuid.UUID]struct{}{}
	for _, id := range userRealmIDs {
		owned[id] = struct{}{}
	}
	grouped := map[uuid.UUID][]discoveryCandidate{}
	for i := range candidates {
		if candidates[i].realmID == nil {
			continue
		}
		id := *candidates[i].realmID
		if _, ok := publicRealmMap[id]; !ok {
			continue
		}
		grouped[id] = append(grouped[id], candidates[i])
	}
	ranked := make([]rankedDiscoveryTarget, 0, len(grouped))
	for realmID, group := range grouped {
		scores := make([]float64, 0, len(group))
		for i := range group {
			scores = append(scores, calculateDiscoveryPostScore(
				&group[i], tagInterest, categoryInterest, collectionInterest,
				collectionMap[group[i].id], now,
			))
		}
		sort.Sort(sort.Reverse(sort.Float64Slice(scores)))
		score := 0.0
		for i := 0; i < len(scores) && i < 3; i++ {
			score += scores[i]
		}
		score += float64(publicRealmMap[realmID].GetBoostLevel()) * realmBoostLevelRankBonus
		if score <= 0.2 {
			continue
		}
		if _, ok := owned[realmID]; ok {
			continue
		}
		if _, ok := hiddenRealms[realmID]; ok {
			continue
		}
		ranked = append(ranked, rankedDiscoveryTarget{referenceID: realmID, score: score})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[j].score < ranked[i].score })
	if len(ranked) > 8 {
		ranked = ranked[:8]
	}
	out := make([]model.RealmRef, 0, len(ranked))
	for _, item := range ranked {
		if realm, ok := publicRealmMap[item.referenceID]; ok {
			out = append(out, *realmRefFromProto(realm))
		}
	}
	return out
}

// buildSuppressedSuggestions mirrors BuildSuppressedSuggestions, returning the
// stored preference rows (the profile's suppression list).
func (s *service) buildSuppressedSuggestions(
	preferences []store.DiscoveryPreferenceEntity,
	publicRealmMap map[uuid.UUID]*gen.DyRealm,
) []model.DiscoveryPreference {
	out := make([]model.DiscoveryPreference, 0, len(preferences))
	for i := range preferences {
		switch discoveryTargetKind(preferences[i].Kind) {
		case discoveryTargetRealm:
			if _, ok := publicRealmMap[preferences[i].ReferenceID]; !ok {
				continue
			}
		}
		out = append(out, discoveryPreferenceToModel(&preferences[i]))
	}
	return out
}

func discoveryPreferenceToModel(entity *store.DiscoveryPreferenceEntity) model.DiscoveryPreference {
	return model.DiscoveryPreference{
		ID:          entity.ID.String(),
		AccountID:   entity.AccountID.String(),
		Kind:        entity.Kind,
		ReferenceID: entity.ReferenceID.String(),
		State:       entity.State,
		Reason:      entity.Reason,
		AppliedAt:   store.TimePtr(entity.AppliedAt),
		CreatedAt:   model.NewTime(entity.CreatedAt),
		UpdatedAt:   model.NewTime(entity.UpdatedAt),
		DeletedAt:   store.DeletedPtr(entity.DeletedAt),
	}
}

// calculateDiscoveryPostScore mirrors CalculateDiscoveryPostScore.
func calculateDiscoveryPostScore(
	post *discoveryCandidate,
	tagInterest map[uuid.UUID]float64,
	categoryInterest map[uuid.UUID]float64,
	collectionInterest map[uuid.UUID]float64,
	collectionIDs []uuid.UUID,
	now time.Time,
) float64 {
	tagScore := 0.0
	for _, id := range post.tagIDs {
		tagScore += tagInterest[id]
	}
	tagScore *= 0.9
	categoryScore := 0.0
	for _, id := range post.categoryIDs {
		categoryScore += categoryInterest[id]
	}
	categoryScore *= 0.8
	collectionScore := 0.0
	for _, id := range collectionIDs {
		collectionScore += collectionInterest[id]
	}
	collectionScore *= 0.65
	engagement := float64(post.reactionScore)*0.15 + post.awardedScore/50 + float64(post.repliesCount)*0.08
	if engagement < 0 {
		engagement = 0
	}
	articleBonus := 0.0
	if post.postType == int(model.PostTypeArticle) || post.postType == int(model.PostTypeBlog) {
		articleBonus = 0.35
	}
	ageDays := now.Sub(post.instant).Hours() / 24
	if ageDays < 0 {
		ageDays = 0
	}
	freshness := 1 / math.Pow(ageDays+1, 0.35)
	return (tagScore + categoryScore + collectionScore + engagement + articleBonus) * freshness
}

// buildReasonLabels mirrors BuildReasonLabels.
func buildReasonLabels(posts []discoveryCandidate, tagNames, categoryNames map[uuid.UUID]string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 3)
	appendLabel := func(label string) {
		if label == "" {
			return
		}
		if _, ok := seen[label]; ok {
			return
		}
		seen[label] = struct{}{}
		out = append(out, label)
	}
	for i := range posts {
		for _, id := range posts[i].tagIDs {
			appendLabel(tagNames[id])
			if len(out) >= 3 {
				return out
			}
		}
	}
	for i := range posts {
		for _, id := range posts[i].categoryIDs {
			appendLabel(categoryNames[id])
			if len(out) >= 3 {
				return out
			}
		}
	}
	return out
}

func (s *service) tagLabels(ctx context.Context, ids []uuid.UUID) map[uuid.UUID]string {
	out := map[uuid.UUID]string{}
	if len(ids) == 0 {
		return out
	}
	var rows []store.PostTagEntity
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return out
	}
	for i := range rows {
		if rows[i].Name != nil && *rows[i].Name != "" {
			out[rows[i].ID] = *rows[i].Name
		} else {
			out[rows[i].ID] = rows[i].Slug
		}
	}
	return out
}

func (s *service) categoryLabels(ctx context.Context, ids []uuid.UUID) map[uuid.UUID]string {
	out := map[uuid.UUID]string{}
	if len(ids) == 0 {
		return out
	}
	var rows []store.PostCategoryEntity
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return out
	}
	for i := range rows {
		if rows[i].Name != nil && *rows[i].Name != "" {
			out[rows[i].ID] = *rows[i].Name
		} else {
			out[rows[i].ID] = rows[i].Slug
		}
	}
	return out
}

func candidateTagIDs(candidates []discoveryCandidate) []uuid.UUID {
	seen := map[uuid.UUID]struct{}{}
	out := make([]uuid.UUID, 0)
	for i := range candidates {
		for _, id := range candidates[i].tagIDs {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

func candidateCategoryIDs(candidates []discoveryCandidate) []uuid.UUID {
	seen := map[uuid.UUID]struct{}{}
	out := make([]uuid.UUID, 0)
	for i := range candidates {
		for _, id := range candidates[i].categoryIDs {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

func truncate3[T any](items []T) []T {
	if len(items) > 3 {
		return items[:3]
	}
	return items
}

// ---------------------------------------------------------------------------
// Preferences
// ---------------------------------------------------------------------------

// markDiscoveryPreference mirrors MarkDiscoveryPreferenceAsync.
func (s *service) markDiscoveryPreference(ctx context.Context, accountID uuid.UUID, kind discoveryTargetKind, referenceID uuid.UUID, reason *string) (model.DiscoveryPreference, error) {
	var preference store.DiscoveryPreferenceEntity
	err := s.db.WithContext(ctx).
		Where("account_id = ? AND kind = ? AND reference_id = ?", accountID, int(kind), referenceID).
		First(&preference).Error
	now := time.Now().UTC()
	switch {
	case err == nil:
		preference.State = discoveryPreferenceUninterested
		preference.Reason = reason
		preference.AppliedAt = &now
		if err := s.db.WithContext(ctx).Save(&preference).Error; err != nil {
			return model.DiscoveryPreference{}, err
		}
	case errors.Is(err, gorm.ErrRecordNotFound):
		preference = store.DiscoveryPreferenceEntity{
			ID:          uuid.New(),
			EntityBase: storeEntityBase(now),
			AccountID:   accountID,
			Kind:        int(kind),
			ReferenceID: referenceID,
			State:       discoveryPreferenceUninterested,
			Reason:      reason,
			AppliedAt:   &now,
		}
		if err := s.db.WithContext(ctx).Create(&preference).Error; err != nil {
			return model.DiscoveryPreference{}, err
		}
	default:
		return model.DiscoveryPreference{}, err
	}
	s.cacheRemove(ctx, discoveryProfileCacheKey(accountID))
	return discoveryPreferenceToModel(&preference), nil
}

// removeDiscoveryPreference mirrors RemoveDiscoveryPreferenceAsync.
func (s *service) removeDiscoveryPreference(ctx context.Context, accountID uuid.UUID, kind discoveryTargetKind, referenceID uuid.UUID) (bool, error) {
	result := s.db.WithContext(ctx).Unscoped().
		Where("account_id = ? AND kind = ? AND reference_id = ?", accountID, int(kind), referenceID).
		Delete(&store.DiscoveryPreferenceEntity{})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		return false, nil
	}
	s.cacheRemove(ctx, discoveryProfileCacheKey(accountID))
	return true, nil
}

func storeEntityBase(now time.Time) store.EntityBase {
	return store.EntityBase{CreatedAt: now, UpdatedAt: now}
}

// ---------------------------------------------------------------------------
// Feedback, weights and reset
// ---------------------------------------------------------------------------

// applyRecommendationFeedback mirrors ApplyRecommendationFeedbackAsync.
func (s *service) applyRecommendationFeedback(
	ctx context.Context,
	accountID uuid.UUID,
	kind string,
	referenceID uuid.UUID,
	feedback recommendationFeedbackValue,
	reason *string,
	suppress bool,
) (*model.RecommendationFeedbackResult, error) {
	now := time.Now().UTC()
	baseScore := explicitPositiveFeedbackScore
	if feedback == feedbackNegative {
		baseScore = explicitNegativeFeedbackScore
	}
	normalized := normalizeKind(kind)

	var adjustments []interestAdjustment
	switch normalized {
	case "post":
		built, err := s.buildPostFeedbackAdjustments(ctx, referenceID, baseScore)
		if err != nil {
			return nil, err
		}
		if len(built) == 0 {
			return nil, nil
		}
		adjustments = built
	case "publisher":
		adjustments = []interestAdjustment{{interestPublisher, referenceID, baseScore}}
	case "collection":
		adjustments = []interestAdjustment{{interestCollection, referenceID, baseScore}}
	case "tag":
		adjustments = []interestAdjustment{{interestTag, referenceID, baseScore}}
	case "category":
		adjustments = []interestAdjustment{{interestCategory, referenceID, baseScore}}
	default:
		return nil, nil
	}

	updated, err := s.applyInterestProfileAdjustments(
		ctx, accountID, adjustments,
		"feedback:"+normalized+":"+feedback.String(), now, 1,
	)
	if err != nil {
		return nil, err
	}

	result := &model.RecommendationFeedbackResult{UpdatedProfiles: updated}
	if suppress && feedback == feedbackNegative && normalized == "publisher" {
		preference, err := s.markDiscoveryPreference(ctx, accountID, discoveryTargetPublisher, referenceID, reason)
		if err != nil {
			return nil, err
		}
		result.Preference = &preference
	}
	return result, nil
}

// buildPostFeedbackAdjustments mirrors BuildPostFeedbackAdjustmentsAsync.
func (s *service) buildPostFeedbackAdjustments(ctx context.Context, postID uuid.UUID, baseScore float64) ([]interestAdjustment, error) {
	var post store.PostEntity
	if err := s.db.WithContext(ctx).Where("id = ?", postID).First(&post).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	adjustments := make([]interestAdjustment, 0, 8)
	if post.PublisherID != uuid.Nil {
		adjustments = append(adjustments, interestAdjustment{interestPublisher, post.PublisherID, baseScore})
	}
	collectionIDs := s.postCollectionMap(ctx, []uuid.UUID{post.ID})[post.ID]
	if len(collectionIDs) > 0 {
		score := baseScore * 0.65 / float64(len(collectionIDs))
		for _, id := range collectionIDs {
			adjustments = append(adjustments, interestAdjustment{interestCollection, id, score})
		}
	}
	tagIDs := s.tagIDsByPost(ctx, []uuid.UUID{post.ID})[post.ID]
	if len(tagIDs) > 0 {
		score := baseScore * 0.75 / float64(len(tagIDs))
		for _, id := range tagIDs {
			adjustments = append(adjustments, interestAdjustment{interestTag, id, score})
		}
	}
	categoryIDs := s.categoryIDsByPost(ctx, []uuid.UUID{post.ID})[post.ID]
	if len(categoryIDs) > 0 {
		score := baseScore / float64(len(categoryIDs))
		for _, id := range categoryIDs {
			adjustments = append(adjustments, interestAdjustment{interestCategory, id, score})
		}
	}
	return adjustments, nil
}

// adjustRecommendationWeight mirrors AdjustRecommendationWeightAsync.
func (s *service) adjustRecommendationWeight(
	ctx context.Context,
	accountID uuid.UUID,
	kind postInterestKind,
	referenceID uuid.UUID,
	scoreDelta float64,
	interactionCount int,
	signalType *string,
) (model.InterestEntry, error) {
	signal := "manual:" + kind.String()
	if signalType != nil && trimSpace(*signalType) != "" {
		signal = *signalType
	}
	updated, err := s.applyInterestProfileAdjustments(
		ctx, accountID,
		[]interestAdjustment{{kind, referenceID, scoreDelta}},
		signal, time.Now().UTC(), interactionCount,
	)
	if err != nil {
		return model.InterestEntry{}, err
	}
	if len(updated) == 0 {
		return model.InterestEntry{}, nil
	}
	return updated[0], nil
}

// applyInterestProfileAdjustments mirrors ApplyInterestProfileAdjustmentsAsync.
func (s *service) applyInterestProfileAdjustments(
	ctx context.Context,
	accountID uuid.UUID,
	adjustments []interestAdjustment,
	signalType string,
	occurredAt time.Time,
	interactionCount int,
) ([]model.InterestEntry, error) {
	if len(adjustments) == 0 {
		return []model.InterestEntry{}, nil
	}
	type profileKey struct {
		kind postInterestKind
		id   uuid.UUID
	}
	aggregated := make([]interestAdjustment, 0, len(adjustments))
	index := map[profileKey]int{}
	for _, item := range adjustments {
		key := profileKey{item.kind, item.referenceID}
		if pos, ok := index[key]; ok {
			aggregated[pos].delta += item.delta
			continue
		}
		index[key] = len(aggregated)
		aggregated = append(aggregated, item)
	}

	tagIDs := make([]uuid.UUID, 0)
	categoryIDs := make([]uuid.UUID, 0)
	publisherIDs := make([]uuid.UUID, 0)
	collectionIDs := make([]uuid.UUID, 0)
	for _, item := range aggregated {
		switch item.kind {
		case interestTag:
			tagIDs = append(tagIDs, item.referenceID)
		case interestCategory:
			categoryIDs = append(categoryIDs, item.referenceID)
		case interestPublisher:
			publisherIDs = append(publisherIDs, item.referenceID)
		case interestCollection:
			collectionIDs = append(collectionIDs, item.referenceID)
		}
	}

	query := s.db.WithContext(ctx).Where("account_id = ?", accountID)
	conditions := make([]string, 0, 4)
	args := make([]any, 0, 8)
	if len(tagIDs) > 0 {
		conditions = append(conditions, "(kind = ? AND reference_id IN ?)")
		args = append(args, int(interestTag), tagIDs)
	}
	if len(categoryIDs) > 0 {
		conditions = append(conditions, "(kind = ? AND reference_id IN ?)")
		args = append(args, int(interestCategory), categoryIDs)
	}
	if len(publisherIDs) > 0 {
		conditions = append(conditions, "(kind = ? AND reference_id IN ?)")
		args = append(args, int(interestPublisher), publisherIDs)
	}
	if len(collectionIDs) > 0 {
		conditions = append(conditions, "(kind = ? AND reference_id IN ?)")
		args = append(args, int(interestCollection), collectionIDs)
	}
	if len(conditions) > 0 {
		query = query.Where("("+joinWithOr(conditions)+")", args...)
	}

	var existing []store.PostInterestProfileEntity
	if err := query.Find(&existing).Error; err != nil {
		return nil, err
	}
	profileMap := map[profileKey]*store.PostInterestProfileEntity{}
	for i := range existing {
		key := profileKey{postInterestKind(existing[i].Kind), existing[i].ReferenceID}
		profileMap[key] = &existing[i]
	}

	if interactionCount < 1 {
		interactionCount = 1
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := occurredAt
		for _, item := range aggregated {
			key := profileKey{item.kind, item.referenceID}
			profile, ok := profileMap[key]
			if !ok {
				profile = &store.PostInterestProfileEntity{
					ID:          uuid.New(),
					EntityBase:  storeEntityBase(now),
					AccountID:   accountID,
					Kind:        int(item.kind),
					ReferenceID: item.referenceID,
				}
				profileMap[key] = profile
				profile.Score = clampScore(item.delta)
				profile.InteractionCount = interactionCount
				profile.LastInteractedAt = &now
				profile.LastSignalType = new(signalType)
				if err := tx.Create(profile).Error; err != nil {
					return err
				}
				continue
			}
			profile.Score = clampScore(profile.Score + item.delta)
			profile.InteractionCount += interactionCount
			profile.LastInteractedAt = &now
			profile.LastSignalType = new(signalType)
			if err := tx.Save(profile).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.cacheRemove(ctx, discoveryProfileCacheKey(accountID))

	out := make([]model.InterestEntry, 0, len(aggregated))
	for _, item := range aggregated {
		profile, ok := profileMap[profileKey{item.kind, item.referenceID}]
		if !ok {
			continue
		}
		out = append(out, model.InterestEntry{
			Kind:             profile.Kind,
			ReferenceID:      profile.ReferenceID.String(),
			Score:            profile.Score,
			InteractionCount: profile.InteractionCount,
			LastInteractedAt: store.TimePtr(profile.LastInteractedAt),
			LastSignalType:   profile.LastSignalType,
		})
	}
	return out, nil
}

func joinWithOr(conditions []string) string {
	out := ""
	for i, condition := range conditions {
		if i > 0 {
			out += " OR "
		}
		out += condition
	}
	return out
}

func clampScore(value float64) float64 {
	if value < -100 {
		return -100
	}
	if value > 100 {
		return 100
	}
	return value
}

// resetInterestProfile mirrors ResetInterestProfileAsync.
func (s *service) resetInterestProfile(ctx context.Context, accountID uuid.UUID) (int, error) {
	now := time.Now().UTC()

	if err := s.db.WithContext(ctx).Unscoped().
		Where("account_id = ?", accountID).
		Delete(&store.PostInterestProfileEntity{}).Error; err != nil {
		return 0, err
	}

	adjustments := make([]interestAdjustment, 0)

	for _, id := range s.subscribedPublishers(ctx, accountID) {
		adjustments = append(adjustments, interestAdjustment{interestPublisher, id, 4.0})
	}

	var subscriptions []store.PostCategorySubscriptionEntity
	if err := s.db.WithContext(ctx).Where("account_id = ?", accountID).Find(&subscriptions).Error; err != nil {
		return 0, err
	}
	for i := range subscriptions {
		if subscriptions[i].TagID != nil {
			adjustments = append(adjustments, interestAdjustment{interestTag, *subscriptions[i].TagID, 4.0})
		}
		if subscriptions[i].CategoryID != nil {
			adjustments = append(adjustments, interestAdjustment{interestCategory, *subscriptions[i].CategoryID, 4.0})
		}
	}

	userPublisherIDs := s.userPublishers(ctx, accountID)
	if len(userPublisherIDs) > 0 {
		recent := now.AddDate(0, 0, -discoveryLookbackDays)
		var posts []store.PostEntity
		if err := s.db.WithContext(ctx).
			Where("publisher_id IN ?", userPublisherIDs).
			Where("drafted_at IS NULL").
			Where(
				"(published_at IS NOT NULL AND published_at >= ?) OR (published_at IS NULL AND created_at >= ?)",
				recent, recent,
			).
			Find(&posts).Error; err != nil {
			return 0, err
		}
		postIDs := make([]uuid.UUID, 0, len(posts))
		for i := range posts {
			postIDs = append(postIDs, posts[i].ID)
		}
		tagCounts := map[uuid.UUID]int{}
		categoryCounts := map[uuid.UUID]int{}
		for _, tags := range s.tagCountsByPost(ctx, postIDs) {
			for id, count := range tags {
				tagCounts[id] += count
			}
		}
		for _, categories := range s.categoryCountsByPost(ctx, postIDs) {
			for id, count := range categories {
				categoryCounts[id] += count
			}
		}
		collectionCounts := map[uuid.UUID]int{}
		for _, collections := range s.collectionCountsByPost(ctx, postIDs) {
			for id, count := range collections {
				collectionCounts[id] += count
			}
		}
		for id, count := range tagCounts {
			adjustments = append(adjustments, interestAdjustment{interestTag, id, 2.0 * float64(count)})
		}
		for id, count := range categoryCounts {
			adjustments = append(adjustments, interestAdjustment{interestCategory, id, 2.0 * float64(count)})
		}
		for id, count := range collectionCounts {
			adjustments = append(adjustments, interestAdjustment{interestCollection, id, 1.5 * float64(count)})
		}
		for _, id := range userPublisherIDs {
			adjustments = append(adjustments, interestAdjustment{interestPublisher, id, 2.0})
		}
	}

	if len(adjustments) > 0 {
		if _, err := s.applyInterestProfileAdjustments(ctx, accountID, adjustments, "reset", now, 1); err != nil {
			return 0, err
		}
	}
	s.cacheRemove(ctx, discoveryProfileCacheKey(accountID))
	return len(adjustments), nil
}

func (s *service) tagCountsByPost(ctx context.Context, postIDs []uuid.UUID) map[uuid.UUID]map[uuid.UUID]int {
	out := map[uuid.UUID]map[uuid.UUID]int{}
	for postID, ids := range s.tagIDsByPost(ctx, postIDs) {
		counts := map[uuid.UUID]int{}
		for _, id := range ids {
			counts[id]++
		}
		out[postID] = counts
	}
	return out
}

func (s *service) categoryCountsByPost(ctx context.Context, postIDs []uuid.UUID) map[uuid.UUID]map[uuid.UUID]int {
	out := map[uuid.UUID]map[uuid.UUID]int{}
	for postID, ids := range s.categoryIDsByPost(ctx, postIDs) {
		counts := map[uuid.UUID]int{}
		for _, id := range ids {
			counts[id]++
		}
		out[postID] = counts
	}
	return out
}

func (s *service) collectionCountsByPost(ctx context.Context, postIDs []uuid.UUID) map[uuid.UUID]map[uuid.UUID]int {
	out := map[uuid.UUID]map[uuid.UUID]int{}
	for postID, ids := range s.postCollectionMap(ctx, postIDs) {
		counts := map[uuid.UUID]int{}
		for _, id := range ids {
			counts[id]++
		}
		out[postID] = counts
	}
	return out
}
