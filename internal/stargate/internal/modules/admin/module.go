// Package admin mounts the administrative console and moderation routes.
package admin

import (
	"context"

	"src.solsynth.dev/sosys/stargate/internal/httpserver/adminctl"
	"src.solsynth.dev/sosys/stargate/internal/module"
	"src.solsynth.dev/sosys/stargate/internal/modules/common"
	"src.solsynth.dev/sosys/stargate/internal/ports"
)

// Factory returns the compiled module descriptor.
func Factory() module.Factory {
	return module.Factory{
		ID:             "app.solian.admin",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.accounts"},
		Capabilities: []string{
			"admin.cache", "admin.accounts", "admin.accounts.devices",
			"admin.accounts.sessions", "admin.accounts.contacts",
			"admin.accounts.factors", "admin.accounts.punishments",
			"admin.stats.geography", "admin.permissions", "admin.permissions.groups",
			"accounts.action-log", "accounts.punishments",
		},
		Ports: []module.PortRequirement{
			{ID: ports.PortPush, Optional: true},
			{ID: ports.PortEmail, Optional: true},
		},
		New: func() module.Instance { return &instance{} },
	}
}

type instance struct{}

func (i *instance) Configure(_ context.Context, host *module.Host) error {
	adminctl.Register(host.API, common.Admin(host.Core))
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }
