package catalog

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/store"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             "app.solian.pulsar.catalog",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Models:         []any{&model.CatalogItem{}, &model.Creator{}, &model.CatalogCreator{}},
		// The fingerprint column is new: records written before it need it
		// filled, or a device that spells a song slightly differently files
		// a second row beside the first.
		SQL:          []module.SchemaHook{store.BackfillCatalogFingerprints},
		Capabilities: []string{"catalog", "creators"},
		New:          func() module.Instance[core.Services] { return &instance{} },
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

	// Creators are the people and organisations behind catalog items —
	// artists, authors, directors, developers, studios, publishers — reached
	// by search or by id, with the items they worked on.
	host.API.GET("/creators", i.listCreators)
	host.API.GET("/creators/:id", i.getCreator)

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

	// A creator_id filter answers "everything by this artist" without
	// searching, which is the whole point of storing creators.
	if raw := c.Query("creator_id"); raw != "" {
		creatorID, err := uuid.Parse(raw)
		if err != nil {
			httpx.BadRequest(c, "INVALID_ID", "invalid creator_id")
			return
		}
		items, total, err := i.core.Store.ListCatalogItemsByCreator(c.Request.Context(), creatorID, domain, page.Offset, page.Take)
		if err != nil {
			httpx.Internal(c)
			return
		}
		httpx.List(c, items, total)
		return
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

type creatorInput struct {
	Name string `json:"name"`
	Role string `json:"role"`
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
	Creators        []creatorInput `json:"creators,omitempty"`
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

	if len(req.Creators) > 0 {
		credits := make([]model.CreatorCredit, 0, len(req.Creators))
		for _, input := range req.Creators {
			credits = append(credits, model.CreatorCredit{Name: input.Name, Role: input.Role})
		}
		if err := i.core.Store.AttachCatalogCreators(c.Request.Context(), saved.ID, saved.Domain, credits); err != nil {
			httpx.Internal(c)
			return
		}
	}

	httpx.Created(c, saved)
}

func (i *instance) listCreators(c *gin.Context) {
	page, ok := httpx.PageQuery(c, i.core.Config.Security.MaxTake)
	if !ok {
		return
	}

	creators, total, err := i.core.Store.ListCreators(c.Request.Context(), c.Query("domain"), c.Query("q"), page.Offset, page.Take)
	if err != nil {
		httpx.Internal(c)
		return
	}

	httpx.List(c, creators, total)
}

func (i *instance) getCreator(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.BadRequest(c, "INVALID_ID", "invalid creator id")
		return
	}

	creator, err := i.core.Store.GetCreatorWithCount(c.Request.Context(), id)
	if err != nil {
		httpx.NotFound(c, "creator not found")
		return
	}

	httpx.OK(c, creator)
}
