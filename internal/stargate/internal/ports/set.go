package ports

import "src.solsynth.dev/sosys/stargate/internal/module"

// Set is the resolved view of every port. Nil fields mean no provider is
// enabled; consumers must treat that as "feature unavailable".
type Set struct {
	Files              Files
	Badges             Badges
	Verification       Verification
	Perks              Perks
	Email              Email
	Push               Push
	Presence           Presence
	OAuthClients       OAuthClients
	NfcTokens          NfcTokens
	VerificationSpells VerificationSpells
}

// FromRegistry resolves every known port. Missing providers stay nil.
func FromRegistry(registry *module.PortRegistry) *Set {
	if registry == nil {
		return &Set{}
	}
	set := &Set{}
	set.Files, _ = module.Get[Files](registry, PortFiles)
	set.Badges, _ = module.Get[Badges](registry, PortBadges)
	set.Verification, _ = module.Get[Verification](registry, PortVerification)
	set.Perks, _ = module.Get[Perks](registry, PortPerks)
	set.Email, _ = module.Get[Email](registry, PortEmail)
	set.Push, _ = module.Get[Push](registry, PortPush)
	set.Presence, _ = module.Get[Presence](registry, PortPresence)
	set.OAuthClients, _ = module.Get[OAuthClients](registry, PortOAuthClients)
	set.NfcTokens, _ = module.Get[NfcTokens](registry, PortNfcTokens)
	set.VerificationSpells, _ = module.Get[VerificationSpells](registry, PortVerificationSpells)
	return set
}
