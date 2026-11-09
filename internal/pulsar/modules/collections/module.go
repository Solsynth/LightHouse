package collections

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
		ID:             "app.solian.pulsar.collections",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Models:         []any{&model.Collection{}, &model.CollectionItem{}},
		Capabilities:   []string{"collections", "playlists", "watchlists"},
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	core *core.Services
}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	i.core = host.Core

	r := host.API.Group("/collections")
	r.GET("", authn.RequireAuth(), i.listCollections)
	r.POST("", authn.RequireAuth(), i.createCollection)
	r.GET("/:id", i.getCollection)
	r.PUT("/:id", authn.RequireAuth(), i.updateCollection)
	r.DELETE("/:id", authn.RequireAuth(), i.deleteCollection)
	r.POST("/:id/items", authn.RequireAuth(), i.addItem)
	r.DELETE("/:id/items/:itemId", authn.RequireAuth(), i.removeItem)

	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

func (i *instance) listCollections(c *gin.Context) {
	accountIDStr := authn.AccountID(c)
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		httpx.Unauthorized(c)
		return
	}

	page, ok := httpx.PageQuery(c, i.core.Config.Security.MaxTake)
	if !ok {
		return
	}

	domain := c.Query("domain")
	kind := c.Query("kind")

	items, total, err := i.core.Store.ListCollections(c.Request.Context(), accountID, domain, kind, page.Offset, page.Take)
	if err != nil {
		httpx.Internal(c)
		return
	}

	httpx.List(c, items, total)
}

type createCollectionRequest struct {
	Domain      string  `json:"domain"`
	Kind        string  `json:"kind"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	IsPublic    *bool   `json:"is_public,omitempty"`
	ArtworkURL  *string `json:"artwork_url,omitempty"`
}

func (i *instance) createCollection(c *gin.Context) {
	accountIDStr := authn.AccountID(c)
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		httpx.Unauthorized(c)
		return
	}

	var req createCollectionRequest
	if !httpx.BindJSON(c, &req) {
		return
	}

	if strings.TrimSpace(req.Name) == "" {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "collection name is required")
		return
	}
	domain := strings.TrimSpace(req.Domain)
	if domain == "" {
		domain = model.DomainMusic
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = model.KindPlaylist
	}
	isPublic := true
	if req.IsPublic != nil {
		isPublic = *req.IsPublic
	}

	coll := &model.Collection{
		AccountID:   accountID,
		Domain:      domain,
		Kind:        kind,
		Name:        strings.TrimSpace(req.Name),
		Description: req.Description,
		IsPublic:    isPublic,
		ArtworkURL:  req.ArtworkURL,
	}

	if err := i.core.Store.CreateCollection(c.Request.Context(), coll); err != nil {
		httpx.Internal(c)
		return
	}

	httpx.Created(c, coll)
}

func (i *instance) getCollection(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		httpx.BadRequest(c, "INVALID_ID", "invalid collection id")
		return
	}

	coll, err := i.core.Store.GetCollection(c.Request.Context(), id)
	if err != nil {
		httpx.NotFound(c, "collection not found")
		return
	}

	// Gate private collection if unauthenticated or not owner
	if !coll.IsPublic {
		accountIDStr := authn.AccountID(c)
		if accountIDStr == "" || accountIDStr != coll.AccountID.String() {
			httpx.Forbidden(c, "private collection")
			return
		}
	}

	httpx.OK(c, coll)
}

func (i *instance) updateCollection(c *gin.Context) {
	accountIDStr := authn.AccountID(c)
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		httpx.Unauthorized(c)
		return
	}

	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		httpx.BadRequest(c, "INVALID_ID", "invalid collection id")
		return
	}

	coll, err := i.core.Store.GetCollection(c.Request.Context(), id)
	if err != nil {
		httpx.NotFound(c, "collection not found")
		return
	}
	if coll.AccountID != accountID {
		httpx.Forbidden(c, "not the owner of this collection")
		return
	}

	var req createCollectionRequest
	if !httpx.BindJSON(c, &req) {
		return
	}

	if strings.TrimSpace(req.Name) != "" {
		coll.Name = strings.TrimSpace(req.Name)
	}
	if req.Description != nil {
		coll.Description = req.Description
	}
	if req.IsPublic != nil {
		coll.IsPublic = *req.IsPublic
	}
	if req.ArtworkURL != nil {
		coll.ArtworkURL = req.ArtworkURL
	}

	if err := i.core.Store.UpdateCollection(c.Request.Context(), coll); err != nil {
		httpx.Internal(c)
		return
	}

	httpx.OK(c, coll)
}

func (i *instance) deleteCollection(c *gin.Context) {
	accountIDStr := authn.AccountID(c)
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		httpx.Unauthorized(c)
		return
	}

	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		httpx.BadRequest(c, "INVALID_ID", "invalid collection id")
		return
	}

	if err := i.core.Store.DeleteCollection(c.Request.Context(), accountID, id); err != nil {
		httpx.NotFound(c, "collection not found")
		return
	}

	httpx.NoContent(c)
}

type addItemRequest struct {
	CatalogID *uuid.UUID `json:"catalog_id,omitempty"`
	Position  int        `json:"position,omitempty"`
	Note      *string    `json:"note,omitempty"`
	Rating    *int       `json:"rating,omitempty"`
	Status    *string    `json:"status,omitempty"`

	// Optional inline catalog payload to create catalog item on the fly
	Title      string  `json:"title,omitempty"`
	Subtitle   *string `json:"subtitle,omitempty"`
	Caption    *string `json:"caption,omitempty"`
	ArtworkURL *string `json:"artwork_url,omitempty"`
}

func (i *instance) addItem(c *gin.Context) {
	accountIDStr := authn.AccountID(c)
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		httpx.Unauthorized(c)
		return
	}

	idStr := c.Param("id")
	collID, err := uuid.Parse(idStr)
	if err != nil {
		httpx.BadRequest(c, "INVALID_ID", "invalid collection id")
		return
	}

	coll, err := i.core.Store.GetCollection(c.Request.Context(), collID)
	if err != nil {
		httpx.NotFound(c, "collection not found")
		return
	}
	if coll.AccountID != accountID {
		httpx.Forbidden(c, "not the owner of this collection")
		return
	}

	var req addItemRequest
	if !httpx.BindJSON(c, &req) {
		return
	}

	catalogID := req.CatalogID
	if catalogID == nil && strings.TrimSpace(req.Title) != "" {
		catItem := &model.CatalogItem{
			Domain:     coll.Domain,
			Title:      strings.TrimSpace(req.Title),
			Subtitle:   req.Subtitle,
			Caption:    req.Caption,
			ArtworkURL: req.ArtworkURL,
		}
		if saved, err := i.core.Store.FindOrCreateCatalogItem(c.Request.Context(), catItem); err == nil && saved != nil {
			catalogID = &saved.ID
		}
	}

	item := &model.CollectionItem{
		CollectionID: collID,
		CatalogID:    catalogID,
		Position:     req.Position,
		Note:         req.Note,
		Rating:       req.Rating,
		Status:       req.Status,
	}

	if err := i.core.Store.AddCollectionItem(c.Request.Context(), item); err != nil {
		httpx.Internal(c)
		return
	}

	httpx.Created(c, item)
}

func (i *instance) removeItem(c *gin.Context) {
	accountIDStr := authn.AccountID(c)
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		httpx.Unauthorized(c)
		return
	}

	collIDStr := c.Param("id")
	collID, err := uuid.Parse(collIDStr)
	if err != nil {
		httpx.BadRequest(c, "INVALID_ID", "invalid collection id")
		return
	}

	coll, err := i.core.Store.GetCollection(c.Request.Context(), collID)
	if err != nil {
		httpx.NotFound(c, "collection not found")
		return
	}
	if coll.AccountID != accountID {
		httpx.Forbidden(c, "not the owner of this collection")
		return
	}

	itemIDStr := c.Param("itemId")
	itemID, err := uuid.Parse(itemIDStr)
	if err != nil {
		httpx.BadRequest(c, "INVALID_ID", "invalid item id")
		return
	}

	if err := i.core.Store.RemoveCollectionItem(c.Request.Context(), collID, itemID); err != nil {
		httpx.NotFound(c, "collection item not found")
		return
	}

	httpx.NoContent(c)
}
