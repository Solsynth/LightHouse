// Package presences owns the account-status and presence-activity surface that
// Passport used to serve: /api/accounts/{me,:name}/statuses, /api/activities
// and the two DyProfileService status RPCs. Its five tables are migrated by
// this module only, so a deployment that disables it never creates them.
package presences

import (
	"context"

	"src.solsynth.dev/sosys/stargate/httpserver/statusctl"
	"src.solsynth.dev/sosys/stargate/module"
	"src.solsynth.dev/sosys/stargate/modules/common"
	"src.solsynth.dev/sosys/stargate/ports"
	"src.solsynth.dev/sosys/stargate/store"
)

// Factory returns the compiled module descriptor.
func Factory() module.Factory {
	return module.Factory{
		ID:             "app.solian.id.presences",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.accounts", "app.solian.id.profiles"},
		Capabilities: []string{
			"accounts.statuses", "presences.activities", "presences.catalog",
			"presences.stats",
		},
		Ports: []module.PortRequirement{
			{ID: ports.PortFiles, Optional: true},
			{ID: ports.PortPresence, Optional: true},
		},
		Models: []any{
			&store.AccountStatusEntity{},
			&store.PresenceActivityEntity{},
			&store.PresenceActivityTagEntity{},
			&store.PresenceCatalogItemEntity{},
			&store.PresenceCatalogTagEntity{},
		},
		New: func() module.Instance { return &instance{} },
	}
}

type instance struct{}

func (i *instance) Configure(_ context.Context, host *module.Host) error {
	statusctl.Register(host.API, common.Status(host.Core, ports.FromRegistry(host.Ports)))
	// DyProfileService itself is registered by the profiles module; the two
	// status RPCs it delegates are served from the shared dependencies, so
	// nothing is mounted on host.GRPC here.
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }
