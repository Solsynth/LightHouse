// Package standard lists the modules compiled into the default Stargate
// binary. A private build can append factories from a git submodule; a
// third-party module should use its own reverse-DNS ID.
package standard

import (
	"src.solsynth.dev/sosys/stargate/module"
	accounteventsmod "src.solsynth.dev/sosys/stargate/modules/accountevents"
	accountrecordsmod "src.solsynth.dev/sosys/stargate/modules/accountrecords"
	"src.solsynth.dev/sosys/stargate/modules/accounts"
	"src.solsynth.dev/sosys/stargate/modules/admin"
	"src.solsynth.dev/sosys/stargate/modules/auth"
	domaintrustmod "src.solsynth.dev/sosys/stargate/modules/domaintrust"
	"src.solsynth.dev/sosys/stargate/modules/e2ee"
	"src.solsynth.dev/sosys/stargate/modules/integrations"
	ipcheckmod "src.solsynth.dev/sosys/stargate/modules/ipcheck"
	nfcmod "src.solsynth.dev/sosys/stargate/modules/nfc"
	"src.solsynth.dev/sosys/stargate/modules/oauthapps"
	"src.solsynth.dev/sosys/stargate/modules/oidc"
	passkitmod "src.solsynth.dev/sosys/stargate/modules/passkit"
	presenceartworksmod "src.solsynth.dev/sosys/stargate/modules/presenceartworks"
	presenceprovidersmod "src.solsynth.dev/sosys/stargate/modules/presenceproviders"
	"src.solsynth.dev/sosys/stargate/modules/presences"
	"src.solsynth.dev/sosys/stargate/modules/profiles"
	progressionmod "src.solsynth.dev/sosys/stargate/modules/progression"
	purchasesmod "src.solsynth.dev/sosys/stargate/modules/purchases"
	realmmod "src.solsynth.dev/sosys/stargate/modules/realm"
	"src.solsynth.dev/sosys/stargate/modules/relationships"
	rewindmod "src.solsynth.dev/sosys/stargate/modules/rewind"
	"src.solsynth.dev/sosys/stargate/modules/social"
	"src.solsynth.dev/sosys/stargate/modules/spells"
	ticketsmod "src.solsynth.dev/sosys/stargate/modules/tickets"
	walletmod "src.solsynth.dev/sosys/stargate/modules/wallet"
	wellbeingmod "src.solsynth.dev/sosys/stargate/modules/wellbeing"
	"src.solsynth.dev/sosys/stargate/modules/wellknown"
)

// Modules returns every compiled-in factory.
func Modules() []module.Factory {
	return []module.Factory{
		// Identity features.
		auth.Factory(),
		accounts.Factory(),
		profiles.Factory(),
		accountrecordsmod.Factory(),
		accounteventsmod.Factory(),
		presences.Factory(),
		presenceprovidersmod.Factory(),
		progressionmod.Factory(),
		relationships.Factory(),
		spells.Factory(),
		social.Factory(),
		oidc.Factory(),
		oauthapps.Factory(),
		realmmod.Factory(),
		domaintrustmod.Factory(),
		nfcmod.Factory(),
		passkitmod.Factory(),
		presenceartworksmod.Factory(),
		purchasesmod.Factory(),
		rewindmod.Factory(),
		ipcheckmod.Factory(),

		// Other features.
		e2ee.Factory(),
		admin.Factory(),
		ticketsmod.Factory(),
		wellbeingmod.Factory(),
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
