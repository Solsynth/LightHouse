// Package wellknown mounts the non-/api documentation endpoints.
package wellknown

import (
	"context"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/stargate/core"
	"src.solsynth.dev/sosys/stargate/httpserver/wellknownctl"
)

// Factory returns the compiled module descriptor.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             "app.solian.wellknown",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct{}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	wellknownctl.RegisterTop(host.Engine, wellknownctl.Deps{})
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }
