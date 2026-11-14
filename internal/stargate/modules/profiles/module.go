// Package profiles mounts the account profile and public account surface.
package profiles

import (
	"context"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/stargate/core"
	"src.solsynth.dev/sosys/stargate/grpcserver"
	"src.solsynth.dev/sosys/stargate/httpserver/profilectl"
	"src.solsynth.dev/sosys/stargate/httpserver/statusctl"
	"src.solsynth.dev/sosys/stargate/modules/common"
	"src.solsynth.dev/sosys/stargate/ports"
	"src.solsynth.dev/sosys/stargate/store"
)

// Factory returns the compiled module descriptor.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             "app.solian.id.profiles",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.accounts"},
		Capabilities: []string{
			"accounts.profile", "accounts.connections", "accounts.statuses",
			"presences.activities", "presences.catalog", "presences.stats",
		},
		Ports: []module.PortRequirement{
			{ID: ports.PortFiles, Optional: true},
			{ID: ports.PortBadges, Optional: true},
			{ID: ports.PortPerks, Optional: true},
		},
		// Account-status and presence-activity tables used by the
		// DyProfileService status RPCs.
		Models: []any{
			&store.AccountStatusEntity{},
			&store.PresenceActivityEntity{},
			&store.PresenceActivityTagEntity{},
			&store.PresenceCatalogItemEntity{},
			&store.PresenceCatalogTagEntity{},
		},
		New: func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct{}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	profilectl.Register(host.API, common.Profile(host.Core, ports.FromRegistry(host.Ports)))
	statusctl.Register(host.API, common.Status(host.Core, ports.FromRegistry(host.Ports)))
	grpcserver.RegisterProfiles(host.GRPC, common.GRPC(host.Core))
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }
