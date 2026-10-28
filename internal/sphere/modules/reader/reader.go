// Package reader ports DysonNetwork.Sphere/Reader: the link preview and article
// scraper. Every outbound request is SSRF-guarded, redirect hops are validated
// one by one and previews are cached in Redis.
package reader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	gen "src.solsynth.dev/sosys/go/proto"
	"src.solsynth.dev/sosys/go/pkg/errs"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
)

// ModuleID is the reader module's registry ID.
const ModuleID = "app.solian.sphere.reader"

// PermissionCacheScrap gates cache invalidation.
const PermissionCacheScrap = "cache.scrap"

const (
	previewCachePrefix   = "scrap:preview:"
	previewCacheGroup    = "scrap:preview"
	previewCacheIndexKey = "scrap:preview:index"
	previewCacheTTL      = 24 * time.Hour
	previewIndexTTL      = 7 * 24 * time.Hour
)

// Factory returns the reader module factory.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Ports:          []module.PortRequirement{{ID: ports.PortDomains, Optional: true}},
		Capabilities:   []string{"reader.link_preview", "reader.article"},
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	core    *core.Services
	log     *slog.Logger
	fetcher *fetcher
}

func (i *instance) Configure(ctx context.Context, host *module.Host[core.Services]) error {
	i.core = host.Core
	i.log = host.Log

	var domains ports.Domains
	if host.Ports != nil {
		domains, _ = module.Get[ports.Domains](host.Ports, ports.PortDomains)
	}
	var schemes []string
	if host.Core.Config != nil {
		schemes = host.Core.Config.Scrap.AllowedSchemes
	}
	i.fetcher = newFetcher(domains, schemes, host.Log)

	api := host.API
	api.GET("/scrap/link", authn.RequireAuth(), i.scrapLink)
	api.DELETE("/scrap/link/cache", authn.RequireAuth(), i.permission(PermissionCacheScrap), i.invalidateLinkCache)
	api.DELETE("/scrap/cache/all", authn.RequireAuth(), i.permission(PermissionCacheScrap), i.invalidateAllCache)

	gen.RegisterDyWebReaderServiceServer(host.GRPC, &grpcServer{svc: i})
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

func (i *instance) permission(key string) gin.HandlerFunc {
	if i.core.Auth == nil {
		return func(c *gin.Context) {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable,
				errs.New("SERVICE_UNAVAILABLE", "The permission service is unavailable.", http.StatusServiceUnavailable))
		}
	}
	return i.core.Auth.RequirePermission(key)
}

// getLinkPreview resolves the OpenGraph preview of url, reading and filling the
// Redis cache unless bypassCache is set.
func (i *instance) getLinkPreview(ctx context.Context, rawURL string, bypassCache bool) (model.LinkEmbed, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return model.LinkEmbed{}, newWebReaderError("Invalid URL format.", err)
	}

	if !bypassCache {
		if cached, ok := i.cachedPreview(ctx, rawURL); ok {
			return cached, nil
		}
	}

	result, err := i.fetcher.fetch(ctx, rawURL)
	if err != nil {
		return model.LinkEmbed{}, err
	}

	if !strings.HasPrefix(result.contentType, "text/html") {
		host := result.finalURL.Host
		embed := model.LinkEmbed{URL: result.finalURL.String(), Title: &host}
		if result.contentType != "" {
			contentType := result.contentType
			embed.Type = &contentType
		}
		i.storePreview(ctx, rawURL, embed)
		return embed, nil
	}

	embed := extractLinkEmbed(result.finalURL, result.body)
	i.storePreview(ctx, rawURL, embed)
	return embed, nil
}

// scrapeArticle returns the preview plus the inner HTML of the page's article
// element.
func (i *instance) scrapeArticle(ctx context.Context, rawURL string) (scrapedArticle, error) {
	embed, err := i.getLinkPreview(ctx, rawURL, false)
	if err != nil {
		return scrapedArticle{}, err
	}
	article := scrapedArticle{LinkEmbed: embed}

	result, err := i.fetcher.fetch(ctx, rawURL)
	if err != nil {
		// The C# reader returns the preview with a null body when the content
		// fetch fails, so a preview that worked still reaches the caller.
		return article, nil
	}
	article.Content = extractArticle(result.body)
	return article, nil
}

func (i *instance) scrapLink(c *gin.Context) {
	raw := c.Query("url")
	if raw == "" {
		httpx.BadRequest(c, "SCRAP_URL_REQUIRED", "URL parameter is required.")
		return
	}

	decoded := decodeURL(raw)
	if parsed, err := url.Parse(decoded); err != nil || !parsed.IsAbs() || parsed.Host == "" {
		httpx.BadRequest(c, "SCRAP_URL_INVALID_FORMAT", "Invalid URL format.")
		return
	}

	embed, err := i.getLinkPreview(c.Request.Context(), decoded, false)
	if err != nil {
		var readerErr *WebReaderError
		if errors.As(err, &readerErr) {
			if i.log != nil {
				i.log.Warn("scrap link", "url", raw, "error", readerErr.Message)
			}
			httpx.BadRequest(c, "SCRAP_LINK_ERROR", readerErr.Message)
			return
		}
		if i.log != nil {
			i.log.Error("scrap link", "url", raw, "error", err)
		}
		httpx.Fail(c, errs.New("SCRAP_UNEXPECTED_ERROR",
			"An unexpected error occurred while processing the link.", http.StatusInternalServerError))
		return
	}
	httpx.OK(c, embed)
}

func (i *instance) invalidateLinkCache(c *gin.Context) {
	raw := c.Query("url")
	if raw == "" {
		httpx.BadRequest(c, "SCRAP_URL_REQUIRED", "URL parameter is required.")
		return
	}
	i.invalidatePreview(c.Request.Context(), raw)
	httpx.OK(c, gin.H{"message": "Cache invalidated for URL"})
}

func (i *instance) invalidateAllCache(c *gin.Context) {
	i.invalidateAllPreviews(c.Request.Context())
	httpx.OK(c, gin.H{"message": "All link preview caches invalidated"})
}

// decodeURL mirrors the C# UrlDecoder: a query value that still contains an
// escape or a plus sign is decoded once more.
func decodeURL(raw string) string {
	if !strings.ContainsAny(raw, "%+") {
		return raw
	}
	decoded, err := url.QueryUnescape(raw)
	if err != nil {
		return raw
	}
	return decoded
}

// previewCacheKey hashes the normalized URL the way the C# reader does.
func previewCacheKey(rawURL string) string {
	sum := sha256.Sum256([]byte(normalizeURL(rawURL)))
	return previewCachePrefix + strings.ToLower(hex.EncodeToString(sum[:]))
}

// normalizeURL lowercases the URL and drops the trailing slash of the path and
// the default port, so equivalent links share one cache entry.
func normalizeURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !parsed.IsAbs() {
		return strings.ToLower(strings.TrimRight(strings.TrimSpace(rawURL), "/"))
	}
	scheme := strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	port := ""
	if value := parsed.Port(); value != "" && !isDefaultPort(scheme, value) {
		port = ":" + value
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	query := ""
	if parsed.RawQuery != "" {
		query = "?" + parsed.RawQuery
	}
	return strings.ToLower(scheme + "://" + host + port + path + query)
}

func isDefaultPort(scheme, port string) bool {
	return (scheme == "http" && port == "80") || (scheme == "https" && port == "443")
}

func (i *instance) cachedPreview(ctx context.Context, rawURL string) (model.LinkEmbed, bool) {
	if i.core.Redis == nil {
		return model.LinkEmbed{}, false
	}
	var embed model.LinkEmbed
	found, err := i.core.Redis.Get(ctx, previewCacheKey(rawURL), &embed)
	if err != nil {
		if i.log != nil {
			i.log.Warn("read link preview cache", "url", rawURL, "error", err)
		}
		return model.LinkEmbed{}, false
	}
	return embed, found
}

func (i *instance) storePreview(ctx context.Context, rawURL string, embed model.LinkEmbed) {
	if i.core.Redis == nil {
		return
	}
	key := previewCacheKey(rawURL)
	if err := i.core.Redis.SetWithGroups(ctx, key, embed, []string{previewCacheGroup}, previewCacheTTL); err != nil {
		if i.log != nil {
			i.log.Warn("write link preview cache", "url", rawURL, "error", err)
		}
		return
	}
	i.indexPreviewKey(ctx, key)
}

// indexPreviewKey records the cache key so InvalidateAll can drop entries even
// when the cache backend cannot enumerate a group.
func (i *instance) indexPreviewKey(ctx context.Context, key string) {
	if i.core.Redis == nil {
		return
	}
	_, err := i.core.Redis.ExecuteWithLock(ctx, previewCacheIndexKey+":lock",
		5*time.Second, 2*time.Second, 100*time.Millisecond, func(lockCtx context.Context) error {
			var keys []string
			if found, err := i.core.Redis.Get(lockCtx, previewCacheIndexKey, &keys); err != nil || !found {
				keys = nil
			}
			for _, existing := range keys {
				if existing == key {
					return nil
				}
			}
			keys = append(keys, key)
			return i.core.Redis.Set(lockCtx, previewCacheIndexKey, keys, previewIndexTTL)
		})
	if err != nil && i.log != nil {
		i.log.Warn("index link preview cache key", "key", key, "error", err)
	}
}

func (i *instance) invalidatePreview(ctx context.Context, rawURL string) {
	if i.core.Redis == nil {
		return
	}
	if err := i.core.Redis.Remove(ctx, previewCacheKey(rawURL)); err != nil && i.log != nil {
		i.log.Warn("invalidate link preview cache", "url", rawURL, "error", err)
	}
}

// groupRemover is implemented by the fleet cache backends; the shared
// CacheService interface deliberately does not require it.
type groupRemover interface {
	RemoveGroup(ctx context.Context, group string) error
}

func (i *instance) invalidateAllPreviews(ctx context.Context) {
	if i.core.Redis == nil {
		return
	}
	if remover, ok := any(i.core.Redis).(groupRemover); ok {
		if err := remover.RemoveGroup(ctx, previewCacheGroup); err != nil && i.log != nil {
			i.log.Warn("invalidate all link preview caches", "error", err)
		}
	}

	var keys []string
	if found, err := i.core.Redis.Get(ctx, previewCacheIndexKey, &keys); err == nil && found {
		for _, key := range keys {
			if err := i.core.Redis.Remove(ctx, key); err != nil && i.log != nil {
				i.log.Warn("invalidate indexed link preview cache", "key", key, "error", err)
			}
		}
	}
	_ = i.core.Redis.Remove(ctx, previewCacheIndexKey)
}
