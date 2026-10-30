package module

import "testing"

type fakePort struct{ name string }

func TestPortRegistryFeatureWins(t *testing.T) {
	reg := NewPortRegistry()
	Provide[PortID](reg, "test", "app.solian.local", KindFeature, PortID("local"))
	Provide[PortID](reg, "test", "app.solian.remote", KindIntegration, PortID("remote"))

	value, ok := Get[PortID](reg, "test")
	if !ok || value != "local" {
		t.Fatalf("Get = %v, %v; want local", value, ok)
	}
}

func TestPortRegistryExplicitSelection(t *testing.T) {
	reg := NewPortRegistry()
	Provide[PortID](reg, "test", "app.solian.local", KindFeature, PortID("local"))
	Provide[PortID](reg, "test", "app.solian.remote", KindIntegration, PortID("remote"))
	reg.Select(map[string]string{"test": "app.solian.remote"})

	value, ok := Get[PortID](reg, "test")
	if !ok || value != "remote" {
		t.Fatalf("Get = %v, %v; want remote", value, ok)
	}
}

func TestPortRegistryAmbiguousIntegrationsFail(t *testing.T) {
	reg := NewPortRegistry()
	Provide[PortID](reg, "test", "app.solian.one", KindIntegration, PortID("one"))
	Provide[PortID](reg, "test", "app.solian.two", KindIntegration, PortID("two"))
	if _, err := reg.Resolve("test"); err == nil {
		t.Fatal("expected ambiguous provider error")
	}
}

func TestPortRegistryMissing(t *testing.T) {
	reg := NewPortRegistry()
	if _, ok := Get[PortID](reg, "missing"); ok {
		t.Fatal("missing port resolved")
	}
}
