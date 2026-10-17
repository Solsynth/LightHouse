package auth

import (
	"sort"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// SudoFactorTypes is the set of an account's enabled factors that can satisfy
// an elevation ("sudo") step. The strength score (Trustworthy) plays no part in
// the count: a passkey (4) must not cost more steps than a password (1), and
// summing it could push the demand past the challenge's step columns. PIN and
// recovery codes never count — they are untrustworthy or have their own flow.
func SudoFactorTypes(factors []model.AuthFactor) []model.AuthFactorType {
	seen := make(map[model.AuthFactorType]bool, len(factors))
	types := make([]model.AuthFactorType, 0, len(factors))
	for _, f := range factors {
		if f.EnabledAt == nil || f.Trustworthy < 1 {
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
func SudoPickableFactorTypes(factors []model.AuthFactor) []model.AuthFactorType {
	var pickable []model.AuthFactorType
	for _, ft := range SudoFactorTypes(factors) {
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
func SudoAssertableFactorTypes(factors []model.AuthFactor) []model.AuthFactorType {
	var assertable []model.AuthFactorType
	for _, ft := range SudoFactorTypes(factors) {
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
// the account has. "email_code" is appended when the account has nothing else
// to present (no pickable and no assertable factor) and a verified email
// contact exists: the emailed fallback is then the only way to elevate.
//
// An empty result means the account cannot elevate at all — the elevation
// cannot be started (AUTH_NO_AUTH_FACTORS) — so callers must surface a lookup
// failure as an error, never as an empty hint.
func SudoFactorHintAnswers(factors []model.AuthFactor, hasVerifiedEmail bool) []string {
	usable := SudoFactorTypes(factors)
	names := make([]string, 0, len(usable)+1)
	for _, ft := range usable {
		names = append(names, sudoFactorTypeName(ft))
	}
	sort.Strings(names)
	// Appended after sorting so the synthetic fallback always trails the real
	// factors.
	if len(SudoPickableFactorTypes(factors)) == 0 && len(SudoAssertableFactorTypes(factors)) == 0 && hasVerifiedEmail {
		names = append(names, sudoFactorTypeName(model.AuthFactorTypeEmailCode))
	}
	return names
}
