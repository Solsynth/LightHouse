package discovery

import "testing"

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
// package owns, so a configured address must never parse as a discovery
// target. Otherwise grpc resolves the registry for a service named "7001".
func TestDialTargetKeepsBladeAddressesOffTheResolverScheme(t *testing.T) {
	target := DialTarget("blade:7001")
	if IsTarget(target) {
		t.Fatalf("DialTarget(%q) = %q parses as a discovery target", "blade:7001", target)
	}
	if !IsTarget(Target("stargate")) {
		t.Fatalf("Target(stargate) = %q does not parse as a discovery target", Target("stargate"))
	}
}
