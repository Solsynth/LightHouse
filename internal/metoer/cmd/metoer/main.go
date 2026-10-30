// Metoer — the Go replacement for DysonNetwork.Ring: notifications, push
// delivery (FCM/APNs/UnifiedPush/SOP), email sending plans and delivery
// observability. Serves the /api/** routes (gateway adds the /ring prefix
// during cutover) plus the DyRingService gRPC surface on a separate port.
package main

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

	"src.solsynth.dev/sosys/go/pkg/auth"

	"src.solsynth.dev/sosys/metoer/internal/config"
	"src.solsynth.dev/sosys/metoer/internal/db"
	"src.solsynth.dev/sosys/metoer/internal/discovery"
	"src.solsynth.dev/sosys/metoer/internal/email"
	"src.solsynth.dev/sosys/metoer/internal/events"
	"src.solsynth.dev/sosys/metoer/internal/grpcclient"
	"src.solsynth.dev/sosys/metoer/internal/grpcserver"
	"src.solsynth.dev/sosys/metoer/internal/httpserver"
	"src.solsynth.dev/sosys/metoer/internal/httpserver/adminctl"
	"src.solsynth.dev/sosys/metoer/internal/httpserver/notificationctl"
	"src.solsynth.dev/sosys/metoer/internal/httpserver/sopctl"
	"src.solsynth.dev/sosys/metoer/internal/middleware"
	"src.solsynth.dev/sosys/metoer/internal/migrate"
	"src.solsynth.dev/sosys/metoer/internal/observability"
	"src.solsynth.dev/sosys/metoer/internal/push"
	"src.solsynth.dev/sosys/metoer/internal/queue"
	redisclient "src.solsynth.dev/sosys/metoer/internal/redis"
	"src.solsynth.dev/sosys/metoer/internal/scheduler"
	"src.solsynth.dev/sosys/metoer/internal/store"

	eb "src.solsynth.dev/sosys/go/pkg/eventbus"
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
	log.Info("metoer starting", "version", version, "commit", gitCommit)

	if err := run(log); err != nil {
		log.Error("metoer exited with error", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load("")
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	log.Info("configuration loaded", "http_port", cfg.HTTP.Port, "grpc_port", cfg.GRPC.Port)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info("connecting database")
	database, err := db.Connect(ctx, cfg.Database.DSN)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer db.Close(database)
	log.Info("database connected")

	log.Info("running database migrations")
	if err := migrate.Run(ctx, database); err != nil {
		return fmt.Errorf("run database migrations: %w", err)
	}
	log.Info("database migrations complete")

	rc, err := redisclient.Connect(ctx, cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB)
	if err != nil {
		log.Warn("redis unavailable; starting without cache", "error", err)
		rc = &redisclient.Client{}
	}

	nc, err := eb.Connect(cfg.NATS.Target)
	if err != nil {
		log.Warn("nats unavailable; events disabled", "error", err)
		nc = nil
	}

	st := store.New(database)

	clients, err := grpcclient.NewClients(cfg)
	if err != nil {
		return err
	}
	defer clients.Close()

	obs := observability.New(st, log)

	// In-process dispatcher first (the push service enqueues through it).
	queueSvc := queue.New(cfg.ConsumerCountValue(), log)

	appSenders := push.NewApps(cfg, &http.Client{Timeout: 30 * time.Second})
	streams := push.NewSopStreams()
	replay := push.NewSopNotificationReplayBuffer(rc.Cache)
	ws := &events.WebSocketService{Bus: nc, Blade: clients, Log: log}
	actionLogs := &grpcclient.ActionLogService{Client: clients.ActionLog(), Log: log}
	pushSvc := push.New(appSenders, st, queueSvc.EnqueuePushNotification, streams, replay, ws, obs, actionLogs, nil, log)

	emailSvc, err := email.New(cfg, obs, log)
	if err != nil {
		log.Warn("email service not configured", "error", err)
	}
	plans := email.NewPlanService(st, clients.Account(), emailSvc, log)

	// Async job dispatch (queue ↔ push/email cycle is broken here in main to
	// avoid an import cycle).
	queueSvc.SetHandler(pushQueueHandler(pushSvc, emailSvc, log))

	// Auth middleware: GrpcTokenAuthenticator against Stargate's
	// DyAuthService, cached sessions (1h), profile hydration (5m) and the
	// throttled accounts.last_active publisher.
	var authenticator auth.TokenAuthenticator
	if cfg.Services.Stargate.GRPC != "" {
		authCfg := auth.GrpcAuthDialConfig{
			Target:        cfg.Services.Stargate.GRPC,
			UseTLS:        true,
			TLSSkipVerify: true,
		}
		grpcAuth, err := auth.NewGrpcTokenAuthenticator(authCfg)
		if err != nil {
			return err
		}
		defer grpcAuth.Close()
		authenticator = grpcAuth
	}
	authMw := middleware.Auth(middleware.AuthDeps{
		Authenticator: authenticator,
		Cache:         rc.Cache,
		Profiles:      clients.Profile(),
		LastActive: &middleware.LastActivePublisher{
			Bus:   nc,
			Cache: rc.Cache,
			Log:   log,
		},
		Log: log,
	})

	srv := httpserver.New(cfg, authMw)
	srv.Register(func(api *gin.RouterGroup) {
		notificationctl.Register(api, notificationctl.Deps{
			Push:  pushSvc,
			Store: st,
			Perm:  clients.Permission(),
			Log:   log,
		})
		sopctl.Register(api, sopctl.Deps{
			Push: pushSvc,
			Perm: clients.Permission(),
			Log:  log,
		})
		adminctl.Register(api, adminctl.Deps{
			Plans: plans,
			Store: st,
			Perm:  clients.Permission(),
			Log:   log,
		})
	})
	srv.Engine.GET("/swagger/v1/swagger.json", adminctl.ServeSwagger)
	srv.Engine.GET("/swagger", adminctl.ServeSwaggerUI)

	// gRPC server. TLS mirrors DysonFS: when grpc.useTLS is set,
	// certFile/keyFile are required (self-signed fleet certs; clients skip
	// CA validation).
	grpcOpts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(16 * 1024 * 1024),
		grpc.MaxSendMsgSize(16 * 1024 * 1024),
	}
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
	grpcserver.Register(grpcSrv, grpcserver.Deps{
		Queue: queueSvc,
		Push:  pushSvc,
		Email: emailSvc,
		Log:   log,
	})

	// Blade service discovery: without registration, Blade's /meta
	// capability aggregator never sees this instance and the
	// notifications.* / admin.* capabilities disappear from /meta.
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
			"service", opts.Service, "instance_id", opts.InstanceID, "target", cfg.Discovery.Target,
			"http_endpoint", opts.HttpEndpoint, "grpc_endpoint", opts.GrpcEndpoint)
	}

	// Scheduled jobs + the filesystem listener.
	go scheduler.Run(ctx, scheduler.Options{
		Store: st,
		Push:  pushSvc,
		Plans: plans,
		Log:   log,
	})
	go func() {
		if err := events.ConsumeFileMetadataUpdated(ctx, nc, st, log); err != nil {
			log.Warn("filesystem listener stopped", "error", err)
		}
	}()
	go func() {
		if err := queueSvc.Run(ctx); err != nil {
			log.Error("queue workers stopped", "error", err)
		}
	}()

	httpAddr := ":" + cfg.HTTP.Port
	httpLn, err := net.Listen("tcp", httpAddr)
	if err != nil {
		return fmt.Errorf("listen http on %s: %w", httpAddr, err)
	}
	httpSrv := &http.Server{Handler: srv.Engine}

	grpcAddr := ":" + cfg.GRPC.Port
	grpcLn, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		_ = httpLn.Close()
		return fmt.Errorf("listen grpc on %s: %w", grpcAddr, err)
	}

	errCh := make(chan error, 2)
	log.Info("http server listening", "addr", httpAddr, "version", version, "commit", gitCommit)
	go func() {
		errCh <- httpSrv.Serve(httpLn)
	}()
	log.Info("grpc server listening", "addr", grpcAddr)
	go func() {
		errCh <- grpcSrv.Serve(grpcLn)
	}()

	select {
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if discoveryReg != nil {
			discoveryReg.Deregister(shutdownCtx)
		}
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Warn("http server shutdown failed", "error", err)
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

// pushQueueHandler dispatches async jobs to push/email (queue ↔ push/email
// would otherwise be an import cycle).
func pushQueueHandler(pushSvc *push.Service, emailSvc *email.Service, log *slog.Logger) queue.MessageHandler {
	return func(ctx context.Context, job *queue.Job) error {
		switch job.Type {
		case queue.JobTypeEmail:
			if emailSvc == nil {
				log.Warn("email service not configured; dropping email job")
				return nil
			}
			if job.Email == nil {
				return nil
			}
			return emailSvc.SendEmail(ctx, job.Email.ToName, job.Email.ToAddress, job.Email.Subject, job.Email.Body, "queue")

		case queue.JobTypePushNotification:
			if job.Push == nil {
				return nil
			}
			return pushSvc.DeliverPushNotification(ctx, job.Push.Notification, job.Push.ExcludedWebSocketDeviceIDs, job.Push.IsSavable)

		default:
			log.Warn("unknown job type", "type", job.Type)
			return nil
		}
	}
}
