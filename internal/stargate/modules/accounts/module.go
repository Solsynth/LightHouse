// Package accounts mounts the account-security surface and the account gRPC
// services.
package accounts

import (
	"context"

	"src.solsynth.dev/sosys/stargate/grpcserver"
	"src.solsynth.dev/sosys/stargate/httpserver/securityctl"
	"src.solsynth.dev/sosys/stargate/module"
	"src.solsynth.dev/sosys/stargate/modules/common"
	"src.solsynth.dev/sosys/stargate/ports"
)

// Factory returns the compiled module descriptor.
func Factory() module.Factory {
	return module.Factory{
		ID:             "app.solian.id.accounts",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.auth"},
		Capabilities: []string{
			"accounts.registration", "accounts.identity", "accounts.factors",
			"accounts.sessions", "accounts.devices", "accounts.contacts",
			"accounts.authorized-apps", "accounts.passkeys", "accounts.connections",
		},
		New: func() module.Instance { return &instance{} },
	}
}

type instance struct{}

func (i *instance) Configure(_ context.Context, host *module.Host) error {
	securityctl.Register(host.API, common.Security(host.Core, ports.FromRegistry(host.Ports)))
	grpcserver.RegisterAccounts(host.GRPC, common.GRPC(host.Core))
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }
