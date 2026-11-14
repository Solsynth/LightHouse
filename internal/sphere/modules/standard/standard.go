// Package standard lists the modules compiled into the default Sphere binary.
// A private build can append factories from a git submodule; a third-party
// module should use its own reverse-DNS ID.
package standard

import (
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/modules/analytics"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/modules/autocomplete"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/modules/automod"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/modules/fediverse"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/modules/integrations"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/modules/livestreams"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/modules/posts"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/modules/publishers"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/modules/reader"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/modules/rewind"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/modules/stickers"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/modules/surveys"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/modules/timeline"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/modules/translation"
)

// Modules returns every compiled-in factory.
func Modules() []module.Factory[core.Services] {
	return []module.Factory[core.Services]{
		// Ecosystem adapters publish their ports before any feature module
		// configures, so consumers can resolve them regardless of order.
		integrations.Factory(),

		// Cross-cutting services. The analytics module provides the port the
		// content features record views and engagement through.
		analytics.Factory(),

		// Content features.
		publishers.Factory(),
		posts.Factory(),
		timeline.Factory(),
		surveys.Factory(),
		stickers.Factory(),
		livestreams.Factory(),
		fediverse.Factory(),
		automod.Factory(),
		autocomplete.Factory(),
		reader.Factory(),
		rewind.Factory(),
		translation.Factory(),
	}
}
