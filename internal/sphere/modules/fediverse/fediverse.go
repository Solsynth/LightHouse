// Package fediverse ports DysonNetwork.Sphere/ActivityPub: the actor and
// object documents, WebFinger/NodeInfo discovery, the shared inbox, HTTP
// signatures (draft-cavage RSA-SHA256), the delivery queue with retries and
// dead letters, actor discovery behind an SSRF guard, quote authorizations and
// fediverse moderation.
package fediverse

import (
	"context"
	"time"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// ModuleID is the fediverse module's registry ID.
const ModuleID = "app.solian.sphere.fediverse"

// Permission keys used by the moderation, key and follow controllers.
const (
	permModerationManage = "fediverse.moderation.rules.manage"
	permModerationCheck  = "fediverse.moderation.check"
	permKeysManage       = "fediverse.keys.manage"
	permPostsBoost       = "posts.boost"
)

// Factory returns the fediverse module factory.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires: []string{
			"app.solian.sphere.posts",
			"app.solian.sphere.publishers",
		},
		Ports: []module.PortRequirement{
			{ID: "accounts", Optional: true},
			{ID: "files", Optional: true},
			{ID: "notify", Optional: true},
			{ID: "domains", Optional: true},
		},
		Models: []any{
			store.FediverseInstanceEntity{},
			store.FediverseModerationRuleEntity{},
			store.FediverseKeyEntity{},
			store.ActivityPubDeliveryEntity{},
			store.DeliveryDeadLetterEntity{},
			store.QuoteAuthorizationEntity{},
		},
		SQL: []module.SchemaHook{module.DDL(
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_fediverse_instances_domain ON fediverse_instances (domain) WHERE deleted_at IS NULL`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_fediverse_moderation_rules_name ON fediverse_moderation_rules (name) WHERE deleted_at IS NULL`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_fediverse_keys_key_id ON fediverse_keys (key_id) WHERE deleted_at IS NULL`,
			`CREATE INDEX IF NOT EXISTS idx_fediverse_keys_publisher ON fediverse_keys (publisher_id)`,
			`CREATE INDEX IF NOT EXISTS idx_activity_pub_deliveries_retry ON activity_pub_deliveries (status, next_retry_at)`,
			`CREATE INDEX IF NOT EXISTS idx_activity_pub_deliveries_activity ON activity_pub_deliveries (activity_id)`,
			`CREATE INDEX IF NOT EXISTS idx_activity_pub_deliveries_actor ON activity_pub_deliveries (actor_uri, sent_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_delivery_dead_letters_failed ON delivery_dead_letters (failed_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_quote_authorizations_target ON quote_authorizations (interaction_target_uri, interacting_object_uri)`,
			`CREATE INDEX IF NOT EXISTS idx_quote_authorizations_publisher ON quote_authorizations (publisher_id)`,
		)},
		Capabilities: []string{
			"fediverse.actor", "fediverse.outbox", "fediverse.inbox",
			"fediverse.delivery", "fediverse.moderation", "fediverse.quotes",
			"fediverse.keys",
		},
		New: func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	srv *server
}

func (i *instance) Configure(ctx context.Context, host *module.Host[core.Services]) error {
	srv := newServer(host.Core, host.ModuleSettings(ModuleID), host.Log)
	i.srv = srv

	if err := srv.bootstrap(ctx); err != nil {
		srv.log.Warn("fediverse bootstrap incomplete", "error", err)
	}

	engine := host.Engine
	api := host.API

	// ---- WebFinger / NodeInfo -------------------------------------------
	engine.GET("/.well-known/host-meta", srv.handleHostMeta)
	engine.GET("/.well-known/webfinger", srv.handleWebFinger)
	engine.GET("/.well-known/nodeinfo", srv.handleNodeInfoDiscovery)
	engine.GET("/.well-known/nodeinfo/2.0", srv.handleNodeInfo20)
	engine.GET("/.well-known/nodeinfo/2.1", srv.handleNodeInfo21)
	engine.GET("/nodeinfo/2.0", srv.handleNodeInfo20)
	engine.GET("/nodeinfo/2.1", srv.handleNodeInfo21)

	// ---- actors, objects and collections --------------------------------
	actors := engine.Group("/activitypub/actors/:username")
	actors.GET("", srv.authorizedFetch, srv.handleActor)
	actors.POST("/inbox", srv.handleInbox)
	actors.GET("/outbox", srv.authorizedFetch, srv.handleOutbox)
	actors.GET("/followers", srv.authorizedFetch, srv.handleFollowers)
	actors.GET("/following", srv.authorizedFetch, srv.handleFollowing)
	actors.GET("/featured", srv.authorizedFetch, srv.handleFeatured)
	actors.GET("/main-key", srv.handleMainKey)
	actors.GET("/publickey", srv.handleMainKey)

	engine.GET("/activitypub/objects/:id", srv.authorizedFetch, srv.handleObject)

	serverActor := engine.Group("/activitypub/actor")
	serverActor.GET("", srv.handleServerActor)
	serverActor.GET("/outbox", srv.handleServerOutbox)
	serverActor.GET("/followers", srv.handleServerFollowers)
	serverActor.GET("/following", srv.handleServerFollowing)
	serverActor.GET("/main-key", srv.handleServerMainKey)

	// ---- quote authorizations -------------------------------------------
	quotes := engine.Group("/quote-authorizations")
	quotes.GET("/:id", srv.handleQuoteGet)
	quotes.POST("", authn.RequireAuth(), srv.handleQuoteCreate)
	quotes.DELETE("/:id", authn.RequireAuth(), srv.handleQuoteRevoke)

	// ---- remote actor browsing ------------------------------------------
	fedActors := api.Group("/fediverse/actors")
	fedActors.GET("/search", srv.handleActorSearch)
	fedActors.GET("/availability", authn.RequireAuth(), srv.handleActorAvailability)
	fedActors.GET("/:id/posts", srv.handleActorPosts)
	fedActors.GET("/:id/followers", srv.handleActorFollowerList)
	fedActors.GET("/:id/following", srv.handleActorFollowingList)
	fedActors.GET("/:id/relationship", authn.RequireAuth(), srv.handleActorRelationship)
	fedActors.POST("/:id/follow", authn.RequireAuth(), srv.core.Auth.RequirePermission(permPostsBoost), srv.handleActorFollow)
	fedActors.POST("/:id/unfollow", authn.RequireAuth(), srv.core.Auth.RequirePermission(permPostsBoost), srv.handleActorUnfollow)
	fedActors.GET("/:id", authn.RequireAuth(), srv.handleActorLookup)

	// ---- moderation ------------------------------------------------------
	moderation := api.Group("/fediverse/moderation", authn.RequireAuth())
	moderation.GET("/rules", srv.handleRuleList)
	moderation.GET("/rules/:id", srv.handleRuleGet)
	moderation.POST("/rules", srv.core.Auth.RequirePermission(permModerationManage), srv.handleRuleCreate)
	moderation.PUT("/rules/:id", srv.core.Auth.RequirePermission(permModerationManage), srv.handleRuleUpdate)
	moderation.DELETE("/rules/:id", srv.core.Auth.RequirePermission(permModerationManage), srv.handleRuleDelete)
	moderation.POST("/rules/:id/toggle", srv.core.Auth.RequirePermission(permModerationManage), srv.handleRuleToggle)
	moderation.POST("/check-domain", srv.core.Auth.RequirePermission(permModerationCheck), srv.handleCheckDomain)
	moderation.POST("/check-actor", srv.core.Auth.RequirePermission(permModerationCheck), srv.handleCheckActor)
	moderation.POST("/check-actor-rule", srv.core.Auth.RequirePermission(permModerationCheck), srv.handleCheckActorRule)
	moderation.GET("/actors", srv.core.Auth.RequirePermission(permModerationManage), srv.handleActorRuleList)
	moderation.POST("/actors/suspend", srv.core.Auth.RequirePermission(permModerationManage), srv.handleSuspendActor)
	moderation.DELETE("/actors/suspend/:id", srv.core.Auth.RequirePermission(permModerationManage), srv.handleUnsuspendActor)

	// ---- key administration ---------------------------------------------
	keys := api.Group("/admin/fediverse/keys", authn.RequireAuth(), srv.requireAdmin)
	keys.GET("/audit", srv.handleKeyAudit)
	keys.POST("/backfill", srv.handleKeyBackfill)
	keys.POST("/cleanup", srv.handleKeyCleanup)
	keys.GET("/stats", srv.handleKeyStats)
	keys.GET("/actor/:actorId", srv.handleActorKeyInfo)
	keys.POST("/actor/:actorId/ensure", srv.handleActorKeyEnsure)
	keys.POST("/actor/:actorId/regenerate", srv.handleActorKeyRegenerate)

	// ---- authenticated follow surface ------------------------------------
	ap := api.Group("/activitypub", authn.RequireAuth())
	ap.GET("/following", srv.handleFollowFollowing)
	ap.GET("/followers", srv.handleFollowFollowers)
	ap.GET("/search", srv.handleFollowSearch)
	ap.GET("/relationships", srv.handleFollowRelationships)
	ap.GET("/check/:username", srv.handleFollowCheckActor)

	// ---- realm (community) actors ---------------------------------------
	realms := engine.Group("/activitypub/realms")
	realms.GET("/:slug", srv.handleRealmActor)
	realms.GET("/:slug/followers", srv.handleRealmFollowers)
	realms.POST("/:slug/inbox", srv.handleRealmInbox)
	realms.GET("/:slug/outbox", srv.handleRealmOutbox)

	// ---- scheduled work --------------------------------------------------
	if host.Core.Jobs != nil {
		host.Core.Jobs.Every("activitypub_delivery_retry", time.Minute, srv.delivery.retryFailed)
		host.Core.Jobs.Cron("activitypub_delivery_cleanup", "0 0 3 * * ?", srv.delivery.cleanup)
		host.Core.Jobs.Cron("activitypub_delivery_health", "0 */5 * * * ?", srv.delivery.healthCheck)
	}
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }
