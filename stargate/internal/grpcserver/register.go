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
	Store *store.Store
	Redis *redis.Client
	Auth  *auth.AuthService
	Token *auth.TokenAuthService
	JWT   *auth.JWTService
	Perm  *permission.Service
	Logs  *actionlog.Service
	E2ee  *e2eectl.Service
	Cfg   *config.Config
	Log   *slog.Logger
}

// Register mounts every inbound gRPC service (the Padlock surface:
// DyAuthService, DyAccountService, DyActionLogService, DyPermissionService,
// DyBotAccountReceiverService, DyAuthorizedAppService, DyCapabilitiesService)
// plus the standard gRPC health service on the given server.
func Register(s *grpc.Server, deps Deps) {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	gen.RegisterDyAuthServiceServer(s, &dyAuthService{d: deps})
	gen.RegisterDyAccountServiceServer(s, &dyAccountService{d: deps})
	gen.RegisterDyActionLogServiceServer(s, &dyActionLogService{d: deps})
	gen.RegisterDyPermissionServiceServer(s, &dyPermissionService{d: deps})
	gen.RegisterDyBotAccountReceiverServiceServer(s, &dyBotAccountReceiverService{d: deps})
	gen.RegisterDyAuthorizedAppServiceServer(s, &dyAuthorizedAppService{d: deps})
	gen.RegisterDyMlsServiceServer(s, &dyMlsService{d: deps})
	gen.RegisterDyCapabilitiesServiceServer(s, &dyCapabilitiesService{})

	hs := health.NewServer()
	hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(s, hs)
}
