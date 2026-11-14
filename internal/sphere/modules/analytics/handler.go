package analytics

import (
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

// Defaults used when the module is constructed outside the configured process
// (tests) or when a config value is missing.
const (
	defaultBufferSize    = 8192
	defaultBatchSize     = 500
	defaultFlushInterval = 10 * time.Second
	defaultRetention     = 180 * 24 * time.Hour
	defaultVisitorCookie = "sol_visitor"
)

// Field limits keep a single hostile request from bloating the event table.
const (
	kindLimit       = 64
	resourceLimit   = 128
	eventLimit      = 64
	uaLimit         = 1024
	referrerLimit   = 2048
	clientNameLimit = 128
	platformLimit   = 64
)

// handler owns the buffered writer and the read-side aggregation.
type handler struct {
	core *core.Services
	db   *gorm.DB
	log  *slog.Logger

	events        chan ports.AnalyticsEvent
	batchSize     int
	flushInterval time.Duration
	retention     time.Duration
	ipPepper      string
	cookieName    string
	secureCookies bool
	trustedProxy  int

	dropped atomic.Uint64
	flushMu sync.Mutex
}

func newHandler(host *module.Host[core.Services]) *handler {
	h := &handler{
		core:          host.Core,
		log:           host.Log,
		events:        make(chan ports.AnalyticsEvent, defaultBufferSize),
		batchSize:     defaultBatchSize,
		flushInterval: defaultFlushInterval,
		retention:     defaultRetention,
		cookieName:    defaultVisitorCookie,
		trustedProxy:  1,
	}
	if h.log == nil {
		h.log = slog.Default()
	}
	if host.Core == nil {
		return h
	}
	h.db = host.Core.DB
	cfg := host.Core.Config
	if cfg == nil {
		return h
	}
	if cfg.Analytics.BufferSize > 0 {
		h.events = make(chan ports.AnalyticsEvent, cfg.Analytics.BufferSize)
	}
	if cfg.Analytics.BatchSize > 0 {
		h.batchSize = cfg.Analytics.BatchSize
	}
	if cfg.Analytics.FlushInterval > 0 {
		h.flushInterval = time.Duration(cfg.Analytics.FlushInterval) * time.Second
	}
	if cfg.Analytics.RetentionDays > 0 {
		h.retention = time.Duration(cfg.Analytics.RetentionDays) * 24 * time.Hour
	} else {
		h.retention = 0
	}
	h.ipPepper = cfg.Analytics.IPPepper
	if cfg.Analytics.VisitorCookie != "" {
		h.cookieName = cfg.Analytics.VisitorCookie
	}
	h.secureCookies = strings.HasPrefix(cfg.SiteUrl, "https")
	if cfg.Security.TrustedProxyHops > 0 {
		h.trustedProxy = cfg.Security.TrustedProxyHops
	}
	return h
}

func (h *handler) startJobs(host *module.Host[core.Services]) {
	if host.Core == nil || host.Core.Jobs == nil {
		return
	}
	host.Core.Jobs.Every("analytics_flush", h.flushInterval, h.flush)
	host.Core.Jobs.Cron("analytics_retention", "0 30 3 * * ?", h.retentionSweep)
}

// visitorOptions is the identity policy shared with other sphere modules.
func (h *handler) visitorOptions() ports.VisitorOptions {
	return ports.VisitorOptions{
		CookieName:       h.cookieName,
		Secure:           h.secureCookies,
		TrustedProxyHops: h.trustedProxy,
	}
}
