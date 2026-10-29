package store

import (
	"testing"

	"github.com/google/uuid"
)

// TestNullableAccountID verifies anonymous challenges (discoverable passkey
// login, QR login) store NULL instead of the all-zero-UUID sentinel, so the
// FK to accounts is satisfied; real account ids pass through unchanged.
func TestNullableAccountID(t *testing.T) {
	sentinel := uuid.Nil.String()
	for name, tt := range map[string]struct {
		in   string
		want any
	}{
		"sentinel maps to NULL": {in: sentinel, want: nil},
		"empty maps to NULL":    {in: "", want: nil},
		"real id passes through": {
			in:   "11111111-1111-1111-1111-111111111111",
			want: "11111111-1111-1111-1111-111111111111",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := nullableAccountID(tt.in); got != tt.want {
				t.Fatalf("nullableAccountID(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

// TestAccountIDOrSentinel verifies a NULL account_id row round-trips back to
// the sentinel, keeping the "no account yet" checks in the handlers intact
// and the JSON contract (account_id is a String on the Dart side).
func TestAccountIDOrSentinel(t *testing.T) {
	sentinel := uuid.Nil.String()
	if got := accountIDOrSentinel(nil); got != sentinel {
		t.Fatalf("accountIDOrSentinel(nil) = %q, want sentinel %q", got, sentinel)
	}
	real := "22222222-2222-2222-2222-222222222222"
	got := accountIDOrSentinel(&real)
	if got != real {
		t.Fatalf("accountIDOrSentinel(%q) = %q, want %q", real, got, real)
	}
}
