// Stargate — the Go replacement for DysonNetwork.Padlock plus the Passport
// profile domain. Serves the /api/** routes (gateway adds the /padlock and
// /passport prefixes) plus /.well-known/* and the gRPC surface on a separate
// port.
package main

import (
	"context"
	"encoding/json"
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
	"src.solsynth.dev/sosys/stargate/internal/model"
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

	rc, err := redisclient.Connect(ctx, cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB)
	if err != nil {
		log.Warn("redis unavailable; starting without cache", "error", err)
		rc = &redisclient.Client{}
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

	clients, err := grpcclient.NewClients(cfg)
	if err != nil {
		return err
	}
	defer clients.Close()

	perkProvider := &grpcclient.WalletPerkProvider{Client: clients.Wallet, Log: log}
	appProvider := &grpcclient.DevelopAppProvider{Client: clients.Develop, Log: log}

	tokenAuth := auth.NewTokenAuthService(st, rc, jwtService, perkProvider, appProvider, log)
	logs := actionlog.New(database)
	geoService := geo.NewService(cfg.GeoIP.DatabasePath)
	authService := auth.NewAuthService(st, rc, cfg, geoService, jwtService, tokenAuth, nc, logs, log)

	permService := permission.New(database)

	toucher := middleware.NewLastSeenToucher(st, log)
	defer toucher.Close()

	if err := seed.Seed(ctx, database); err != nil {
		return err
	}

	authMw := middleware.Auth(middleware.AuthDeps{
		Token:        tokenAuth,
		Renewer:      authService,
		Toucher:      toucher,
		CookieDomain: cfg.Auth.CookieDomain,
		CookieSecure: cfg.Auth.CookieSecure,
		Log:          log,
	})

	spellService := spell.NewService(st, rc, clients.Ring, cfg.SiteUrl, cfg, log)

	// Passport owns entry-test (exam) logic; when activation requirements are
	// satisfied there, it publishes accounts.activated and Stargate applies
	// the activation side effect (Padlock's old consumer wrote to its own DB,
	// which is no longer authoritative after cutover). The same applies to
	// permission-group grants from passed tests.
	if nc != nil {
		go consumeAccountActivated(ctx, nc, st, rc, log)
		go consumeTestPassedGroupGrant(ctx, nc, permService, rc, log)
		go consumeProfileFieldUpdated(ctx, nc, st, log)
		go consumeLastActive(ctx, nc, st, log)
	}

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
			Store: st, Redis: rc, Cfg: cfg, Auth: authService, Logs: logs, Log: log, Spells: spellService,
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
	registerGrpcServices(grpcSrv, authService, tokenAuth, st, permService, logs, jwtService, rc, cfg, log,
		e2eectl.NewService(st, nc, clients, log))

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
		conn, err := grpc.NewClient(cfg.Discovery.Target, grpc.WithTransportCredentials(insecure.NewCredentials()))
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

// consumeAccountActivated mirrors Padlock's AccountActivatedEvent consumer:
// it sets activated_at, grants the `verified` group and clears the
// permission cache. Runs until ctx is cancelled. Malformed events are acked
// (they can never succeed on redelivery), mirroring the C# behavior; DB
// errors leave the message unacked for redelivery.
func consumeAccountActivated(ctx context.Context, nc *nats.Client, st *store.Store, rc *redisclient.Client, log *slog.Logger) {
	if err := nc.ConsumeAccountEvents(ctx, "accounts.activated", "stargate_accountactivatedevent_consumer", func(payload []byte) error {
		var ev nats.AccountActivatedEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			log.Warn("malformed accounts.activated event", "error", err)
			return nil
		}
		id, err := uuid.Parse(ev.AccountID)
		if err != nil {
			log.Warn("accounts.activated event has invalid account id", "account_id", ev.AccountID, "payload", string(payload))
			return nil
		}
		activated, err := st.ActivateAccountAndGrantVerified(ctx, id, ev.ActivatedAt)
		if err != nil {
			return err
		}
		if !activated {
			log.Warn("accounts.activated for missing or already-activated account", "account_id", ev.AccountID)
			return nil
		}
		rc.ClearActorPermissionCache(ctx, ev.AccountID)
		log.Info("activated account from accounts.activated", "account_id", ev.AccountID)
		return nil
	}); err != nil {
		log.Warn("accounts.activated consumer stopped", "error", err)
	}
}

// consumeTestPassedGroupGrant mirrors Padlock's
// AccountTestPassedPermissionGroupEvent consumer: it grants the account the
// permission group a passed test configured and clears the permission cache.
// Runs until ctx is cancelled. Malformed events are acked; missing groups
// are logged and acked (nothing to do); DB errors redeliver.
func consumeTestPassedGroupGrant(ctx context.Context, nc *nats.Client, perm *permission.Service, rc *redisclient.Client, log *slog.Logger) {
	if err := nc.ConsumeAccountEvents(ctx, "accounts.tests.permission-group-granted", "stargate_accounttestpassedpermissiongroupevent_consumer", func(payload []byte) error {
		var ev nats.AccountTestPassedPermissionGroupEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			log.Warn("malformed accounts.tests.permission-group-granted event", "error", err)
			return nil
		}
		id, err := uuid.Parse(ev.AccountID)
		if err != nil {
			log.Warn("accounts.tests.permission-group-granted event has invalid account id", "account_id", ev.AccountID, "payload", string(payload))
			return nil
		}
		granted, err := perm.GrantPermissionGroup(ctx, id, ev.PermissionGroupKey)
		if err != nil {
			return err
		}
		if !granted {
			log.Warn("permission group not found for passed test",
				"account_id", ev.AccountID, "group_key", ev.PermissionGroupKey, "test_id", ev.TestID)
			return nil
		}
		rc.ClearActorPermissionCache(ctx, ev.AccountID)
		log.Info("granted permission group from passed test",
			"account_id", ev.AccountID, "group_key", ev.PermissionGroupKey, "test_id", ev.TestID)
		return nil
	}); err != nil {
		log.Warn("accounts.tests.permission-group-granted consumer stopped", "error", err)
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
	e2eeService *e2eectl.Service,
) {
	grpcserver.Register(grpcSrv, grpcserver.Deps{
		Store: st, Redis: rc, Auth: authService, Token: tokenAuth, JWT: jwtService,
		Perm: perm, Logs: logs, E2ee: e2eeService, Cfg: cfg, Log: log,
	})
}

// consumeProfileFieldUpdated applies Passport-published profile field patches
// (last_seen touches, XP deltas, social-credit recomputes, active badge and
// verification changes) to Stargate's account_profiles after the profile
// table moved here. Malformed events are acked; DB errors redeliver.
func consumeProfileFieldUpdated(ctx context.Context, nc *nats.Client, st *store.Store, log *slog.Logger) {
	if err := nc.ConsumeAccountEvents(ctx, "accounts.profile_updated", "stargate_profilefieldupdatedevent_consumer", func(payload []byte) error {
		var ev nats.ProfileFieldUpdatedEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			log.Warn("malformed accounts.profile_updated event", "error", err)
			return nil
		}
		id, err := uuid.Parse(ev.AccountID)
		if err != nil {
			log.Warn("accounts.profile_updated event has invalid account id", "account_id", ev.AccountID, "payload", string(payload))
			return nil
		}
		patch := &store.ProfileFieldPatch{
			LastSeenAt:      ev.LastSeenAt,
			Experience:      ev.Experience,
			ExperienceDelta: ev.ExperienceDelta,
			SocialCredits:   ev.SocialCredits,
		}
		if len(ev.ActiveBadge) > 0 {
			patch.HasActiveBadge = true
			if string(ev.ActiveBadge) != "null" {
				var value any
				if err := json.Unmarshal(ev.ActiveBadge, &value); err != nil {
					log.Warn("accounts.profile_updated event has malformed active_badge", "account_id", ev.AccountID, "error", err)
					return nil
				}
				patch.ActiveBadge = value
			}
		}
		if len(ev.Verification) > 0 {
			patch.HasVerification = true
			if string(ev.Verification) != "null" {
				var mark model.SnVerificationMark
				if err := json.Unmarshal(ev.Verification, &mark); err != nil {
					log.Warn("accounts.profile_updated event has malformed verification", "account_id", ev.AccountID, "error", err)
					return nil
				}
				patch.Verification = &mark
			}
		}
		if err := st.ApplyProfileFieldPatch(ctx, id, patch); err != nil {
			return err
		}
		log.Debug("applied accounts.profile_updated patch", "account_id", ev.AccountID)
		return nil
	}); err != nil {
		log.Warn("accounts.profile_updated consumer stopped", "error", err)
	}
}

// consumeLastActive applies the fleet's last-active signals (published by the
// shared DysonTokenAuthHandler on accounts.last_active): profile last_seen_at
// plus the session last_granted_at/keep-alive. Malformed events are acked;
// DB errors redeliver.
func consumeLastActive(ctx context.Context, nc *nats.Client, st *store.Store, log *slog.Logger) {
	if err := nc.ConsumeAccountEvents(ctx, "accounts.last_active", "stargate_lastactiveevent_consumer", func(payload []byte) error {
		var ev nats.LastActiveEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			log.Warn("malformed accounts.last_active event", "error", err)
			return nil
		}
		if _, err := uuid.Parse(ev.AccountID); err != nil {
			log.Warn("accounts.last_active event has invalid account id", "account_id", ev.AccountID, "payload", string(payload))
			return nil
		}
		sessionID := ""
		if ev.SessionID != "" {
			if _, err := uuid.Parse(ev.SessionID); err == nil {
				sessionID = ev.SessionID
			}
		}
		if err := st.TouchLastActive(ctx, ev.AccountID, sessionID, ev.SeenAt); err != nil {
			return err
		}
		log.Debug("applied accounts.last_active", "account_id", ev.AccountID)
		return nil
	}); err != nil {
		log.Warn("accounts.last_active consumer stopped", "error", err)
	}
}
