package actionlog

import (
	"strings"
	"testing"

	"src.solsynth.dev/sosys/stargate/model"
)

func TestMaskContactContent(t *testing.T) {
	cases := []struct {
		name    string
		typ     int
		content string
		want    string
	}{
		{"email keeps domain", int(model.ContactTypeEmail), "alice@example.com", "a***@example.com"},
		{"email masks short local", int(model.ContactTypeEmail), "a@example.com", "***@example.com"},
		{"email without domain keeps first rune", int(model.ContactTypeEmail), "alice", "a***"},
		{"phone keeps last two digits", int(model.ContactTypePhoneNumber), "+1 555 010 9876", "***76"},
		{"address keeps first rune", int(model.ContactTypeAddress), "221B Baker Street", "2***"},
		{"empty stays empty", int(model.ContactTypeEmail), "   ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MaskContactContent(tc.typ, tc.content)
			if got != tc.want {
				t.Fatalf("MaskContactContent(%d, %q) = %q, want %q", tc.typ, tc.content, got, tc.want)
			}
			if trimmed := strings.TrimSpace(tc.content); trimmed != "" && got == trimmed {
				t.Fatalf("mask revealed the full content %q", tc.content)
			}
		})
	}
}

func TestContactTypeName(t *testing.T) {
	for typ, want := range map[int]string{
		int(model.ContactTypeEmail):       "Email",
		int(model.ContactTypePhoneNumber): "PhoneNumber",
		int(model.ContactTypeAddress):     "Address",
		99:                                "99",
	} {
		if got := ContactTypeName(typ); got != want {
			t.Fatalf("ContactTypeName(%d) = %q, want %q", typ, got, want)
		}
	}
}
