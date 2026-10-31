package discovery

import (
	"net/url"
	"testing"
)

func TestDialTarget(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"bare host:port", "blade:7001", "dns:///blade:7001"},
		{"surrounding whitespace", "  blade:7001\n", "dns:///blade:7001"},
		{"explicit dns target", "dns:///blade:7001", "dns:///blade:7001"},
		{"logical service target", Target("stargate"), "blade:///stargate"},
		{"empty", "   ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DialTarget(tt.raw)
			if got != tt.want {
				t.Fatalf("DialTarget(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// TestDialTargetKeepsBladeAddressesOffTheResolverScheme pins why the dns
// prefix exists: grpc parses "blade:7001" as scheme "blade", which this
// package owns, so a configured address must not parse with that scheme.
// Otherwise grpc resolves the registry for a service named "7001".
func TestDialTargetKeepsBladeAddressesOffTheResolverScheme(t *testing.T) {
	parsed, err := url.Parse(DialTarget("blade:7001"))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme == ResolverScheme {
		t.Fatalf("DialTarget(%q) parses as scheme %q", "blade:7001", parsed.Scheme)
	}
	if parsed.Scheme != "dns" || parsed.Path != "/blade:7001" {
		t.Fatalf("DialTarget(%q) = %q, want dns:///blade:7001", "blade:7001", parsed)
	}
}
