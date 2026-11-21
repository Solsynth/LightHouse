package metadata

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/metadata"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
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

	// Register built-in upstream metadata providers. The generic search asks
	// the track providers of the requested domain, so every media domain
	// needs one here: dropping a provider does not degrade the search, it
	// makes that domain return only locally-cached catalog rows.
	metaCfg := host.Core.Config.Metadata
	music := metadata.NewMusicBrainzSet(
		metaCfg.MusicBrainzBaseURL,
		metaCfg.MusicBrainzCoverArtURL,
		metaCfg.UserAgent,
	)
	engine.RegisterProvider(music.Recording)
	engine.RegisterProvider(music.Artist)
	engine.RegisterProvider(music.Album)
	engine.RegisterProvider(metadata.NewSteamProvider(metaCfg.SteamBaseURL, metaCfg.UserAgent))
	engine.RegisterProvider(metadata.NewAniListProvider(metaCfg.AniListEndpoint, metaCfg.UserAgent))
	// Bangumi carries the book domain and adds a second anime and Japanese
	// game source. Its API is public; the access token only raises quotas.
	bangumi := metadata.NewBangumiSet(metaCfg.BangumiBaseURL, metaCfg.UserAgent, metaCfg.BangumiAccessToken)
	engine.RegisterProvider(bangumi.Anime)
	engine.RegisterProvider(bangumi.Book)
	engine.RegisterProvider(bangumi.Game)
	engine.RegisterProvider(bangumi.Video)
	// TMDb is the one provider that cannot work without a deployment secret.
	// Register it only when the key exists, so an unconfigured video search
	// is an explicit absence rather than a provider that always returns nil.
	if key := strings.TrimSpace(metaCfg.TMDbAPIKey); key != "" {
		engine.RegisterProvider(metadata.NewTMDbProvider(key, metaCfg.TMDbBaseURL, metaCfg.UserAgent))
	}

	i.engine = engine

	// API routes (under /api)
	host.API.GET("/metadata/:id", i.getMetadata)
	host.API.GET("/metadata/search", i.searchMetadata)
	// The entity endpoints the client's artist and album pages read: a name —
	// the only handle every connected library shares — in, one cached catalog
	// record out. They cannot be /metadata/:id because a library has no
	// MusicBrainz id to ask with.
	host.API.GET("/metadata/artist", i.getArtistMetadata)
	host.API.GET("/metadata/album", i.getAlbumMetadata)
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

// getArtistMetadata answers an artist page: the fleet's record for the act,
// resolved and cached once. The key is the name a library gives it, plus the
// reader's language, so a description comes back in the interface's own
// language when Wikipedia has the article.
func (i *instance) getArtistMetadata(c *gin.Context) {
	name := strings.TrimSpace(c.Query("name"))
	if name == "" {
		name = strings.TrimSpace(c.Query("q"))
	}
	if name == "" {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "artist name is required")
		return
	}
	ctx := metadata.ContextWithReaderLang(c.Request.Context(), c.Query("lang"))
	media, err := i.engine.ResolveArtist(ctx, name)
	if err != nil {
		metadataFailure(c, err)
		return
	}
	httpx.OK(c, media)
}

// getAlbumMetadata answers an album page from a title and its artist — the
// pair every library spells, since none of them shares MusicBrainz's ids.
func (i *instance) getAlbumMetadata(c *gin.Context) {
	title := strings.TrimSpace(c.Query("title"))
	if title == "" {
		title = strings.TrimSpace(c.Query("name"))
	}
	if title == "" {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "album title is required")
		return
	}
	ctx := metadata.ContextWithReaderLang(c.Request.Context(), c.Query("lang"))
	media, err := i.engine.ResolveAlbum(ctx, title, c.Query("artist"))
	if err != nil {
		metadataFailure(c, err)
		return
	}
	httpx.OK(c, media)
}

// metadataFailure maps an entity resolve failure onto the status it means:
// an upstream that is throttling or down is a 503 the client can retry, not a
// 404 telling the user the artist does not exist — the two send a reader to
// very different conclusions about their own library.
func metadataFailure(c *gin.Context, err error) {
	var busy metadata.BusyError
	if errors.As(err, &busy) {
		httpx.Fail(c, errs.New("UPSTREAM_BUSY", "The metadata service is busy; try again shortly.", http.StatusServiceUnavailable))
		return
	}
	httpx.NotFound(c, err.Error())
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

	// Render rich HTML OpenGraph preview via webui mainframe if installed,
	// falling back to the standalone card renderer.
	if i.core != nil && i.core.RenderMediaShare != nil {
		i.core.RenderMediaShare(c, media)
		return
	}
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
