package auth

import (
	"reflect"
	"testing"
	"time"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

func enabledFactor(ftype model.AuthFactorType, trustworthy int) model.AuthFactor {
	now := model.NewTime(time.Now().UTC())
	return model.AuthFactor{Type: ftype, Trustworthy: trustworthy, EnabledAt: now}
}

func disabledFactor(ftype model.AuthFactorType, trustworthy int) model.AuthFactor {
	return model.AuthFactor{Type: ftype, Trustworthy: trustworthy}
}

// TestSudoFactorTypes pins the step-counting rules: one entry per enabled,
// trustworthy factor type, regardless of its strength score, with PIN and
// recovery codes excluded and duplicates collapsed.
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
		model.AuthFactorTypePassword,
		model.AuthFactorTypePasskey,
		model.AuthFactorTypeTimedCode,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SudoFactorTypes = %v, want %v", got, want)
	}

	// The strength score never inflates the demand: a password + passkey account
	// costs two steps, not five.
	if len(got[0:2]) != 2 {
		t.Fatalf("password + passkey counted as %d steps, want 2", len(got[0:2]))
	}
}

func TestSudoPickableFactorTypes(t *testing.T) {
	factors := []model.AuthFactor{
		enabledFactor(model.AuthFactorTypePassword, 1),
		enabledFactor(model.AuthFactorTypePasskey, 4),
		enabledFactor(model.AuthFactorTypeQrLogin, 3),
		enabledFactor(model.AuthFactorTypeInAppCode, 2),
	}
	got := SudoPickableFactorTypes(factors)
	want := []model.AuthFactorType{model.AuthFactorTypePassword}
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
			name:    "password and totp, sorted",
			factors: []model.AuthFactor{enabledFactor(model.AuthFactorTypeTimedCode, 3), enabledFactor(model.AuthFactorTypePassword, 1)},
			want:    []string{"password", "timed_code"},
		},
		{
			name:    "password only never arms the email fallback",
			factors: []model.AuthFactor{enabledFactor(model.AuthFactorTypePassword, 1)},
			// The verified email is irrelevant while a pickable factor exists.
			hasEmail: true,
			want:     []string{"password"},
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
