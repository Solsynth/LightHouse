package module

import (
	"fmt"
	"sort"
	"strings"
)

// Resolution is the outcome of applying the config to the compiled registry.
type Resolution[C any] struct {
	Factories []Factory[C]
	Enabled   map[string]bool
	// Warnings records modules removed because a dependency was disabled.
	Warnings []string
}

// Resolve applies [modules] enabled/disabled to the compiled factories.
//
// Rules:
//   - DefaultEnabled seeds the set.
//   - A non-empty enabled list becomes the allowlist.
//   - Required modules are always present.
//   - disabled removes modules; disabling a required module is an error.
//   - Dependents of a disabled module are removed with a warning.
//   - Module dependencies are topologically ordered; cycles fail.
func Resolve[C any](factories []Factory[C], enabled, disabled []string) (*Resolution[C], error) {
	byID := make(map[string]Factory[C], len(factories))
	for _, factory := range factories {
		if factory.ID == "" {
			return nil, fmt.Errorf("module with an empty ID")
		}
		if _, exists := byID[factory.ID]; exists {
			return nil, fmt.Errorf("duplicate module ID %q", factory.ID)
		}
		byID[factory.ID] = factory
	}

	normalize := func(ids []string) ([]string, error) {
		out := make([]string, 0, len(ids))
		seen := map[string]bool{}
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id == "" || seen[id] {
				continue
			}
			if _, ok := byID[id]; !ok {
				return nil, fmt.Errorf("unknown module %q", id)
			}
			seen[id] = true
			out = append(out, id)
		}
		return out, nil
	}

	enabledIDs, err := normalize(enabled)
	if err != nil {
		return nil, err
	}
	disabledIDs, err := normalize(disabled)
	if err != nil {
		return nil, err
	}

	selected := make(map[string]bool, len(byID))
	if len(enabledIDs) > 0 {
		for _, id := range enabledIDs {
			selected[id] = true
		}
	} else {
		for id, factory := range byID {
			if factory.DefaultEnabled {
				selected[id] = true
			}
		}
	}

	// Required modules are part of every deployment.
	for id, factory := range byID {
		if factory.Required {
			selected[id] = true
		}
	}

	for _, id := range disabledIDs {
		if byID[id].Required {
			return nil, fmt.Errorf("module %q is required and cannot be disabled", id)
		}
		delete(selected, id)
	}

	// Cascade: a module whose dependency is absent cannot run.
	var warnings []string
	for {
		changed := false
		ids := make([]string, 0, len(selected))
		for id := range selected {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			for _, requirement := range byID[id].Requires {
				if selected[requirement] {
					continue
				}
				delete(selected, id)
				warnings = append(warnings, fmt.Sprintf("module %q disabled: required module %q is not enabled", id, requirement))
				changed = true
				break
			}
		}
		if !changed {
			break
		}
	}

	ordered, err := topologicalOrder(selected, byID)
	if err != nil {
		return nil, err
	}

	result := make([]Factory[C], 0, len(ordered))
	for _, id := range ordered {
		result = append(result, byID[id])
	}
	return &Resolution[C]{Factories: result, Enabled: selected, Warnings: warnings}, nil
}

// topologicalOrder sorts the selected modules so dependencies come first. Ties
// are broken by module ID for deterministic route registration.
func topologicalOrder[C any](selected map[string]bool, byID map[string]Factory[C]) ([]string, error) {
	ids := make([]string, 0, len(selected))
	for id := range selected {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	state := make(map[string]int, len(ids)) // 0 unvisited, 1 visiting, 2 done
	ordered := make([]string, 0, len(ids))
	var visit func(id string, path []string) error
	visit = func(id string, path []string) error {
		switch state[id] {
		case 2:
			return nil
		case 1:
			return fmt.Errorf("module dependency cycle: %s", strings.Join(append(path, id), " -> "))
		}
		state[id] = 1
		deps := append([]string(nil), byID[id].Requires...)
		sort.Strings(deps)
		for _, dep := range deps {
			if !selected[dep] {
				continue
			}
			if err := visit(dep, append(path, id)); err != nil {
				return err
			}
		}
		state[id] = 2
		ordered = append(ordered, id)
		return nil
	}
	for _, id := range ids {
		if err := visit(id, nil); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

// Capabilities collects unique capability names from the resolved modules.
func Capabilities[C any](factories []Factory[C]) []string {
	seen := map[string]bool{}
	out := make([]string, 0)
	for _, factory := range factories {
		for _, capability := range factory.Capabilities {
			if capability == "" || seen[capability] {
				continue
			}
			seen[capability] = true
			out = append(out, capability)
		}
	}
	sort.Strings(out)
	return out
}
