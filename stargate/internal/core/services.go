// Package core holds the shared service graph handed to modules. It is built
// once by package app and never imports the module registry, which keeps the
// dependency direction one-way: app -> core + module, modules -> core + module.
package core

import (
	"log/slog"

	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/internal/actionlog"
	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/geo"
	"src.solsynth.dev/sosys/stargate/internal/grpcclient"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/nats"
	"src.solsynth.dev/sosys/stargate/internal/permission"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/spell"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// Services is the shared, already-constructed core graph. Modules must treat
// nil optional services as unavailable rather than assuming they exist.
type Services struct {
	Config     *config.Config
	DB         *gorm.DB
	Store      *store.Store
	Redis      *redis.Client
	NATS       *nats.Client
	JWT        *auth.JWTService
	Auth       *auth.AuthService
	Token      *auth.TokenAuthService
	SingleUser *auth.SingleUser
	Perm       *permission.Service
	Logs       *actionlog.Service
	Spells     *spell.Service
	Geo        *geo.Service
	Presence   *auth.DevicePresence
	Toucher    *middleware.LastSeenToucher
	Clients    *grpcclient.Clients
	Log        *slog.Logger
}
