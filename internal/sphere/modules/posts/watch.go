package posts

import (
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

const (
	watchSourceBookmark = 0
	watchSourceReaction = 1
	watchSourceReply    = 2
)

var watchSources = []int{watchSourceBookmark, watchSourceReaction, watchSourceReply}

func watchSourceName(source int) string {
	switch source {
	case watchSourceBookmark:
		return "bookmark"
	case watchSourceReaction:
		return "reaction"
	case watchSourceReply:
		return "reply"
	default:
		return strings.ToLower(string(rune('0' + source)))
	}
}

type watchFiltersRequest struct {
	Reactions *bool `json:"reactions"`
	Replies   *bool `json:"replies"`
	Chains    *bool `json:"chains"`
	Forwards  *bool `json:"forwards"`
	Edits     *bool `json:"edits"`
}

type watchPreferencesRequest struct {
	Bookmark *watchFiltersRequest `json:"bookmark"`
	Reaction *watchFiltersRequest `json:"reaction"`
	Reply    *watchFiltersRequest `json:"reply"`
}

func defaultWatchPreference(source int) (reactions, replies, chains, forwards, edits bool) {
	chains = true
	edits = true
	if source == watchSourceBookmark {
		reactions = true
		replies = true
		forwards = true
	}
	return
}

func (h *handler) registerWatchRoutes(host *module.Host[core.Services]) {
	api := host.API
	api.GET("/posts/watch", authn.RequireAuth(), h.getWatchPreferences)
	api.PUT("/posts/watch", authn.RequireAuth(), host.Core.Auth.RequirePermission("post.subscriptions.manage"), h.updateWatchPreferences)
	api.GET("/ads/:name", h.listPublisherAds)
}

func (h *handler) getWatchPreferences(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := h.viewerAccountID(c)
	if accountID == uuid.Nil {
		httpx.Unauthorized(c)
		return
	}
	httpx.OK(c, h.effectiveWatchPreferences(ctx, accountID))
}

func (h *handler) effectiveWatchPreferences(ctx interface {
	Done() <-chan struct{}
	Err() error
	Value(any) any
	Deadline() (time.Time, bool)
}, accountID uuid.UUID) []model.PostWatchPreferenceResponse {
	var stored []store.PostWatchPreferenceEntity
	h.db.Where("account_id = ?", accountID).Find(&stored)
	bySource := map[int]store.PostWatchPreferenceEntity{}
	for _, row := range stored {
		bySource[row.Source] = row
	}
	out := make([]model.PostWatchPreferenceResponse, 0, len(watchSources))
	for _, source := range watchSources {
		if row, ok := bySource[source]; ok {
			out = append(out, watchPreferenceResponse(source, row.NotifyReactions, row.NotifyReplies, row.NotifyChains, row.NotifyForwards, row.NotifyEdits))
			continue
		}
		reactions, replies, chains, forwards, edits := defaultWatchPreference(source)
		out = append(out, watchPreferenceResponse(source, reactions, replies, chains, forwards, edits))
	}
	return out
}

func watchPreferenceResponse(source int, reactions, replies, chains, forwards, edits bool) model.PostWatchPreferenceResponse {
	return model.PostWatchPreferenceResponse{
		Source:          watchSourceName(source),
		NotifyReactions: reactions,
		NotifyReplies:   replies,
		NotifyChains:    chains,
		NotifyForwards:  forwards,
		NotifyEdits:     edits,
	}
}

func (h *handler) updateWatchPreferences(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := h.viewerAccountID(c)
	if accountID == uuid.Nil {
		httpx.Unauthorized(c)
		return
	}
	var request watchPreferencesRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	updates := []struct {
		source  int
		filters *watchFiltersRequest
	}{
		{watchSourceBookmark, request.Bookmark},
		{watchSourceReaction, request.Reaction},
		{watchSourceReply, request.Reply},
	}
	for _, update := range updates {
		if update.filters == nil {
			continue
		}
		var row store.PostWatchPreferenceEntity
		err := h.db.WithContext(ctx).
			Where("account_id = ? AND source = ?", accountID, update.source).First(&row).Error
		if err != nil {
			reactions, replies, chains, forwards, edits := defaultWatchPreference(update.source)
			row = store.PostWatchPreferenceEntity{
				ID:              uuid.New(),
				AccountID:       accountID,
				Source:          update.source,
				NotifyReactions: reactions,
				NotifyReplies:   replies,
				NotifyChains:    chains,
				NotifyForwards:  forwards,
				NotifyEdits:     edits,
			}
		}
		if update.filters.Reactions != nil {
			row.NotifyReactions = *update.filters.Reactions
		}
		if update.filters.Replies != nil {
			row.NotifyReplies = *update.filters.Replies
		}
		if update.filters.Chains != nil {
			row.NotifyChains = *update.filters.Chains
		}
		if update.filters.Forwards != nil {
			row.NotifyForwards = *update.filters.Forwards
		}
		if update.filters.Edits != nil {
			row.NotifyEdits = *update.filters.Edits
		}
		if err := h.db.WithContext(ctx).Save(&row).Error; err != nil {
			httpx.Internal(c)
			return
		}
	}
	httpx.OK(c, h.effectiveWatchPreferences(ctx, accountID))
}

// ---------------------------------------------------------------------------
// Advertising
// ---------------------------------------------------------------------------

func (h *handler) listPublisherAds(c *gin.Context) {
	ctx := c.Request.Context()
	publisher, err := h.publisherByName(ctx, c.Param("name"))
	if err != nil {
		httpx.NotFound(c, "Publisher not found.")
		return
	}
	offset, take := paginate(c, 20, h.maxTake())

	now := time.Now().UTC()
	type bidRow struct {
		PostID      uuid.UUID `gorm:"column:post_id"`
		TotalAmount float64   `gorm:"column:total_amount"`
		BidCount    int       `gorm:"column:bid_count"`
	}
	var bids []bidRow
	h.db.WithContext(ctx).Model(&store.PostSponsorBidEntity{}).
		Select("post_id, sum(amount) AS total_amount, count(*) AS bid_count").
		Where("expires_at > ?", now).Group("post_id").Scan(&bids)
	bidByPost := map[uuid.UUID]bidRow{}
	bidIDs := make([]uuid.UUID, 0, len(bids))
	for _, row := range bids {
		bidByPost[row.PostID] = row
		bidIDs = append(bidIDs, row.PostID)
	}

	validCandidates := map[uuid.UUID]struct{}{}
	if len(bidIDs) > 0 {
		var valid []uuid.UUID
		h.db.WithContext(ctx).Model(&store.PostEntity{}).
			Where("id IN ?", bidIDs).
			Where("visibility = ?", int(model.VisibilityPublic)).
			Where("(shadowban_reason IS NULL OR shadowban_reason = ?)", int(model.ShadowbanNone)).
			Where("publisher_id NOT IN (SELECT id FROM publishers WHERE shadowban_reason IS NOT NULL AND shadowban_reason <> 0)").
			Pluck("id", &valid)
		for _, id := range valid {
			validCandidates[id] = struct{}{}
		}
	}
	totalWeight := 0.0
	for id := range validCandidates {
		totalWeight += bidByPost[id].TotalAmount
	}

	type postRow struct {
		ID    uuid.UUID `gorm:"column:id"`
		Title *string   `gorm:"column:title"`
		Slug  *string   `gorm:"column:slug"`
	}
	var posts []postRow
	if err := h.db.WithContext(ctx).Model(&store.PostEntity{}).
		Select("id, title, slug").
		Where("publisher_id = ?", publisher.ID).
		Where("visibility = ?", int(model.VisibilityPublic)).
		Where("(shadowban_reason IS NULL OR shadowban_reason = ?)", int(model.ShadowbanNone)).
		Scan(&posts).Error; err != nil {
		httpx.Internal(c)
		return
	}
	postIDs := make([]uuid.UUID, 0, len(posts))
	for _, post := range posts {
		postIDs = append(postIDs, post.ID)
	}
	stats := map[uuid.UUID]store.PostAggregatedStatsEntity{}
	if len(postIDs) > 0 {
		var rows []store.PostAggregatedStatsEntity
		h.db.WithContext(ctx).Where("post_id IN ?", postIDs).Find(&rows)
		for _, row := range rows {
			stats[row.PostID] = row
		}
	}
	placed := map[uuid.UUID]struct{}{}
	if len(postIDs) > 0 {
		var ids []uuid.UUID
		h.db.WithContext(ctx).Model(&store.PostSponsorPlacementEntity{}).
			Where("post_id IN ? AND valid_from <= ? AND valid_until > ?", postIDs, now, now).
			Pluck("post_id", &ids)
		for _, id := range ids {
			placed[id] = struct{}{}
		}
	}

	out := make([]model.PublicAdvertisingPostStats, 0, len(posts))
	for _, post := range posts {
		bid := bidByPost[post.ID]
		stat := stats[post.ID]
		_, isPlaced := placed[post.ID]
		_, isValid := validCandidates[post.ID]
		chance := 0.0
		if totalWeight > 0 && isValid {
			chance = bid.TotalAmount / totalWeight
		}
		entry := model.PublicAdvertisingPostStats{
			PostID:            post.ID.String(),
			Title:             post.Title,
			Slug:              post.Slug,
			ActiveBidTotal:    bid.TotalAmount,
			BidCount:          bid.BidCount,
			IsCurrentlyPlaced: isPlaced,
			ShownCount:        stat.ShownCount,
			LastShownAt:       store.TimePtr(stat.LastShownAt),
			DisplayChance:     chance,
		}
		if entry.ActiveBidTotal > 0 || entry.ShownCount > 0 || entry.IsCurrentlyPlaced {
			out = append(out, entry)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ActiveBidTotal != out[j].ActiveBidTotal {
			return out[i].ActiveBidTotal > out[j].ActiveBidTotal
		}
		return out[i].ShownCount > out[j].ShownCount
	})
	total := int64(len(out))
	if offset > len(out) {
		offset = len(out)
	}
	end := offset + take
	if end > len(out) {
		end = len(out)
	}
	httpx.List(c, out[offset:end], total)
}
