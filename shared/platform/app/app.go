// Package app owns a Lighthouse service's process bootstrap: it resolves the
// compiled-in module set, connects the shared dependencies, applies the schema,
// builds the service's core graph and runs the HTTP and gRPC servers. Each
// service supplies its own core type, migrations and module factories, so the
// Sphere and Messager processes share the machinery but never a process.
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
	"gorm.io/gorm"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/config"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/db"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/discovery"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/eventbus"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/grpcclient"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/grpccore"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/jobs"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	redisclient "src.solsynth.dev/sosys/lighthouse/shared/platform/redis"
)

// Env is the shared dependency graph handed to the service's core builder.
type Env struct {
	DB      *gorm.DB
	Redis   CacheService
	NATS    *eventbus.Client
	Clients *grpcclient.Clients
	Auth    *authn.Service
	Jobs    *jobs.Scheduler
	Log     *slog.Logger
}

// CacheService is the shared cache interface, re-exported so services do not
// import the fleet package for this one type.
type CacheService = interface {
	Set(ctx context.Context, key string, value any, ttl time.Duration) error
	Get(ctx context.Context, key string, dest any) (bool, error)
	Remove(ctx context.Context, key string) error
	SetWithGroups(ctx context.Context, key string, value any, groups []string, ttl time.Duration) error
	ExecuteWithLock(ctx context.Context, resource string, expiry time.Duration, waitTime time.Duration, retryInterval time.Duration, action func(context.Context) error) (bool, error)
}

// Options configures one service process.
type Options[C any] struct {
	// Name is the service's registry name ("sphere", "messager").
	Name string
	// Base is the shared configuration block of the service config.
	Base *config.Base
	// Factories is the compiled-in module set.
	Factories []module.Factory[C]
	// Migrate applies the service's core schema (the tables every module can
	// assume exist). It runs before module schema hooks.
	Migrate func(ctx context.Context, db *gorm.DB) error
	// Build constructs the service's core graph.
	Build func(ctx context.Context, env *Env) (*C, error)
	// RegisterCore wires the service's core gRPC services and extra routes.
	RegisterCore func(ctx context.Context, host *module.Host[C]) error
}

// moduleInfo is one entry in GET /api/modules.
type moduleInfo struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	Enabled      bool     `json:"enabled"`
	Required     bool     `json:"required"`
	Requires     []string `json:"requires,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type modulesResponse struct {
	Service      string       `json:"service"`
	Modules      []moduleInfo `json:"modules"`
	Capabilities []string     `json:"capabilities"`
}

type namedInstance[C any] struct {
	factory  module.Factory[C]
	instance module.Instance[C]
}

// Run boots the service with the supplied compiled-in modules.
func Run[C any](parent context.Context, log *slog.Logger, opts Options[C]) error {
	if opts.Base == nil {
		return fmt.Errorf("app: base configuration is required")
	}
	base := opts.Base

	resolution, err := module.Resolve(opts.Factories, base.Modules.Enabled, base.Modules.Disabled)
	if err != nil {
		return fmt.Errorf("resolve modules: %w", err)
	}
	for _, warning := range resolution.Warnings {
		log.Warn(warning)
	}
	log.Info("modules resolved", "service", opts.Name, "enabled", enabledIDs(resolution))

	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	database, err := db.Connect(ctx, base.Database.DSN)
	if err != nil {
		return err
	}
	defer func() {
		if err := db.Close(database); err != nil {
			log.Error("close database", "error", err)
		}
	}()

	if opts.Migrate != nil {
		if err := opts.Migrate(ctx, database); err != nil {
			return fmt.Errorf("core migrations: %w", err)
		}
	}
	if err := module.MigrateSchema(ctx, database, resolution.Factories); err != nil {
		return err
	}

	rc, err := redisclient.Connect(ctx, base.Redis.Addr, base.Redis.Password, base.Redis.DB)
	if err != nil {
		log.Warn("redis unavailable; falling back to the in-process cache", "error", err)
		rc, _ = redisclient.Connect(ctx, "", "", 0)
	}

	nc, err := eventbus.Connect(base.NATS.Target, base.NATS.WebsocketPushSubject, base.NATS.WebsocketPushStream, log)
	if err != nil {
		log.Warn("nats unavailable; events disabled", "error", err)
		nc, _ = eventbus.Connect("", "", "", log)
	}
	defer nc.Close()

	var discoveryProvider *discovery.RemoteProvider
	var discoveryResolver *discovery.Resolver
	if base.Discovery.Enabled && strings.TrimSpace(base.Discovery.Target) != "" {
		provider, err := discovery.NewRemoteProvider(base.Discovery.Target)
		if err != nil {
			log.Warn("blade discovery resolver unavailable; using static [services] targets", "error", err)
		} else {
			discoveryProvider = provider
			discoveryResolver = discovery.NewResolver(provider, serviceFallbacks(base))
			discovery.RegisterResolver(discoveryResolver)
			defer discoveryProvider.Close()
			log.Info("blade discovery resolver enabled", "target", base.Discovery.Target)
		}
	}

	clients, err := grpcclient.NewWithResolver(grpcclient.Targets{
		Stargate: base.Services.Stargate.GRPC,
		Sphere:   base.Services.Sphere.GRPC,
		Messager: base.Services.Messager.GRPC,
		Ring:     base.Services.Ring.GRPC,
		Drive:    base.Services.Drive.GRPC,
		Wallet:   base.Services.Wallet.GRPC,
		Develop:  base.Services.Develop.GRPC,
		Blade:    base.Services.Blade.GRPC,
	}, discoveryResolver)
	if err != nil {
		return err
	}
	defer clients.Close()

	authTarget := base.Services.Stargate.GRPC
	authUseTLS := !strings.HasPrefix(strings.ToLower(base.Services.Stargate.GRPC), "http://") &&
		!strings.HasPrefix(strings.ToLower(base.Services.Stargate.GRPC), "grpc://")
	if discoveryResolver != nil {
		authTarget = discovery.Target("stargate")
	}
	authenticator, err := authn.New(authn.Options{
		Target: authTarget,
		UseTLS: authUseTLS,
		Cache:  rc,
		Log:    log,
	})
	if err != nil {
		return err
	}
	defer authenticator.Close()
	if !authenticator.Configured() {
		log.Warn("auth service not configured; authenticated routes will answer 401")
	}

	scheduler := jobs.New(ctx, log)

	env := &Env{
		DB: database, Redis: rc, NATS: nc, Clients: clients,
		Auth: authenticator, Jobs: scheduler, Log: log,
	}
	core, err := opts.Build(ctx, env)
	if err != nil {
		return err
	}

	srv := httpx.New(httpx.Options{
		Log:              log,
		Auth:             authenticator.Middleware(),
		TrustedProxyHops: base.Security.TrustedProxyHops,
	})

	grpcOpts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(100 * 1024 * 1024),
		grpc.MaxSendMsgSize(100 * 1024 * 1024),
	}
	if base.GRPC.UseTLS {
		if base.GRPC.CertFile == "" || base.GRPC.KeyFile == "" {
			return fmt.Errorf("grpc tls requires grpc.certFile and grpc.keyFile")
		}
		creds, err := credentials.NewServerTLSFromFile(base.GRPC.CertFile, base.GRPC.KeyFile)
		if err != nil {
			return fmt.Errorf("load grpc tls credentials: %w", err)
		}
		grpcOpts = append(grpcOpts, grpc.Creds(creds))
	}
	grpcSrv := grpc.NewServer(grpcOpts...)
	grpccore.Register(grpcSrv, module.Capabilities(resolution.Factories))
	_ = opts.Factories

	host := &module.Host[C]{
		Core: core, Engine: srv.Engine, API: srv.API(), GRPC: grpcSrv,
		Ports: module.NewPortRegistry(), Enabled: resolution.Enabled,
		Settings: base.Modules.Settings, Log: log,
	}
	host.Ports.Select(base.Ports)

	instances := make([]namedInstance[C], 0, len(resolution.Factories))
	for _, factory := range resolution.Factories {
		instance := factory.New()
		if instance == nil {
			return fmt.Errorf("module %s returned a nil instance", factory.ID)
		}
		instances = append(instances, namedInstance[C]{factory: factory, instance: instance})
	}

	// Port providers publish before any Configure so consumers can resolve
	// local or remote implementations regardless of registration order.
	for _, entry := range instances {
		if provider, ok := entry.instance.(module.PortProvider[C]); ok {
			if err := provider.ProvidePorts(ctx, host); err != nil {
				return &module.BuildError{Module: entry.factory.ID, Err: err}
			}
		}
	}
	registered := make([]module.Instance[C], 0, len(instances))
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

	if opts.RegisterCore != nil {
		if err := opts.RegisterCore(ctx, host); err != nil {
			return err
		}
	}

	srv.API().GET("/modules", func(c *gin.Context) {
		c.JSON(http.StatusOK, modulesResponse{
			Service:      opts.Name,
			Modules:      moduleInfos(resolution.Factories, resolution.Enabled),
			Capabilities: module.Capabilities(resolution.Factories),
		})
	})

	// Blade service discovery is optional; without a target there is no
	// registration and no dependency on Blade.
	var discoveryReg *discovery.Registration
	if base.Discovery.Enabled && strings.TrimSpace(base.Discovery.Target) != "" {
		discoveryOpts := discovery.Options{
			Service:      base.Discovery.Service,
			InstanceID:   base.Discovery.InstanceID,
			HttpEndpoint: base.Discovery.HttpEndpoint,
			GrpcEndpoint: base.Discovery.GrpcEndpoint,
			LeaseSeconds: base.Discovery.LeaseSeconds,
			Weight:       base.Discovery.Weight,
		}
		if discoveryOpts.InstanceID == "" {
			discoveryOpts.InstanceID = uuid.NewString()
		}
		if discoveryOpts.HttpEndpoint == "" {
			discoveryOpts.HttpEndpoint = "http://" + discoveryOpts.Service + ":" + base.HTTP.Port
		}
		if discoveryOpts.GrpcEndpoint == "" {
			discoveryOpts.GrpcEndpoint = discoveryOpts.Service + ":" + base.GRPC.Port
		}
		if err := discovery.Validate(discoveryOpts); err != nil {
			return fmt.Errorf("discovery: %w", err)
		}
		conn, err := grpc.NewClient(base.Discovery.Target, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log.Warn("blade discovery unavailable; registration disabled", "target", base.Discovery.Target, "error", err)
		} else {
			defer conn.Close()
			discoveryReg = discovery.New(gen.NewDyServiceDiscoveryServiceClient(conn), discoveryOpts, log)
			go discoveryReg.Run(ctx)
			log.Info("blade service discovery enabled",
				"service", discoveryOpts.Service, "instance_id", discoveryOpts.InstanceID, "target", base.Discovery.Target)
		}
	} else if base.Discovery.Enabled {
		log.Warn("blade discovery enabled without a target; registration disabled")
	}

	errCh := make(chan error, 2)
	go func() {
		addr := ":" + base.HTTP.Port
		log.Info("http server listening", "service", opts.Name, "addr", addr)
		errCh <- srv.Engine.Run(addr)
	}()
	go func() {
		addr := ":" + base.GRPC.Port
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			errCh <- err
			return
		}
		log.Info("grpc server listening", "service", opts.Name, "addr", addr)
		errCh <- grpcSrv.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		log.Info("shutting down", "service", opts.Name)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if discoveryReg != nil {
			discoveryReg.Deregister(shutdownCtx)
		}
		grpcSrv.GracefulStop()
		scheduler.Wait()
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func enabledIDs[C any](resolution *module.Resolution[C]) []string {
	ids := make([]string, 0, len(resolution.Factories))
	for _, factory := range resolution.Factories {
		ids = append(ids, factory.ID)
	}
	return ids
}

// serviceFallbacks maps each configured service name to its static [services]
// target. Blade's discovery resolver uses these only while a service has no
// registration, so they remain a migration-safe fallback rather than the
// primary address. Blade itself never registers, so its discovery target is
// the natural fallback for the outbound websocket client.
func serviceFallbacks(base *config.Base) map[string]string {
	bladeTarget := base.Services.Blade.GRPC
	if strings.TrimSpace(bladeTarget) == "" {
		bladeTarget = base.Discovery.Target
	}
	return map[string]string{
		"stargate": base.Services.Stargate.GRPC,
		"sphere":   base.Services.Sphere.GRPC,
		"messager": base.Services.Messager.GRPC,
		"ring":     base.Services.Ring.GRPC,
		"drive":    base.Services.Drive.GRPC,
		"wallet":   base.Services.Wallet.GRPC,
		"develop":  base.Services.Develop.GRPC,
		"blade":    bladeTarget,
	}
}

func moduleInfos[C any](factories []module.Factory[C], enabled map[string]bool) []moduleInfo {
	out := make([]moduleInfo, 0, len(factories))
	for _, factory := range factories {
		out = append(out, moduleInfo{
			ID:           factory.ID,
			Kind:         string(factory.Kind),
			Enabled:      enabled[factory.ID],
			Required:     factory.Required,
			Requires:     factory.Requires,
			Capabilities: factory.Capabilities,
		})
	}
	return out
}

// LogLevel maps the LOG_LEVEL env value to a slog level (default debug).
func LogLevel() slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))) {
	case "debug":
		return slog.LevelDebug
	case "info", "":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
