// Package passkitmod wires the ported Passport Apple Wallet domain into
// Stargate as a feature module: the apple_passes/apple_pass_registrations
// schema, the /api/passkit/v1 Web Service endpoints and the signed member
// .pkpass download.
package passkitmod

import (
	"context"
	"errors"
	"log/slog"

	"src.solsynth.dev/sosys/stargate/httpserver/passkitctl"
	"src.solsynth.dev/sosys/stargate/module"
	"src.solsynth.dev/sosys/stargate/passkit"
	"src.solsynth.dev/sosys/stargate/ports"
)

// Factory returns the compiled module descriptor.
func Factory() module.Factory {
	return module.Factory{
		ID:             passkit.ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.auth", "app.solian.id.accounts"},
		Capabilities:   []string{"passkit", "passkit.member-pass"},
		Ports: []module.PortRequirement{
			{ID: ports.PortPerks, Optional: true},
			{ID: ports.PortFiles, Optional: true},
		},
		Models: passkit.Models(),
		SQL:    schemaHooks(),
		New:    func() module.Instance { return &instance{} },
	}
}

func schemaHooks() []module.SchemaHook {
	hooks := passkit.Hooks()
	out := make([]module.SchemaHook, 0, len(hooks))
	for _, hook := range hooks {
		out = append(out, module.SchemaHook(hook))
	}
	return out
}

type instance struct{}

func (i *instance) Configure(_ context.Context, host *module.Host) error {
	if host == nil || host.Core == nil || host.Core.DB == nil {
		return errors.New("passkit: missing host core database")
	}
	set := ports.FromRegistry(host.Ports)
	settings := passkit.SettingsFromMap(host.ModuleSettings(passkit.ModuleID))

	service := passkit.NewService(passkit.Deps{
		Store:    host.Core.Store,
		Settings: settings,
		SiteURL:  siteURL(host),
		Perks:    set.Perks,
		Files:    set.Files,
		Log:      coreLog(host),
	})
	passkitctl.Register(host.API, passkitctl.Deps{
		Service: service,
		Log:     coreLog(host),
	})
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

func siteURL(host *module.Host) string {
	if host == nil || host.Core == nil || host.Core.Config == nil {
		return ""
	}
	return host.Core.Config.SiteUrl
}

func coreLog(host *module.Host) *slog.Logger {
	if host != nil && host.Core != nil && host.Core.Log != nil {
		return host.Core.Log
	}
	return slog.Default()
}
