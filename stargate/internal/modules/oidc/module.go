// Package oidc mounts Stargate's own OAuth/OIDC provider endpoints.
package oidc

import (
	"context"

	"src.solsynth.dev/sosys/stargate/internal/httpserver/oidcctl"
	"src.solsynth.dev/sosys/stargate/internal/module"
	"src.solsynth.dev/sosys/stargate/internal/modules/common"
	"src.solsynth.dev/sosys/stargate/internal/ports"
)

// Factory returns the compiled module descriptor.
func Factory() module.Factory {
	return module.Factory{
		ID:             "app.solian.id.oidc",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.auth", "app.solian.id.accounts"},
		New:            func() module.Instance { return &instance{} },
	}
}

type instance struct{}

func (i *instance) Configure(_ context.Context, host *module.Host) error {
	deps := common.OIDC(host.Core, ports.FromRegistry(host.Ports))
	oidcctl.Register(host.API, deps)
	oidcctl.RegisterTop(host.Engine, deps)
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }
