package sessions

import (
	"context"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             "app.solian.pulsar.sessions",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Models:         []any{&model.ActiveSession{}},
		Capabilities:   []string{"sessions", "now_playing"},
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	core *core.Services
}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	i.core = host.Core

	// Clean direct API routes (no /v1)
	r := host.API.Group("/sessions")
	r.POST("/now-playing", authn.RequireAuth(), i.reportNowPlaying)
	r.POST("/pause", authn.RequireAuth(), i.reportPause)
	r.POST("/end", authn.RequireAuth(), i.endSession)
	r.GET("/active", authn.RequireAuth(), i.getActiveSessions)

	// User public now-playing endpoint
	host.API.GET("/users/:identifier/now-playing", i.getUserNowPlaying)

	// gRPC DyPresenceServiceServer
	if host.GRPC != nil {
		gen.RegisterDyPresenceServiceServer(host.GRPC, newPresenceServer(host.Core))
	}

	// Background job: periodic expired session cleanup
	if host.Core != nil && host.Core.Jobs != nil {
		host.Core.Jobs.Every("pulsar.sessions.cleanup", 5*time.Minute, func(ctx context.Context) error {
			_, err := i.core.Store.CleanupExpiredSessions(ctx, 15*time.Minute)
			return err
		})
	}

	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

type nowPlayingRequest struct {
	URN             string         `json:"urn,omitempty"`
	CatalogID       *uuid.UUID     `json:"catalog_id,omitempty"`
	ManualID        string         `json:"manual_id"`
	Domain          string         `json:"domain"`
	Title           string         `json:"title"`
	Subtitle        *string        `json:"subtitle,omitempty"`
	Caption         *string        `json:"caption,omitempty"`
	ArtworkURL      *string        `json:"artwork_url,omitempty"`
	LargeImage      *string        `json:"large_image,omitempty"`
	SmallImage      *string        `json:"small_image,omitempty"`
	ProgressSeconds int            `json:"progress_seconds"`
	DurationSeconds int            `json:"duration_seconds"`
	LeaseMinutes    *int           `json:"lease_minutes,omitempty"`
	Provider        *string        `json:"provider,omitempty"`
	ProviderKey     *string        `json:"provider_key,omitempty"`
	Meta            map[string]any `json:"meta,omitempty"`
}

func (i *instance) reportNowPlaying(c *gin.Context) {
	accountIDStr := authn.AccountID(c)
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		httpx.Unauthorized(c)
		return
	}

	var req nowPlayingRequest
	if !httpx.BindJSON(c, &req) {
		return
	}

	if strings.TrimSpace(req.ManualID) == "" {
		req.ManualID = "default"
	}
	if strings.TrimSpace(req.Title) == "" {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "title is required")
		return
	}
	domain := strings.TrimSpace(req.Domain)
	if domain == "" {
		domain = model.DomainMusic
	}

	leaseMin := 5
	if req.LeaseMinutes != nil && *req.LeaseMinutes > 0 && *req.LeaseMinutes <= 60 {
		leaseMin = *req.LeaseMinutes
	}

	// Resolve or upsert catalog item
	var catalogID *uuid.UUID
	if req.CatalogID != nil && *req.CatalogID != uuid.Nil {
		catalogID = req.CatalogID
	} else if strings.TrimSpace(req.URN) != "" {
		var match model.CatalogItem
		if err := i.core.DB.WithContext(c.Request.Context()).
			Where("urn = ? OR slug = ?", req.URN, req.URN).First(&match).Error; err == nil {
			catalogID = &match.ID
		}
	}

	artURL := req.ArtworkURL
	if artURL == nil {
		artURL = req.LargeImage
	}

	if catalogID == nil {
		catItem := &model.CatalogItem{
			Domain:          domain,
			Title:           strings.TrimSpace(req.Title),
			Subtitle:        req.Subtitle,
			Caption:         req.Caption,
			ArtworkURL:      artURL,
			DurationSeconds: req.DurationSeconds,
		}
		if req.Provider != nil {
			catItem.Provider = strings.TrimSpace(*req.Provider)
		}
		if req.ProviderKey != nil {
			catItem.ProviderKey = strings.TrimSpace(*req.ProviderKey)
		}

		savedCatalog, err := i.core.Store.FindOrCreateCatalogItem(c.Request.Context(), catItem)
		if err == nil && savedCatalog != nil {
			catalogID = &savedCatalog.ID
		}
	}

	session := &model.ActiveSession{
		AccountID:       accountID,
		CatalogID:       catalogID,
		Domain:          domain,
		ManualID:        req.ManualID,
		Title:           req.Title,
		Subtitle:        req.Subtitle,
		Caption:         req.Caption,
		ArtworkURL:      artURL,
		LargeImage:      req.LargeImage,
		SmallImage:      req.SmallImage,
		ProgressSeconds: req.ProgressSeconds,
		DurationSeconds: req.DurationSeconds,
		IsPaused:        false,
		LeaseMinutes:    leaseMin,
		StartedAt:       time.Now().UTC(),
	}
	if req.Meta != nil {
		session.Meta = model.ToJSON(req.Meta)
	}

	if err := i.core.Store.UpsertActiveSession(c.Request.Context(), session); err != nil {
		httpx.Internal(c)
		return
	}

	i.core.PublishSessionUpdate(c.Request.Context(), accountID, session)
	httpx.OK(c, session)
}

type pauseRequest struct {
	ManualID string `json:"manual_id"`
	IsPaused bool   `json:"is_paused"`
}

func (i *instance) reportPause(c *gin.Context) {
	accountIDStr := authn.AccountID(c)
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		httpx.Unauthorized(c)
		return
	}

	var req pauseRequest
	if !httpx.BindJSON(c, &req) {
		return
	}
	if strings.TrimSpace(req.ManualID) == "" {
		req.ManualID = "default"
	}

	session, err := i.core.Store.GetActiveSession(c.Request.Context(), accountID, req.ManualID)
	if err != nil {
		httpx.NotFound(c, "session not found")
		return
	}

	session.IsPaused = req.IsPaused
	if err := i.core.Store.UpsertActiveSession(c.Request.Context(), session); err != nil {
		httpx.Internal(c)
		return
	}

	i.core.PublishSessionUpdate(c.Request.Context(), accountID, session)
	httpx.OK(c, session)
}

type endSessionRequest struct {
	ManualID   string `json:"manual_id"`
	AutoScrobble bool `json:"auto_scrobble"`
}

func (i *instance) endSession(c *gin.Context) {
	accountIDStr := authn.AccountID(c)
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		httpx.Unauthorized(c)
		return
	}

	var req endSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		req.ManualID = "default"
	}
	if strings.TrimSpace(req.ManualID) == "" {
		req.ManualID = "default"
	}

	ended, err := i.core.Store.EndActiveSession(c.Request.Context(), accountID, req.ManualID)
	if err != nil {
		httpx.OK(c, gin.H{"status": "ended"})
		return
	}

	// Optionally convert to scrobble if auto_scrobble was requested or listened long enough
	if req.AutoScrobble && ended.ProgressSeconds >= 30 {
		scrobble := &model.Scrobble{
			AccountID:       accountID,
			CatalogID:       ended.CatalogID,
			Domain:          ended.Domain,
			ClientName:      "session",
			Title:           ended.Title,
			Subtitle:        ended.Subtitle,
			Caption:         ended.Caption,
			ArtworkURL:      ended.ArtworkURL,
			ListenedSeconds: ended.ProgressSeconds,
			DurationSeconds: ended.DurationSeconds,
			PlayedAt:        time.Now().UTC(),
			Meta:            ended.Meta,
		}
		_ = i.core.Store.CreateScrobble(c.Request.Context(), scrobble)
		i.core.PublishScrobbleCreated(c.Request.Context(), accountID, scrobble)
	}

	i.core.PublishSessionUpdate(c.Request.Context(), accountID, nil)
	httpx.OK(c, gin.H{"status": "ended", "session": ended})
}

func (i *instance) getActiveSessions(c *gin.Context) {
	accountIDStr := authn.AccountID(c)
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		httpx.Unauthorized(c)
		return
	}

	sessions, err := i.core.Store.GetActiveSessionsForAccount(c.Request.Context(), accountID)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, sessions)
}

func (i *instance) getUserNowPlaying(c *gin.Context) {
	identifier := c.Param("identifier")
	accountID, err := i.core.ResolveAccount(c.Request.Context(), identifier)
	if err != nil {
		httpx.NotFound(c, "user not found")
		return
	}

	sessions, err := i.core.Store.GetActiveSessionsForAccount(c.Request.Context(), accountID)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, sessions)
}
