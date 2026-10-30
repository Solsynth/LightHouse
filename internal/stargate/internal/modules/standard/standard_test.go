package standard

import (
	"testing"

	"src.solsynth.dev/sosys/stargate/internal/module"
)

func TestStandardModulesResolve(t *testing.T) {
	factories := Modules()
	resolution, err := module.Resolve(factories, nil, nil)
	if err != nil {
		t.Fatalf("resolve standard modules: %v", err)
	}
	required := map[string]bool{}
	for _, f := range factories {
		if f.Required {
			required[f.ID] = true
		}
	}
	for id := range required {
		if !resolution.Enabled[id] {
			t.Fatalf("required module %s missing", id)
		}
	}
	if !resolution.Enabled["app.solian.id.relationships"] {
		t.Fatal("relationships should be enabled by default")
	}
	if !resolution.Enabled["app.solian.drive"] {
		t.Fatal("drive integration should be enabled by default")
	}
}

func TestStandardModulesCanRunAuthOnly(t *testing.T) {
	resolution, err := module.Resolve(Modules(), []string{"app.solian.id.auth"}, nil)
	if err != nil {
		t.Fatalf("resolve auth-only: %v", err)
	}
	if len(resolution.Factories) != 1 || resolution.Factories[0].ID != "app.solian.id.auth" {
		t.Fatalf("auth-only resolved %d modules: %v", len(resolution.Factories), resolution.Factories)
	}
}
