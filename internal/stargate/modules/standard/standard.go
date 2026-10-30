// Package standard lists the modules compiled into the default Stargate
// binary. A private build can append factories from a git submodule; a
// third-party module should use its own reverse-DNS ID.
package standard

import (
	"src.solsynth.dev/sosys/stargate/module"
	"src.solsynth.dev/sosys/stargate/modules/accounts"
	"src.solsynth.dev/sosys/stargate/modules/admin"
	"src.solsynth.dev/sosys/stargate/modules/auth"
	"src.solsynth.dev/sosys/stargate/modules/e2ee"
	"src.solsynth.dev/sosys/stargate/modules/integrations"
	"src.solsynth.dev/sosys/stargate/modules/oauthapps"
	"src.solsynth.dev/sosys/stargate/modules/oidc"
	"src.solsynth.dev/sosys/stargate/modules/presences"
	"src.solsynth.dev/sosys/stargate/modules/profiles"
	"src.solsynth.dev/sosys/stargate/modules/relationships"
	"src.solsynth.dev/sosys/stargate/modules/social"
	"src.solsynth.dev/sosys/stargate/modules/spells"
	walletmod "src.solsynth.dev/sosys/stargate/modules/wallet"
	"src.solsynth.dev/sosys/stargate/modules/wellknown"
)

// Modules returns every compiled-in factory.
func Modules() []module.Factory {
	return []module.Factory{
		// Identity features.
		auth.Factory(),
		accounts.Factory(),
		profiles.Factory(),
		presences.Factory(),
		relationships.Factory(),
		spells.Factory(),
		social.Factory(),
		oidc.Factory(),
		oauthapps.Factory(),

		// Other features.
		e2ee.Factory(),
		admin.Factory(),
		wellknown.Factory(),
		walletmod.Factory(),

		// Ecosystem adapters.
		integrations.Drive(),
		integrations.Wallet(),
		integrations.Pass(),
		integrations.Ring(),
		integrations.Blade(),
		integrations.Develop(),
	}
}
