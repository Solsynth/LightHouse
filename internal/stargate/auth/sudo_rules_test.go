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

// TestSudoFactorTypes pins the eligibility rules: only enabled factors at or
// above SudoMinTrustworthy count, with PIN/recovery excluded and duplicate
// types collapsed. Strength is a floor, not a step count.
func TestSudoFactorTypes(t *testing.T) {
	got := SudoFactorTypes([]model.AuthFactor{
		enabledFactor(model.AuthFactorTypePassword, 1),
		enabledFactor(model.AuthFactorTypePasskey, 4),
		enabledFactor(model.AuthFactorTypeTimedCode, 3),
		enabledFactor(model.AuthFactorTypeTimedCode, 3), // duplicate type
		enabledFactor(model.AuthFactorTypePinCode, 0),
		enabledFactor(model.AuthFactorTypeRecoveryCode, 0),
		disabledFactor(model.AuthFactorTypeNfcToken, 1),
	})
	want := []model.AuthFactorType{
		model.AuthFactorTypePasskey,
		model.AuthFactorTypeTimedCode,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SudoFactorTypes = %v, want %v", got, want)
	}

	// A password (or NFC token) sits below the floor: proving the login secret
	// is not the same as proving presence for an elevation.
	for _, ft := range got {
		if ft == model.AuthFactorTypePassword || ft == model.AuthFactorTypeNfcToken {
			t.Fatalf("weak factor %v counted as sudo-eligible", ft)
		}
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
	got := SudoPickableFactorTypes(factors)
	want := []model.AuthFactorType{model.AuthFactorTypeEmailCode}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SudoPickableFactorTypes = %v, want %v", got, want)
	}
}

func TestSudoFactorHintAnswers(t *testing.T) {
	for _, tc := range []struct {
		name     string
		factors  []model.AuthFactor
		hasEmail bool
		want     []string
	}{
		{
			name:    "password is below the sudo floor",
			factors: []model.AuthFactor{enabledFactor(model.AuthFactorTypeTimedCode, 3), enabledFactor(model.AuthFactorTypePassword, 1)},
			want:    []string{"timed_code"},
		},
		{
			name:    "password only arms the email fallback",
			factors: []model.AuthFactor{enabledFactor(model.AuthFactorTypePassword, 1)},
			// The password cannot elevate, so the verified email is the only
			// remaining path.
			hasEmail: true,
			want:     []string{"email_code"},
		},
		{
			name:    "password only without a verified email",
			factors: []model.AuthFactor{enabledFactor(model.AuthFactorTypePassword, 1)},
			want:    []string{},
		},
		{
			name:     "passkey only needs no emailed fallback",
			factors:  []model.AuthFactor{enabledFactor(model.AuthFactorTypePasskey, 4)},
			hasEmail: true,
			want:     []string{"passkey"},
		},
		{
			name:    "passkey only without a verified email",
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
			got := SudoFactorHintAnswers(tc.factors, tc.hasEmail)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("hint = %v, want %v", got, tc.want)
			}
		})
	}
}
