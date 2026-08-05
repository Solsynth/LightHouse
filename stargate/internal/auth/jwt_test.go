package auth

import (
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// TestClaimInt pins the string-or-number claim parsing: the C# minting
// writes ver/epoch as strings and validates with int.TryParse, so both
// serializations must be read (a float64-only assertion silently skips the
// epoch/version checks on fleet-issued tokens).
func TestClaimInt(t *testing.T) {
	claims := jwt.MapClaims{
		"epoch_string":  "1", // C#/Go minted token
		"epoch_number":  1.0, // a JSON-number minting
		"epoch_garbage": "abc",
		"epoch_zero":    "0",
	}
	cases := []struct {
		key  string
		want int
		ok   bool
	}{
		{"epoch_string", 1, true},
		{"epoch_number", 1, true},
		{"epoch_zero", 0, true},
		{"epoch_garbage", 0, false},
		{"missing", 0, false},
	}
	for _, tc := range cases {
		got, ok := ClaimInt(claims, tc.key)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ClaimInt(%q) = (%d, %v), want (%d, %v)", tc.key, got, ok, tc.want, tc.ok)
		}
	}
}
