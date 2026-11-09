package scrobbles

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
)

func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             "app.solian.pulsar.scrobbles",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Models:         []any{&model.Scrobble{}},
		Capabilities:   []string{"scrobbles", "history"},
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	core *core.Services
}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	i.core = host.Core

	// Scrobble submission & management
	host.API.POST("/scrobbles", authn.RequireAuth(), i.submitScrobbles)
	host.API.GET("/history", authn.RequireAuth(), i.myHistory)
	host.API.DELETE("/scrobbles/:id", authn.RequireAuth(), i.deleteScrobble)

	// User public playback history
	host.API.GET("/users/:identifier/history", i.userHistory)

	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

type singleScrobblePayload struct {
	Domain          string         `json:"domain"`
	ClientName      string         `json:"client_name"`
	Title           string         `json:"title"`
	Subtitle        *string        `json:"subtitle,omitempty"`
	Caption         *string        `json:"caption,omitempty"`
	ArtworkURL      *string        `json:"artwork_url,omitempty"`
	ListenedSeconds int            `json:"listened_seconds"`
	DurationSeconds int            `json:"duration_seconds"`
	PlayedAt        *time.Time     `json:"played_at,omitempty"`
	Provider        *string        `json:"provider,omitempty"`
	ProviderKey     *string        `json:"provider_key,omitempty"`
	Meta            map[string]any `json:"meta,omitempty"`
}

type batchScrobblesRequest struct {
	Scrobbles []singleScrobblePayload `json:"scrobbles"`
}

func (i *instance) submitScrobbles(c *gin.Context) {
	accountIDStr := authn.AccountID(c)
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		httpx.Unauthorized(c)
		return
	}

	// Support both single scrobble object and array/batch envelope
	var rawData []singleScrobblePayload
	var single singleScrobblePayload
	var batch batchScrobblesRequest

	if err := c.ShouldBindJSON(&batch); err == nil && len(batch.Scrobbles) > 0 {
		rawData = batch.Scrobbles
	} else if err := c.ShouldBindJSON(&rawData); err == nil && len(rawData) > 0 {
		// parsed as array directly
	} else if err := c.ShouldBindJSON(&single); err == nil && strings.TrimSpace(single.Title) != "" {
		rawData = []singleScrobblePayload{single}
	} else {
		httpx.BadRequest(c, "INVALID_BODY", "unable to parse scrobble payload")
		return
	}

	maxBatch := i.core.Config.Scrobbler.MaxBatchScrobble
	if len(rawData) > maxBatch {
		rawData = rawData[:maxBatch]
	}

	created := make([]model.Scrobble, 0, len(rawData))
	for _, item := range rawData {
		if strings.TrimSpace(item.Title) == "" {
			continue
		}
		domain := strings.TrimSpace(item.Domain)
		if domain == "" {
			domain = model.DomainMusic
		}
		clientName := strings.TrimSpace(item.ClientName)
		if clientName == "" {
			clientName = "Unknown"
		}

		// Ensure catalog entry exists
		var catalogID *uuid.UUID
		catItem := &model.CatalogItem{
			Domain:          domain,
			Title:           strings.TrimSpace(item.Title),
			Subtitle:        item.Subtitle,
			Caption:         item.Caption,
			ArtworkURL:      item.ArtworkURL,
			DurationSeconds: item.DurationSeconds,
		}
		if item.Provider != nil {
			catItem.Provider = strings.TrimSpace(*item.Provider)
		}
		if item.ProviderKey != nil {
			catItem.ProviderKey = strings.TrimSpace(*item.ProviderKey)
		}

		savedCat, err := i.core.Store.FindOrCreateCatalogItem(c.Request.Context(), catItem)
		if err == nil && savedCat != nil {
			catalogID = &savedCat.ID
		}

		playedAt := time.Now().UTC()
		if item.PlayedAt != nil && !item.PlayedAt.IsZero() {
			playedAt = item.PlayedAt.UTC()
		}

		scrobble := model.Scrobble{
			ID:              uuid.New(),
			AccountID:       accountID,
			CatalogID:       catalogID,
			Domain:          domain,
			ClientName:      clientName,
			Title:           item.Title,
			Subtitle:        item.Subtitle,
			Caption:         item.Caption,
			ArtworkURL:      item.ArtworkURL,
			ListenedSeconds: item.ListenedSeconds,
			DurationSeconds: item.DurationSeconds,
			PlayedAt:        playedAt,
		}
		if item.Meta != nil {
			scrobble.Meta = model.ToJSON(item.Meta)
		}

		if err := i.core.Store.CreateScrobble(c.Request.Context(), &scrobble); err == nil {
			i.core.PublishScrobbleCreated(c.Request.Context(), accountID, &scrobble)
			created = append(created, scrobble)
		}
	}

	httpx.Created(c, created)
}

func (i *instance) myHistory(c *gin.Context) {
	accountIDStr := authn.AccountID(c)
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		httpx.Unauthorized(c)
		return
	}

	i.renderHistory(c, accountID)
}

func (i *instance) userHistory(c *gin.Context) {
	identifier := c.Param("identifier")
	accountID, err := i.core.ResolveAccount(c.Request.Context(), identifier)
	if err != nil {
		httpx.NotFound(c, "user not found")
		return
	}

	i.renderHistory(c, accountID)
}

func (i *instance) renderHistory(c *gin.Context, accountID uuid.UUID) {
	page, ok := httpx.PageQuery(c, i.core.Config.Security.MaxTake)
	if !ok {
		return
	}

	domain := c.Query("domain")
	var fromTime, toTime *time.Time
	if fromStr := c.Query("from"); fromStr != "" {
		if t, err := time.Parse(time.RFC3339, fromStr); err == nil {
			fromTime = &t
		}
	}
	if toStr := c.Query("to"); toStr != "" {
		if t, err := time.Parse(time.RFC3339, toStr); err == nil {
			toTime = &t
		}
	}

	items, total, err := i.core.Store.ListScrobbles(c.Request.Context(), accountID, domain, fromTime, toTime, page.Offset, page.Take)
	if err != nil {
		httpx.Internal(c)
		return
	}

	httpx.List(c, items, total)
}

func (i *instance) deleteScrobble(c *gin.Context) {
	accountIDStr := authn.AccountID(c)
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		httpx.Unauthorized(c)
		return
	}

	idStr := c.Param("id")
	scrobbleID, err := uuid.Parse(idStr)
	if err != nil {
		httpx.BadRequest(c, "INVALID_ID", "invalid scrobble id")
		return
	}

	if err := i.core.Store.DeleteScrobble(c.Request.Context(), accountID, scrobbleID); err != nil {
		httpx.NotFound(c, "scrobble not found")
		return
	}

	httpx.NoContent(c)
}
