// Package auth mounts the authentication surface: challenges, tokens,
// registration, password recovery, WebAuthn discovery and the auth gRPC
// service. It is the only required module.
package auth

import (
	"context"

	"src.solsynth.dev/sosys/stargate/internal/grpcserver"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/authctl"
	"src.solsynth.dev/sosys/stargate/internal/module"
	"src.solsynth.dev/sosys/stargate/internal/modules/common"
	"src.solsynth.dev/sosys/stargate/internal/ports"
)

// Factory returns the compiled module descriptor.
func Factory() module.Factory {
	return module.Factory{
		ID:             "app.solian.id.auth",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Required:       true,
		Capabilities: []string{
			"auth", "auth.challenge", "auth.passkey", "auth.session",
			"auth.token", "auth.sudo", "auth.captcha", "auth.qr-login",
			"auth.api-keys",
		},
		Ports: []module.PortRequirement{
			{ID: ports.PortPush, Optional: true},
			{ID: ports.PortNfcTokens, Optional: true},
			{ID: ports.PortVerificationSpells, Optional: true},
		},
		New: func() module.Instance { return &instance{} },
	}
}

type instance struct{}

func (i *instance) Configure(_ context.Context, host *module.Host) error {
	authctl.Register(host.API, common.Auth(host.Core, ports.FromRegistry(host.Ports)))
	authctl.RegisterWellKnown(host.Engine, host.Core.Config)
	grpcserver.RegisterAuth(host.GRPC, common.GRPC(host.Core))
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }
