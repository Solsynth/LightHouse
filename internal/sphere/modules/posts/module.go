// Package posts ports DysonNetwork.Sphere's post feature: the post read/write
// controllers, taxonomy (categories/tags), collections, watch preferences,
// advertising, admin endpoints, the post gRPC service, and the background jobs
// and event consumers that keep posts consistent.
package posts

import (
	"context"

	gen "src.solsynth.dev/sosys/go/proto"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// ModuleID is the module's registry ID.
const ModuleID = "app.solian.sphere.posts"

// Factory returns the posts module factory.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.sphere.publishers"},
		Ports: []module.PortRequirement{
			{ID: "accounts", Optional: true},
			{ID: "files", Optional: true},
			{ID: "notify", Optional: true},
			{ID: "realtime", Optional: true},
			{ID: "payments", Optional: true},
			{ID: "subscriptions", Optional: true},
		},
		Models: []any{
			store.PostEntity{},
			store.PostReactionEntity{},
			store.PostBookmarkEntity{},
			store.PostWatchPreferenceEntity{},
			store.PostAwardEntity{},
			store.PostTagEntity{},
			store.PostCategoryEntity{},
			store.PostTagLink{},
			store.PostCategoryLink{},
			store.PostCollectionEntity{},
			store.PostCollectionItemEntity{},
			store.PostFeaturedRecordEntity{},
			store.PostSponsorBidEntity{},
			store.PostSponsorPlacementEntity{},
			store.PostAggregatedStatsEntity{},
			store.PostCategorySubscriptionEntity{},
			store.PostInterestProfileEntity{},
			store.RealmPostModerationLogEntity{},
			store.BoostEntity{},
		},
		SQL: []module.SchemaHook{schemaHook},
		Capabilities: []string{
			"posts.read", "posts.create", "posts.update", "posts.delete",
			"posts.react", "posts.bookmark", "posts.award", "posts.sponsor",
			"posts.pin", "posts.boost", "posts.moderate",
			"posts.tags", "posts.categories", "posts.collections",
			"posts.watch", "ads",
		},
		New: func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct{}

func (i *instance) Configure(ctx context.Context, host *module.Host[core.Services]) error {
	h := newHandler(host)
	h.registerRoutes(host)
	if host.GRPC != nil {
		gen.RegisterDyPostServiceServer(host.GRPC, &grpcService{h: h})
	}
	h.startConsumers(ctx)
	h.startJobs(host)
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

// schemaHook applies the indexes AutoMigrate cannot express and enables the
// pg_trgm extension the post search relies on.
func schemaHook(ctx context.Context, db *gorm.DB) error {
	statements := []string{
		`CREATE EXTENSION IF NOT EXISTS pg_trgm`,
		`CREATE INDEX IF NOT EXISTS ix_posts_published_at ON posts (published_at)`,
		`CREATE INDEX IF NOT EXISTS ix_posts_publisher_published ON posts (publisher_id, published_at DESC)`,
		`CREATE INDEX IF NOT EXISTS ix_posts_replied_post ON posts (replied_post_id)`,
		`CREATE INDEX IF NOT EXISTS ix_posts_forwarded_post ON posts (forwarded_post_id)`,
		`CREATE INDEX IF NOT EXISTS ix_posts_chained_post ON posts (chained_post_id)`,
		`CREATE INDEX IF NOT EXISTS ix_posts_title_trgm ON posts USING gin (title gin_trgm_ops)`,
		`CREATE INDEX IF NOT EXISTS ix_posts_description_trgm ON posts USING gin (description gin_trgm_ops)`,
		`CREATE INDEX IF NOT EXISTS ix_posts_content_trgm ON posts USING gin (content gin_trgm_ops)`,
		`CREATE INDEX IF NOT EXISTS ix_post_reactions_post_symbol ON post_reactions (post_id, symbol)`,
		`CREATE INDEX IF NOT EXISTS ix_post_reactions_account ON post_reactions (account_id)`,
		`CREATE INDEX IF NOT EXISTS ix_post_bookmarks_account_post ON post_bookmarks (account_id, post_id)`,
		`CREATE INDEX IF NOT EXISTS ix_post_awards_post ON post_awards (post_id)`,
		`CREATE INDEX IF NOT EXISTS ix_post_tags_slug_lower ON post_tags (lower(slug))`,
		`CREATE INDEX IF NOT EXISTS ix_post_categories_slug_lower ON post_categories (lower(slug))`,
		`CREATE INDEX IF NOT EXISTS ix_post_collections_publisher_slug ON post_collections (publisher_id, slug)`,
		`CREATE INDEX IF NOT EXISTS ix_post_collection_items_collection ON post_collection_items (collection_id, "order")`,
		`CREATE INDEX IF NOT EXISTS ix_post_sponsor_bids_post_expires ON post_sponsor_bids (post_id, expires_at)`,
		`CREATE INDEX IF NOT EXISTS ix_post_category_subscriptions_account ON post_category_subscriptions (account_id)`,
		`CREATE INDEX IF NOT EXISTS ix_boosts_post ON boosts (post_id)`,
		`CREATE INDEX IF NOT EXISTS ix_boosts_post_publisher ON boosts (post_id, publisher_id)`,
	}
	for _, statement := range statements {
		if err := db.WithContext(ctx).Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}
