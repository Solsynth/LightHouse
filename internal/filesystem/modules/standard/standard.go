// Package standard lists the modules compiled into the default DysonFS binary.
// A private build can append factories from a git submodule; a third-party
// module should use its own reverse-DNS ID.
package standard

import (
	"src.solsynth.dev/sosys/filesystem/module"
	"src.solsynth.dev/sosys/filesystem/modules/analysis"
	"src.solsynth.dev/sosys/filesystem/modules/auth"
	"src.solsynth.dev/sosys/filesystem/modules/derivatives"
	"src.solsynth.dev/sosys/filesystem/modules/media"
	"src.solsynth.dev/sosys/filesystem/modules/storage"
	"src.solsynth.dev/sosys/filesystem/modules/surfaces"
	"src.solsynth.dev/sosys/filesystem/modules/worker"
)

// Modules returns every compiled-in factory.
func Modules() []module.Factory {
	out := []module.Factory{
		auth.Factory(),
		surfaces.Core(),
		surfaces.Files(),
		surfaces.Uploads(),
		surfaces.Pools(),
		surfaces.Billing(),
		surfaces.Account(),
		surfaces.ContentEdit(),
		surfaces.Tokens(),
		surfaces.Nodes(),
		surfaces.Admin(),
		analysis.Image(),
		analysis.Video(),
		analysis.Audio(),
		analysis.Reanalysis(),
		derivatives.Image(),
		derivatives.Video(),
		worker.Factory(),
		media.Factory(),
		surfaces.WebDAV(),
		surfaces.WOPI(),
		surfaces.StorageNode(),
		surfaces.GRPC(),
		storage.MasterS3(),
		storage.NodeS3(),
	}
	return out
}
