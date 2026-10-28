// Package standard lists the modules compiled into the default Messager
// binary.
package standard

import (
	"src.solsynth.dev/sosys/lighthouse/internal/messager/core"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/modules/chat"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/modules/integrations"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/modules/realtime"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
)

// Modules returns every compiled-in factory.
func Modules() []module.Factory[core.Services] {
	return []module.Factory[core.Services]{
		integrations.Factory(),
		chat.Factory(),
		realtime.Factory(),
	}
}
