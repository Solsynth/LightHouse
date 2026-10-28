package timeline

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"src.solsynth.dev/sosys/go/pkg/errs"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
)

// requireAuth mirrors [Authorize]: it rejects anonymous requests with the
// canonical 401 ApiError.
func requireAuth() gin.HandlerFunc { return authn.RequireAuth() }

// requirePermission mirrors [AskPermission]. A process without the auth service
// fails closed instead of granting.
func requirePermission(host *module.Host[core.Services], key string) gin.HandlerFunc {
	if host.Core != nil && host.Core.Auth != nil {
		return host.Core.Auth.RequirePermission(key)
	}
	return func(c *gin.Context) {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable,
			errs.New("SERVICE_UNAVAILABLE", "The permission service is unavailable.", http.StatusServiceUnavailable))
	}
}

// handleListEvents serves GET /api/timeline.
func (s *service) handleListEvents(c *gin.Context) {
	ctx := c.Request.Context()

	var cursor *time.Time
	if raw := strings.TrimSpace(c.Query("cursor")); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			httpx.BadRequest(c, "TIMELINE_INVALID_CURSOR", "Invalid cursor format")
			return
		}
		value := parsed.UTC()
		cursor = &value
	}

	mode, ok := parseTimelineMode(c.Query("mode"))
	if !ok {
		httpx.BadRequest(c, "TIMELINE_INVALID_MODE", "Invalid mode. Expected personalized, top, or latest.")
		return
	}

	take := 20
	if raw := strings.TrimSpace(c.Query("take")); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			take = value
		}
	}
	aggressive := true
	if raw := strings.TrimSpace(c.Query("aggressive")); raw != "" {
		if value, err := strconv.ParseBool(raw); err == nil {
			aggressive = value
		}
	}

	scope, err := s.resolveFeedScope(ctx, c.Query("pub"), c.Query("collection"))
	if err != nil {
		httpx.BadRequest(c, "TIMELINE_INVALID_SCOPE", err.Error())
		return
	}
	if scope.requestedPublisherName != nil && scope.publisherID == nil {
		httpx.Fail(c, errs.New("PUBLISHER_NOT_FOUND", "Publisher not found.", http.StatusNotFound))
		return
	}
	if scope.requestedCollectionSlug != nil && scope.collectionID == nil {
		httpx.Fail(c, errs.New("COLLECTION_NOT_FOUND", "Collection not found.", http.StatusNotFound))
		return
	}

	user := authn.CurrentUser(c)
	var page any
	if user == nil {
		result, err := s.listEventsForAnyone(ctx, take, cursor, mode, scope)
		if err != nil {
			s.log.Error("timeline: anonymous feed", "error", err)
			httpx.Internal(c)
			return
		}
		page = result
	} else {
		result, err := s.listEvents(ctx, take, cursor, user, mode, c.Query("filter"), scope, aggressive)
		if err != nil {
			s.log.Error("timeline: feed", "error", err)
			httpx.Internal(c)
			return
		}
		page = result
	}
	httpx.OK(c, page)
}

// handleDiscoveryProfile serves GET /api/timeline/discovery/profile.
func (s *service) handleDiscoveryProfile(c *gin.Context) {
	user := authn.CurrentUser(c)
	if user == nil {
		httpx.Unauthorized(c)
		return
	}
	profile, err := s.discoveryProfile(c.Request.Context(), user)
	if err != nil {
		s.log.Error("timeline: discovery profile", "error", err)
		httpx.Internal(c)
		return
	}
	httpx.OK(c, profile)
}

// handleMarkUninterested serves POST /api/timeline/discovery/uninterested.
func (s *service) handleMarkUninterested(c *gin.Context) {
	user := authn.CurrentUser(c)
	if user == nil {
		httpx.Unauthorized(c)
		return
	}
	var request discoveryPreferenceRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	kind, ok := parseDiscoveryTargetKind(request.Kind)
	if !ok {
		httpx.BadRequest(c, "DISCOVERY_INVALID_KIND", "Invalid kind. Expected publisher, realm, or account.")
		return
	}
	referenceID, ok := parseBodyUUID(c, request.ReferenceID, "referenceId")
	if !ok {
		return
	}
	accountID, ok := accountUUID(c, user.GetId())
	if !ok {
		return
	}
	preference, err := s.markDiscoveryPreference(c.Request.Context(), accountID, kind, referenceID, request.Reason)
	if err != nil {
		s.log.Error("timeline: mark uninterested", "error", err)
		httpx.Internal(c)
		return
	}
	httpx.OK(c, preference)
}

// handleRemoveUninterested serves DELETE /api/timeline/discovery/uninterested.
func (s *service) handleRemoveUninterested(c *gin.Context) {
	user := authn.CurrentUser(c)
	if user == nil {
		httpx.Unauthorized(c)
		return
	}
	kind, ok := parseDiscoveryTargetKind(c.Query("kind"))
	if !ok {
		httpx.BadRequest(c, "DISCOVERY_INVALID_KIND", "Invalid kind. Expected publisher, realm, or account.")
		return
	}
	referenceID, ok := parseQueryUUID(c, c.Query("referenceId"), "referenceId")
	if !ok {
		return
	}
	accountID, ok := accountUUID(c, user.GetId())
	if !ok {
		return
	}
	removed, err := s.removeDiscoveryPreference(c.Request.Context(), accountID, kind, referenceID)
	if err != nil {
		s.log.Error("timeline: remove uninterested", "error", err)
		httpx.Internal(c)
		return
	}
	if !removed {
		c.JSON(http.StatusNotFound, errs.NotFound(""))
		return
	}
	httpx.OKEmpty(c)
}

// handleFeedback serves POST /api/timeline/discovery/feedback.
func (s *service) handleFeedback(c *gin.Context) {
	user := authn.CurrentUser(c)
	if user == nil {
		httpx.Unauthorized(c)
		return
	}
	var request recommendationFeedbackRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	feedback, ok := parseRecommendationFeedback(request.Feedback)
	if !ok {
		httpx.BadRequest(c, "DISCOVERY_INVALID_FEEDBACK", "Invalid feedback. Expected positive or negative.")
		return
	}
	if !isSupportedFeedbackKind(request.Kind) {
		httpx.BadRequest(c, "DISCOVERY_INVALID_KIND", "Invalid kind. Expected post, publisher, collection, tag, or category.")
		return
	}
	referenceID, ok := parseBodyUUID(c, request.ReferenceID, "referenceId")
	if !ok {
		return
	}
	accountID, ok := accountUUID(c, user.GetId())
	if !ok {
		return
	}
	result, err := s.applyRecommendationFeedback(
		c.Request.Context(), accountID, request.Kind, referenceID,
		feedback, request.Reason, request.Suppress,
	)
	if err != nil {
		s.log.Error("timeline: feedback", "error", err)
		httpx.Internal(c)
		return
	}
	if result == nil {
		httpx.Fail(c, errs.New("DISCOVERY_CONTENT_NOT_FOUND", "Referenced content was not found.", http.StatusNotFound))
		return
	}
	httpx.OK(c, result)
}

// handleAdjustWeight serves PUT /api/timeline/discovery/weights.
func (s *service) handleAdjustWeight(c *gin.Context) {
	user := authn.CurrentUser(c)
	if user == nil {
		httpx.Unauthorized(c)
		return
	}
	var request recommendationWeightChangeRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	kind, ok := parsePostInterestKind(request.Kind)
	if !ok {
		httpx.BadRequest(c, "DISCOVERY_INVALID_KIND", "Invalid kind. Expected publisher, collection, tag, or category.")
		return
	}
	referenceID, ok := parseBodyUUID(c, request.ReferenceID, "referenceId")
	if !ok {
		return
	}
	accountID, ok := accountUUID(c, user.GetId())
	if !ok {
		return
	}
	profile, err := s.adjustRecommendationWeight(
		c.Request.Context(), accountID, kind, referenceID,
		request.ScoreDelta, request.InteractionCount, request.SignalType,
	)
	if err != nil {
		s.log.Error("timeline: adjust weight", "error", err)
		httpx.Internal(c)
		return
	}
	httpx.OK(c, profile)
}

// handleResetInterests serves POST /api/timeline/discovery/reset.
func (s *service) handleResetInterests(c *gin.Context) {
	user := authn.CurrentUser(c)
	if user == nil {
		httpx.Unauthorized(c)
		return
	}
	accountID, ok := accountUUID(c, user.GetId())
	if !ok {
		return
	}
	count, err := s.resetInterestProfile(c.Request.Context(), accountID)
	if err != nil {
		s.log.Error("timeline: reset interests", "error", err)
		httpx.Internal(c)
		return
	}
	httpx.OK(c, count)
}

// accountUUID parses the authenticated account ID.
func accountUUID(c *gin.Context, raw string) (uuid.UUID, bool) {
	id, err := uuid.Parse(raw)
	if err != nil {
		httpx.Internal(c)
		return uuid.Nil, false
	}
	return id, true
}

// parseBodyUUID parses a UUID from a JSON body field. A missing value keeps the
// C# model's Guid.Empty default; a malformed value is the model binding
// failure the C# binder produces.
func parseBodyUUID(c *gin.Context, raw, field string) (uuid.UUID, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return uuid.Nil, true
	}
	id, err := uuid.Parse(trimmed)
	if err != nil {
		httpx.Fail(c, errs.Validation(map[string][]string{
			field: {"The JSON value could not be converted to System.Guid."},
		}))
		return uuid.Nil, false
	}
	return id, true
}

// parseQueryUUID parses a required query UUID, rejecting a missing value like
// the C# model binder does.
func parseQueryUUID(c *gin.Context, raw, field string) (uuid.UUID, bool) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		httpx.Fail(c, errs.Validation(map[string][]string{
			field: {"The value '" + raw + "' is not valid."},
		}))
		return uuid.Nil, false
	}
	return id, true
}

// isSupportedFeedbackKind mirrors the controller helper.
func isSupportedFeedbackKind(raw string) bool {
	switch normalizeKind(raw) {
	case "post", "publisher", "collection", "tag", "category":
		return true
	default:
		return false
	}
}
