// Package translation ports DysonNetwork.Sphere/Translation: the text
// translation endpoint and the configured outbound provider. An unconfigured or
// unknown provider answers 503 instead of failing at request time.
package translation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"src.solsynth.dev/sosys/go/pkg/errs"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/config"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
)

// ModuleID is the translation module's registry ID.
const ModuleID = "app.solian.sphere.translation"

const (
	cacheKeyPrefix = "translation:"
	cacheTTL       = 24 * time.Hour
)

// Provider translates text into a target language. An empty sourceLanguage
// means auto-detection.
type Provider interface {
	Translate(ctx context.Context, text, targetLanguage, sourceLanguage string) (string, error)
}

// Factory returns the translation module factory.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Capabilities:   []string{"translation.text"},
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	core        *core.Services
	log         *slog.Logger
	provider    Provider
	providerErr error
}

func (i *instance) Configure(ctx context.Context, host *module.Host[core.Services]) error {
	i.core = host.Core
	i.log = host.Log
	i.provider, i.providerErr = buildProvider(host.Core.Config)
	if i.provider == nil && i.log != nil {
		i.log.Warn("translation disabled", "reason", i.providerErr)
	}

	host.API.POST("/translate", authn.RequireAuth(), i.translate)
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

// buildProvider resolves the configured provider. It returns an error instead of
// a provider whenever the configuration cannot serve requests, so the handler
// can answer 503 without ever dialing out with empty credentials.
func buildProvider(cfg *config.Config) (Provider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("translation is not configured")
	}

	switch strings.ToLower(strings.TrimSpace(cfg.Translation.Provider)) {
	case "":
		return nil, fmt.Errorf("no translation provider is configured")
	case "tencent":
		secretID := strings.TrimSpace(cfg.Translation.SecretID)
		secretKey := strings.TrimSpace(cfg.Translation.SecretKey)
		if secretID == "" || secretKey == "" {
			return nil, fmt.Errorf("the tencent translation provider is missing credentials")
		}
		var projectID int64
		if raw := strings.TrimSpace(cfg.Translation.ProjectID); raw != "" {
			if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
				projectID = parsed
			}
		}
		return newTencentProvider(
			cfg.Translation.Region,
			secretID,
			secretKey,
			projectID,
		), nil
	default:
		return nil, fmt.Errorf("unknown translation provider %q", cfg.Translation.Provider)
	}
}

func (i *instance) translate(c *gin.Context) {
	user := authn.CurrentUser(c)
	if user == nil {
		httpx.Unauthorized(c)
		return
	}
	if user.GetPerkLevel() == 0 {
		httpx.Fail(c, errs.Forbidden("You need a subscription to use this feature."))
		return
	}

	var text string
	if !httpx.BindJSON(c, &text) {
		return
	}

	if i.provider == nil {
		httpx.Fail(c, errs.New("SERVICE_UNAVAILABLE", "Translation is not available.", http.StatusServiceUnavailable))
		return
	}

	targetLanguage := c.Query("to")
	sourceLanguage := c.Query("from")
	ctx := c.Request.Context()

	cacheKey := translationCacheKey(text, targetLanguage)
	if cached, ok := i.cachedTranslation(ctx, cacheKey); ok {
		httpx.OK(c, cached)
		return
	}

	result, err := i.provider.Translate(ctx, text, targetLanguage, sourceLanguage)
	if err != nil {
		if i.log != nil {
			i.log.Error("translate text", "error", err)
		}
		httpx.Fail(c, errs.New("SERVICE_UNAVAILABLE", "The translation provider is unavailable.", http.StatusServiceUnavailable))
		return
	}
	if result != "" {
		i.storeTranslation(ctx, cacheKey, result)
	}
	httpx.OK(c, result)
}

func translationCacheKey(text, targetLanguage string) string {
	sum := sha256.Sum256([]byte(text + ":" + targetLanguage))
	return cacheKeyPrefix + strings.ToUpper(hex.EncodeToString(sum[:]))
}

func (i *instance) cachedTranslation(ctx context.Context, key string) (string, bool) {
	if i.core.Redis == nil {
		return "", false
	}
	var cached string
	found, err := i.core.Redis.Get(ctx, key, &cached)
	if err != nil {
		if i.log != nil {
			i.log.Warn("read translation cache", "error", err)
		}
		return "", false
	}
	if !found || cached == "" {
		return "", false
	}
	return cached, true
}

func (i *instance) storeTranslation(ctx context.Context, key, value string) {
	if i.core.Redis == nil {
		return
	}
	if err := i.core.Redis.Set(ctx, key, value, cacheTTL); err != nil && i.log != nil {
		i.log.Warn("write translation cache", "error", err)
	}
}
