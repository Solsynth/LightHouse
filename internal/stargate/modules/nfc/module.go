// Package nfcmod wires the ported Passport NFC domain into Stargate as a
// feature module: the nfc_tags schema, the /api/nfc and /api/admin/nfc HTTP
// surface, the DyNfcService gRPC port and the local nfcTokens provider that
// replaces the remote Passport adapter for NFC login.
package nfcmod

import (
	"context"
	"errors"
	"log/slog"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
	"src.solsynth.dev/sosys/stargate/httpserver/nfcctl"
	"src.solsynth.dev/sosys/stargate/module"
	"src.solsynth.dev/sosys/stargate/nfc"
	"src.solsynth.dev/sosys/stargate/ports"
)

// Factory returns the compiled module descriptor.
func Factory() module.Factory {
	return module.Factory{
		ID:             nfc.ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.auth", "app.solian.id.accounts"},
		Capabilities:   []string{"nfc", "nfc.tags", "nfc.admin"},
		Ports: []module.PortRequirement{
			{ID: ports.PortPerks, Optional: true},
			{ID: ports.PortBadges, Optional: true},
		},
		Provides: []module.PortID{ports.PortNfcTokens},
		Models:   nfc.Models(),
		SQL:      schemaHooks(),
		New:      func() module.Instance { return &instance{} },
	}
}

func schemaHooks() []module.SchemaHook {
	hooks := nfc.Hooks()
	out := make([]module.SchemaHook, 0, len(hooks))
	for _, hook := range hooks {
		out = append(out, module.SchemaHook(hook))
	}
	return out
}

type instance struct {
	service *nfc.Service
}

// ProvidePorts publishes the local NFC token validator. It runs before any
// Configure, so the auth module can resolve nfcTokens regardless of order.
func (i *instance) ProvidePorts(_ context.Context, host *module.Host) error {
	service, err := i.ensureService(host)
	if err != nil {
		return err
	}
	module.Provide[ports.NfcTokens](host.Ports, ports.PortNfcTokens, nfc.ModuleID, module.KindFeature,
		&nfc.TokenProvider{Service: service})
	return nil
}

func (i *instance) Configure(_ context.Context, host *module.Host) error {
	service, err := i.ensureService(host)
	if err != nil {
		return err
	}
	log := coreLog(host)

	deps := nfcctl.Deps{
		Service: service,
		Perm:    host.Core.Perm,
		Log:     log,
	}
	nfcctl.Register(host.API, deps)
	nfcctl.RegisterAdmin(host.API, deps)

	if host.GRPC != nil {
		gen.RegisterDyNfcServiceServer(host.GRPC, nfc.NewGRPCService(service))
	}
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

func (i *instance) ensureService(host *module.Host) (*nfc.Service, error) {
	if i.service != nil {
		return i.service, nil
	}
	if host == nil || host.Core == nil || host.Core.DB == nil {
		return nil, errors.New("nfc: missing host core database")
	}
	set := ports.FromRegistry(host.Ports)
	i.service = nfc.NewService(nfc.Deps{
		Store:         host.Core.Store,
		Relationships: host.Core.Relationships,
		Perks:         set.Perks,
		Badges:        set.Badges,
		Log:           coreLog(host),
	})
	return i.service, nil
}

func coreLog(host *module.Host) *slog.Logger {
	if host != nil && host.Core != nil && host.Core.Log != nil {
		return host.Core.Log
	}
	return slog.Default()
}
