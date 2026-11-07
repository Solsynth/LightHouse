package discovery

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/metoer/grpctls"
)

// ResolverScheme is the gRPC target scheme that routes a logical service name
// through Blade's discovery registry. A target looks like "blade:///stargate";
// grpcclient and the fleet services build one with Target.
const ResolverScheme = "blade"

const (
	resolveTimeout  = 2 * time.Second
	resolveCacheTTL = time.Second
	serviceProtocol = "grpc"
	legacyHTTPProto = "http"
	legacyGRPCProto = "grpc"
)

// Resolution is one provider answer. Registered reports whether the service
// currently has any registration at all, even if none of those instances is
// healthy. That distinction is what prevents a service that is registered but
// unhealthy from silently falling back to a static address.
type Resolution struct {
	Endpoints  []string
	Registered bool
}

// AddressProvider resolves a logical service name to live endpoints. A remote
// provider talks to Blade over DyServiceDiscoveryService; Blade itself uses an
// in-process provider over its registry.
type AddressProvider interface {
	Resolve(ctx context.Context, service string) (Resolution, error)
}

// Resolver adds short caching, fallback and endpoint normalisation on top of an
// AddressProvider. It is safe for concurrent use.
type Resolver struct {
	provider AddressProvider
	fallback map[string]string
	ttl      time.Duration

	mu    sync.Mutex
	cache map[string]cachedResolution
}

type cachedResolution struct {
	resolution Resolution
	expiresAt  time.Time
}

// NewResolver wraps a provider. fallback maps a service name to the static
// address from [services]; it is used only while the service has no
// registration at all (or while discovery is unreachable).
func NewResolver(provider AddressProvider, fallback map[string]string) *Resolver {
	normalized := make(map[string]string, len(fallback))
	for name, endpoint := range fallback {
		name = normalizeService(name)
		if endpoint = EndpointAddress(endpoint); name != "" && endpoint != "" {
			normalized[name] = endpoint
		}
	}
	return &Resolver{
		provider: provider,
		fallback: normalized,
		ttl:      resolveCacheTTL,
		cache:    make(map[string]cachedResolution),
	}
}

// Target returns the gRPC target for a logical service name.
func Target(service string) string {
	return ResolverScheme + ":///" + strings.TrimSpace(service)
}

// DialTarget normalizes a configured Blade address into a gRPC dial target.
// A bare host:port is forced to the dns scheme: RFC 3986 parses "blade:7001"
// as scheme "blade", which is ResolverScheme, so gRPC would resolve the
// registry for a service named "7001" instead of dialing the Blade gateway.
func DialTarget(raw string) string {
	target := strings.TrimSpace(raw)
	if target == "" || strings.Contains(target, "://") {
		return target
	}
	return "dns:///" + target
}

// Dial opens a client connection to Blade's discovery endpoint. The target
// scheme selects the transport, matching the fleet's grpcclient convention:
// grpc:// and http:// are plaintext, https:// and grpcs:// are TLS. A bare
// host:port defaults to TLS, like every other fleet target; plaintext is the
// explicit http:// or grpc:// choice.
//
// files carries the trust anchor for the server certificate and the client
// certificate presented to a server enforcing mTLS. An empty CAFile skips
// validation, as fleet certificates are self-signed.
func Dial(target string, files grpctls.Files) (*grpc.ClientConn, error) {
	address, useTLS := SplitTarget(target)
	if address == "" {
		return nil, fmt.Errorf("discovery target is empty")
	}
	creds := credentials.TransportCredentials(insecure.NewCredentials())
	if useTLS {
		var err error
		if creds, err = grpctls.Client(files); err != nil {
			return nil, err
		}
	}
	return grpc.NewClient(DialTarget(address), grpc.WithTransportCredentials(creds))
}

// SplitTarget reports the dial address and whether the target asks for TLS.
func SplitTarget(raw string) (string, bool) {
	target := strings.TrimSpace(raw)
	if target == "" {
		return "", true
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return target, true
	}
	switch strings.ToLower(parsed.Scheme) {
	case "grpc", "http":
		return parsed.Host, false
	case "grpcs", "https":
		return parsed.Host, true
	default:
		return target, true
	}
}

// IsTarget reports whether raw is a discovery target.
func IsTarget(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && strings.EqualFold(parsed.Scheme, ResolverScheme)
}

// Resolve returns the healthy gRPC endpoints for a service. When the service
// has no registration at all it falls back to its configured static address.
func (r *Resolver) Resolve(ctx context.Context, service string) (Resolution, error) {
	service = normalizeService(service)
	if service == "" {
		return Resolution{}, fmt.Errorf("service is required")
	}
	if cached, ok := r.cached(service); ok {
		return cached, nil
	}

	callCtx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	resolution, err := r.provider.Resolve(callCtx, service)

	// A registered service owns its routing state. Only an unregistered
	// service (or an unreachable registry) may use the static fallback.
	if len(resolution.Endpoints) == 0 && (!resolution.Registered || err != nil) {
		if fallback := r.fallback[service]; fallback != "" {
			resolution.Endpoints = []string{fallback}
		}
	}
	resolution.Endpoints = dedupeEndpoints(resolution.Endpoints)
	r.store(service, resolution)
	return resolution, err
}

func (r *Resolver) cached(service string) (Resolution, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cache[service]
	if !ok || time.Now().After(entry.expiresAt) {
		return Resolution{}, false
	}
	return entry.resolution, true
}

func (r *Resolver) store(service string, resolution Resolution) {
	r.mu.Lock()
	r.cache[service] = cachedResolution{resolution: resolution, expiresAt: time.Now().Add(r.ttl)}
	r.mu.Unlock()
}

// RemoteProvider resolves service names by calling Blade's discovery RPC.
// Blade serves that RPC over TLS when [grpc] useTLS is set; the discovery
// target scheme selects the transport (see Dial).
type RemoteProvider struct {
	client gen.DyServiceDiscoveryServiceClient
	conn   *grpc.ClientConn
}

// NewRemoteProvider dials Blade's discovery endpoint. The connection is lazy:
// calling this function does not require Blade to be up yet.
func NewRemoteProvider(target string, files grpctls.Files) (*RemoteProvider, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, fmt.Errorf("discovery target is empty")
	}
	conn, err := Dial(target, files)
	if err != nil {
		return nil, fmt.Errorf("dial discovery target %s: %w", target, err)
	}
	return &RemoteProvider{client: gen.NewDyServiceDiscoveryServiceClient(conn), conn: conn}, nil
}

// Resolve implements AddressProvider.
func (p *RemoteProvider) Resolve(ctx context.Context, service string) (Resolution, error) {
	response, err := p.client.Resolve(ctx, &gen.DyResolveServiceRequest{Service: service})
	if err != nil {
		return Resolution{}, err
	}
	instances := response.GetInstances()
	resolution := Resolution{Registered: len(instances) > 0}
	for _, instance := range instances {
		if !instance.GetHealthy() {
			continue
		}
		if endpoint := Endpoint(instance, serviceProtocol); endpoint != "" {
			resolution.Endpoints = append(resolution.Endpoints, endpoint)
		}
	}
	return resolution, nil
}

// Close closes the discovery connection.
func (p *RemoteProvider) Close() error {
	if p == nil || p.conn == nil {
		return nil
	}
	return p.conn.Close()
}

// Endpoint returns a registered endpoint by protocol. Map entries take
// precedence over the legacy http_endpoint and grpc_endpoint fields.
func Endpoint(instance *gen.DyServiceInstance, protocol string) string {
	if instance == nil {
		return ""
	}
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	for name, endpoint := range instance.GetEndpoints() {
		if strings.EqualFold(strings.TrimSpace(name), protocol) {
			return strings.TrimSpace(endpoint)
		}
	}
	switch protocol {
	case legacyHTTPProto:
		return strings.TrimSpace(instance.GetHttpEndpoint())
	case legacyGRPCProto:
		return strings.TrimSpace(instance.GetGrpcEndpoint())
	default:
		return ""
	}
}

// EndpointAddress strips an endpoint's scheme (and any path) so it can be used
// as a gRPC address. Bare host:port values are returned unchanged.
func EndpointAddress(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return endpoint
	}
	return parsed.Host
}

func normalizeService(service string) string {
	return strings.ToLower(strings.TrimSpace(service))
}

func dedupeEndpoints(endpoints []string) []string {
	seen := make(map[string]struct{}, len(endpoints))
	out := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		endpoint = EndpointAddress(endpoint)
		if endpoint == "" {
			continue
		}
		if _, ok := seen[endpoint]; ok {
			continue
		}
		seen[endpoint] = struct{}{}
		out = append(out, endpoint)
	}
	return out
}
