package module

import (
	"reflect"
	"testing"
)

func factory(id string, defaultEnabled bool, requires ...string) Factory[struct{}] {
	return Factory[struct{}]{ID: id, DefaultEnabled: defaultEnabled, Requires: requires}
}

func ids(resolution *Resolution[struct{}]) []string {
	out := make([]string, 0, len(resolution.Factories))
	for _, f := range resolution.Factories {
		out = append(out, f.ID)
	}
	return out
}

func TestResolveDefaultsAndOrder(t *testing.T) {
	factories := []Factory[struct{}]{
		factory("app.solian.id.profiles", true, "app.solian.id.accounts"),
		factory("app.solian.id.accounts", true, "app.solian.id.auth"),
		factory("app.solian.id.auth", true),
		factory("app.solian.id.relationships", true, "app.solian.id.profiles"),
	}
	resolution, err := Resolve(factories, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"app.solian.id.auth",
		"app.solian.id.accounts",
		"app.solian.id.profiles",
		"app.solian.id.relationships",
	}
	if got := ids(resolution); !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if got := resolution.Factories[0].Kind; got != KindFeature {
		t.Fatalf("default kind = %q, want %q", got, KindFeature)
	}
}

func TestResolveEnabledAllowlistAndRequired(t *testing.T) {
	factories := []Factory[struct{}]{
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
	factories := []Factory[struct{}]{
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
	factories := []Factory[struct{}]{{ID: "app.solian.id.auth", Required: true, DefaultEnabled: true}}
	if _, err := Resolve(factories, nil, []string{"missing.module"}); err == nil {
		t.Fatal("expected unknown module error")
	}
	if _, err := Resolve(factories, nil, []string{"app.solian.id.auth"}); err == nil {
		t.Fatal("expected required module error")
	}
}

func TestResolveRejectsCycle(t *testing.T) {
	factories := []Factory[struct{}]{
		factory("a", true, "b"),
		factory("b", true, "a"),
	}
	if _, err := Resolve(factories, nil, nil); err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestResolveRejectsDuplicateID(t *testing.T) {
	factories := []Factory[struct{}]{factory("a", true), factory("a", true)}
	if _, err := Resolve(factories, nil, nil); err == nil {
		t.Fatal("expected duplicate ID error")
	}
}

func TestCapabilitiesUniqueSorted(t *testing.T) {
	got := Capabilities([]Factory[struct{}]{
		{Capabilities: []string{"b", "a"}},
		{Capabilities: []string{"a", "c"}},
	})
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("capabilities = %v, want %v", got, want)
	}
}

func TestInfosReportsResolution(t *testing.T) {
	factories := []Factory[struct{}]{
		{ID: "app.solian.a", Kind: KindFeature, Required: true, Capabilities: []string{"a"}},
		{ID: "app.solian.b", Kind: KindIntegration, Requires: []string{"app.solian.a"}},
	}
	infos := Infos(factories, map[string]bool{"app.solian.a": true})
	if len(infos) != 2 {
		t.Fatalf("infos = %v, want 2 entries", infos)
	}
	if !infos[0].Enabled || infos[0].Kind != string(KindFeature) {
		t.Fatalf("first info = %+v", infos[0])
	}
	if infos[1].Enabled || infos[1].Kind != string(KindIntegration) {
		t.Fatalf("second info = %+v", infos[1])
	}
}
