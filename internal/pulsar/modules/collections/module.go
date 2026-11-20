package collections

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
	r.PUT("/:id/items", authn.RequireAuth(), i.replaceItems)
	r.PATCH("/:id/items/:itemId", authn.RequireAuth(), i.updateItem)

	// The account's favourites are a collection of their own, kind
	// favorites: one per account and domain, made on first use. They are
	// reached by name rather than by id so a client never has to find them
	// in a list it may have filtered.
	r.GET("/favourites", authn.RequireAuth(), i.getFavourites)
	r.PUT("/favourites", authn.RequireAuth(), i.setFavourite)
	r.DELETE("/favourites/:catalogId", authn.RequireAuth(), i.unsetFavourite)

	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

func (i *instance) listCollections(c *gin.Context) {
	accountID, ok := ownerOf(c, i)
	if !ok {
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
	accountID, ok := ownerOf(c, i)
	if !ok {
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
	accountID, ok := ownerOf(c, i)
	if !ok {
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
	accountID, ok := ownerOf(c, i)
	if !ok {
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
	Title           string  `json:"title,omitempty"`
	Subtitle        *string `json:"subtitle,omitempty"`
	Caption         *string `json:"caption,omitempty"`
	ArtworkURL      *string `json:"artwork_url,omitempty"`
	Provider        string  `json:"provider,omitempty"`
	ProviderKey     string  `json:"provider_key,omitempty"`
	DurationSeconds int     `json:"duration_seconds,omitempty"`
}

func (i *instance) addItem(c *gin.Context) {
	accountID, ok := ownerOf(c, i)
	if !ok {
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
			Domain:          coll.Domain,
			Title:           strings.TrimSpace(req.Title),
			Subtitle:        req.Subtitle,
			Caption:         req.Caption,
			ArtworkURL:      req.ArtworkURL,
			Provider:        strings.TrimSpace(req.Provider),
			ProviderKey:     strings.TrimSpace(req.ProviderKey),
			DurationSeconds: req.DurationSeconds,
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
	accountID, ok := ownerOf(c, i)
	if !ok {
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

type updateItemRequest struct {
	Note   *string `json:"note,omitempty"`
	Rating *int    `json:"rating,omitempty"`
	Status *string `json:"status,omitempty"`
}

// updateItem marks progress on one collection item — status, rating, or
// note — without re-uploading the whole list.
func (i *instance) updateItem(c *gin.Context) {
	accountID, ok := ownerOf(c, i)
	if !ok {
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

	var req updateItemRequest
	if !httpx.BindJSON(c, &req) {
		return
	}

	if req.Status != nil && !validCollectionStatus(*req.Status) {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "unknown status "+*req.Status)
		return
	}

	item, err := i.core.Store.UpdateCollectionItem(c.Request.Context(), collID, itemID, store.CollectionItemUpdate{
		Note:   req.Note,
		Rating: req.Rating,
		Status: req.Status,
	})
	if err != nil {
		httpx.NotFound(c, "collection item not found")
		return
	}

	httpx.OK(c, item)
}

// validCollectionStatus accepts the statuses a client may set on an item.
func validCollectionStatus(status string) bool {
	switch status {
	case model.StatusPlanToWatch, model.StatusWatching, model.StatusCompleted,
		model.StatusOnHold, model.StatusDropped:
		return true
	}
	return false
}

// replaceItemsRequest is a playlist upload or re-sync: the caller's list is
// the truth for the whole collection, in order.
type replaceItemsRequest struct {
	Items []struct {
		CatalogID       *uuid.UUID `json:"catalog_id,omitempty"`
		Title           string     `json:"title,omitempty"`
		Subtitle        *string    `json:"subtitle,omitempty"`
		Caption         *string    `json:"caption,omitempty"`
		ArtworkURL      *string    `json:"artwork_url,omitempty"`
		Provider        string     `json:"provider,omitempty"`
		ProviderKey     string     `json:"provider_key,omitempty"`
		DurationSeconds int        `json:"duration_seconds,omitempty"`
	} `json:"items"`
}

func (i *instance) replaceItems(c *gin.Context) {
	accountID, ok := ownerOf(c, i)
	if !ok {
		return
	}

	collID, err := uuid.Parse(c.Param("id"))
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

	var req replaceItemsRequest
	if !httpx.BindJSON(c, &req) {
		return
	}

	items := make([]store.ReplacementItem, 0, len(req.Items))
	for _, entry := range req.Items {
		items = append(items, store.ReplacementItem{
			CatalogID:       entry.CatalogID,
			Title:           entry.Title,
			Subtitle:        entry.Subtitle,
			Caption:         entry.Caption,
			ArtworkURL:      entry.ArtworkURL,
			Provider:        entry.Provider,
			ProviderKey:     entry.ProviderKey,
			DurationSeconds: entry.DurationSeconds,
		})
	}

	saved, err := i.core.Store.ReplaceCollectionItems(c.Request.Context(), collID, items)
	if err != nil {
		httpx.Internal(c)
		return
	}

	httpx.OK(c, gin.H{"items": saved, "count": len(saved)})
}

// ── Favourites ───────────────────────────────────────────────────────────

func (i *instance) favouritesOf(c *gin.Context) (*model.Collection, bool) {
	accountID, ok := ownerOf(c, i)
	if !ok {
		return nil, false
	}
	domain := strings.TrimSpace(c.Query("domain"))
	if domain == "" {
		domain = model.DomainMusic
	}
	coll, err := i.core.Store.EnsureFavouritesCollection(c.Request.Context(), accountID, domain)
	if err != nil {
		httpx.Internal(c)
		return nil, false
	}
	return coll, true
}

// getFavourites returns the account's favourites with their catalog records,
// newest first. One record per track: the collection is the account's taste,
// not a log of heart taps.
func (i *instance) getFavourites(c *gin.Context) {
	coll, ok := i.favouritesOf(c)
	if !ok {
		return
	}

	full, err := i.core.Store.GetCollection(c.Request.Context(), coll.ID)
	if err != nil {
		httpx.Internal(c)
		return
	}

	items := full.Items
	if items == nil {
		items = []model.CollectionItem{}
	}
	// Newest decision first: the heart is an opinion that moves, and the
	// list should lead with what the user just said.
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}

	httpx.OK(c, gin.H{
		"id":    full.ID,
		"kind":  full.Kind,
		"items": items,
		"total": len(items),
	})
}

type favouriteRequest struct {
	CatalogID       *uuid.UUID `json:"catalog_id,omitempty"`
	Title           string     `json:"title,omitempty"`
	Subtitle        *string    `json:"subtitle,omitempty"`
	Caption         *string    `json:"caption,omitempty"`
	ArtworkURL      *string    `json:"artwork_url,omitempty"`
	Provider        string     `json:"provider,omitempty"`
	ProviderKey     string     `json:"provider_key,omitempty"`
	DurationSeconds int        `json:"duration_seconds,omitempty"`

	// Kind says what is being favourited. Empty means a track, which is what
	// every client wrote before albums and playlists could be hearts.
	Kind string `json:"kind,omitempty"`
}

// setFavourite puts one thing into the account's favourites — a track, an
// album or a playlist. It is idempotent: the same catalog record twice is one
// entry, so a client may re-send on every sync without growing the list.
func (i *instance) setFavourite(c *gin.Context) {
	coll, ok := i.favouritesOf(c)
	if !ok {
		return
	}

	var req favouriteRequest
	if !httpx.BindJSON(c, &req) {
		return
	}

	entry := store.ReplacementItem{
		CatalogID:       req.CatalogID,
		Title:           req.Title,
		Subtitle:        req.Subtitle,
		Caption:         req.Caption,
		ArtworkURL:      req.ArtworkURL,
		Provider:        req.Provider,
		ProviderKey:     req.ProviderKey,
		DurationSeconds: req.DurationSeconds,
		Kind:            req.Kind,
	}
	if req.Kind != "" && !model.FavouriteKinds[req.Kind] {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "unknown favourite kind "+req.Kind)
		return
	}
	if entry.CatalogID == nil && strings.TrimSpace(entry.Title) == "" {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "catalog_id or title is required")
		return
	}

	item, created, err := i.core.Store.AddFavourite(c.Request.Context(), coll.ID, coll.Domain, entry)
	if err != nil {
		httpx.Internal(c)
		return
	}
	if created {
		httpx.Created(c, item)
		return
	}
	httpx.OK(c, item)
}

func (i *instance) unsetFavourite(c *gin.Context) {
	coll, ok := i.favouritesOf(c)
	if !ok {
		return
	}

	catalogID, err := uuid.Parse(c.Param("catalogId"))
	if err != nil {
		httpx.BadRequest(c, "INVALID_ID", "invalid catalog id")
		return
	}

	if err := i.core.Store.RemoveFavourite(c.Request.Context(), coll.ID, catalogID); err != nil {
		httpx.NotFound(c, "favourite not found")
		return
	}

	httpx.NoContent(c)
}

// ownerOf resolves the caller's account id, answering 401 itself when the
// session carries none.
func ownerOf(c *gin.Context, i *instance) (uuid.UUID, bool) {
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return uuid.Nil, false
	}
	return accountID, true
}
