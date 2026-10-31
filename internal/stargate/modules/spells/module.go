// Package spells mounts the magic-spell HTTP surface. Spell storage and email
// delivery remain in the shared service for now; later phases move them into
// this module's schema and provide verificationSpells as a port.
package spells

import (
	"context"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/stargate/core"
	"src.solsynth.dev/sosys/stargate/httpserver/spellctl"
	"src.solsynth.dev/sosys/stargate/modules/common"
	"src.solsynth.dev/sosys/stargate/ports"
)

// Factory returns the compiled module descriptor.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             "app.solian.id.spells",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.accounts"},
		Ports:          []module.PortRequirement{{ID: ports.PortEmail, Optional: true}},
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct{}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	spellctl.Register(host.API, common.Spell(host.Core))
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }
