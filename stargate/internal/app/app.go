// Package app owns the Stargate process bootstrap. It builds the shared core
// services, resolves the compiled-in module set and runs the HTTP and gRPC
// servers. cmd/stargate/main.go is deliberately thin.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/actionlog"
	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/core"
	"src.solsynth.dev/sosys/stargate/internal/db"
	"src.solsynth.dev/sosys/stargate/internal/discovery"
	"src.solsynth.dev/sosys/stargate/internal/geo"
	"src.solsynth.dev/sosys/stargate/internal/grpcclient"
	"src.solsynth.dev/sosys/stargate/internal/grpcserver"
	"src.solsynth.dev/sosys/stargate/internal/httpserver"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/migrate"
	"src.solsynth.dev/sosys/stargate/internal/module"
	"src.solsynth.dev/sosys/stargate/internal/nats"
	"src.solsynth.dev/sosys/stargate/internal/permission"
	redisclient "src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/relationship"
	"src.solsynth.dev/sosys/stargate/internal/seed"
	"src.solsynth.dev/sosys/stargate/internal/spell"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// moduleInfo is one entry in GET /api/modules.
type moduleInfo struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

type modulesResponse struct {
	Modules      []moduleInfo `json:"modules"`
	Capabilities []string     `json:"capabilities"`
}

// Run boots Stargate with the supplied compiled-in modules.
func Run(parent context.Context, log *slog.Logger, factories []module.Factory) error {
	cfg, err := config.Load("")
	if err != nil {
		return err
	}

	resolution, err := module.Resolve(factories, cfg.Modules.Enabled, cfg.Modules.Disabled)
	if err != nil {
		return fmt.Errorf("resolve modules: %w", err)
	}
	for _, warning := range resolution.Warnings {
		log.Warn(warning)
	}
	log.Info("modules resolved", "enabled", enabledIDs(resolution))

	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	database, err := db.Connect(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer func() {
		if err := db.Close(database); err != nil {
			log.Error("close database", "error", err)
		}
	}()

	if err := migrate.Run(ctx, database); err != nil {
		return err
	}
	if err := module.MigrateSchema(ctx, database, resolution.Factories); err != nil {
		return err
	}

	rc, err := redisclient.Connect(ctx, cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB)
	if err != nil {
		log.Warn("redis unavailable; cache-backed features disabled", "error", err)
		rc = nil
	}
	nc, err := nats.Connect(ctx, cfg)
	if err != nil {
		log.Warn("nats unavailable; events disabled", "error", err)
	}

	st := store.New(database)

	jwtService, err := auth.NewJWTService(cfg)
	if err != nil {
		return err
	}

	var singleUser *auth.SingleUser
	if cfg.SingleUserEnabled() {
		singleUser, err = auth.NewSingleUser(cfg, jwtService)
		if err != nil {
			return err
		}
		log.Info("single-user auth provider enabled", "username", cfg.Auth.SingleUser.Username)
	}

	clients, err := grpcclient.NewClients(cfg)
	if err != nil {
		return err
	}
	defer clients.Close()
	clients.FilterServices(map[string]bool{
		"drive":   resolution.Enabled["app.solian.drive"],
		"wallet":  resolution.Enabled["app.solian.wallet"],
		"pass":    resolution.Enabled["app.solian.pass"],
		"ring":    resolution.Enabled["app.solian.ring"],
		"blade":   resolution.Enabled["app.solian.blade"],
		"develop": resolution.Enabled["app.solian.develop"],
	})

	perkProvider := &grpcclient.WalletPerkProvider{Client: clients.Wallet, Log: log}
	appProvider := &grpcclient.DevelopAppProvider{Client: clients.Develop, Cfg: cfg, Log: log}

	presence := auth.NewDevicePresence(st, clients.Blade, log)
	if clients.Blade == nil {
		log.Warn("blade integration disabled or unconfigured; online presence reports offline")
	}

	tokenAuth := auth.NewTokenAuthService(st, rc, jwtService, perkProvider, appProvider, log)
	if singleUser != nil {
		tokenAuth.SetSingleUser(singleUser)
	}
	logs := actionlog.New(database)
	logs.Publish = func(ctx context.Context, id uuid.UUID, accountID, action string, meta map[string]any, sessionID *string, occurredAt time.Time) {
		if err := nc.PublishActionLogTriggered(ctx, id.String(), accountID, action, meta, sessionID, occurredAt); err != nil {
			log.Warn("publish action log event", "action", action, "account_id", accountID, "error", err)
		}
	}
	geoService := geo.NewService(cfg.GeoIP.DatabasePath)
	authService := auth.NewAuthService(st, rc, cfg, geoService, jwtService, tokenAuth, nc, logs, log)
	middleware.SetSudoChecker(authService)

	permService := permission.New(database)
	toucher := middleware.NewLastSeenToucher(st, log)
	defer toucher.Close()

	if err := seed.Seed(ctx, database); err != nil {
		return err
	}

	var renewer middleware.TokenRenewer = authService
	if singleUser != nil {
		renewer = singleUser
	}

	authMw := middleware.Auth(middleware.AuthDeps{
		Token:        tokenAuth,
		Renewer:      renewer,
		Toucher:      toucher,
		CookieDomain: cfg.Auth.CookieDomain,
		CookieSecure: cfg.Auth.CookieSecure,
		Log:          log,
	})

	spellService := spell.NewService(st, rc, clients.Ring, cfg.SiteUrl, cfg, log)

	// Event consumers are owned by the modules whose tables they touch.
	if nc != nil {
		if resolution.Enabled["app.solian.id.accounts"] {
			go consumeAccountActivated(ctx, nc, st, rc, log)
			go consumeTestPassedGroupGrant(ctx, nc, permService, rc, log)
		}
		if resolution.Enabled["app.solian.id.profiles"] {
			go consumeProfileFieldUpdated(ctx, nc, st, log)
			go consumeLastActive(ctx, nc, st, log)
		}
	}

	srv := httpserver.New(cfg, authMw)
	api := srv.API()

	services := &core.Services{
		Config: cfg, DB: database, Store: st, Redis: rc, NATS: nc,
		JWT: jwtService, Auth: authService, Token: tokenAuth, SingleUser: singleUser,
		Perm: permService, Logs: logs, Spells: spellService, Geo: geoService,
		Presence: presence, Toucher: toucher, Clients: clients, Log: log,
	}

	// The social graph is a module-owned table. Build its read service only
	// when the module is enabled: a disabled module leaves it nil, and the
	// gRPC relationship RPCs report Unimplemented rather than serving a
	// table that a fresh database never created.
	if resolution.Enabled["app.solian.id.relationships"] {
		services.Relationships = relationship.New(st, rc, log)
		// Passport's RelationshipExpiryJob (every 5 minutes) went away with
		// the table it swept, so the module owns the sweep now: without it a
		// timed block/mute never lapses and an unaccepted friend request
		// never resolves.
		services.Relationships.StartSweeper(ctx)
		// Passport's account-removal flow no longer deletes the relationship
		// rows (the table is not its own); it publishes
		// accounts.removal.confirmed and this consumer drops them.
		if nc != nil {
			go consumeAccountRemoval(ctx, nc, services.Relationships, log)
		}
	}
	// gRPC server. TLS mirrors DysonFS: when grpc.useTLS is set, certFile and
	// keyFile are required (self-signed fleet certs; clients skip CA checks).
	grpcOpts := []grpc.ServerOption{}
	if cfg.GRPC.UseTLS {
		if cfg.GRPC.CertFile == "" || cfg.GRPC.KeyFile == "" {
			return fmt.Errorf("grpc tls requires grpc.certFile and grpc.keyFile")
		}
		creds, err := credentials.NewServerTLSFromFile(cfg.GRPC.CertFile, cfg.GRPC.KeyFile)
		if err != nil {
			return fmt.Errorf("load grpc tls credentials: %w", err)
		}
		grpcOpts = append(grpcOpts, grpc.Creds(creds))
	}
	grpcSrv := grpc.NewServer(grpcOpts...)

	host := &module.Host{
		Core: services, Engine: srv.Engine, API: api, GRPC: grpcSrv,
		Ports: module.NewPortRegistry(), Enabled: resolution.Enabled,
		Settings: cfg.Modules.Settings, Log: log,
	}
	host.Ports.Select(cfg.Ports)

	registered := make([]module.Instance, 0, len(resolution.Factories))
	instances := make([]namedInstance, 0, len(resolution.Factories))
	for _, factory := range resolution.Factories {
		instance := factory.New()
		if instance == nil {
			return fmt.Errorf("module %s returned a nil instance", factory.ID)
		}
		instances = append(instances, namedInstance{factory: factory, instance: instance})
	}

	// Port providers publish before any Configure so consumers can resolve
	// local or remote implementations regardless of registration order.
	for _, entry := range instances {
		if provider, ok := entry.instance.(module.PortProvider); ok {
			if err := provider.ProvidePorts(ctx, host); err != nil {
				return &module.BuildError{Module: entry.factory.ID, Err: err}
			}
		}
	}
	for _, entry := range instances {
		if err := entry.instance.Configure(ctx, host); err != nil {
			return &module.BuildError{Module: entry.factory.ID, Err: err}
		}
		registered = append(registered, entry.instance)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for i := len(registered) - 1; i >= 0; i-- {
			if err := registered[i].Stop(shutdownCtx); err != nil {
				log.Warn("stop module", "error", err)
			}
		}
	}()

	// Core surface: permissions and action logs are infrastructure, and the
	// capabilities service reflects the enabled module set.
	grpcserver.RegisterCore(grpcSrv, grpcserver.Deps{
		Store: st, Redis: rc, Auth: authService, Token: tokenAuth, JWT: jwtService,
		Perm: permService, Logs: logs, Cfg: cfg, Log: log, Presence: presence,
		Capabilities: module.Capabilities(resolution.Factories),
	})

	api.GET("/modules", func(c *gin.Context) {
		c.JSON(http.StatusOK, modulesResponse{
			Modules:      moduleInfos(resolution.Factories),
			Capabilities: module.Capabilities(resolution.Factories),
		})
	})

	if srv.WebError != nil {
		log.Warn("web SPA hosting disabled", "dist", cfg.Web.DistPath, "error", srv.WebError)
	} else if strings.TrimSpace(cfg.Web.DistPath) != "" {
		log.Info("web SPA hosted", "dist", cfg.Web.DistPath)
	}

	// Blade service discovery is an integration; without the module there is
	// no registration and no dependency on Blade.
	var discoveryReg *discovery.Registration
	if resolution.Enabled["app.solian.blade"] && cfg.Discovery.Enabled && strings.TrimSpace(cfg.Discovery.Target) != "" {
		opts := discovery.Options{
			Service:           cfg.Discovery.Service,
			InstanceID:        cfg.Discovery.InstanceID,
			HttpEndpoint:      cfg.Discovery.HttpEndpoint,
			GrpcEndpoint:      cfg.Discovery.GrpcEndpoint,
			RegistrationToken: cfg.Discovery.RegistrationToken,
			LeaseSeconds:      cfg.Discovery.LeaseSeconds,
			Weight:            cfg.Discovery.Weight,
		}
		if opts.InstanceID == "" {
			opts.InstanceID = uuid.NewString()
		}
		if opts.HttpEndpoint == "" {
			opts.HttpEndpoint = "http://" + opts.Service + ":" + cfg.HTTP.Port
		}
		if opts.GrpcEndpoint == "" {
			opts.GrpcEndpoint = opts.Service + ":" + cfg.GRPC.Port
		}
		if err := discovery.Validate(opts); err != nil {
			return fmt.Errorf("discovery: %w", err)
		}
		conn, err := grpc.NewClient(cfg.Discovery.Target, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log.Warn("blade discovery unavailable; registration disabled", "target", cfg.Discovery.Target, "error", err)
		} else {
			defer conn.Close()
			discoveryReg = discovery.New(gen.NewDyServiceDiscoveryServiceClient(conn), opts, log)
			go discoveryReg.Run(ctx)
			log.Info("blade service discovery enabled",
				"service", opts.Service, "instance_id", opts.InstanceID, "target", cfg.Discovery.Target)
		}
	} else if resolution.Enabled["app.solian.blade"] && cfg.Discovery.Enabled {
		log.Warn("blade discovery enabled without a target; registration disabled")
	}

	errCh := make(chan error, 2)
	go func() {
		addr := ":" + cfg.HTTP.Port
		log.Info("http server listening", "addr", addr)
		errCh <- srv.Engine.Run(addr)
	}()
	go func() {
		addr := ":" + cfg.GRPC.Port
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			errCh <- err
			return
		}
		log.Info("grpc server listening", "addr", addr)
		errCh <- grpcSrv.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if discoveryReg != nil {
			discoveryReg.Deregister(shutdownCtx)
		}
		grpcSrv.GracefulStop()
		if nc != nil {
			nc.Close()
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

type namedInstance struct {
	factory  module.Factory
	instance module.Instance
}

func enabledIDs(resolution *module.Resolution) []string {
	ids := make([]string, 0, len(resolution.Factories))
	for _, factory := range resolution.Factories {
		ids = append(ids, factory.ID)
	}
	return ids
}

func moduleInfos(factories []module.Factory) []moduleInfo {
	out := make([]moduleInfo, 0, len(factories))
	for _, factory := range factories {
		out = append(out, moduleInfo{ID: factory.ID, Kind: string(factory.Kind)})
	}
	return out
}

// parseLogLevel maps the LOG_LEVEL env value to a slog level (default debug).
func parseLogLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "error":
		return slog.LevelError
	case "warn", "warning":
		return slog.LevelWarn
	case "info":
		return slog.LevelInfo
	default:
		return slog.LevelDebug
	}
}

// LogLevel is exported for cmd/stargate.
func LogLevel() slog.Level { return parseLogLevel(os.Getenv("LOG_LEVEL")) }
