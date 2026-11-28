package wellknownctl

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/stargate/config"
)

type landingAssetResponse struct {
	ID              string  `json:"id"`
	URL             string  `json:"url"`
	Orientation     string  `json:"orientation"`
	BackgroundColor string  `json:"background_color,omitempty"`
	IsLimitedTime   bool    `json:"is_limited_time"`
	StartsAt        *string `json:"starts_at,omitempty"`
	EndsAt          *string `json:"ends_at,omitempty"`
}

type landingResponse struct {
	Assets []landingAssetResponse `json:"assets"`
}

func (d Deps) getLanding(c *gin.Context) {
	c.JSON(http.StatusOK, buildLandingResponse(d.Cfg, time.Now().UTC()))
}

func buildLandingResponse(cfg *config.Config, now time.Time) landingResponse {
	if cfg == nil || len(cfg.Landing.Assets) == 0 {
		return landingResponse{Assets: []landingAssetResponse{}}
	}

	items := make([]landingAssetResponse, 0, len(cfg.Landing.Assets))
	for _, asset := range cfg.Landing.Assets {
		rawURL := strings.TrimSpace(asset.URL)
		orientation := asset.NormalizedOrientation()
		if rawURL == "" || orientation == "" {
			continue
		}

		var startsAtPtr, endsAtPtr *string
		if raw := strings.TrimSpace(asset.StartsAt); raw != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
				formatted := parsed.UTC().Format(time.RFC3339)
				startsAtPtr = &formatted
			}
		}
		if raw := strings.TrimSpace(asset.EndsAt); raw != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
				if parsed.Before(now) {
					continue
				}
				formatted := parsed.UTC().Format(time.RFC3339)
				endsAtPtr = &formatted
			}
		}

		id := strings.TrimSpace(asset.ID)
		if id == "" {
			sum := sha256.Sum256([]byte(orientation + ":" + rawURL))
			id = hex.EncodeToString(sum[:8])
		}

		items = append(items, landingAssetResponse{
			ID:              id,
			URL:             rawURL,
			Orientation:     orientation,
			BackgroundColor: strings.TrimSpace(asset.BackgroundColor),
			IsLimitedTime:   startsAtPtr != nil || endsAtPtr != nil,
			StartsAt:        startsAtPtr,
			EndsAt:          endsAtPtr,
		})
	}

	return landingResponse{Assets: items}
}
