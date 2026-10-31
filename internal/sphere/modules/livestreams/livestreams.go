// Package livestreams ports DysonNetwork.Sphere/Live: LiveKit rooms, RTMP/WHIP
// ingress, room-composite and HLS egress, live chat, awards and the ingress
// cleanup job. LiveKit is driven over its Twirp/JSON API with HS256 JWTs signed
// by LiveStream:ApiSecret, exactly like the C# LiveKit server SDK does.
package livestreams

import (
	"context"
	"time"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// ModuleID is the livestreams module's registry ID.
const ModuleID = "app.solian.sphere.livestreams"

// publishersModuleID is the feature module that owns publisher rows and
// memberships.
const publishersModuleID = "app.solian.sphere.publishers"

// Permission keys declared by the C# LiveStreamController.
const (
	permCreate   = "live.streams.create"
	permUpdate   = "live.streams.update"
	permDelete   = "live.streams.delete"
	permStart    = "live.streams.start"
	permEnd      = "live.streams.end"
	permEgress   = "live.streams.egress"
	permHLS      = "live.streams.hls"
	permPin      = "live.streams.pin"
	permModerate = "live.streams.chat.moderate"
	permAwards   = "live.streams.awards"
	permThumb    = "live.streams.thumbnail"
)

// Factory returns the livestreams module factory.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{publishersModuleID},
		Ports: []module.PortRequirement{
			{ID: "files", Optional: true},
			{ID: "accounts", Optional: true},
			{ID: "payments", Optional: true},
		},
		Models: []any{
			store.LiveStreamEntity{},
			store.LiveStreamChatMessageEntity{},
			store.LiveStreamAwardEntity{},
		},
		SQL: []module.SchemaHook{module.DDL(
			`CREATE INDEX IF NOT EXISTS idx_live_streams_publisher ON live_streams (publisher_id, created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_live_streams_active ON live_streams (status, visibility, started_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_live_stream_chat_stream ON live_stream_chat_messages (live_stream_id, created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_live_stream_chat_sender ON live_stream_chat_messages (live_stream_id, sender_id, timeout_until)`,
			`CREATE INDEX IF NOT EXISTS idx_live_stream_awards_stream ON live_stream_awards (live_stream_id, created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_live_stream_awards_account ON live_stream_awards (live_stream_id, account_id)`,
		)},
		Capabilities: []string{
			permCreate, permUpdate, permDelete, permStart, permEnd,
			permEgress, permHLS, permPin, permModerate, permAwards, permThumb,
		},
		New: func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	svc *service
}

func (i *instance) Configure(ctx context.Context, host *module.Host[core.Services]) error {
	svc := newService(host.Core, host.Ports, host.Log)
	i.svc = svc

	// Route order follows the C# attribute routing; static segments are
	// registered before :id so gin does not shadow them.
	api := host.API.Group("/livestreams")

	api.POST("", authn.RequireAuth(), svc.create)
	api.GET("", svc.list)
	api.GET("/publisher/:publisherId", svc.listByPublisher)

	api.POST("/:id/start", authn.RequireAuth(), svc.core.Auth.RequirePermission(permStart), svc.start)
	api.POST("/:id/egress", authn.RequireAuth(), svc.core.Auth.RequirePermission(permEgress), svc.startEgress)
	api.POST("/:id/egress/stop", authn.RequireAuth(), svc.core.Auth.RequirePermission(permEgress), svc.stopEgress)
	api.POST("/:id/hls", authn.RequireAuth(), svc.core.Auth.RequirePermission(permHLS), svc.startHLS)
	api.POST("/:id/hls/stop", authn.RequireAuth(), svc.core.Auth.RequirePermission(permHLS), svc.stopHLS)
	api.POST("/:id/end", authn.RequireAuth(), svc.core.Auth.RequirePermission(permEnd), svc.end)
	api.PATCH("/:id/thumbnail", authn.RequireAuth(), svc.core.Auth.RequirePermission(permThumb), svc.updateThumbnail)
	api.PATCH("/:id", authn.RequireAuth(), svc.core.Auth.RequirePermission(permUpdate), svc.update)
	api.DELETE("/:id", authn.RequireAuth(), svc.core.Auth.RequirePermission(permDelete), svc.delete)
	api.GET("/:id/token", authn.RequireAuth(), svc.token)
	api.GET("/:id/details", svc.details)
	api.GET("/:id/chat", authn.RequireAuth(), svc.listChat)
	api.POST("/:id/chat", authn.RequireAuth(), svc.core.Auth.RequirePermission(permPin), svc.sendChat)
	api.DELETE("/:id/chat/:messageId", authn.RequireAuth(), svc.core.Auth.RequirePermission(permModerate), svc.deleteChat)
	api.POST("/:id/chat/:messageId/timeout", authn.RequireAuth(), svc.core.Auth.RequirePermission(permModerate), svc.timeoutChat)
	api.GET("/:id/awards", svc.listAwards)
	api.GET("/:id/awards/active", svc.activeAwards)
	api.GET("/:id/awards/leaderboard", svc.awardLeaderboard)
	api.POST("/:id/awards", authn.RequireAuth(), svc.core.Auth.RequirePermission(permAwards), svc.award)
	api.GET("/:id", svc.get)

	if host.Core.Jobs != nil {
		host.Core.Jobs.Every("livestream_ingress_cleanup", 5*time.Minute, svc.cleanupIngresses)
	}

	// Wallet order confirmations arrive on the payment_orders subject; the C#
	// service registered the same listener for livestreams.award orders.
	if host.Core.NATS != nil && host.Core.NATS.Enabled() {
		go svc.consumePaymentEvents(ctx)
	}
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }
