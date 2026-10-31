// Package presenceartworksctl ports Passport's PresenceArtworkController:
// POST /api/presence/artworks (deduplicating image upload) and the public
// GET /api/presence/artworks/:hash byte stream.
package presenceartworksctl

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"

	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/permission"
	"src.solsynth.dev/sosys/stargate/presenceartworks"
)

// multipartOverhead allows for the multipart boundary and part headers on top
// of the configured file limit.
const multipartOverhead = 64 << 10

// Deps is the dependency set the artwork controller needs.
type Deps struct {
	Service *presenceartworks.Service
	Perm    middleware.PermissionChecker
	Log     *slog.Logger
}

// Register mounts /api/presence/artworks.
func Register(api *gin.RouterGroup, d Deps) {
	group := api.Group("/presence/artworks")
	group.POST("", middleware.RequireAuth(), middleware.AskPermission(d.Perm, permission.PresencesArtworkManage), d.upload)
	group.GET("/:hash", d.get)
}

type artworkResponse struct {
	Hash     string `json:"hash"`
	MimeType string `json:"mime_type"`
	Size     int64  `json:"size"`
	URL      string `json:"url"`
}

func (d Deps) upload(c *gin.Context) {
	if d.Service == nil || d.Service.Storage == nil {
		serviceUnavailable(c)
		return
	}
	limit := d.Service.Settings.MaxFileSizeBytes
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit+multipartOverhead)

	file, err := c.FormFile("file")
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			uploadTooLarge(c, limit)
			return
		}
		uploadFailed(c, "Artwork file is required.")
		return
	}
	if file.Size > limit {
		uploadTooLarge(c, limit)
		return
	}
	source, err := file.Open()
	if err != nil {
		d.internal(c, err)
		return
	}
	defer func() { _ = source.Close() }()

	data, err := io.ReadAll(io.LimitReader(source, limit+1))
	if err != nil {
		uploadFailed(c, "Artwork file could not be read.")
		return
	}
	if int64(len(data)) > limit {
		uploadTooLarge(c, limit)
		return
	}

	artwork, _, err := d.Service.SaveArtwork(c.Request.Context(), file.Filename, file.Header.Get("Content-Type"), data)
	if err != nil {
		d.handleSaveError(c, err)
		return
	}
	c.JSON(http.StatusOK, artworkResponse{
		Hash:     artwork.Hash,
		MimeType: artwork.MimeType,
		Size:     artwork.Size,
		URL:      "/api/presence/artworks/" + artwork.Hash,
	})
}

func (d Deps) handleSaveError(c *gin.Context, err error) {
	var invalid *presenceartworks.InvalidInputError
	switch {
	case errors.As(err, &invalid):
		uploadFailed(c, invalid.Message)
	case errors.Is(err, presenceartworks.ErrStorageUnavailable):
		serviceUnavailable(c)
	default:
		d.internal(c, err)
	}
}

func (d Deps) get(c *gin.Context) {
	if d.Service == nil || d.Service.Storage == nil {
		serviceUnavailable(c)
		return
	}
	artwork, err := d.Service.GetArtwork(c.Request.Context(), c.Param("hash"))
	if err != nil {
		var invalid *presenceartworks.InvalidInputError
		if errors.As(err, &invalid) {
			c.JSON(http.StatusBadRequest, errs.BadRequest("PASSPORT_ARTWORK_GET_FAILED", invalid.Message))
			return
		}
		d.internal(c, err)
		return
	}
	if artwork == nil {
		c.JSON(http.StatusNotFound, errs.New("PASSPORT_ARTWORK_NOT_FOUND", "Artwork not found.", http.StatusNotFound))
		return
	}
	stream, err := d.Service.OpenArtwork(c.Request.Context(), artwork)
	if err != nil {
		if errors.Is(err, presenceartworks.ErrArtworkStreamNotFound) {
			c.JSON(http.StatusNotFound, errs.New("PASSPORT_ARTWORK_STREAM_NOT_FOUND", "Artwork stream not found.", http.StatusNotFound))
			return
		}
		d.internal(c, err)
		return
	}
	defer func() { _ = stream.Close() }()

	c.Header("Content-Type", artwork.MimeType)
	// ServeContent adds Range/If-Modified-Since handling; the object is an
	// io.ReadSeeker backed by the bucket.
	http.ServeContent(c.Writer, c.Request, "", artwork.UpdatedAt, stream)
}

func (d Deps) internal(c *gin.Context, err error) {
	if d.Log != nil {
		d.Log.Error("presence artwork request failed", "error", err)
	}
	c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Internal server error.", http.StatusInternalServerError))
}

func uploadFailed(c *gin.Context, message string) {
	c.JSON(http.StatusBadRequest, errs.BadRequest("PASSPORT_ARTWORK_UPLOAD_FAILED", message))
}

func uploadTooLarge(c *gin.Context, limit int64) {
	uploadFailed(c, "Artwork file is too large. Maximum allowed is "+strconv.FormatInt(limit, 10)+" bytes.")
}

func serviceUnavailable(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, errs.New(
		"SERVICE_UNAVAILABLE", "Presence artwork storage is not configured.", http.StatusServiceUnavailable))
}
