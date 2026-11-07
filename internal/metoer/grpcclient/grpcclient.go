// Package grpcclient holds the outbound gRPC clients Metoer uses to call
// sibling services: stargate (account, auth, permission, profile,
// action-log) and blade (websocket connection status, service discovery).
// Targets come from the [services] config section; an empty target leaves the
// corresponding client nil unless Blade discovery is enabled, in which case
// the service name is resolved from the registry. A missing target degrades
// gracefully, matching the C# services' behavior with a dependency down.
package grpcclient

import (
	"context"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
	"src.solsynth.dev/sosys/metoer/discovery"
	"src.solsynth.dev/sosys/metoer/grpctls"

	"src.solsynth.dev/sosys/metoer/config"
)

// Dial creates a TLS gRPC connection using the fleet defaults: server
// validation is skipped and no client certificate is presented. Use dialTLS
// with configured Files for servers enforcing mTLS.
func Dial(target string) (*grpc.ClientConn, error) {
	return dialTLS(target, grpctls.Files{})
}

// dialTLS creates a TLS gRPC connection with files supplying the CA bundle,
// the client certificate and the verification server name.
func dialTLS(target string, files grpctls.Files) (*grpc.ClientConn, error) {
	if target == "" {
		return nil, nil
	}
	creds, err := grpctls.Client(files)
	if err != nil {
		return nil, err
	}
	return grpc.NewClient(target,
		grpc.WithTransportCredentials(creds),
		grpc.WithDefaultCallOptions(grpc.WaitForReady(false)),
	)
}

// DialPlaintext creates a plaintext gRPC connection. It is selected by an
// explicit http:// or grpc:// target, for deployments that still serve a
// dependency over h2c; the fleet default is TLS via Dial.
func DialPlaintext(target string) (*grpc.ClientConn, error) {
	if target == "" {
		return nil, nil
	}
	return grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.WaitForReady(false)),
	)
}

// Clients holds the outbound connections.
type Clients struct {
	Stargate *grpc.ClientConn
	Blade    *grpc.ClientConn
	conns    []*grpc.ClientConn
}

// NewClients dials every configured target. Use NewClientsWithResolver when
// Blade discovery is enabled.
func NewClients(cfg *config.Config) (*Clients, error) {
	return NewClientsWithResolver(cfg, nil)
}

// NewClientsWithResolver follows Blade's registry while it is available and
// falls back to the configured [services] target for a service with no
// registration. An empty target still creates a discovery-backed client.
func NewClientsWithResolver(cfg *config.Config, resolver *discovery.Resolver) (*Clients, error) {
	c := &Clients{}
	files := grpctls.Files{
		CAFile:   cfg.GRPC.CAFile,
		CertFile: cfg.GRPC.ClientCertFile,
		KeyFile:  cfg.GRPC.ClientKeyFile,
	}
	dial := func(service, target string, plaintext bool) *grpc.ClientConn {
		address, useTLS := splitTarget(target, !plaintext)
		if resolver != nil {
			address = discovery.Target(service)
		}
		if address == "" {
			return nil
		}
		if discovery.IsTarget(address) && resolver == nil {
			return nil
		}
		var conn *grpc.ClientConn
		var err error
		if useTLS {
			conn, err = dialTLS(address, files)
		} else {
			conn, err = DialPlaintext(address)
		}
		if err != nil || conn == nil {
			return nil
		}
		c.conns = append(c.conns, conn)
		return conn
	}
	if conn := dial("stargate", cfg.Services.Stargate.GRPC, false); conn != nil {
		c.Stargate = conn
	}
	// Blade serves TLS like the rest of the fleet; an explicit http:// target
	// selects the plaintext h2c path for older deployments.
	if conn := dial("blade", cfg.Services.Blade.GRPC, false); conn != nil {
		c.Blade = conn
	}
	return c, nil
}

// splitTarget splits a configured target into its dial address and transport.
// An explicit scheme wins; otherwise defaultTLS selects the fleet default.
func splitTarget(raw string, defaultTLS bool) (string, bool) {
	target := strings.TrimSpace(raw)
	if target == "" {
		return "", defaultTLS
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return target, defaultTLS
	}
	switch strings.ToLower(parsed.Scheme) {
	case "grpc", "http":
		return parsed.Host, false
	case "grpcs", "https":
		return parsed.Host, true
	default:
		return target, defaultTLS
	}
}

// Close closes all dialed connections.
func (c *Clients) Close() {
	for _, conn := range c.conns {
		if conn != nil {
			_ = conn.Close()
		}
	}
}

// Account returns the DyAccountService client (nil-safe).
func (c *Clients) Account() gen.DyAccountServiceClient {
	if c == nil || c.Stargate == nil {
		return nil
	}
	return gen.NewDyAccountServiceClient(c.Stargate)
}

// Permission returns the DyPermissionService client (nil-safe).
func (c *Clients) Permission() gen.DyPermissionServiceClient {
	if c == nil || c.Stargate == nil {
		return nil
	}
	return gen.NewDyPermissionServiceClient(c.Stargate)
}

// Profile returns the DyProfileService client (nil-safe).
func (c *Clients) Profile() gen.DyProfileServiceClient {
	if c == nil || c.Stargate == nil {
		return nil
	}
	return gen.NewDyProfileServiceClient(c.Stargate)
}

// ActionLog returns the DyActionLogService client (nil-safe).
func (c *Clients) ActionLog() gen.DyActionLogServiceClient {
	if c == nil || c.Stargate == nil {
		return nil
	}
	return gen.NewDyActionLogServiceClient(c.Stargate)
}

// WebSocket returns the WebSocketService client (nil-safe).
func (c *Clients) WebSocket() gen.WebSocketServiceClient {
	if c == nil || c.Blade == nil {
		return nil
	}
	return gen.NewWebSocketServiceClient(c.Blade)
}

// ActionLogService mirrors RemoteActionLogService.CreateActionLog: a
// fire-and-forget action-log write (SubscribeDevice's AccountPushEnable log).
type ActionLogService struct {
	Client gen.DyActionLogServiceClient
	Log    *slog.Logger
}

// CreateActionLog mirrors RemoteActionLogService.CreateActionLog(accountId,
// action, meta): fire-and-forget (the C# drops the Task).
func (s *ActionLogService) CreateActionLog(accountID string, action string, meta map[string]string) {
	if s == nil || s.Client == nil {
		return
	}
	req := &gen.DyCreateActionLogRequest{
		AccountId: accountID,
		Action:    action,
		Meta:      map[string]*structpb.Value{},
	}
	for k, v := range meta {
		req.Meta[k] = structpb.NewStringValue(v)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if _, err := s.Client.CreateActionLog(ctx, req); err != nil && s.Log != nil {
		s.Log.Warn("failed to create action log", "action", action, "error", err)
	}
}
