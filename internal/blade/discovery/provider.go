package discovery

import (
	"context"

	platformdiscovery "src.solsynth.dev/sosys/lighthouse/shared/platform/discovery"
)

// RegistryProvider adapts the in-process registry to the shared discovery
// resolver. Blade uses it to follow its own registry for outbound gRPC
// connections (the websocket gateway's auth and profile services) instead of
// requiring those targets in [services].
type RegistryProvider struct {
	registry *Registry
}

func NewRegistryProvider(registry *Registry) *RegistryProvider {
	return &RegistryProvider{registry: registry}
}

// Resolve implements platformdiscovery.AddressProvider.
func (p *RegistryProvider) Resolve(ctx context.Context, service string) (platformdiscovery.Resolution, error) {
	if p == nil || p.registry == nil {
		return platformdiscovery.Resolution{}, nil
	}
	instances, err := p.registry.List(ctx, service)
	if err != nil {
		return platformdiscovery.Resolution{}, err
	}
	resolution := platformdiscovery.Resolution{Registered: len(instances) > 0}
	for _, instance := range instances {
		if !instance.GetHealthy() {
			continue
		}
		if endpoint := Endpoint(instance, "grpc"); endpoint != "" {
			resolution.Endpoints = append(resolution.Endpoints, endpoint)
		}
	}
	return resolution, nil
}
