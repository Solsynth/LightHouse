// Package rewindmod wires the ported Passport account-rewind domain into
// Stargate as a feature module: the rewind_points table, /api/rewind and the
// optional sphere/messager DyRewindService clients.
package rewindmod

import (
	"context"
	"errors"
	"log/slog"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/stargate/core"
	"src.solsynth.dev/sosys/stargate/httpserver/rewindctl"
	"src.solsynth.dev/sosys/stargate/rewind"
)

// Factory returns the compiled module descriptor.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             rewind.ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.auth", "app.solian.id.accounts"},
		Capabilities:   []string{"rewind"},
		Models:         rewind.Models(),
		SQL:            schemaHooks(),
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

func schemaHooks() []module.SchemaHook {
	hooks := rewind.Hooks()
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
		return errors.New("rewind: missing host core database")
	}
	log := coreLog(host)
	settings := rewind.SettingsFromMap(host.ModuleSettings(rewind.ModuleID))
	if settings.SphereGRPC == "" {
		log.Info("rewind: sphereGrpc is not configured; sphere rewind data is omitted",
			"module", rewind.ModuleID)
	}
	if settings.MessagerGRPC == "" {
		log.Info("rewind: messagerGrpc is not configured; messager rewind data is omitted",
			"module", rewind.ModuleID)
	}
	sources, err := rewind.NewSources(settings, host.Core.Config.ClientFiles())
	if err != nil {
		return err
	}
	service := rewind.NewService(host.Core.DB, host.Core.Store, sources, log)
	rewindctl.Register(host.API, rewindctl.Deps{
		Service: service,
		Perm:    host.Core.Perm,
		Log:     log,
	})
	if sources != nil {
		i.stop = sources.Close
	}
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
