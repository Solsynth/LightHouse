// Package syncyomi implements a SyncYomi-compatible sync endpoint layer for
// TachiyomiSY and Komikku clients, authenticated with Pulsar's reusable custom
// sync API keys and projecting read chapters into Pulsar's book scrobble
// history.
package syncyomi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/modules/synckeys"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

const maxSyncPayloadBytes = 64 << 20 // 64 MiB

// Factory returns the SyncYomi compatibility module.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             "app.solian.pulsar.syncyomi",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Models:         []any{&model.SyncYomiBlob{}},
		Capabilities:   []string{"sync.syncyomi"},
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	core *core.Services
	log  *slog.Logger
}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	i.core = host.Core
	i.log = host.Log
	if i.log == nil {
		i.log = slog.Default()
	}

	group := host.API.Group("/sync", synckeys.RequireAPIKey(i.core.Store, "syncyomi"))
	group.GET("/content", i.getContent)
	group.PUT("/content", i.putContent)
	group.GET("/status", i.getStatus)

	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

func (i *instance) getContent(c *gin.Context) {
	accountID, ok := synckeys.AccountID(c)
	if !ok {
		httpx.Unauthorized(c)
		return
	}

	var blob model.SyncYomiBlob
	err := i.core.DB.WithContext(c.Request.Context()).
		Where("account_id = ?", accountID).
		First(&blob).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.NotFound(c, "no sync content stored")
			return
		}
		httpx.Internal(c)
		return
	}

	if inm := normalizeETag(c.GetHeader("If-None-Match")); inm != "" && inm == blob.ETag {
		c.Header("ETag", formatETag(blob.ETag))
		c.Status(http.StatusNotModified)
		return
	}

	contentType := blob.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.Header("ETag", formatETag(blob.ETag))
	c.Data(http.StatusOK, contentType, blob.Payload)
}

func (i *instance) putContent(c *gin.Context) {
	accountID, ok := synckeys.AccountID(c)
	if !ok {
		httpx.Unauthorized(c)
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxSyncPayloadBytes+1))
	if err != nil {
		httpx.BadRequest(c, "INVALID_BODY", "failed to read sync payload")
		return
	}
	if int64(len(body)) > maxSyncPayloadBytes {
		c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, errs.BadRequest("PAYLOAD_TOO_LARGE", "sync payload exceeds 64 MiB"))
		return
	}

	contentType := strings.TrimSpace(c.GetHeader("Content-Type"))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	ifMatch := normalizeETag(c.GetHeader("If-Match"))
	newETag := "uuid=" + uuid.NewString()
	now := time.Now().UTC()

	err = i.core.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var existing model.SyncYomiBlob
		findErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("account_id = ?", accountID).
			First(&existing).Error
		if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return findErr
		}
		if findErr == nil && ifMatch != "" && ifMatch != "*" && ifMatch != existing.ETag {
			return errPreconditionFailed
		}

		row := model.SyncYomiBlob{
			AccountID:   accountID,
			ETag:        newETag,
			ContentType: contentType,
			Payload:     body,
			SizeBytes:   int64(len(body)),
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if findErr == nil {
			row.CreatedAt = existing.CreatedAt
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "account_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"e_tag", "content_type", "payload", "size_bytes", "updated_at"}),
		}).Create(&row).Error
	})
	if err != nil {
		if errors.Is(err, errPreconditionFailed) {
			c.AbortWithStatusJSON(http.StatusPreconditionFailed, errs.New("ETAG_MISMATCH", "sync content modified by another client", http.StatusPreconditionFailed))
			return
		}
		i.log.Error("syncyomi put failed", "account_id", accountID, "error", err)
		httpx.Internal(c)
		return
	}

	// Best-effort scrobble projection: never fail the raw sync if the payload
	// is an unfamiliar format.
	if created, projErr := projectBackupScrobbles(i.core.DB.WithContext(c.Request.Context()), accountID, body); projErr != nil {
		i.log.Debug("syncyomi scrobble projection skipped", "account_id", accountID, "error", projErr)
	} else {
		for _, sc := range created {
			i.core.PublishScrobbleCreated(c.Request.Context(), accountID, sc)
		}
	}

	c.Header("ETag", formatETag(newETag))
	httpx.OK(c, gin.H{"etag": newETag})
}

func (i *instance) getStatus(c *gin.Context) {
	accountID, ok := synckeys.AccountID(c)
	if !ok {
		httpx.Unauthorized(c)
		return
	}

	var blob model.SyncYomiBlob
	err := i.core.DB.WithContext(c.Request.Context()).
		Select("account_id, e_tag, content_type, size_bytes, updated_at").
		Where("account_id = ?", accountID).
		First(&blob).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.OK(c, gin.H{"has_content": false})
			return
		}
		httpx.Internal(c)
		return
	}

	httpx.OK(c, gin.H{
		"has_content":  true,
		"etag":         blob.ETag,
		"content_type": blob.ContentType,
		"size_bytes":   blob.SizeBytes,
		"updated_at":   blob.UpdatedAt,
	})
}

var errPreconditionFailed = errors.New("precondition failed")

func formatETag(tag string) string {
	return `"` + tag + `"`
}

func normalizeETag(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "W/")
	raw = strings.Trim(raw, `"`)
	return raw
}
