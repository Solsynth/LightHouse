// Package grpcclient holds the outbound gRPC clients Metoer uses to call
// sibling services: stargate (account, auth, permission, profile,
// action-log) and blade (websocket connection status, service discovery).
// Targets come from the [services] config section; when a target is empty
// the corresponding client is nil and callers degrade gracefully, matching
// the C# services' behavior with a dependency down.
package grpcclient

import (
	"context"
	"crypto/tls"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/metoer/internal/config"
)

// Dial creates a TLS gRPC connection with CA validation skipped. The fleet
// uses self-signed certs issued by the DysonNetwork CA; per the Golaunch
// README, CA validation is off.
func Dial(target string) (*grpc.ClientConn, error) {
	if target == "" {
		return nil, nil
	}
	return grpc.NewClient(target,
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: true})),
		grpc.WithDefaultCallOptions(grpc.WaitForReady(false)),
	)
}

// DialPlaintext creates a plaintext gRPC connection. Blade is the only
// sibling that serves gRPC without TLS (its server is started with no
// credentials), so outbound Blade clients must use this instead of Dial.
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

// NewClients dials every configured target.
func NewClients(cfg *config.Config) (*Clients, error) {
	c := &Clients{}
	dial := func(dialFn func(string) (*grpc.ClientConn, error), target string) *grpc.ClientConn {
		conn, err := dialFn(target)
		if err != nil {
			return nil
		}
		c.conns = append(c.conns, conn)
		return conn
	}
	if conn := dial(Dial, cfg.Services.Stargate.GRPC); conn != nil {
		c.Stargate = conn
	}
	// Blade serves plaintext gRPC, unlike the TLS fleet (stargate et al.).
	if conn := dial(DialPlaintext, cfg.Services.Blade.GRPC); conn != nil {
		c.Blade = conn
	}
	return c, nil
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
