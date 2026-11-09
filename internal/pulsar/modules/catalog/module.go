package catalog

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             "app.solian.pulsar.catalog",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Models:         []any{&model.CatalogItem{}},
		Capabilities:   []string{"catalog"},
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	core *core.Services
}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	i.core = host.Core

	host.API.GET("/catalog", i.listCatalog)
	host.API.GET("/catalog/:id", i.getCatalogItem)
	host.API.POST("/catalog", authn.RequireAuth(), i.createCatalogItem)

	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

func (i *instance) listCatalog(c *gin.Context) {
	page, ok := httpx.PageQuery(c, i.core.Config.Security.MaxTake)
	if !ok {
		return
	}

	domain := c.Query("domain")
	provider := c.Query("provider")
	search := c.Query("q")
	if search == "" {
		search = c.Query("query")
	}

	items, total, err := i.core.Store.ListCatalogItems(c.Request.Context(), domain, provider, search, page.Offset, page.Take)
	if err != nil {
		httpx.Internal(c)
		return
	}

	httpx.List(c, items, total)
}

func (i *instance) getCatalogItem(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		httpx.BadRequest(c, "INVALID_ID", "invalid catalog item id")
		return
	}

	item, err := i.core.Store.GetCatalogItem(c.Request.Context(), id)
	if err != nil {
		httpx.NotFound(c, "catalog item not found")
		return
	}

	httpx.OK(c, item)
}

type createCatalogRequest struct {
	Domain          string         `json:"domain"`
	Provider        string         `json:"provider,omitempty"`
	ProviderKey     string         `json:"provider_key,omitempty"`
	Title           string         `json:"title"`
	Subtitle        *string        `json:"subtitle,omitempty"`
	Caption         *string        `json:"caption,omitempty"`
	ArtworkURL      *string        `json:"artwork_url,omitempty"`
	DurationSeconds int            `json:"duration_seconds,omitempty"`
	ExternalIDs     map[string]any `json:"external_ids,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

func (i *instance) createCatalogItem(c *gin.Context) {
	var req createCatalogRequest
	if !httpx.BindJSON(c, &req) {
		return
	}

	if strings.TrimSpace(req.Title) == "" {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "title is required")
		return
	}
	domain := strings.TrimSpace(req.Domain)
	if domain == "" {
		domain = model.DomainMusic
	}

	catItem := &model.CatalogItem{
		Domain:          domain,
		Provider:        strings.TrimSpace(req.Provider),
		ProviderKey:     strings.TrimSpace(req.ProviderKey),
		Title:           strings.TrimSpace(req.Title),
		Subtitle:        req.Subtitle,
		Caption:         req.Caption,
		ArtworkURL:      req.ArtworkURL,
		DurationSeconds: req.DurationSeconds,
	}

	if req.ExternalIDs != nil {
		catItem.ExternalIDs = model.ToJSON(req.ExternalIDs)
	}
	if req.Metadata != nil {
		catItem.Metadata = model.ToJSON(req.Metadata)
	}

	saved, err := i.core.Store.FindOrCreateCatalogItem(c.Request.Context(), catItem)
	if err != nil {
		httpx.Internal(c)
		return
	}

	httpx.Created(c, saved)
}
