package auth

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// TokenAuthenticator is the interface for validating tokens against an auth service.
// Implementations can back this with gRPC, HTTP, local JWT verification, etc.
type TokenAuthenticator interface {
	Authenticate(ctx context.Context, tokenInfo TokenInfo, r *http.Request) (*AuthResult, error)
}

// GrpcAuthDialConfig holds connection settings for the gRPC auth service.
type GrpcAuthDialConfig struct {
	Target        string
	UseTLS        bool
	TLSSkipVerify bool
	TLSServerName string
}

// GrpcTokenAuthenticator validates tokens by calling the DyAuthService gRPC endpoint.
type GrpcTokenAuthenticator struct {
	conn *grpc.ClientConn
}

// NewGrpcTokenAuthenticator dials the auth service and returns a ready-to-use authenticator.
func NewGrpcTokenAuthenticator(cfg GrpcAuthDialConfig) (*GrpcTokenAuthenticator, error) {
	target, useTLS := NormalizeAuthGRPCTarget(cfg.Target, cfg.UseTLS)
	if target == "" {
		return nil, errors.New("auth gRPC target is empty")
	}

	var transportCredentials credentials.TransportCredentials
	if useTLS {
		tlsCfg := &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: cfg.TLSSkipVerify,
		}
		if cfg.TLSServerName != "" {
			tlsCfg.ServerName = cfg.TLSServerName
		}
		transportCredentials = credentials.NewTLS(tlsCfg)
	} else {
		transportCredentials = insecure.NewCredentials()
	}

	conn, err := grpc.Dial(
		target,
		grpc.WithTransportCredentials(transportCredentials),
	)
	if err != nil {
		return nil, fmt.Errorf("dial auth service: %w", err)
	}
	return &GrpcTokenAuthenticator{conn: conn}, nil
}

// NormalizeAuthGRPCTarget parses a target string and infers TLS from the scheme.
// Schemes grpc://, http:// → no TLS; grpcs://, https:// → TLS.
func NormalizeAuthGRPCTarget(rawTarget string, useTLS bool) (string, bool) {
	target := strings.TrimSpace(rawTarget)
	if target == "" {
		return "", useTLS
	}

	parsed, err := url.Parse(target)
	if err != nil || parsed.Scheme == "" {
		return target, useTLS
	}

	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case "grpc":
		return parsed.Host, false
	case "grpcs", "https":
		return parsed.Host, true
	case "http":
		return parsed.Host, false
	default:
		return target, useTLS
	}
}

// Authenticate calls the remote DyAuthService.Authenticate RPC.
func (a *GrpcTokenAuthenticator) Authenticate(ctx context.Context, tokenInfo TokenInfo, r *http.Request) (*AuthResult, error) {
	ip := ExtractIP(r)
	req := &gen.DyAuthenticateRequest{Token: tokenInfo.Token}
	if ip != "" {
		req.IpAddress = wrapperspb.String(ip)
	}

	client := gen.NewDyAuthServiceClient(a.conn)

	resp, err := client.Authenticate(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.GetValid() {
		msg := strings.TrimSpace(resp.GetMessage())
		if msg == "" {
			msg = "token is not valid"
		}
		return nil, errors.New(msg)
	}
	if resp.GetSession() == nil || resp.GetSession().GetAccount() == nil {
		return nil, errors.New("session not found")
	}
	return &AuthResult{Account: resp.GetSession().GetAccount(), Session: resp.GetSession()}, nil
}

// AuthenticateRequest extracts the token from the request and authenticates it
// with an 8-second timeout.
func AuthenticateRequest(ctx context.Context, auth TokenAuthenticator, r *http.Request) (*AuthResult, error) {
	if auth == nil {
		return nil, errors.New("token authenticator is not configured")
	}
	tokenInfo, ok := ExtractToken(r)
	if !ok || strings.TrimSpace(tokenInfo.Token) == "" {
		return nil, errors.New("no token was provided")
	}

	authCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	return auth.Authenticate(authCtx, tokenInfo, r)
}

// Reauthenticate is like AuthenticateRequest but accepts an explicit TokenInfo,
// useful for refreshing tokens on long-lived WebSocket connections.
func Reauthenticate(ctx context.Context, auth TokenAuthenticator, tokenInfo TokenInfo, r *http.Request) (*AuthResult, error) {
	if auth == nil {
		return nil, errors.New("token authenticator is not configured")
	}
	if strings.TrimSpace(tokenInfo.Token) == "" {
		return nil, errors.New("no token was provided")
	}

	authCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	return auth.Authenticate(authCtx, tokenInfo, r)
}

// CloneRequestMetadata returns a shallow clone of the request with the body set to nil,
// useful for retaining headers (e.g. forwarded IPs) across async operations.
func CloneRequestMetadata(r *http.Request) *http.Request {
	if r == nil {
		return nil
	}

	clone := r.Clone(context.Background())
	clone.Body = nil
	return clone
}

// Close shuts down the underlying gRPC connection.
func (a *GrpcTokenAuthenticator) Close() error {
	if a.conn != nil {
		return a.conn.Close()
	}
	return nil
}
