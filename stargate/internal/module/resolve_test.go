package module

import (
	"reflect"
	"testing"
)

func factory(id string, defaultEnabled bool, requires ...string) Factory {
	return Factory{ID: id, Kind: KindFeature, DefaultEnabled: defaultEnabled, Requires: requires}
}

func TestResolveDefaultsAndOrder(t *testing.T) {
	factories := []Factory{
		factory("app.solian.id.profiles", true, "app.solian.id.accounts"),
		factory("app.solian.id.accounts", true, "app.solian.id.auth"),
		factory("app.solian.id.auth", true),
		factory("app.solian.id.relationships", true, "app.solian.id.profiles"),
	}
	resolution, err := Resolve(factories, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(resolution.Factories))
	for _, f := range resolution.Factories {
		got = append(got, f.ID)
	}
	want := []string{
		"app.solian.id.auth",
		"app.solian.id.accounts",
		"app.solian.id.profiles",
		"app.solian.id.relationships",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestResolveEnabledAllowlistAndRequired(t *testing.T) {
	factories := []Factory{
		{ID: "app.solian.id.auth", DefaultEnabled: true, Required: true},
		factory("app.solian.id.accounts", true),
		factory("app.solian.admin", true),
	}
	resolution, err := Resolve(factories, []string{"app.solian.admin"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !resolution.Enabled["app.solian.id.auth"] {
		t.Fatal("required auth module missing")
	}
	if resolution.Enabled["app.solian.id.accounts"] {
		t.Fatal("accounts should not be enabled by the allowlist")
	}
	if !resolution.Enabled["app.solian.admin"] {
		t.Fatal("admin should be enabled")
	}
}

func TestResolveDisableCascades(t *testing.T) {
	factories := []Factory{
		{ID: "app.solian.id.auth", DefaultEnabled: true, Required: true},
		factory("app.solian.id.accounts", true, "app.solian.id.auth"),
		factory("app.solian.id.profiles", true, "app.solian.id.accounts"),
		factory("app.solian.id.relationships", true, "app.solian.id.profiles"),
	}
	resolution, err := Resolve(factories, nil, []string{"app.solian.id.accounts"})
	if err != nil {
		t.Fatal(err)
	}
	if !resolution.Enabled["app.solian.id.auth"] {
		t.Fatal("auth must stay enabled")
	}
	for _, id := range []string{"app.solian.id.accounts", "app.solian.id.profiles", "app.solian.id.relationships"} {
		if resolution.Enabled[id] {
			t.Fatalf("%s should have been cascaded off", id)
		}
	}
	if len(resolution.Warnings) != 2 {
		t.Fatalf("warnings = %v, want 2 cascade warnings", resolution.Warnings)
	}
}

func TestResolveRejectsUnknownAndRequiredDisable(t *testing.T) {
	factories := []Factory{{ID: "app.solian.id.auth", Required: true, DefaultEnabled: true}}
	if _, err := Resolve(factories, nil, []string{"missing.module"}); err == nil {
		t.Fatal("expected unknown module error")
	}
	if _, err := Resolve(factories, nil, []string{"app.solian.id.auth"}); err == nil {
		t.Fatal("expected required module error")
	}
}

func TestResolveRejectsCycle(t *testing.T) {
	factories := []Factory{
		factory("a", true, "b"),
		factory("b", true, "a"),
	}
	if _, err := Resolve(factories, nil, nil); err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestResolveRejectsDuplicateID(t *testing.T) {
	factories := []Factory{factory("a", true), factory("a", true)}
	if _, err := Resolve(factories, nil, nil); err == nil {
		t.Fatal("expected duplicate ID error")
	}
}

func TestCapabilitiesUniqueSorted(t *testing.T) {
	got := Capabilities([]Factory{
		{Capabilities: []string{"b", "a"}},
		{Capabilities: []string{"a", "c"}},
	})
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("capabilities = %v, want %v", got, want)
	}
}
