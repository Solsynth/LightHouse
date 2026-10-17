// Package profiles mounts the account profile and public account surface.
package profiles

import (
	"context"

	"src.solsynth.dev/sosys/stargate/internal/grpcserver"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/profilectl"
	"src.solsynth.dev/sosys/stargate/internal/module"
	"src.solsynth.dev/sosys/stargate/internal/modules/common"
	"src.solsynth.dev/sosys/stargate/internal/ports"
)

// Factory returns the compiled module descriptor.
func Factory() module.Factory {
	return module.Factory{
		ID:             "app.solian.id.profiles",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.accounts"},
		Capabilities:   []string{"accounts.profile", "accounts.connections"},
		Ports: []module.PortRequirement{
			{ID: ports.PortFiles, Optional: true},
			{ID: ports.PortBadges, Optional: true},
			{ID: ports.PortPerks, Optional: true},
		},
		New: func() module.Instance { return &instance{} },
	}
}

type instance struct{}

func (i *instance) Configure(_ context.Context, host *module.Host) error {
	profilectl.Register(host.API, common.Profile(host.Core, ports.FromRegistry(host.Ports)))
	grpcserver.RegisterProfiles(host.GRPC, common.GRPC(host.Core))
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }
