package statusctl

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/permission"
	"src.solsynth.dev/sosys/stargate/store"
)

// registerActivities mounts /api/activities — Passport's
// PresenceActivityController, plus the session start/end actions and the
// catalog/statistics reads.
func registerActivities(api *gin.RouterGroup, d Deps) {
	r := api.Group("/activities")
	r.Use(middleware.RequireAuth())
	// Literal paths are registered before the catch-all :identifier probe so
	// gin's router resolves them (it would otherwise reject the conflict).
	r.GET("", d.listActivities)
	r.GET("/catalog", d.listCatalog)
	r.GET("/stats", d.catalogStats)
	r.GET("/tags/stats", d.tagStats)
	r.POST("/sessions/start", d.startSession)
	r.POST("/sessions/end", d.endSession)
	r.POST("", d.setActivity)
	r.PUT("/:id", middleware.AskPermission(d.Perm, permission.PresencesActivityManage), d.updateActivity)
	r.DELETE("", middleware.AskPermission(d.Perm, permission.PresencesActivityManage), d.deleteActivity)
	r.GET("/:identifier", d.activitiesByAccount)
}

// setActivityRequest mirrors PresenceActivityController.SetActivityRequest.
type setActivityRequest struct {
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

// startSessionRequest mirrors StartSessionRequest.
type startSessionRequest struct {
	Type           *string             `json:"type"`
	ManualId       *string             `json:"manual_id"`
	Provider       *string             `json:"provider"`
	ReferenceId    *string             `json:"reference_id"`
	Title          *string             `json:"title"`
	Subtitle       *string             `json:"subtitle"`
	Caption        *string             `json:"caption"`
	LargeImage     *string             `json:"large_image"`
	SmallImage     *string             `json:"small_image"`
	TitleUrl       *string             `json:"title_url"`
	SubtitleUrl    *string             `json:"subtitle_url"`
	QueryableTerms []string            `json:"queryable_terms"`
	Meta           map[string]any      `json:"meta"`
	Tags           []model.PresenceTag `json:"tags"`
	Visibility     *int                `json:"visibility"`
	CatalogKey     *string             `json:"catalog_key"`
	CatalogName    *string             `json:"catalog_name"`
	LeaseMinutes   *int                `json:"lease_minutes"`
}

// endSessionRequest mirrors EndSessionRequest.
type endSessionRequest struct {
	ManualId *string `json:"manual_id"`
}

// Lease bounds from SetActivity / StartActivitySession; C# defaults the
// request's LeaseMinutes property to 5.
const (
	minLeaseMinutes     = 1
	maxLeaseMinutes     = 60
	defaultLeaseMinutes = 5
)

// listActivities serves GET /api/activities.
//
// Without includeExpired and without any filter it returns only the live
// activities; with either it pages through the filtered query and reports the
// unpaged count in X-Total.
func (d Deps) listActivities(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := currentAccountID(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	ctx := c.Request.Context()

	includeExpired, ok := parseBool(c, "includeExpired", "include_expired")
	if !ok {
		return
	}
	// C# defaults: offset 0, take 20 for the activity list.
	offset, take, ok := parseOffsetTake(c, 20)
	if !ok {
		return
	}
	query, _ := queryAny(c, "query")
	typeFilter, _ := queryAny(c, "type")
	provider, _ := queryAny(c, "provider")
	referenceID, _ := queryAny(c, "referenceId", "reference_id")
	term, _ := queryAny(c, "term")
	tags := queryValues(c, "tags")
	catalogID, _ := queryAny(c, "catalogId", "catalog_id")
	from, ok := parseOptionalTime(c, "from")
	if !ok {
		return
	}
	to, ok := parseOptionalTime(c, "to")
	if !ok {
		return
	}
	visibility, ok := parseOptionalInt(c, "visibility")
	if !ok {
		return
	}

	hasFilters := strings.TrimSpace(query) != "" ||
		strings.TrimSpace(typeFilter) != "" ||
		strings.TrimSpace(provider) != "" ||
		strings.TrimSpace(referenceID) != "" ||
		strings.TrimSpace(term) != "" ||
		len(tags) > 0 ||
		visibility != nil ||
		strings.TrimSpace(catalogID) != "" ||
		from != nil ||
		to != nil

	if !includeExpired && !hasFilters {
		activities, err := d.Store.GetActiveActivities(ctx, accountID)
		if err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, activities)
		return
	}

	filter := store.ActivityFilter{
		Offset:      offset,
		Take:        take,
		Query:       nullableTrimmed(query),
		Type:        nullableTrimmed(typeFilter),
		Provider:    nullableTrimmed(provider),
		ReferenceID: nullableTrimmed(referenceID),
		Term:        nullableTrimmed(term),
		Tags:        tags,
		From:        from,
		To:          to,
	}
	if !includeExpired {
		active := true
		filter.IsActive = &active
	}
	if visibility != nil {
		value := model.PresenceVisibility(*visibility)
		filter.Visibility = &value
	}
	if strings.TrimSpace(catalogID) != "" {
		// The C# returns an empty page (and X-Total: 0) for an unparsable
		// catalog id instead of a validation error.
		parsed, err := uuid.Parse(strings.TrimSpace(catalogID))
		if err != nil {
			c.Header("X-Total", "0")
			c.JSON(http.StatusOK, []model.PresenceActivity{})
			return
		}
		filter.CatalogID = &parsed
	}

	activities, total, err := d.Store.ListActivities(ctx, accountID, filter)
	if err != nil {
		internalError(c, err)
		return
	}
	c.Header("X-Total", strconv.Itoa(total))
	c.JSON(http.StatusOK, activities)
}

// activitiesByAccount serves GET /api/activities/:identifier for a GUID or a
// name; unlike the public status read, an unknown account is a 404 here.
func (d Deps) activitiesByAccount(c *gin.Context) {
	identifier := c.Param("identifier")
	ctx := c.Request.Context()
	account, err := lookupAccount(ctx, d.Accounts, identifier)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, errs.New("PASSPORT_ACCOUNT_NOT_FOUND", "Account not found.", http.StatusNotFound))
			return
		}
		internalError(c, err)
		return
	}
	accountID, err := uuid.Parse(account.Id)
	if err != nil {
		internalError(c, err)
		return
	}
	activities, err := d.Store.GetActiveActivities(ctx, accountID)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, activities)
}

// setActivity serves POST /api/activities: update a live activity with the same
// manual id in place, otherwise create a new one.
func (d Deps) setActivity(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := currentAccountID(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	var req setActivityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"request": {"The request body is invalid."}}))
		return
	}
	ctx := c.Request.Context()

	if manualID := strings.TrimSpace(derefString(req.ManualId)); manualID != "" {
		existing, err := d.Store.GetActivityByManualID(ctx, accountID, manualID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			internalError(c, err)
			return
		}
		if existing != nil {
			// The C# update path validates the lease only once the row is
			// found: an unknown manual id falls through to create.
			lease, ok := leaseMinutes(c, req.LeaseMinutes)
			if !ok {
				return
			}
			applySetActivity(existing, &req)
			existing.LeaseMinutes = lease
			existing.LeaseExpiresAt = model.NewTime(time.Now().UTC().Add(time.Duration(lease) * time.Minute))
			if err := d.Store.UpdateActivity(ctx, existing); err != nil {
				internalError(c, err)
				return
			}
			d.broadcastActivities(ctx, accountID)
			c.JSON(http.StatusOK, existing)
			return
		}
	}

	if strings.TrimSpace(derefString(req.Type)) == "" {
		c.JSON(http.StatusBadRequest, errs.New("PASSPORT_ACTIVITY_TYPE_REQUIRED", "Type is required when creating a new activity.", http.StatusBadRequest))
		return
	}
	lease, ok := leaseMinutes(c, req.LeaseMinutes)
	if !ok {
		return
	}
	activity := &model.PresenceActivity{
		AccountId:      accountID.String(),
		Type:           trimOrNil(req.Type),
		Provider:       normalizeProvider(req.Provider),
		ReferenceId:    normalizeNullableField(req.ReferenceId),
		ManualId:       req.ManualId,
		Title:          req.Title,
		Subtitle:       req.Subtitle,
		Caption:        req.Caption,
		LargeImage:     req.LargeImage,
		SmallImage:     req.SmallImage,
		TitleUrl:       req.TitleUrl,
		SubtitleUrl:    req.SubtitleUrl,
		QueryableTerms: normalizeQueryableTerms(req.QueryableTerms),
		Meta:           req.Meta,
		Tags:           []model.PresenceTag{},
		LeaseMinutes:   lease,
		LeaseExpiresAt: model.NewTime(time.Now().UTC().Add(time.Duration(lease) * time.Minute)),
	}

	if err := d.Store.InsertActivity(ctx, activity); err != nil {
		internalError(c, err)
		return
	}
	d.broadcastActivities(ctx, accountID)
	c.JSON(http.StatusOK, activity)
}

// updateActivity serves PUT /api/activities/:id: only supplied fields change.
func (d Deps) updateActivity(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := currentAccountID(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	activityID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		// The C# route constraint ({id:guid}) never matches, so ASP.NET
		// answers 404 rather than a model-state 400.
		c.Status(http.StatusNotFound)
		return
	}
	var req setActivityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"request": {"The request body is invalid."}}))
		return
	}
	ctx := c.Request.Context()

	activity, err := d.Store.GetActivityByID(ctx, accountID, activityID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		internalError(c, err)
		return
	}
	if activity == nil {
		c.JSON(http.StatusNotFound, errs.New("PASSPORT_ACTIVITY_NOT_FOUND", "Activity not found.", http.StatusNotFound))
		return
	}
	if req.LeaseMinutes != nil {
		if !validateLease(c, *req.LeaseMinutes) {
			return
		}
	}

	if strings.TrimSpace(derefString(req.Type)) != "" {
		activity.Type = trimOrNil(req.Type)
	}
	if req.Provider != nil {
		activity.Provider = normalizeProvider(req.Provider)
	}
	if req.ReferenceId != nil {
		activity.ReferenceId = normalizeNullableField(req.ReferenceId)
	}
	if req.Title != nil {
		activity.Title = req.Title
	}
	if req.Subtitle != nil {
		activity.Subtitle = req.Subtitle
	}
	if req.Caption != nil {
		activity.Caption = req.Caption
	}
	if req.ManualId != nil {
		activity.ManualId = req.ManualId
	}
	if req.LargeImage != nil {
		activity.LargeImage = req.LargeImage
	}
	if req.SmallImage != nil {
		activity.SmallImage = req.SmallImage
	}
	if req.TitleUrl != nil {
		activity.TitleUrl = req.TitleUrl
	}
	if req.SubtitleUrl != nil {
		activity.SubtitleUrl = req.SubtitleUrl
	}
	if req.QueryableTerms != nil {
		activity.QueryableTerms = normalizeQueryableTerms(req.QueryableTerms)
	}
	if req.Meta != nil {
		activity.Meta = req.Meta
	}
	applyLease(activity, req.LeaseMinutes)

	if err := d.Store.UpdateActivity(ctx, activity); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, errs.New("PASSPORT_ACTIVITY_NOT_FOUND", "Activity not found.", http.StatusNotFound))
			return
		}
		internalError(c, err)
		return
	}
	d.broadcastActivities(ctx, accountID)
	c.JSON(http.StatusOK, activity)
}

// deleteActivity serves DELETE /api/activities?id=|manualId=.
func (d Deps) deleteActivity(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := currentAccountID(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	ctx := c.Request.Context()
	manualID, _ := queryAny(c, "manualId", "manual_id")

	if strings.TrimSpace(manualID) != "" {
		activity, err := d.Store.GetActivityByManualID(ctx, accountID, strings.TrimSpace(manualID))
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			internalError(c, err)
			return
		}
		if activity == nil {
			c.JSON(http.StatusNotFound, errs.New("PASSPORT_ACTIVITY_NOT_FOUND", "Activity not found.", http.StatusNotFound))
			return
		}
		activityID, parseErr := uuid.Parse(activity.Id)
		if parseErr != nil {
			internalError(c, parseErr)
			return
		}
		if err := d.Store.DeleteActivity(ctx, accountID, activityID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				c.JSON(http.StatusNotFound, errs.New("PASSPORT_ACTIVITY_NOT_FOUND", "Activity not found.", http.StatusNotFound))
				return
			}
			internalError(c, err)
			return
		}
		d.broadcastActivities(ctx, accountID)
		c.Status(http.StatusNoContent)
		return
	}

	id, _ := queryAny(c, "id")
	activityID, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("PASSPORT_ACTIVITY_ID_REQUIRED", "Either 'id' (GUID) or 'manualId' must be provided.", http.StatusBadRequest))
		return
	}
	if err := d.Store.DeleteActivity(ctx, accountID, activityID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, errs.New("PASSPORT_ACTIVITY_NOT_FOUND", "Activity not found.", http.StatusNotFound))
			return
		}
		internalError(c, err)
		return
	}
	d.broadcastActivities(ctx, accountID)
	c.Status(http.StatusNoContent)
}

// startSession serves POST /api/activities/sessions/start: re-starting the same
// manual id refreshes the open session instead of creating a second row, and a
// catalog key upserts (get-or-create) the catalog item the session accumulates
// into.
func (d Deps) startSession(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := currentAccountID(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	var req startSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"request": {"The request body is invalid."}}))
		return
	}
	ctx := c.Request.Context()

	manualID := strings.TrimSpace(derefString(req.ManualId))
	if manualID == "" {
		c.JSON(http.StatusBadRequest, errs.New("PASSPORT_ACTIVITY_MANUAL_ID_REQUIRED", "ManualId is required.", http.StatusBadRequest))
		return
	}
	if strings.TrimSpace(derefString(req.Type)) == "" {
		c.JSON(http.StatusBadRequest, errs.New("PASSPORT_ACTIVITY_TYPE_REQUIRED", "Type is required when starting a session.", http.StatusBadRequest))
		return
	}
	lease, ok := leaseMinutes(c, req.LeaseMinutes)
	if !ok {
		return
	}

	visibility := model.PresenceVisibilityPublic
	if req.Visibility != nil {
		visibility = model.PresenceVisibility(*req.Visibility)
	}
	terms := normalizeQueryableTerms(req.QueryableTerms)
	tags := normalizeTagList(req.Tags)

	// Catalog resolution happens only for a non-blank key. An existing item is
	// reused untouched; a missing one is created from this session's metadata.
	var catalog *model.PresenceCatalogItem
	if key := strings.TrimSpace(derefString(req.CatalogKey)); key != "" {
		provider := normalizeProvider(req.Provider)
		existing, err := d.Store.GetCatalogItemByKey(ctx, accountID, provider, key)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			internalError(c, err)
			return
		}
		if existing != nil {
			catalog = existing
		} else {
			name := req.CatalogName
			if strings.TrimSpace(derefString(name)) == "" {
				name = req.Title
			}
			catalogKey := key
			candidate := &model.PresenceCatalogItem{
				AccountId:      accountID.String(),
				Provider:       provider,
				CatalogKey:     &catalogKey,
				ReferenceId:    normalizeNullableField(req.ReferenceId),
				Name:           name,
				Subtitle:       req.Subtitle,
				Caption:        req.Caption,
				Visibility:     visibility,
				Tags:           tags,
				LargeImage:     req.LargeImage,
				SmallImage:     req.SmallImage,
				TitleUrl:       req.TitleUrl,
				SubtitleUrl:    req.SubtitleUrl,
				QueryableTerms: terms,
				Meta:           req.Meta,
			}
			if candidate.Meta == nil {
				candidate.Meta = map[string]any{}
			}
			saved, err := d.Store.UpsertCatalogItem(ctx, candidate)
			if err != nil {
				internalError(c, err)
				return
			}
			catalog = saved
		}
	}

	activity, err := d.Store.GetActiveSessionByManualID(ctx, accountID, manualID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		internalError(c, err)
		return
	}
	if activity != nil {
		// Refresh the open session in place: same row, new metadata and lease.
		activity.Type = trimOrNil(req.Type)
		activity.Provider = normalizeProvider(req.Provider)
		activity.ReferenceId = normalizeNullableField(req.ReferenceId)
		activity.Title = req.Title
		activity.Subtitle = req.Subtitle
		activity.Caption = req.Caption
		activity.LargeImage = req.LargeImage
		activity.SmallImage = req.SmallImage
		activity.TitleUrl = req.TitleUrl
		activity.SubtitleUrl = req.SubtitleUrl
		activity.QueryableTerms = terms
		activity.Meta = req.Meta
		activity.Visibility = visibility
		activity.Tags = tags
		if catalog != nil {
			catalogID := catalog.Id
			activity.CatalogId = &catalogID
			activity.Catalog = catalog
		}
		// A refreshed session always renews its lease (C# rewrites
		// LeaseMinutes/LeaseExpiresAt on every start).
		activity.LeaseMinutes = lease
		activity.LeaseExpiresAt = model.NewTime(time.Now().UTC().Add(time.Duration(lease) * time.Minute))
		if err := d.Store.UpdateActivity(ctx, activity); err != nil {
			internalError(c, err)
			return
		}
		d.broadcastActivities(ctx, accountID)
		c.JSON(http.StatusOK, activity)
		return
	}

	now := time.Now().UTC()
	created := &model.PresenceActivity{
		AccountId:      accountID.String(),
		ManualId:       &manualID,
		Type:           trimOrNil(req.Type),
		Tags:           tags,
		Visibility:     visibility,
		Provider:       normalizeProvider(req.Provider),
		ReferenceId:    normalizeNullableField(req.ReferenceId),
		Title:          req.Title,
		Subtitle:       req.Subtitle,
		Caption:        req.Caption,
		LargeImage:     req.LargeImage,
		SmallImage:     req.SmallImage,
		TitleUrl:       req.TitleUrl,
		SubtitleUrl:    req.SubtitleUrl,
		QueryableTerms: terms,
		Meta:           req.Meta,
		StartedAt:      model.NewTime(now),
	}
	created.LeaseMinutes = lease
	created.LeaseExpiresAt = model.NewTime(now.Add(time.Duration(lease) * time.Minute))
	if catalog != nil {
		catalogID := catalog.Id
		created.CatalogId = &catalogID
		created.Catalog = catalog
	}

	if err := d.Store.InsertActivity(ctx, created); err != nil {
		internalError(c, err)
		return
	}
	d.broadcastActivities(ctx, accountID)
	c.JSON(http.StatusOK, created)
}

// endSession serves POST /api/activities/sessions/end: idempotent, 404 when
// there is no open session.
func (d Deps) endSession(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := currentAccountID(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	var req endSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"request": {"The request body is invalid."}}))
		return
	}
	manualID := strings.TrimSpace(derefString(req.ManualId))
	if manualID == "" {
		c.JSON(http.StatusBadRequest, errs.New("PASSPORT_ACTIVITY_MANUAL_ID_REQUIRED", "ManualId is required.", http.StatusBadRequest))
		return
	}
	ctx := c.Request.Context()

	activity, err := d.Store.EndActivitySession(ctx, accountID, manualID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		internalError(c, err)
		return
	}
	if activity == nil {
		c.JSON(http.StatusNotFound, errs.New("PASSPORT_ACTIVITY_NOT_FOUND", "Activity not found.", http.StatusNotFound))
		return
	}
	d.broadcastActivities(ctx, accountID)
	c.JSON(http.StatusOK, activity)
}

// listCatalog serves GET /api/activities/catalog.
func (d Deps) listCatalog(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := currentAccountID(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	tags := queryValues(c, "tags")
	provider, _ := queryAny(c, "provider")
	query, _ := queryAny(c, "query")
	isActive, ok := parseOptionalBool(c, "isActive", "is_active")
	if !ok {
		return
	}
	// C# defaults: offset 0, take 50 for the catalog list.
	offsetValue, takeValue, ok := parseOffsetTake(c, 50)
	if !ok {
		return
	}
	items, err := d.Store.GetCatalogItems(c.Request.Context(), accountID, tags, provider, query, isActive, offsetValue, takeValue)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, items)
}

// catalogStats serves GET /api/activities/stats.
func (d Deps) catalogStats(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := currentAccountID(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	from, ok := parseOptionalTime(c, "from")
	if !ok {
		return
	}
	to, ok := parseOptionalTime(c, "to")
	if !ok {
		return
	}
	stats, err := d.Store.GetCatalogStats(c.Request.Context(), accountID, from, to, queryValues(c, "tags"))
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, stats)
}

// tagStats serves GET /api/activities/tags/stats.
func (d Deps) tagStats(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := currentAccountID(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	from, ok := parseOptionalTime(c, "from")
	if !ok {
		return
	}
	to, ok := parseOptionalTime(c, "to")
	if !ok {
		return
	}
	stats, err := d.Store.GetTagStats(c.Request.Context(), accountID, from, to)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, stats)
}

// ─────────────────────────── request helpers ───────────────────────────

// applySetActivity copies the POST body onto an existing live activity. The
// POST semantics are unconditional: provider/reference/terms are normalized
// even when absent (C# SetActivity), while the type is only replaced when
// supplied.
func applySetActivity(existing *model.PresenceActivity, req *setActivityRequest) {
	if strings.TrimSpace(derefString(req.Type)) != "" {
		existing.Type = trimOrNil(req.Type)
	}
	existing.Provider = normalizeProvider(req.Provider)
	existing.ReferenceId = normalizeNullableField(req.ReferenceId)
	existing.Title = req.Title
	existing.Subtitle = req.Subtitle
	existing.Caption = req.Caption
	existing.LargeImage = req.LargeImage
	existing.SmallImage = req.SmallImage
	existing.TitleUrl = req.TitleUrl
	existing.SubtitleUrl = req.SubtitleUrl
	existing.QueryableTerms = normalizeQueryableTerms(req.QueryableTerms)
	existing.Meta = req.Meta
}

// applyLease stamps an already validated lease length onto an activity.
func applyLease(activity *model.PresenceActivity, minutes *int) {
	if minutes == nil {
		return
	}
	activity.LeaseMinutes = *minutes
	activity.LeaseExpiresAt = model.NewTime(time.Now().UTC().Add(time.Duration(*minutes) * time.Minute))
}

// leaseMinutes applies the C# default (5 minutes) to an optional lease and
// rejects values outside 1..60.
func leaseMinutes(c *gin.Context, minutes *int) (int, bool) {
	if minutes == nil {
		return defaultLeaseMinutes, true
	}
	if !validateLease(c, *minutes) {
		return 0, false
	}
	return *minutes, true
}

// validateLease enforces the 1..60 bound. The C# throws
// ArgumentException("Lease minutes must be between 1 and 60") from the service,
// which reaches the client as a 400 PROBLEM_DETAILS response.
func validateLease(c *gin.Context, minutes int) bool {
	if minutes >= minLeaseMinutes && minutes <= maxLeaseMinutes {
		return true
	}
	c.JSON(http.StatusBadRequest, &errs.ApiError{
		Code:    "VALIDATION_ERROR",
		Message: "Lease minutes must be between 1 and 60",
		Status:  http.StatusBadRequest,
	})
	return false
}

func normalizeTagList(tags []model.PresenceTag) []model.PresenceTag {
	out := make([]model.PresenceTag, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		slug := strings.TrimSpace(tag.Slug)
		if slug == "" {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		out = append(out, model.PresenceTag{Slug: slug, Name: tag.Name})
	}
	return out
}

func nullableTrimmed(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// broadcastActivities publishes the activity list change (C#
// BroadcastPresenceActivitiesUpdated).
func (d Deps) broadcastActivities(ctx context.Context, accountID uuid.UUID) {
	if d.Broadcast == nil {
		return
	}
	if err := d.Broadcast.ActivitiesChanged(ctx, accountID); err != nil {
		if d.Log != nil {
			d.Log.Warn("broadcast presence activities", "account", accountID, "error", err)
		}
	}
}
