// Package timeline ports the Sphere activity feed: the personalized/top/latest
// post timeline, the friend presence and status events it interleaves, and the
// recommendation discovery surface that feeds interest weights back into the
// ranking.
package timeline

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// ModuleID is the timeline module's registry ID.
const ModuleID = "app.solian.sphere.timeline"

// Module IDs the timeline reads posts and publishers from.
const (
	PostsModuleID      = "app.solian.sphere.posts"
	PublishersModuleID = "app.solian.sphere.publishers"
)

// Factory returns the timeline module factory.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{PostsModuleID, PublishersModuleID},
		Ports: []module.PortRequirement{
			{ID: ports.PortPostSignals, Optional: true},
			{ID: ports.PortRealms, Optional: true},
		},
		Models:       []any{store.PostInterestProfileEntity{}, store.DiscoveryPreferenceEntity{}},
		SQL:          []module.SchemaHook{indexHook},
		Capabilities: []string{"timeline", "timeline.discovery"},
		New:          func() module.Instance[core.Services] { return &instance{} },
	}
}

// indexHook adds the unique (account, kind, reference) indexes the C# EF model
// declared, scoped to live rows so a soft-deleted row can be recreated.
func indexHook(ctx context.Context, db *gorm.DB) error {
	statements := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS ix_post_interest_profiles_account_kind_reference
			ON post_interest_profiles (account_id, kind, reference_id) WHERE deleted_at IS NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ix_discovery_preferences_account_kind_reference
			ON discovery_preferences (account_id, kind, reference_id) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS ix_posts_published_at_feed
			ON posts (published_at DESC) WHERE drafted_at IS NULL AND replied_post_id IS NULL AND chained_post_id IS NULL`,
	}
	for _, statement := range statements {
		if err := db.WithContext(ctx).Exec(statement).Error; err != nil {
			return fmt.Errorf("timeline schema hook: %w", err)
		}
	}
	return nil
}

type instance struct {
	svc *service
}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	i.svc = newService(host.Core, host.Log)
	i.svc.signals, _ = module.Get[ports.PostSignals](host.Ports, ports.PortPostSignals)
	i.svc.realms, _ = module.Get[ports.Realms](host.Ports, ports.PortRealms)

	api := host.API
	api.GET("/timeline", i.svc.handleListEvents)
	api.GET("/timeline/discovery/profile", requireAuth(), i.svc.handleDiscoveryProfile)
	api.POST("/timeline/discovery/uninterested",
		requireAuth(), requirePermission(host, permissionTimelinesFeedback), i.svc.handleMarkUninterested)
	api.DELETE("/timeline/discovery/uninterested",
		requireAuth(), requirePermission(host, permissionTimelinesFeedback), i.svc.handleRemoveUninterested)
	api.POST("/timeline/discovery/feedback",
		requireAuth(), requirePermission(host, permissionTimelinesFeedback), i.svc.handleFeedback)
	api.PUT("/timeline/discovery/weights",
		requireAuth(), requirePermission(host, permissionTimelinesWeightsManage), i.svc.handleAdjustWeight)
	api.POST("/timeline/discovery/reset",
		requireAuth(), requirePermission(host, permissionTimelinesReset), i.svc.handleResetInterests)
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }
