// Package wellknown mounts the non-/api documentation endpoints.
package wellknown

import (
	"context"

	"src.solsynth.dev/sosys/stargate/internal/httpserver/wellknownctl"
	"src.solsynth.dev/sosys/stargate/internal/module"
)

// Factory returns the compiled module descriptor.
func Factory() module.Factory {
	return module.Factory{
		ID:             "app.solian.wellknown",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		New:            func() module.Instance { return &instance{} },
	}
}

type instance struct{}

func (i *instance) Configure(_ context.Context, host *module.Host) error {
	wellknownctl.RegisterTop(host.Engine, wellknownctl.Deps{})
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }
