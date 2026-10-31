// Package wellbeingmod wires Stargate's account activity statistics and
// night-owl reminder surface into a feature module. The domain lives in
// internal/wellbeing; this package owns the factory and the lifecycle.
package wellbeingmod

import (
	"context"
	"errors"
	"log/slog"

	"src.solsynth.dev/sosys/stargate/httpserver/wellbeingctl"
	"src.solsynth.dev/sosys/stargate/module"
	"src.solsynth.dev/sosys/stargate/ports"
	"src.solsynth.dev/sosys/stargate/wellbeing"
)

// Factory returns the compiled module descriptor. The module owns no tables:
// it reads the core account/profile tables and uses Redis for dedupe.
func Factory() module.Factory {
	return module.Factory{
		ID:             wellbeing.ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.accounts", "app.solian.id.profiles"},
		Capabilities:   []string{"accounts.stats", "accounts.wellbeing"},
		Ports: []module.PortRequirement{
			{ID: ports.PortPush, Optional: true},
		},
		New: func() module.Instance { return &instance{} },
	}
}

type instance struct {
	stop func()
}

func (i *instance) Configure(ctx context.Context, host *module.Host) error {
	if host == nil || host.Core == nil || host.Core.DB == nil {
		return errors.New("wellbeing: missing host core database")
	}
	log := coreLog(host)
	service := wellbeing.NewService(host.Core.DB, pushPort(host), host.Core.Redis, log)
	wellbeingctl.Register(host.API, wellbeingctl.Deps{
		Service: service,
		Store:   host.Core.Store,
		Perm:    host.Core.Perm,
		Log:     log,
	})
	i.stop = service.StartJobs(ctx)
	return nil
}

func (i *instance) Stop(context.Context) error {
	if i.stop != nil {
		i.stop()
		i.stop = nil
	}
	return nil
}

func coreLog(host *module.Host) *slog.Logger {
	if host != nil && host.Core != nil && host.Core.Log != nil {
		return host.Core.Log
	}
	return slog.Default()
}

// pushPort resolves the optional push provider; nil keeps the HTTP route
// available (503) and the background job idle.
func pushPort(host *module.Host) ports.Push {
	if host == nil || host.Ports == nil {
		return nil
	}
	push := ports.FromRegistry(host.Ports).Push
	if push == nil || !push.Available() {
		return nil
	}
	return push
}
