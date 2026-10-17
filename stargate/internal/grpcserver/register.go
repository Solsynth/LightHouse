// Package grpcserver implements Stargate's inbound gRPC servers — the
// surface the C# fleet calls via services__padlock__grpc__0. Every service is
// a port of the corresponding Padlock *Grpc.cs file against the pinned
// Golaunch generated interfaces (src.solsynth.dev/sosys/go/proto).
package grpcserver

import (
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/actionlog"
	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/e2eectl"
	"src.solsynth.dev/sosys/stargate/internal/permission"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// Deps carries the shared service dependencies. Fields are the same objects
// the HTTP layer uses; unused ones (Redis) are kept for contract parity.
type Deps struct {
	Store      *store.Store
	Redis      *redis.Client
	Auth       *auth.AuthService
	Token      *auth.TokenAuthService
	JWT        *auth.JWTService
	Perm       *permission.Service
	Logs       *actionlog.Service
	E2ee       *e2eectl.Service
	Presence   *auth.DevicePresence
	SingleUser *auth.SingleUser
	Cfg        *config.Config
	Log        *slog.Logger

	// Capabilities is the enabled capability list. Nil keeps the historical
	// full registry (tests and legacy callers).
	Capabilities []string
}

// RegisterCore mounts the infrastructure services that are always present:
// permissions, action logs, capabilities and gRPC health.
func RegisterCore(s *grpc.Server, deps Deps) {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	gen.RegisterDyPermissionServiceServer(s, &dyPermissionService{d: deps})
	gen.RegisterDyActionLogServiceServer(s, &dyActionLogService{d: deps})
	gen.RegisterDyCapabilitiesServiceServer(s, &dyCapabilitiesService{capabilities: deps.Capabilities})

	hs := health.NewServer()
	hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(s, hs)
}

// RegisterAuth mounts DyAuthService.
func RegisterAuth(s *grpc.Server, deps Deps) {
	gen.RegisterDyAuthServiceServer(s, &dyAuthService{d: deps})
}

// RegisterAccounts mounts DyAccountService plus the bot and authorized-app
// receivers.
func RegisterAccounts(s *grpc.Server, deps Deps) {
	gen.RegisterDyAccountServiceServer(s, &dyAccountService{d: deps})
	gen.RegisterDyBotAccountReceiverServiceServer(s, &dyBotAccountReceiverService{d: deps})
	gen.RegisterDyAuthorizedAppServiceServer(s, &dyAuthorizedAppService{d: deps})
}

// RegisterProfiles mounts the read-side DyProfileService.
func RegisterProfiles(s *grpc.Server, deps Deps) {
	gen.RegisterDyProfileServiceServer(s, &dyProfileService{d: deps})
}

// RegisterMls mounts DyMlsService. It is a no-op when the E2EE service is
// absent.
func RegisterMls(s *grpc.Server, deps Deps) {
	if deps.E2ee == nil {
		return
	}
	gen.RegisterDyMlsServiceServer(s, &dyMlsService{d: deps})
}

// Register mounts every inbound gRPC service (the historical Padlock
// surface). Kept for tests and compatibility; the module bootstrap calls the
// narrower registration functions.
func Register(s *grpc.Server, deps Deps) {
	RegisterCore(s, deps)
	RegisterAuth(s, deps)
	RegisterAccounts(s, deps)
	RegisterProfiles(s, deps)
	RegisterMls(s, deps)
}
