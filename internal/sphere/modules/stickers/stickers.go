// Package stickers ports the Sphere sticker catalogue: packs, sticker content,
// identifier lookup, per-account ownership and the popularity counter.
package stickers

import (
	"context"
	"time"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// ModuleID is the stickers module's registry ID.
const ModuleID = "app.solian.sphere.stickers"

// Factory returns the stickers module factory.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.sphere.publishers"},
		Ports:          []module.PortRequirement{{ID: "files", Optional: true}},
		Models: []any{
			store.StickerEntity{},
			store.StickerPackEntity{},
			store.StickerPackOwnershipEntity{},
		},
		SQL: []module.SchemaHook{module.DDL(
			`CREATE INDEX IF NOT EXISTS idx_stickers_slug ON stickers (slug)`,
			`CREATE INDEX IF NOT EXISTS idx_stickers_pack ON stickers (pack_id, "order")`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_sticker_packs_prefix ON sticker_packs (prefix, deleted_at)`,
			`CREATE INDEX IF NOT EXISTS idx_sticker_pack_ownerships_account ON sticker_pack_ownerships (account_id, "order")`,
		)},
		Capabilities: []string{
			"stickers.packs.create", "stickers.packs.update", "stickers.packs.own",
			"stickers.packs.order", "stickers.create", "stickers.content.update",
		},
		New: func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	svc *service
}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	svc := newService(host.Core, host.Ports)
	i.svc = svc

	api := host.API.Group("/stickers")
	api.GET("", svc.listPacks)
	api.GET("/me", authn.RequireAuth(), svc.listOwnedPacks)
	api.GET("/search", svc.searchStickers)
	api.GET("/by-prefix/:prefix", svc.getPackByPrefix)
	api.POST("/lookup/batch", svc.lookupBatch)
	api.GET("/lookup/:identifier", svc.lookupSticker)
	api.GET("/lookup/:identifier/open", svc.openSticker)

	api.POST("", authn.RequireAuth(), svc.core.Auth.RequirePermission("stickers.packs.create"), svc.createPack)
	api.PATCH("/me/order", authn.RequireAuth(), svc.core.Auth.RequirePermission("stickers.packs.order"), svc.reorderOwnedPacks)

	api.GET("/:packId", svc.getPack)
	api.PATCH("/:packId", authn.RequireAuth(), svc.core.Auth.RequirePermission("stickers.packs.update"), svc.updatePack)
	api.DELETE("/:packId", authn.RequireAuth(), svc.deletePack)

	api.GET("/:packId/content", svc.listStickers)
	api.POST("/:packId/content", authn.RequireAuth(), svc.core.Auth.RequirePermission("stickers.create"), svc.createSticker)
	api.PATCH("/:packId/content/batch/rendering-settings", authn.RequireAuth(), svc.batchRenderingSettings)
	api.PATCH("/:packId/content/order", authn.RequireAuth(), svc.core.Auth.RequirePermission("stickers.content.update"), svc.reorderStickers)
	api.GET("/:packId/content/:id", svc.getSticker)
	api.PATCH("/:packId/content/:id", authn.RequireAuth(), svc.updateSticker)
	api.DELETE("/:packId/content/:id", authn.RequireAuth(), svc.deleteSticker)

	api.GET("/:packId/own", authn.RequireAuth(), svc.getOwnership)
	api.POST("/:packId/own", authn.RequireAuth(), svc.core.Auth.RequirePermission("stickers.packs.own"), svc.acquirePack)
	api.DELETE("/:packId/own", authn.RequireAuth(), svc.core.Auth.RequirePermission("stickers.packs.own"), svc.releasePack)

	if host.GRPC != nil {
		gen.RegisterDyStickerServiceServer(host.GRPC, &stickerServer{svc: svc})
	}
	if host.Core.Jobs != nil {
		host.Core.Jobs.Every("StickerPopularityFlush", time.Minute, func(ctx context.Context) error {
			return svc.popularity.flush(ctx, svc.db)
		})
	}
	return nil
}

func (i *instance) Stop(ctx context.Context) error {
	if i.svc == nil {
		return nil
	}
	flushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = i.svc.popularity.flush(flushCtx, i.svc.db)
	return nil
}
