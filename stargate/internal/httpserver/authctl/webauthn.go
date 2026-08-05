package authctl

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/stargate/internal/config"
)

// WebAuthnDiscoveryController port. GET /api/auth/webauthn/config registers
// on the /api group; GET /.well-known/webauthn is engine-level (served via
// RegisterWellKnown).

func (h *handler) webauthnConfig(c *gin.Context) {
	rpId := h.d.Cfg.WebAuthn.RpId
	if rpId == "" {
		rpId = c.Request.Host
	}
	rpName := h.d.Cfg.WebAuthn.RpName
	if rpName == "" {
		rpName = "Solar Network"
	}
	c.JSON(http.StatusOK, gin.H{
		"rp_id":   rpId,
		"rp_name": rpName,
	})
}

// RegisterWellKnown registers GET /.well-known/webauthn on the engine (the
// /api RouterGroup cannot express engine-level paths). Wire it in main.go
// alongside the other /.well-known routes.
func RegisterWellKnown(engine *gin.Engine, cfg *config.Config) {
	engine.GET("/.well-known/webauthn", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"origins": cfg.WebAuthn.RelatedOrigins})
	})
}
