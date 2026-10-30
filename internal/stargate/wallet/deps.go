package wallet

import (
	"log/slog"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/stargate/config"
	"src.solsynth.dev/sosys/stargate/grpcclient"
	"src.solsynth.dev/sosys/stargate/permission"
	"src.solsynth.dev/sosys/stargate/ports"
)

// Deps is everything the wallet HTTP, gRPC and job surfaces share.
type Deps struct {
	Service  *Service
	Perm     *permission.Service
	Log      *slog.Logger
	Ports    *ports.Set
	Clients  *grpcclient.Clients
	Config   *config.Config
	Settings map[string]any
}

// registerFunc mounts one surface on the /api router group.
type registerFunc func(api *gin.RouterGroup, d *Deps)
