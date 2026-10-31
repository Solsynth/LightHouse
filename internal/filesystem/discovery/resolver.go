package discovery

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

// ResolverScheme routes a logical service name through Blade's registry.
const ResolverScheme = "blade"

const (
	resolveTimeout  = 2 * time.Second
	resolveCacheTTL = time.Second
)

// Resolution is one provider answer. Registered reports whether the service has
// any registration, even if no instance is healthy; that prevents falling back
// to a static address after a service has registered but failed its probe.
type Resolution struct {
	Endpoints  []string
	Registered bool
}

// Resolver caches remote registry answers and applies the static fallback.
type Resolver struct {
	client   gen.DyServiceDiscoveryServiceClient
	conn     *grpc.ClientConn
	fallback map[string]string

	mu    sync.Mutex
	cache map[string]cachedResolution
}

type cachedResolution struct {
	resolution Resolution
	expiresAt  time.Time
}

// NewResolver dials Blade's discovery RPC. fallback maps a service name to the
// static address used only while that service has no registration.
func NewResolver(target string, fallback map[string]string) (*Resolver, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, fmt.Errorf("service discovery: target is empty")
	}
	conn, err := grpc.NewClient(DialTarget(target), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("service discovery: dial %s: %w", target, err)
	}
	normalized := make(map[string]string, len(fallback))
	for name, endpoint := range fallback {
		name = normalizeService(name)
		if endpoint = endpointAddress(endpoint); name != "" && endpoint != "" {
			normalized[name] = endpoint
		}
	}
	return &Resolver{
		client:   gen.NewDyServiceDiscoveryServiceClient(conn),
		conn:     conn,
		fallback: normalized,
		cache:    make(map[string]cachedResolution),
	}, nil
}

// Close closes the discovery connection.
func (r *Resolver) Close() error {
	if r == nil || r.conn == nil {
		return nil
	}
	return r.conn.Close()
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

// Resolve returns healthy gRPC endpoints, falling back to the static address
// only when the service has no registration at all.
func (r *Resolver) Resolve(ctx context.Context, service string) (Resolution, error) {
	service = normalizeService(service)
	if service == "" {
		return Resolution{}, fmt.Errorf("service discovery: service is required")
	}
	if cached, ok := r.cached(service); ok {
		return cached, nil
	}

	callCtx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	response, err := r.client.Resolve(callCtx, &gen.DyResolveServiceRequest{Service: service})

	resolution := Resolution{Registered: len(response.GetInstances()) > 0}
	for _, instance := range response.GetInstances() {
		if !instance.GetHealthy() {
			continue
		}
		if value := endpoint(instance, "grpc"); value != "" {
			resolution.Endpoints = append(resolution.Endpoints, value)
		}
	}
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
	r.cache[service] = cachedResolution{resolution: resolution, expiresAt: time.Now().Add(resolveCacheTTL)}
	r.mu.Unlock()
}

func endpointAddress(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return value
	}
	return parsed.Host
}

func dedupeEndpoints(endpoints []string) []string {
	seen := make(map[string]struct{}, len(endpoints))
	out := make([]string, 0, len(endpoints))
	for _, value := range endpoints {
		value = endpointAddress(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
