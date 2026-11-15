package reviews

import (
	"context"

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
		ID:             "app.solian.pulsar.reviews",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Models:         []any{&model.Review{}},
		Capabilities:   []string{"reviews", "ratings"},
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	core *core.Services
}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	i.core = host.Core

	r := host.API.Group("/reviews")
	r.GET("", i.listReviews)
	r.GET("/stats", i.getStats)
	r.GET("/me", authn.RequireAuth(), i.getMyReview)
	r.PUT("", authn.RequireAuth(), i.setReview)
	r.DELETE("/:id", authn.RequireAuth(), i.deleteReview)

	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

// setReviewRequest is a comment, a rating, or both. Rating is a 1–10 scale,
// matching the collection item rating a client already knows.
type setReviewRequest struct {
	CatalogID uuid.UUID `json:"catalog_id" binding:"required"`
	Rating    *int      `json:"rating,omitempty"`
	Body      *string   `json:"body,omitempty"`
}

func (i *instance) setReview(c *gin.Context) {
	accountID, ok := ownerOf(c, i)
	if !ok {
		return
	}

	var req setReviewRequest
	if !httpx.BindJSON(c, &req) {
		return
	}

	if req.Rating == nil && req.Body == nil {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "a review needs a rating or a body")
		return
	}
	if req.Rating != nil && (*req.Rating < 1 || *req.Rating > 10) {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "rating must be between 1 and 10")
		return
	}
	if req.Body != nil && *req.Body == "" {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "review body cannot be empty")
		return
	}

	// The item must exist: a review of nothing has nothing to be listed under.
	if _, err := i.core.Store.GetCatalogItem(c.Request.Context(), req.CatalogID); err != nil {
		httpx.NotFound(c, "catalog item not found")
		return
	}

	// Upsert on the account+catalog key: the caller's copy is the truth, so a
	// re-send updates in place and the existing review's ID survives.
	created := true
	review := &model.Review{
		AccountID: accountID,
		CatalogID: req.CatalogID,
		Rating:    req.Rating,
		Body:      req.Body,
	}
	if existing, err := i.core.Store.GetCatalogReview(c.Request.Context(), accountID, req.CatalogID); err == nil {
		created = false
		review.ID = existing.ID
		review.CreatedAt = existing.CreatedAt
	} else {
		review.ID = uuid.New()
	}

	if err := i.core.Store.UpsertReview(c.Request.Context(), review); err != nil {
		httpx.Internal(c)
		return
	}

	if created {
		httpx.Created(c, review)
	} else {
		httpx.OK(c, review)
	}
}

func (i *instance) listReviews(c *gin.Context) {
	page, ok := httpx.PageQuery(c, i.core.Config.Security.MaxTake)
	if !ok {
		return
	}

	catalogID, err := uuid.Parse(c.Query("catalog_id"))
	if err != nil {
		httpx.BadRequest(c, "INVALID_ID", "a catalog_id query parameter is required")
		return
	}

	var accountFilter *uuid.UUID
	if raw := c.Query("account_id"); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			httpx.BadRequest(c, "INVALID_ID", "invalid account_id")
			return
		}
		accountFilter = &parsed
	}

	reviews, total, err := i.core.Store.ListCatalogReviews(c.Request.Context(), catalogID, accountFilter, page.Offset, page.Take)
	if err != nil {
		httpx.Internal(c)
		return
	}

	httpx.List(c, reviews, total)
}

func (i *instance) getStats(c *gin.Context) {
	catalogID, err := uuid.Parse(c.Query("catalog_id"))
	if err != nil {
		httpx.BadRequest(c, "INVALID_ID", "a catalog_id query parameter is required")
		return
	}

	stats, err := i.core.Store.CatalogReviewStats(c.Request.Context(), catalogID)
	if err != nil {
		httpx.Internal(c)
		return
	}

	httpx.OK(c, stats)
}

func (i *instance) getMyReview(c *gin.Context) {
	accountID, ok := ownerOf(c, i)
	if !ok {
		return
	}

	catalogID, err := uuid.Parse(c.Query("catalog_id"))
	if err != nil {
		httpx.BadRequest(c, "INVALID_ID", "a catalog_id query parameter is required")
		return
	}

	review, err := i.core.Store.GetCatalogReview(c.Request.Context(), accountID, catalogID)
	if err != nil {
		httpx.NotFound(c, "review not found")
		return
	}

	httpx.OK(c, review)
}

func (i *instance) deleteReview(c *gin.Context) {
	accountID, ok := ownerOf(c, i)
	if !ok {
		return
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.BadRequest(c, "INVALID_ID", "invalid review id")
		return
	}

	if err := i.core.Store.DeleteReview(c.Request.Context(), accountID, id); err != nil {
		httpx.NotFound(c, "review not found")
		return
	}

	httpx.NoContent(c)
}

// ownerOf resolves the caller's account id, answering 401 itself when the
// session carries none.
func ownerOf(c *gin.Context, i *instance) (uuid.UUID, bool) {
	raw := authn.AccountID(c)
	if raw == "" {
		httpx.Unauthorized(c)
		return uuid.Nil, false
	}
	accountID, err := uuid.Parse(raw)
	if err != nil {
		httpx.Unauthorized(c)
		return uuid.Nil, false
	}
	return accountID, true
}
