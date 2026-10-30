// Package core holds the shared service graph handed to modules. It is built
// once by package app and never imports the module registry, which keeps the
// dependency direction one-way: app -> core + module, modules -> core + module.
package core

import (
	"log/slog"

	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/actionlog"
	"src.solsynth.dev/sosys/stargate/auth"
	"src.solsynth.dev/sosys/stargate/broadcast"
	"src.solsynth.dev/sosys/stargate/config"
	"src.solsynth.dev/sosys/stargate/geo"
	"src.solsynth.dev/sosys/stargate/grpcclient"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/nats"
	"src.solsynth.dev/sosys/stargate/permission"
	"src.solsynth.dev/sosys/stargate/presence"
	"src.solsynth.dev/sosys/stargate/redis"
	"src.solsynth.dev/sosys/stargate/relationship"
	"src.solsynth.dev/sosys/stargate/spell"
	"src.solsynth.dev/sosys/stargate/store"
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
	// PresenceResolver assembles the computed half of an account status
	// (live devices, idle state, viewer visibility) for the status RPCs and
	// the websocket broadcasts. Redis and Blade are optional.
	PresenceResolver *presence.Resolver
	// Broadcast fans status and presence-activity changes out to friends on
	// Blade's websocket push subject.
	Broadcast *broadcast.Broadcaster

	// Relationships is the social-graph read service. It is nil unless the
	// relationships module is enabled, so gRPC relationship RPCs report
	// Unimplemented instead of answering from a table that may not exist.
	Relationships *relationship.Service
}
