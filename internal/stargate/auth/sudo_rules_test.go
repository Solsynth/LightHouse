package auth

import (
	"reflect"
	"testing"
	"time"

	"src.solsynth.dev/sosys/stargate/model"
)

func enabledFactor(ftype model.AuthFactorType, trustworthy int) model.AuthFactor {
	now := model.NewTime(time.Now().UTC())
	return model.AuthFactor{Type: ftype, Trustworthy: trustworthy, EnabledAt: now}
}

func disabledFactor(ftype model.AuthFactorType, trustworthy int) model.AuthFactor {
	return model.AuthFactor{Type: ftype, Trustworthy: trustworthy}
}

// TestSudoMinTrustworthyFor pins the mode → floor mapping: lockdown keeps the
// strong-proof floor, every other mode accepts any enabled factor (>= 1).
func TestSudoMinTrustworthyFor(t *testing.T) {
	for _, tc := range []struct {
		mode model.SecurityMode
		want int
	}{
		{model.SecurityModeDefault, 1},
		{model.SecurityModeLockoff, 1},
		{model.SecurityModeLockdown, SudoMinTrustworthy},
	} {
		if got := SudoMinTrustworthyFor(tc.mode); got != tc.want {
			t.Fatalf("SudoMinTrustworthyFor(%v) = %d, want %d", tc.mode, got, tc.want)
		}
	}
}

// TestSudoFactorTypes pins the eligibility rules: only enabled factors at or
// above the given floor count, with PIN/recovery excluded and duplicate types
// collapsed. Strength is a floor, not a step count.
func TestSudoFactorTypes(t *testing.T) {
	factors := []model.AuthFactor{
		enabledFactor(model.AuthFactorTypePassword, 1),
		enabledFactor(model.AuthFactorTypePasskey, 4),
		enabledFactor(model.AuthFactorTypeTimedCode, 3),
		enabledFactor(model.AuthFactorTypeTimedCode, 3), // duplicate type
		enabledFactor(model.AuthFactorTypePinCode, 0),
		enabledFactor(model.AuthFactorTypeRecoveryCode, 0),
		disabledFactor(model.AuthFactorTypeNfcToken, 1),
	}

	// Lockdown floor: a password (or NFC token) sits below it — proving the
	// login secret is not the same as proving presence for an elevation.
	got := SudoFactorTypes(factors, SudoMinTrustworthy)
	want := []model.AuthFactorType{
		model.AuthFactorTypePasskey,
		model.AuthFactorTypeTimedCode,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SudoFactorTypes (lockdown) = %v, want %v", got, want)
	}
	for _, ft := range got {
		if ft == model.AuthFactorTypePassword || ft == model.AuthFactorTypeNfcToken {
			t.Fatalf("weak factor %v counted as sudo-eligible under lockdown", ft)
		}
	}

	// Outside lockdown any enabled factor qualifies, password included.
	got = SudoFactorTypes(factors, 1)
	want = []model.AuthFactorType{
		model.AuthFactorTypePassword,
		model.AuthFactorTypePasskey,
		model.AuthFactorTypeTimedCode,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SudoFactorTypes (default floor) = %v, want %v", got, want)
	}
}

func TestSudoPickableFactorTypes(t *testing.T) {
	factors := []model.AuthFactor{
		enabledFactor(model.AuthFactorTypePassword, 1),
		enabledFactor(model.AuthFactorTypeEmailCode, 2),
		enabledFactor(model.AuthFactorTypePasskey, 4),
		enabledFactor(model.AuthFactorTypeQrLogin, 3),
		enabledFactor(model.AuthFactorTypeInAppCode, 2),
	}
	got := SudoPickableFactorTypes(factors, SudoMinTrustworthy)
	want := []model.AuthFactorType{model.AuthFactorTypeEmailCode}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SudoPickableFactorTypes = %v, want %v", got, want)
	}
}

func TestSudoFactorHintAnswers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		factors []model.AuthFactor
		want    []string
	}{
		{
			name:    "password is below the lockdown sudo floor",
			factors: []model.AuthFactor{enabledFactor(model.AuthFactorTypeTimedCode, 3), enabledFactor(model.AuthFactorTypePassword, 1)},
			want:    []string{"timed_code"},
		},
		{
			name:    "password only cannot elevate under lockdown",
			factors: []model.AuthFactor{enabledFactor(model.AuthFactorTypePassword, 1)},
			want:    []string{},
		},
		{
			name:    "passkey only",
			factors: []model.AuthFactor{enabledFactor(model.AuthFactorTypePasskey, 4)},
			want:    []string{"passkey"},
		},
		{
			name:    "no factor at all",
			factors: nil,
			want:    []string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := SudoFactorHintAnswers(tc.factors, SudoMinTrustworthy)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("hint = %v, want %v", got, tc.want)
			}
		})
	}
}
