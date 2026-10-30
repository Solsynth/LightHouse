package domaintrust

import "testing"

func TestMatchesDomainPattern(t *testing.T) {
	cases := []struct {
		pattern string
		host    string
		want    bool
	}{
		{"*", "anything.example", true},
		{"**.example.com", "example.com", true},
		{"**.example.com", "a.example.com", true},
		{"**.example.com", "a.b.example.com", true},
		{"**.example.com", "badexample.com", false},
		{"*.example.com", "example.com", false},
		{"*.example.com", "a.example.com", true},
		{"*.example.com", "a.b.example.com", true},
		{"*.example.com", "badexample.com", false},
		{"wild*domain.com", "wildcarddomain.com", true},
		{"example.com", "www.example.com", false},
		{"img?.example.com", "img1.example.com", true},
		{"img?.example.com", "img.example.com", false},
		{"", "example.com", false},
		{"*", "", false},
	}
	for _, tc := range cases {
		if got := MatchesDomainPattern(tc.pattern, tc.host); got != tc.want {
			t.Errorf("MatchesDomainPattern(%q, %q) = %v, want %v", tc.pattern, tc.host, got, tc.want)
		}
	}
}

func TestIsPrivateNetworkMatchesDotNet(t *testing.T) {
	cases := map[string]bool{
		"10.0.0.1":     true,
		"172.16.0.1":   true,
		"172.31.255.1": true,
		"172.32.0.1":   false,
		"192.168.1.1":  true,
		"127.0.0.1":    true,
		"8.8.8.8":      false,
		"fe80::1":      true,
		"fc00::1":      true,
		// Passport checked fc00:: exactly, not the whole fc00::/7 ULA range.
		"fd00::1": false,
	}
	for host, want := range cases {
		if got := IsPrivateNetwork(host); got != want {
			t.Errorf("IsPrivateNetwork(%q) = %v, want %v", host, got, want)
		}
	}
}
