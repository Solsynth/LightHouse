package publishers

import (
	"math"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/go/pkg/errs"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

func (i *instance) registerPublicRoutes(api *gin.RouterGroup) {
	api.GET("/publishers/search", i.handleSearchPublishers)
	api.GET("/publishers/leaderboard", i.handleLeaderboard)
	api.GET("/publishers/of/:accountId", i.handleAccountPublishers)
	api.GET("/publishers/:name", i.handleGetPublisher)
	api.GET("/publishers/:name/heatmap", i.handlePublisherHeatmap)
	api.GET("/publishers/:name/stats", i.handlePublisherStats)
	api.GET("/publishers/:name/content-stats", i.handlePublisherContentStats)
	api.GET("/publishers/:name/rating", i.handlePublisherRating)
	api.GET("/publishers/:name/rating/overview", i.handleRatingOverview)
}

func (i *instance) hiddenAccountIDs(c *gin.Context) []uuid.UUID {
	user := authn.CurrentUser(c)
	if user == nil || i.core.Clients == nil || i.core.Clients.Profile == nil {
		return nil
	}
	accountID := user.GetId()
	seen := map[uuid.UUID]bool{}
	out := make([]uuid.UUID, 0)
	collect := func(resp *gen.DyListRelationshipSimpleResponse) {
		if resp == nil {
			return
		}
		for _, raw := range resp.GetAccountsId() {
			id, err := uuid.Parse(raw)
			if err == nil && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	blocked, err := i.core.Clients.Profile.ListBlocked(c.Request.Context(), &gen.DyListRelationshipSimpleRequest{
		RelationIdentifier: &gen.DyListRelationshipSimpleRequest_AccountId{AccountId: accountID},
	})
	if err == nil {
		collect(blocked)
	}
	muted, err := i.core.Clients.Profile.ListMuted(c.Request.Context(), &gen.DyListRelationshipSimpleRequest{
		RelationIdentifier: &gen.DyListRelationshipSimpleRequest_AccountId{AccountId: accountID},
	})
	if err == nil {
		collect(muted)
	}
	return out
}

func (i *instance) handleSearchPublishers(c *gin.Context) {
	query := strings.TrimSpace(c.Query("query"))
	if query == "" {
		httpx.OK(c, []model.Publisher{})
		return
	}
	take, err := strconv.Atoi(c.DefaultQuery("take", "20"))
	if err != nil || take <= 0 {
		take = 20
	}
	if take > i.maxTake() {
		take = i.maxTake()
	}
	fuzzy := len([]rune(query)) >= 3
	pattern := "%" + strings.ToLower(query) + "%"

	sql := "SELECT * FROM publishers WHERE deleted_at IS NULL AND type <> ?"
	args := []any{publisherFediverse}

	hidden := i.hiddenAccountIDs(c)
	if len(hidden) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(hidden)), ",")
		sql += " AND (account_id IS NULL OR account_id NOT IN (" + placeholders + "))"
		for _, id := range hidden {
			args = append(args, id)
		}
	}
	search := "lower(name) LIKE ? OR lower(nick) LIKE ? OR (bio IS NOT NULL AND lower(bio) LIKE ?)"
	args = append(args, pattern, pattern, pattern)
	if fuzzy {
		search += " OR name % ? OR nick % ? OR (bio IS NOT NULL AND ? <% bio)"
		args = append(args, query, query, query)
	}
	sql += " AND (" + search + ")"
	if fuzzy {
		sql += " ORDER BY CASE WHEN lower(name) LIKE ? OR lower(nick) LIKE ? OR (bio IS NOT NULL AND lower(bio) LIKE ?) THEN 1 ELSE 0 END DESC," +
			" similarity(name, ?) DESC, similarity(nick, ?) DESC, COALESCE(word_similarity(?, bio), 0) DESC, name ASC"
		args = append(args, pattern, pattern, pattern, query, query, query)
	} else {
		sql += " ORDER BY name ASC"
	}
	sql += " LIMIT ?"
	args = append(args, take)

	var entities []store.PublisherEntity
	if err := i.store.DB.WithContext(c.Request.Context()).Raw(sql, args...).Scan(&entities).Error; err != nil {
		i.internal(c)
		return
	}
	across := make([]*store.PublisherEntity, 0, len(entities))
	for index := range entities {
		across = append(across, &entities[index])
	}
	httpx.OK(c, i.publisherModels(c.Request.Context(), across, true, false))
}

func (i *instance) handleGetPublisher(c *gin.Context) {
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	httpx.OK(c, i.publisherModelFor(c.Request.Context(), publisher, true))
}

func (i *instance) handlePublisherHeatmap(c *gin.Context) {
	heatmap, found, err := i.publisherHeatmap(c.Request.Context(), c.Param("name"))
	if err != nil {
		i.internal(c)
		return
	}
	if !found {
		i.notFound(c)
		return
	}
	httpx.OK(c, heatmap)
}

func (i *instance) handlePublisherStats(c *gin.Context) {
	stats, found, err := i.publisherStats(c.Request.Context(), c.Param("name"))
	if err != nil {
		i.internal(c)
		return
	}
	if !found {
		i.notFound(c)
		return
	}
	httpx.OK(c, stats)
}

func (i *instance) handlePublisherContentStats(c *gin.Context) {
	stats, found, err := i.publisherContentStats(c.Request.Context(), c.Param("name"))
	if err != nil {
		i.internal(c)
		return
	}
	if !found {
		i.notFound(c)
		return
	}
	httpx.OK(c, stats)
}

func (i *instance) handleAccountPublishers(c *gin.Context) {
	accountID, err := uuid.Parse(c.Param("accountId"))
	if err != nil {
		i.notFound(c)
		return
	}
	rows, err := i.memberRowsForAccount(c.Request.Context(), accountID, true)
	if err != nil {
		i.internal(c)
		return
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.PublisherID)
	}
	entities := i.publishersByIDs(c.Request.Context(), ids)
	ordered := make([]*store.PublisherEntity, 0, len(rows))
	for _, row := range rows {
		if entity, ok := entities[row.PublisherID.String()]; ok {
			ordered = append(ordered, entity)
		}
	}
	httpx.OK(c, i.publisherModels(c.Request.Context(), ordered, false, false))
}

func (i *instance) handlePublisherRating(c *gin.Context) {
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	rating, err := i.rating(c.Request.Context(), publisher.ID)
	if err != nil {
		i.internal(c)
		return
	}
	httpx.OK(c, rating)
}

func (i *instance) leaderboardEntries(c *gin.Context) ([]leaderboardEntry, error) {
	rows, err := i.store.PublisherLeaderboard(c.Request.Context())
	if err != nil {
		return nil, err
	}
	total := len(rows)
	entries := make([]leaderboardEntry, 0, total)
	currentRank := 1
	for index, row := range rows {
		if index > 0 && rows[index].Rating < rows[index-1].Rating {
			currentRank = index + 1
		}
		percentile := float64(total-currentRank+1) / float64(total) * 100
		entries = append(entries, leaderboardEntry{
			PublisherID: row.ID.String(),
			Name:        row.Name,
			Nick:        row.Nick,
			Picture:     store.JSONValue(row.Picture),
			Rating:      row.Rating,
			Rank:        currentRank,
			Percentile:  math.Round(percentile*10) / 10,
			Grade:       gradeFor(percentile, currentRank),
		})
	}
	return entries, nil
}

func gradeFor(percentile float64, rank int) string {
	switch {
	case rank == 1:
		return "S++"
	case percentile >= 99:
		return "S+"
	case percentile >= 95:
		return "S"
	case percentile >= 90:
		return "A++"
	case percentile >= 80:
		return "A+"
	case percentile >= 70:
		return "A"
	case percentile >= 60:
		return "A-"
	case percentile >= 50:
		return "B+"
	case percentile >= 40:
		return "B"
	case percentile >= 20:
		return "C"
	default:
		return "D"
	}
}

func (i *instance) handleLeaderboard(c *gin.Context) {
	take, err := strconv.Atoi(c.DefaultQuery("take", "20"))
	if err != nil || take <= 0 {
		take = 20
	}
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 {
		offset = 0
	}
	entries, err := i.leaderboardEntries(c)
	if err != nil {
		i.internal(c)
		return
	}
	total := int64(len(entries))
	start := offset
	if start > len(entries) {
		start = len(entries)
	}
	end := start + take
	if end > len(entries) {
		end = len(entries)
	}
	httpx.List(c, entries[start:end], total)
}

func (i *instance) handleRatingOverview(c *gin.Context) {
	entries, err := i.leaderboardEntries(c)
	if err != nil {
		i.internal(c)
		return
	}
	name := c.Param("name")
	for _, entry := range entries {
		if strings.EqualFold(entry.Name, name) {
			httpx.OK(c, ratingOverview{
				Rating:          entry.Rating,
				Rank:            entry.Rank,
				TotalPublishers: len(entries),
				Percentile:      entry.Percentile,
				Grade:           entry.Grade,
			})
			return
		}
	}
	httpx.Fail(c, errs.NotFound("Not found."))
}
