package fediverse

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"src.solsynth.dev/sosys/go/pkg/errs"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

type createQuoteAuthorizationRequest struct {
	InteractingObjectURI string `json:"interacting_object_uri"`
	InteractionTargetURI string `json:"interaction_target_uri"`
}

// handleQuoteGet serves the public FEP-044f QuoteAuthorization document.
func (s *server) handleQuoteGet(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	var authorization store.QuoteAuthorizationEntity
	if err := s.db.WithContext(c.Request.Context()).
		Where("id = ? AND is_valid = ?", id, true).
		First(&authorization).Error; err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	var publisher store.PublisherEntity
	_ = s.db.WithContext(c.Request.Context()).Where("id = ?", authorization.PublisherID).First(&publisher).Error

	writeActivityPub(c, map[string]any{
		"@context": []any{
			"https://www.w3.org/ns/activitystreams",
			map[string]any{
				"QuoteAuthorization": "https://w3id.org/fep/044f#QuoteAuthorization",
				"gts":                "https://gotosocial.org/ns#",
				"interactingObject": map[string]any{
					"@id":   "gts:interactingObject",
					"@type": "@id",
				},
				"interactionTarget": map[string]any{
					"@id":   "gts:interactionTarget",
					"@type": "@id",
				},
			},
		},
		"type":              "QuoteAuthorization",
		"id":                s.baseURL + "/quote-authorizations/" + authorization.ID.String(),
		"attributedTo":      deref(publisher.URI),
		"interactingObject": authorization.InteractingObjectURI,
		"interactionTarget": authorization.InteractionTargetURI,
	})
}

// federatedPublisherForAccount finds the publisher that represents an account
// on the fediverse. The C# controller compared the account id with the actor
// id directly; this falls back to the account's owned publisher so the
// endpoint stays usable when ids differ.
func (s *server) federatedPublisherForAccount(c *gin.Context) (*store.PublisherEntity, bool) {
	user := authn.CurrentUser(c)
	if user == nil {
		httpx.Unauthorized(c)
		return nil, false
	}
	accountID, err := uuid.Parse(user.GetId())
	if err != nil {
		httpx.Unauthorized(c)
		return nil, false
	}
	ctx := c.Request.Context()
	var publisher store.PublisherEntity
	if err := s.db.WithContext(ctx).Where("id = ? AND uri IS NOT NULL", accountID).First(&publisher).Error; err == nil {
		return &publisher, true
	}
	if err := s.db.WithContext(ctx).
		Where("account_id = ? AND uri IS NOT NULL", accountID).
		Order("created_at ASC").
		First(&publisher).Error; err == nil {
		return &publisher, true
	}
	httpx.Unauthorized(c)
	return nil, false
}

func (s *server) handleQuoteCreate(c *gin.Context) {
	actor, ok := s.federatedPublisherForAccount(c)
	if !ok {
		return
	}
	var request createQuoteAuthorizationRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	ctx := c.Request.Context()

	var existing store.QuoteAuthorizationEntity
	err := s.db.WithContext(ctx).
		Where("interaction_target_uri = ? AND interacting_object_uri = ? AND publisher_id = ? AND is_valid = ?",
			request.InteractionTargetURI, request.InteractingObjectURI, actor.ID, true).
		First(&existing).Error
	if err == nil {
		httpx.OK(c, gin.H{"id": existing.ID.String(), "existing": true})
		return
	}

	var targetPost store.PostEntity
	targetFound := s.db.WithContext(ctx).Where("fediverse_uri = ?", request.InteractionTargetURI).First(&targetPost).Error == nil
	var quotePost store.PostEntity
	quoteFound := s.db.WithContext(ctx).Where("fediverse_uri = ?", request.InteractingObjectURI).First(&quotePost).Error == nil

	authorization := &store.QuoteAuthorizationEntity{
		ID:                   uuid.New(),
		FediverseURI:         stringPtr(s.baseURL + "/quote-authorizations/" + uuid.NewString()),
		PublisherID:          actor.ID,
		InteractingObjectURI: request.InteractingObjectURI,
		InteractionTargetURI: request.InteractionTargetURI,
		IsValid:              true,
	}
	if targetFound {
		authorization.TargetPostID = &targetPost.ID
	}
	if quoteFound {
		authorization.QuotePostID = &quotePost.ID
	}
	if err := s.db.WithContext(ctx).Create(authorization).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, gin.H{"id": authorization.ID.String()})
}

func (s *server) handleQuoteRevoke(c *gin.Context) {
	actor, ok := s.federatedPublisherForAccount(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	var authorization store.QuoteAuthorizationEntity
	if err := s.db.WithContext(c.Request.Context()).
		Where("id = ? AND publisher_id = ?", id, actor.ID).
		First(&authorization).Error; err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	now := time.Now().UTC()
	authorization.IsValid = false
	authorization.RevokedAt = &now
	if err := s.db.WithContext(c.Request.Context()).Save(&authorization).Error; err != nil {
		httpx.Fail(c, errs.New("INTERNAL_ERROR", "An unexpected error occurred.", http.StatusInternalServerError))
		return
	}
	httpx.OKEmpty(c)
}
