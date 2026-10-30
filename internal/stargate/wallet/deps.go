package wallet

import (
	"context"
	"log/slog"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/stargate/config"
	"src.solsynth.dev/sosys/stargate/grpcclient"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/permission"
	"src.solsynth.dev/sosys/stargate/ports"
)

// ActionLogSink records account action logs. It matches
// actionlog.Service.Create, so modules hand their core sink straight in; the
// C# wallet called it RemoteActionLogService (a fire-and-forget gRPC client to
// Stargate), so the same failure semantics apply here: emit and ignore.
type ActionLogSink interface {
	Create(ctx context.Context, accountID string, action model.ActionLogType, meta map[string]any, userAgent, ipAddress string, location *string, sessionID *string) error
}

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
