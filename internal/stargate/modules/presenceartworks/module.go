// Package presenceartworksmod wires the ported Passport presence-artwork
// domain into Stargate as a feature module: the presence_artworks table, the
// S3/MinIO bucket binding, /api/presence/artworks and the hourly retention
// sweep.
package presenceartworksmod

import (
	"context"
	"errors"
	"log/slog"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/stargate/core"
	"src.solsynth.dev/sosys/stargate/httpserver/presenceartworksctl"
	"src.solsynth.dev/sosys/stargate/presenceartworks"
)

// Factory returns the compiled module descriptor.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             presenceartworks.ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.auth", "app.solian.id.accounts"},
		Capabilities:   []string{"presences.artworks"},
		Models:         presenceartworks.Models(),
		SQL:            schemaHooks(),
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

func schemaHooks() []module.SchemaHook {
	hooks := presenceartworks.Hooks()
	out := make([]module.SchemaHook, 0, len(hooks))
	for _, hook := range hooks {
		out = append(out, module.SchemaHook(hook))
	}
	return out
}

type instance struct {
	stop func()
}

func (i *instance) Configure(ctx context.Context, host *module.Host[core.Services]) error {
	if host == nil || host.Core == nil || host.Core.DB == nil {
		return errors.New("presenceartworks: missing host core database")
	}
	log := coreLog(host)
	settings := presenceartworks.SettingsFromMap(host.ModuleSettings(presenceartworks.ModuleID))
	service, err := presenceartworks.NewService(host.Core.DB, settings, log)
	if err != nil {
		return err
	}
	if service.Storage == nil {
		log.Warn("presence artwork storage is not configured; routes will answer 503",
			"module", presenceartworks.ModuleID)
	}
	presenceartworksctl.Register(host.API, presenceartworksctl.Deps{
		Service: service,
		Perm:    host.Core.Perm,
		Log:     log,
	})
	i.stop = presenceartworks.StartCleanupJob(ctx, service, 0, log)
	return nil
}

func (i *instance) Stop(context.Context) error {
	if i.stop != nil {
		i.stop()
		i.stop = nil
	}
	return nil
}

func coreLog(host *module.Host[core.Services]) *slog.Logger {
	if host != nil && host.Core != nil && host.Core.Log != nil {
		return host.Core.Log
	}
	return slog.Default()
}
