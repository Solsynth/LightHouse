// Package analytics implements a general-purpose, high-performance event
// pipeline for resource views and engagement. Events are buffered in memory,
// classified on the flush goroutine (client class, browser, platform) and
// written in batches to the append-only analytics_events table. Sibling
// modules record events through the ports.Analytics port; browsers can post
// events to POST /analytics/events.
package analytics

import (
	"context"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

// ModuleID is the module's registry ID.
const ModuleID = "app.solian.sphere.analytics"

// Factory returns the analytics module factory. It has no module dependency:
// it stores whatever kind/resource pairs sibling modules record.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Provides:       []module.PortID{ports.PortAnalytics},
		Models:         []any{store.AnalyticsEventEntity{}},
		SQL: []module.SchemaHook{module.DDL(
			`CREATE INDEX IF NOT EXISTS ix_analytics_events_resource ON analytics_events (kind, resource_id, event, created_at)`,
			`CREATE INDEX IF NOT EXISTS ix_analytics_events_created ON analytics_events (created_at)`,
			`CREATE INDEX IF NOT EXISTS ix_analytics_events_account ON analytics_events (account_id)`,
			`CREATE INDEX IF NOT EXISTS ix_analytics_events_visitor ON analytics_events (visitor_id)`,
		)},
		Capabilities: []string{"analytics.ingest", "analytics.read"},
		New:          func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct{ h *handler }

// ProvidePorts publishes the analytics port so every module can record and
// query events without importing this package.
func (i *instance) ProvidePorts(_ context.Context, host *module.Host[core.Services]) error {
	i.h = newHandler(host)
	module.Provide[ports.Analytics](host.Ports, ports.PortAnalytics, ModuleID,
		module.KindFeature, &analyticsPort{h: i.h})
	return nil
}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	h := i.h
	if h == nil {
		h = newHandler(host)
	}
	h.registerRoutes(host)
	h.startJobs(host)
	return nil
}

// Stop flushes the buffer so a graceful shutdown does not lose queued events.
func (i *instance) Stop(ctx context.Context) error {
	if i.h == nil {
		return nil
	}
	return i.h.flush(ctx)
}
