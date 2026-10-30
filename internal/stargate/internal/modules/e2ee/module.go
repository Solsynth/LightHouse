// Package e2ee mounts the E2EE/MLS HTTP and gRPC surface. Its tables move to
// this module in the schema phase.
package e2ee

import (
	"context"

	"src.solsynth.dev/sosys/stargate/internal/grpcserver"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/e2eectl"
	"src.solsynth.dev/sosys/stargate/internal/module"
	"src.solsynth.dev/sosys/stargate/internal/modules/common"
)

// Factory returns the compiled module descriptor.
func Factory() module.Factory {
	return module.Factory{
		ID:             "app.solian.e2ee",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.accounts"},
		Capabilities:   []string{"e2ee", "e2ee.mls"},
		New:            func() module.Instance { return &instance{} },
	}
}

type instance struct{}

func (i *instance) Configure(_ context.Context, host *module.Host) error {
	deps := common.E2EE(host.Core)
	e2eectl.Register(host.API, deps)

	grpcDeps := common.GRPC(host.Core)
	grpcDeps.E2ee = e2eectl.NewService(host.Core.Store, host.Core.NATS, host.Core.Clients, host.Core.Log)
	grpcserver.RegisterMls(host.GRPC, grpcDeps)
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }
