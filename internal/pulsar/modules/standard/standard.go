package standard

import (
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/modules/activities"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/modules/catalog"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/modules/collections"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/modules/metadata"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/modules/rewind"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/modules/scrobbles"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/modules/sessions"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

// Modules returns the standard set of compiled-in Pulsar modules.
func Modules() []module.Factory[core.Services] {
	return []module.Factory[core.Services]{
		catalog.Factory(),
		metadata.Factory(),
		sessions.Factory(),
		scrobbles.Factory(),
		collections.Factory(),
		rewind.Factory(),
		activities.Factory(),
	}
}
