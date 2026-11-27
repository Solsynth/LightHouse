package auth

import (
	"sort"

	"src.solsynth.dev/sosys/stargate/model"
)

// SudoMinTrustworthy is the lockdown elevation floor: the minimum factor
// strength that may satisfy an elevation while the account is in lockdown
// mode. A password (1) or NFC token (1) proves the login secret, not that the
// account holder had to provision something beyond it, so lockdown demands
// more: email/in-app codes (2), TOTP (3), passkeys (4) and QR (3) qualify;
// PIN and recovery codes never do (they have their own flow).
//
// Outside lockdown one factor of any strength suffices — see
// SudoMinTrustworthyFor.
const SudoMinTrustworthy = 2

// SudoMinTrustworthyFor maps a per-account security mode to the elevation
// factor-strength floor. Lockdown keeps the strong-proof floor
// (SudoMinTrustworthy); every other mode accepts any enabled factor (>= 1),
// including the login password — the elevation then re-proves the person at
// the keyboard without demanding a second provisioned factor.
func SudoMinTrustworthyFor(mode model.SecurityMode) int {
	if mode == model.SecurityModeLockdown {
		return SudoMinTrustworthy
	}
	return 1
}

// SudoFactorTypes is the set of an account's enabled factors that can satisfy
// an elevation ("sudo") step at the given strength floor. Strength sets the
// floor, never the step count: an elevation asks for one qualifying factor,
// so a passkey (4) costs the same one step as a password (1).
func SudoFactorTypes(factors []model.AuthFactor, minTrustworthy int) []model.AuthFactorType {
	seen := make(map[model.AuthFactorType]bool, len(factors))
	types := make([]model.AuthFactorType, 0, len(factors))
	for _, f := range factors {
		if f.EnabledAt == nil || f.Trustworthy < minTrustworthy {
			continue
		}
		ft := model.AuthFactorType(f.Type)
		if ft == model.AuthFactorTypePinCode || ft == model.AuthFactorTypeRecoveryCode {
			continue
		}
		if seen[ft] {
			continue
		}
		seen[ft] = true
		types = append(types, ft)
	}
	return types
}

// exclusivelyAsserted reports whether a factor can only be satisfied through a
// dedicated endpoint rather than a code submitted against the challenge.
func exclusivelyAsserted(ft model.AuthFactorType) bool {
	switch ft {
	case model.AuthFactorTypePasskey, model.AuthFactorTypeInAppCode, model.AuthFactorTypeQrLogin:
		return true
	default:
		return false
	}
}

// SudoPickableFactorTypes is SudoFactorTypes without the factors an elevation
// challenge cannot offer as a pickable step: passkeys and QR login use their
// own endpoints, and the in-app notification is no longer selectable.
func SudoPickableFactorTypes(factors []model.AuthFactor, minTrustworthy int) []model.AuthFactorType {
	var pickable []model.AuthFactorType
	for _, ft := range SudoFactorTypes(factors, minTrustworthy) {
		if exclusivelyAsserted(ft) {
			continue
		}
		pickable = append(pickable, ft)
	}
	return pickable
}

// SudoAssertableFactorTypes is SudoFactorTypes of the factors presented through
// a dedicated endpoint (the single passkey type, however many are registered).
// Such a factor satisfies an elevation step without appearing in the picker.
func SudoAssertableFactorTypes(factors []model.AuthFactor, minTrustworthy int) []model.AuthFactorType {
	var assertable []model.AuthFactorType
	for _, ft := range SudoFactorTypes(factors, minTrustworthy) {
		if ft == model.AuthFactorTypePasskey {
			assertable = append(assertable, ft)
		}
	}
	return assertable
}

// SudoFactorHintAnswers lists the lowercase factor type names the account may
// present to elevate, sorted for a stable wire value. It advertises every
// usable factor — including passkeys, which are presented through their own
// assertion endpoint rather than the picker — because it tells the client what
// the account has.
//
// An empty result means the account cannot elevate at all; RequireSudo then
// treats the session as lifted rather than starting a challenge that could
// never complete. Callers must surface a lookup failure as an error, never as
// an empty hint.
func SudoFactorHintAnswers(factors []model.AuthFactor, minTrustworthy int) []string {
	usable := SudoFactorTypes(factors, minTrustworthy)
	names := make([]string, 0, len(usable))
	for _, ft := range usable {
		names = append(names, sudoFactorTypeName(ft))
	}
	sort.Strings(names)
	return names
}
