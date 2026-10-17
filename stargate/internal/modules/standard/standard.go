// Package standard lists the modules compiled into the default Stargate
// binary. A private build can append factories from a git submodule; a
// third-party module should use its own reverse-DNS ID.
package standard

import (
	"src.solsynth.dev/sosys/stargate/internal/module"
	"src.solsynth.dev/sosys/stargate/internal/modules/accounts"
	"src.solsynth.dev/sosys/stargate/internal/modules/admin"
	"src.solsynth.dev/sosys/stargate/internal/modules/auth"
	"src.solsynth.dev/sosys/stargate/internal/modules/e2ee"
	"src.solsynth.dev/sosys/stargate/internal/modules/integrations"
	"src.solsynth.dev/sosys/stargate/internal/modules/oidc"
	"src.solsynth.dev/sosys/stargate/internal/modules/profiles"
	"src.solsynth.dev/sosys/stargate/internal/modules/relationships"
	"src.solsynth.dev/sosys/stargate/internal/modules/social"
	"src.solsynth.dev/sosys/stargate/internal/modules/spells"
	"src.solsynth.dev/sosys/stargate/internal/modules/wellknown"
)

// Modules returns every compiled-in factory.
func Modules() []module.Factory {
	return []module.Factory{
		// Identity features.
		auth.Factory(),
		accounts.Factory(),
		profiles.Factory(),
		relationships.Factory(),
		spells.Factory(),
		social.Factory(),
		oidc.Factory(),

		// Other features.
		e2ee.Factory(),
		admin.Factory(),
		wellknown.Factory(),

		// Ecosystem adapters.
		integrations.Drive(),
		integrations.Wallet(),
		integrations.Pass(),
		integrations.Ring(),
		integrations.Blade(),
		integrations.Develop(),
	}
}
