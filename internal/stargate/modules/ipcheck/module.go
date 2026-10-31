// Package ipcheckmod wires the ported Passport IP diagnostics into Stargate
// as a feature module: the anonymous geo lookup and the admin IP/header dump.
// It owns no schema.
package ipcheckmod

import (
	"context"

	"src.solsynth.dev/sosys/stargate/httpserver/ipcheckctl"
	"src.solsynth.dev/sosys/stargate/ipcheck"
	"src.solsynth.dev/sosys/stargate/module"
)

// Factory returns the compiled module descriptor.
func Factory() module.Factory {
	return module.Factory{
		ID:             ipcheck.ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.auth", "app.solian.id.accounts"},
		Capabilities:   []string{"ip-check", "ip.geo"},
		New:            func() module.Instance { return &instance{} },
	}
}

type instance struct{}

func (i *instance) Configure(_ context.Context, host *module.Host) error {
	ipcheckctl.Register(host.API, ipcheckctl.Deps{
		Geo:  geoService(host),
		Perm: host.Core.Perm,
	})
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

// geoService adapts *geo.Service to the narrow controller contract; a nil
// service (missing GeoIP database configuration) stays nil.
func geoService(host *module.Host) ipcheck.GeoService {
	if host == nil || host.Core == nil || host.Core.Geo == nil {
		return nil
	}
	return host.Core.Geo
}
