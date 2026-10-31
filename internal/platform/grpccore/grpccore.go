// Package grpccore registers the gRPC surface every Lighthouse service has:
// health checking, server reflection, and the capabilities service Blade's
// /meta aggregator asks for.
package grpccore

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/protobuf/types/known/emptypb"
	gen "src.solsynth.dev/sosys/go/proto"
)

// capabilityEnums maps the fleet's capability names onto the proto enum. The
// enum only names the shared cross-service capabilities; a service's own
// feature list is served by GET /api/modules.
var capabilityEnums = map[string]gen.DyCapability{
	"voice":           gen.DyCapability_DY_CAPABILITY_VOICE,
	"passkeys":        gen.DyCapability_DY_CAPABILITY_PASSKEYS,
	"stories":         gen.DyCapability_DY_CAPABILITY_STORIES,
	"drive-resumable": gen.DyCapability_DY_CAPABILITY_DRIVE_RESUMABLE,
	"realm-v2":        gen.DyCapability_DY_CAPABILITY_REALM_V2,
}

// Register installs health, reflection and the capabilities service.
func Register(server *grpc.Server, capabilities []string) {
	healthpb.RegisterHealthServer(server, health.NewServer())
	reflection.Register(server)
	gen.RegisterDyCapabilitiesServiceServer(server, &capabilitiesServer{capabilities: capabilities})
}

type capabilitiesServer struct {
	gen.UnimplementedDyCapabilitiesServiceServer
	capabilities []string
}

// GetCapabilities answers Blade with the shared capabilities this deployment
// enables.
func (s *capabilitiesServer) GetCapabilities(context.Context, *emptypb.Empty) (*gen.DyCapabilitiesResponse, error) {
	seen := map[gen.DyCapability]bool{}
	states := make([]*gen.DyCapabilityState, 0)
	for _, name := range s.capabilities {
		enum, ok := capabilityEnums[strings.ToLower(name)]
		if !ok || seen[enum] {
			continue
		}
		seen[enum] = true
		states = append(states, &gen.DyCapabilityState{
			Capability: enum,
			Enabled:    true,
			Revision:   1,
			Name:       name,
		})
	}
	return &gen.DyCapabilitiesResponse{ApiRevision: 1, Capabilities: states}, nil
}
