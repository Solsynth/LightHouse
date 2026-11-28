// Package applemusic mints short-lived, cached Apple MusicKit ES256 Developer
// Tokens on the server from the deployment's .p8 private key so non-Apple
// GroovyBox clients (Android, Web, Windows, Linux) never bundle credentials.
package applemusic

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             "app.solian.pulsar.applemusic",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Capabilities:   []string{"applemusic.token"},
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	signer *tokenSigner
}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	cfg := host.Core.Config.AppleMusic
	signer, err := newTokenSigner(signerConfig{
		TeamID:         cfg.TeamID,
		KeyID:          cfg.KeyID,
		PrivateKeyPath: cfg.PrivateKeyPath,
		PrivateKeyPEM:  cfg.PrivateKeyPEM,
		TTL:            time.Duration(cfg.TokenTTLHours) * time.Hour,
	})
	if err != nil {
		return err
	}
	i.signer = signer

	// Authenticated fleet endpoint: protects the deployment's Apple MusicKit
	// quota while letting signed-in GroovyBox clients fetch a fresh developer
	// token on demand.
	host.API.GET("/apple-music/developer-token", authn.RequireAuth(), i.getDeveloperToken)
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

type developerTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (i *instance) getDeveloperToken(c *gin.Context) {
	if i.signer == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"code":    "APPLE_MUSIC_UNCONFIGURED",
			"message": "apple music developer token minting is not configured on this server",
		})
		return
	}

	token, expiresAt, err := i.signer.Token()
	if err != nil {
		httpx.Internal(c)
		return
	}

	remaining := int(time.Until(expiresAt).Seconds())
	if remaining > 60 {
		// Leave a small buffer so clients refresh before the token actually expires.
		c.Header("Cache-Control", fmt.Sprintf("private, max-age=%d", remaining-60))
	} else {
		c.Header("Cache-Control", "no-store")
	}

	httpx.OK(c, developerTokenResponse{
		Token:     token,
		ExpiresAt: expiresAt,
	})
}
