// Package social mounts the external social-login providers and their OAuth
// callbacks.
package social

import (
	"context"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/stargate/core"
	"src.solsynth.dev/sosys/stargate/httpserver/socialctl"
	"src.solsynth.dev/sosys/stargate/modules/common"
)

// Factory returns the compiled module descriptor.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             "app.solian.id.social",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.auth", "app.solian.id.accounts"},
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct{}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	socialctl.Register(host.API, common.Social(host.Core))
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }
