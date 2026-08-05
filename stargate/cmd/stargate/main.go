// Stargate — the Go replacement for DysonNetwork.Padlock plus the Passport
// profile domain. Serves the /api/** routes (gateway adds the /padlock and
// /passport prefixes) plus /.well-known/* and the gRPC surface on a separate
// port.
package main

import (
	"context"
	"crypto/tls"
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
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/actionlog"
	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/db"
	"src.solsynth.dev/sosys/stargate/internal/discovery"
	"src.solsynth.dev/sosys/stargate/internal/geo"
	"src.solsynth.dev/sosys/stargate/internal/grpcclient"
	"src.solsynth.dev/sosys/stargate/internal/grpcserver"
	"src.solsynth.dev/sosys/stargate/internal/httpserver"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/adminctl"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/authctl"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/e2eectl"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/oidcctl"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/profilectl"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/securityctl"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/socialctl"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/spellctl"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/wellknownctl"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/migrate"
	"src.solsynth.dev/sosys/stargate/internal/nats"
	"src.solsynth.dev/sosys/stargate/internal/permission"
	redisclient "src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/seed"
	"src.solsynth.dev/sosys/stargate/internal/spell"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

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

// version and gitCommit are injected at build time (see Dockerfile).
var (
	version   = "dev"
	gitCommit = "unknown"
)

func main() {
	level := parseLogLevel(os.Getenv("LOG_LEVEL"))
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("stargate exited with error", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load("")
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := migrate.Run(ctx, pool); err != nil {
		return err
	}

	rc, err := redisclient.Connect(ctx, cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB)
	if err != nil {
		log.Warn("redis unavailable; starting without cache", "error", err)
		rc = &redisclient.Client{}
	}

	nc, err := nats.Connect(ctx, cfg)
	if err != nil {
		log.Warn("nats unavailable; events disabled", "error", err)
	}

	st := store.New(pool)

	jwtService, err := auth.NewJWTService(cfg)
	if err != nil {
		return err
	}

	clients, err := grpcclient.NewClients(cfg)
	if err != nil {
		return err
	}
	defer clients.Close()

	perkProvider := &grpcclient.WalletPerkProvider{Client: clients.Wallet, Log: log}
	appProvider := &grpcclient.DevelopAppProvider{Client: clients.Develop, Log: log}

	tokenAuth := auth.NewTokenAuthService(st, rc, jwtService, perkProvider, appProvider, log)
	logs := actionlog.New(pool)
	geoService := geo.NewService(cfg.GeoIP.DatabasePath)
	authService := auth.NewAuthService(st, rc, cfg, geoService, jwtService, tokenAuth, nc, logs, log)

	permService := permission.New(pool)

	toucher := middleware.NewLastSeenToucher(pool, log)
	defer toucher.Close()

	if err := seed.Seed(ctx, pool); err != nil {
		return err
	}

	authMw := middleware.Auth(middleware.AuthDeps{
		Token:   tokenAuth,
		Toucher: toucher,
		Log:     log,
	})

	spellService := spell.NewService(st, rc, clients.Ring, cfg.SiteUrl, log)

	srv := httpserver.New(cfg, authMw)
	srv.Register(registerCoreRoutes(authService, tokenAuth, permService, logs, st, cfg, log))
	srv.Register(func(api *gin.RouterGroup) {
		authctl.Register(api, authctl.Deps{
			Store: st, Redis: rc, Cfg: cfg, Token: tokenAuth, Auth: authService,
			Geo: geoService, Clients: clients, Events: nc, Log: log, Spells: spellService,
		})
		securityctl.Register(api, securityctl.Deps{
			Store: st, Redis: rc, Cfg: cfg, Auth: authService, Token: tokenAuth,
			Perm: permService, Logs: logs, Clients: clients, Log: log, Spells: spellService,
		})
		socialctl.Register(api, socialctl.Deps{
			Store: st, Redis: rc, Cfg: cfg, Auth: authService, Logs: logs, Log: log,
		})
		oidcctl.Register(api, oidcctl.Deps{
			Store: st, Redis: rc, Cfg: cfg, JWT: jwtService, Token: tokenAuth,
			Auth: authService, Clients: clients, Log: log,
		})
		adminctl.Register(api, adminctl.Deps{
			Store: st, Redis: rc, Cfg: cfg, Perm: permService, Logs: logs, Clients: clients, Log: log, Spells: spellService,
		})
		profilectl.Register(api, profilectl.Deps{
			Store: st, Redis: rc, Cfg: cfg, Perm: permService, Logs: logs, Clients: clients, Log: log,
		})
		e2eectl.Register(api, e2eectl.Deps{
			Store: st, Events: nc, Clients: clients, Log: log,
		})
		spellctl.Register(api, spellctl.Deps{
			Store: st, Spell: spellService, Log: log,
		})
	})
	wellknownctl.RegisterTop(srv.Engine, wellknownctl.Deps{})
	oidcctl.RegisterTop(srv.Engine, oidcctl.Deps{
		Store: st, Redis: rc, Cfg: cfg, JWT: jwtService, Token: tokenAuth,
		Auth: authService, Clients: clients, Log: log,
	})
	authctl.RegisterWellKnown(srv.Engine, cfg)

	// gRPC server (Phase 9 service servers). TLS mirrors DysonFS: when
	// grpc.useTLS is set, certFile/keyFile are required (self-signed fleet
	// certs; clients skip CA validation).
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
	registerGrpcServices(grpcSrv, authService, tokenAuth, st, permService, logs, jwtService, rc, cfg, log)

	// Blade service discovery: without registration, Blade's /meta capability
	// aggregator never sees this instance and the Padlock-family capabilities
	// (auth.*, e2ee, permissions, admin.*, accounts.*) disappear from /meta.
	var discoveryReg *discovery.Registration
	if cfg.Discovery.Enabled {
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
		conn, err := grpc.NewClient(cfg.Discovery.Target,
			grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: true})))
		if err != nil {
			return fmt.Errorf("dial blade discovery %s: %w", cfg.Discovery.Target, err)
		}
		defer conn.Close()
		discoveryReg = discovery.New(gen.NewDyServiceDiscoveryServiceClient(conn), opts, log)
		go discoveryReg.Run(ctx)
		log.Info("blade service discovery enabled",
			"service", opts.Service, "instance_id", opts.InstanceID, "target", cfg.Discovery.Target)
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
		_ = shutdownCtx
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

// registerCoreRoutes wires the Phase 4 controller routes; extended in later
// phases via additional registrars.
func registerCoreRoutes(
	authService *auth.AuthService,
	tokenAuth *auth.TokenAuthService,
	perm *permission.Service,
	logs *actionlog.Service,
	st *store.Store,
	cfg *config.Config,
	log *slog.Logger,
) httpserver.RouteRegistrar {
	return func(api *gin.RouterGroup) {
		// Phase 4+ controllers register here.
		_ = authService
		_ = tokenAuth
		_ = perm
		_ = logs
		_ = st
		_ = cfg
		_ = log
	}
}

// registerGrpcServices registers the inbound gRPC servers (Phase 9).
func registerGrpcServices(
	grpcSrv *grpc.Server,
	authService *auth.AuthService,
	tokenAuth *auth.TokenAuthService,
	st *store.Store,
	perm *permission.Service,
	logs *actionlog.Service,
	jwtService *auth.JWTService,
	rc *redisclient.Client,
	cfg *config.Config,
	log *slog.Logger,
) {
	grpcserver.Register(grpcSrv, grpcserver.Deps{
		Store: st, Redis: rc, Auth: authService, Token: tokenAuth, JWT: jwtService,
		Perm: perm, Logs: logs, Cfg: cfg, Log: log,
	})
}
