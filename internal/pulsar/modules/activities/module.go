package activities

import (
	"context"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

// Factory returns the Stargate legacy activities compatibility module.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             "app.solian.pulsar.activities_compat",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Capabilities:   []string{"activities_compat"},
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	core *core.Services
}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	i.core = host.Core

	r := host.API.Group("/activities")
	r.GET("", authn.RequireAuth(), i.listActivities)
	r.GET("/catalog", i.listCatalog)
	r.GET("/stats", authn.RequireAuth(), i.stats)
	r.POST("/sessions/start", authn.RequireAuth(), i.startSession)
	r.POST("/sessions/end", authn.RequireAuth(), i.endSession)
	r.POST("", authn.RequireAuth(), i.setActivity)
	r.GET("/:identifier", i.activitiesByAccount)

	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

type activityRequest struct {
	Type           *string        `json:"type"`
	ManualId       *string        `json:"manual_id"`
	Provider       *string        `json:"provider"`
	ReferenceId    *string        `json:"reference_id"`
	Title          *string        `json:"title"`
	Subtitle       *string        `json:"subtitle"`
	Caption        *string        `json:"caption"`
	LargeImage     *string        `json:"large_image"`
	SmallImage     *string        `json:"small_image"`
	TitleUrl       *string        `json:"title_url"`
	SubtitleUrl    *string        `json:"subtitle_url"`
	QueryableTerms []string       `json:"queryable_terms"`
	Meta           map[string]any `json:"meta"`
	LeaseMinutes   *int           `json:"lease_minutes"`
}

func (i *instance) listActivities(c *gin.Context) {
	accountIDStr := authn.AccountID(c)
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		httpx.Unauthorized(c)
		return
	}

	includeExpired := c.Query("includeExpired") == "true" || c.Query("include_expired") == "true"
	if !includeExpired {
		sessions, err := i.core.Store.GetActiveSessionsForAccount(c.Request.Context(), accountID)
		if err != nil {
			httpx.Internal(c)
			return
		}
		httpx.OK(c, sessions)
		return
	}

	page, ok := httpx.PageQuery(c, i.core.Config.Security.MaxTake)
	if !ok {
		return
	}
	domain := c.Query("type")
	items, total, err := i.core.Store.ListScrobbles(c.Request.Context(), accountID, domain, nil, nil, page.Offset, page.Take)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.List(c, items, total)
}

func (i *instance) setActivity(c *gin.Context) {
	accountIDStr := authn.AccountID(c)
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		httpx.Unauthorized(c)
		return
	}

	var req activityRequest
	if !httpx.BindJSON(c, &req) {
		return
	}

	title := ""
	if req.Title != nil {
		title = strings.TrimSpace(*req.Title)
	}
	if title == "" {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "title is required")
		return
	}

	manualID := "default"
	if req.ManualId != nil && strings.TrimSpace(*req.ManualId) != "" {
		manualID = strings.TrimSpace(*req.ManualId)
	}

	domain := model.DomainCustom
	if req.Type != nil && strings.TrimSpace(*req.Type) != "" {
		domain = strings.TrimSpace(*req.Type)
	}

	leaseMin := 5
	if req.LeaseMinutes != nil && *req.LeaseMinutes >= 1 && *req.LeaseMinutes <= 60 {
		leaseMin = *req.LeaseMinutes
	}

	var catID *uuid.UUID
	catItem := &model.CatalogItem{
		Domain:     domain,
		Title:      title,
		Subtitle:   req.Subtitle,
		Caption:    req.Caption,
		ArtworkURL: req.LargeImage,
	}
	if req.Provider != nil {
		catItem.Provider = *req.Provider
	}
	if req.ReferenceId != nil {
		catItem.ProviderKey = *req.ReferenceId
	}
	if saved, err := i.core.Store.FindOrCreateCatalogItem(c.Request.Context(), catItem); err == nil && saved != nil {
		catID = &saved.ID
	}

	session := &model.ActiveSession{
		AccountID:       accountID,
		CatalogID:       catID,
		Domain:          domain,
		ManualID:        manualID,
		Title:           title,
		Subtitle:        req.Subtitle,
		Caption:         req.Caption,
		LargeImage:      req.LargeImage,
		SmallImage:      req.SmallImage,
		ArtworkURL:      req.LargeImage,
		LeaseMinutes:    leaseMin,
		StartedAt:       time.Now().UTC(),
	}
	if req.Meta != nil {
		session.Meta = model.ToJSON(req.Meta)
	}

	if err := i.core.Store.UpsertActiveSession(c.Request.Context(), session); err != nil {
		httpx.Internal(c)
		return
	}

	i.core.PublishSessionUpdate(c.Request.Context(), accountID, session)
	httpx.OK(c, session)
}

func (i *instance) startSession(c *gin.Context) {
	// Re-uses setActivity semantics
	i.setActivity(c)
}

type endSessionBody struct {
	ManualId *string `json:"manual_id"`
}

func (i *instance) endSession(c *gin.Context) {
	accountIDStr := authn.AccountID(c)
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		httpx.Unauthorized(c)
		return
	}

	var body endSessionBody
	_ = c.ShouldBindJSON(&body)
	manualID := "default"
	if body.ManualId != nil && strings.TrimSpace(*body.ManualId) != "" {
		manualID = strings.TrimSpace(*body.ManualId)
	}

	ended, err := i.core.Store.EndActiveSession(c.Request.Context(), accountID, manualID)
	if err != nil {
		httpx.OK(c, gin.H{"status": "ended"})
		return
	}

	// Also record scrobble on session end
	if ended != nil {
		scrobble := &model.Scrobble{
			AccountID:       accountID,
			CatalogID:       ended.CatalogID,
			Domain:          ended.Domain,
			ClientName:      "session",
			Title:           ended.Title,
			Subtitle:        ended.Subtitle,
			Caption:         ended.Caption,
			ArtworkURL:      ended.ArtworkURL,
			ListenedSeconds: ended.ProgressSeconds,
			DurationSeconds: ended.DurationSeconds,
			PlayedAt:        time.Now().UTC(),
			Meta:            ended.Meta,
		}
		_ = i.core.Store.CreateScrobble(c.Request.Context(), scrobble)
		i.core.PublishScrobbleCreated(c.Request.Context(), accountID, scrobble)
	}

	i.core.PublishSessionUpdate(c.Request.Context(), accountID, nil)
	httpx.OK(c, gin.H{"status": "ended", "session": ended})
}

func (i *instance) listCatalog(c *gin.Context) {
	page, ok := httpx.PageQuery(c, i.core.Config.Security.MaxTake)
	if !ok {
		return
	}

	query := c.Query("query")
	provider := c.Query("provider")
	items, total, err := i.core.Store.ListCatalogItems(c.Request.Context(), "", provider, query, page.Offset, page.Take)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.List(c, items, total)
}

func (i *instance) stats(c *gin.Context) {
	accountIDStr := authn.AccountID(c)
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		httpx.Unauthorized(c)
		return
	}

	stats, err := i.core.Store.GetMediaStats(c.Request.Context(), accountID, "", nil, nil)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, stats)
}

func (i *instance) activitiesByAccount(c *gin.Context) {
	identifier := c.Param("identifier")
	accountID, err := i.core.ResolveAccount(c.Request.Context(), identifier)
	if err != nil {
		httpx.NotFound(c, "user not found")
		return
	}

	sessions, err := i.core.Store.GetActiveSessionsForAccount(c.Request.Context(), accountID)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, sessions)
}
