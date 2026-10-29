// Package social mounts the external social-login providers and their OAuth
// callbacks.
package social

import (
	"context"

	"src.solsynth.dev/sosys/stargate/internal/httpserver/socialctl"
	"src.solsynth.dev/sosys/stargate/internal/module"
	"src.solsynth.dev/sosys/stargate/internal/modules/common"
)

// Factory returns the compiled module descriptor.
func Factory() module.Factory {
	return module.Factory{
		ID:             "app.solian.id.social",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.auth", "app.solian.id.accounts"},
		New:            func() module.Instance { return &instance{} },
	}
}

type instance struct{}

func (i *instance) Configure(_ context.Context, host *module.Host) error {
	socialctl.Register(host.API, common.Social(host.Core))
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }
