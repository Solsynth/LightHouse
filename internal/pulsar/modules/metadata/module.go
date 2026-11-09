package metadata

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/metadata"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             "app.solian.pulsar.metadata",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Capabilities:   []string{"metadata", "share", "unified_media"},
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	core   *core.Services
	engine *metadata.Engine
}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	i.core = host.Core

	// Initialize metadata engine with upstream providers
	cfg := host.Core.Config
	siteURL := cfg.SiteUrl
	if siteURL == "" {
		siteURL = cfg.BaseUrl
	}
	engine := metadata.NewEngine(host.Core.DB, siteURL)

	// Register built-in upstream metadata providers
	engine.RegisterProvider(metadata.NewSteamProvider("", ""))
	engine.RegisterProvider(metadata.NewMusicBrainzProvider("", "", ""))
	engine.RegisterProvider(metadata.NewAniListProvider("", ""))
	engine.RegisterProvider(metadata.NewTMDbProvider("", "", ""))

	i.engine = engine

	// API routes (under /api)
	host.API.GET("/metadata/:id", i.getMetadata)
	host.API.GET("/metadata/search", i.searchMetadata)
	host.API.GET("/m/:id", i.getMetadata) // shorthand endpoint
	host.API.GET("/metadata/:id/scrobbles", i.getMediaScrobbles)
	host.API.PUT("/scrobbles/:id/link", authn.RequireAuth(), i.linkScrobble)
	host.API.POST("/scrobbles/batch-link", authn.RequireAuth(), i.batchLinkScrobbles)

	// Public share routes (under /share and /m directly on engine)
	host.Engine.GET("/share/m/:id", i.handleShare)
	host.Engine.GET("/m/:id", i.handleShare)

	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

func (i *instance) getMetadata(c *gin.Context) {
	id := c.Param("id")
	media, err := i.engine.Resolve(c.Request.Context(), id)
	if err != nil {
		httpx.NotFound(c, err.Error())
		return
	}
	httpx.OK(c, media)
}

func (i *instance) searchMetadata(c *gin.Context) {
	query := c.Query("q")
	if query == "" {
		query = c.Query("query")
	}
	if strings.TrimSpace(query) == "" {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "search query is required")
		return
	}

	domain := c.Query("domain")
	results, err := i.engine.Search(c.Request.Context(), domain, query, 20)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, results)
}

func (i *instance) handleShare(c *gin.Context) {
	id := c.Param("id")
	media, err := i.engine.Resolve(c.Request.Context(), id)
	if err != nil {
		httpx.NotFound(c, "media not found")
		return
	}

	// Check if client expects JSON or is an API request
	accept := c.GetHeader("Accept")
	if strings.Contains(accept, "application/json") || c.Query("format") == "json" {
		httpx.OK(c, media)
		return
	}

	// Render rich HTML OpenGraph preview
	renderHTMLShare(c, media)
}

func (i *instance) getMediaScrobbles(c *gin.Context) {
	id := c.Param("id")
	media, err := i.engine.Resolve(c.Request.Context(), id)
	if err != nil {
		httpx.NotFound(c, "media not found")
		return
	}

	page, ok := httpx.PageQuery(c, i.core.Config.Security.MaxTake)
	if !ok {
		return
	}

	var scrobbles []model.Scrobble
	var total int64
	db := i.core.DB.WithContext(c.Request.Context()).Model(&model.Scrobble{}).Where("catalog_id = ?", media.ID)
	if err := db.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	if err := db.Order("played_at DESC").Offset(page.Offset).Limit(page.Take).Find(&scrobbles).Error; err != nil {
		httpx.Internal(c)
		return
	}

	httpx.List(c, scrobbles, total)
}

type linkScrobbleRequest struct {
	CatalogID *uuid.UUID `json:"catalog_id,omitempty"`
	URN       string     `json:"urn,omitempty"`
}

func (i *instance) linkScrobble(c *gin.Context) {
	accountIDStr := authn.AccountID(c)
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		httpx.Unauthorized(c)
		return
	}

	scrobbleIDStr := c.Param("id")
	scrobbleID, err := uuid.Parse(scrobbleIDStr)
	if err != nil {
		httpx.BadRequest(c, "INVALID_ID", "invalid scrobble id")
		return
	}

	var req linkScrobbleRequest
	if !httpx.BindJSON(c, &req) {
		return
	}

	var targetCatalogID *uuid.UUID
	if req.CatalogID != nil {
		targetCatalogID = req.CatalogID
	} else if strings.TrimSpace(req.URN) != "" {
		resolved, err := i.engine.Resolve(c.Request.Context(), req.URN)
		if err == nil && resolved != nil {
			targetCatalogID = &resolved.ID
		}
	}

	if targetCatalogID == nil {
		httpx.BadRequest(c, "INVALID_TARGET", "valid catalog_id or urn is required")
		return
	}

	// Update scrobble
	res := i.core.DB.WithContext(c.Request.Context()).
		Model(&model.Scrobble{}).
		Where("id = ? AND account_id = ?", scrobbleID, accountID).
		Update("catalog_id", targetCatalogID)
	if res.Error != nil {
		httpx.Internal(c)
		return
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "scrobble not found")
		return
	}

	httpx.OK(c, gin.H{"status": "linked", "catalog_id": targetCatalogID})
}

type batchLinkRequest struct {
	Domain    string `json:"domain"`
	Title     string `json:"title"`
	Subtitle  string `json:"subtitle,omitempty"`
	TargetURN string `json:"target_urn"`
}

func (i *instance) batchLinkScrobbles(c *gin.Context) {
	accountIDStr := authn.AccountID(c)
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		httpx.Unauthorized(c)
		return
	}

	var req batchLinkRequest
	if !httpx.BindJSON(c, &req) {
		return
	}

	if strings.TrimSpace(req.TargetURN) == "" || strings.TrimSpace(req.Title) == "" {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "title and target_urn are required")
		return
	}

	resolved, err := i.engine.Resolve(c.Request.Context(), req.TargetURN)
	if err != nil || resolved == nil {
		httpx.BadRequest(c, "INVALID_TARGET", "unable to resolve target_urn")
		return
	}

	query := i.core.DB.WithContext(c.Request.Context()).
		Model(&model.Scrobble{}).
		Where("account_id = ? AND LOWER(title) = LOWER(?)", accountID, strings.TrimSpace(req.Title))
	if req.Domain != "" {
		query = query.Where("domain = ?", req.Domain)
	}
	if strings.TrimSpace(req.Subtitle) != "" {
		query = query.Where("LOWER(subtitle) = LOWER(?)", strings.TrimSpace(req.Subtitle))
	}

	res := query.Update("catalog_id", resolved.ID)
	if res.Error != nil {
		httpx.Internal(c)
		return
	}

	httpx.OK(c, gin.H{
		"status":        "batch_linked",
		"catalog_id":    resolved.ID,
		"rows_affected": res.RowsAffected,
	})
}
