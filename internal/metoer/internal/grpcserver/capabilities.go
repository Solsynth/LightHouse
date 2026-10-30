// DyCapabilitiesService serves the static capability registry for the Ring
// surface. The registry mirrors the [ApiFeature(...)] attributes on the
// Ring controllers; every feature ships at revision 1, non-experimental.
package grpcserver

import (
	"context"

	"google.golang.org/protobuf/types/known/emptypb"
	gen "src.solsynth.dev/sosys/go/proto"
)

type dyCapabilitiesService struct {
	gen.UnimplementedDyCapabilitiesServiceServer
}

// capability is one registered feature (the ApiFeature attribute).
type capability struct {
	name         string
	revision     uint32
	experimental bool
}

// capabilityRegistry is the static registry: NotificationController
// ("notifications", "notifications.preferences"), SopNotificationController
// ("notifications.sop"), EmailSendingPlanAdminController
// ("admin.email-plans"), DeliveryObservabilityAdminController
// ("admin.delivery-observability"), NotificationStatsAdminController
// ("admin.stats").
var capabilityRegistry = []capability{
	{"notifications", 1, false},
	{"notifications.preferences", 1, false},
	{"notifications.sop", 1, false},
	{"admin.email-plans", 1, false},
	{"admin.delivery-observability", 1, false},
	{"admin.stats", 1, false},
}

// capabilityEnumByName mirrors CapabilityGrpcService.CapabilityMap
// (unknown names map to the unspecified enum value).
func capabilityEnumByName(name string) gen.DyCapability {
	switch name {
	case "voice":
		return gen.DyCapability_DY_CAPABILITY_VOICE
	case "passkeys":
		return gen.DyCapability_DY_CAPABILITY_PASSKEYS
	case "stories":
		return gen.DyCapability_DY_CAPABILITY_STORIES
	case "drive-resumable":
		return gen.DyCapability_DY_CAPABILITY_DRIVE_RESUMABLE
	case "realm-v2":
		return gen.DyCapability_DY_CAPABILITY_REALM_V2
	default:
		return gen.DyCapability_DY_CAPABILITY_UNSPECIFIED
	}
}

// GetCapabilities mirrors CapabilityGrpcService.GetCapabilities: features
// are grouped by capability name; each group reports the max revision,
// enabled when it has features, experimental when ALL of its features are
// experimental. api_revision is the highest revision across groups.
func (s *dyCapabilitiesService) GetCapabilities(ctx context.Context, req *emptypb.Empty) (*gen.DyCapabilitiesResponse, error) {
	response := &gen.DyCapabilitiesResponse{MinimumRevision: 0}

	index := make(map[string]int)
	for _, f := range capabilityRegistry {
		i, ok := index[f.name]
		if !ok {
			response.Capabilities = append(response.Capabilities, &gen.DyCapabilityState{
				Capability:   capabilityEnumByName(f.name),
				Name:         f.name,
				Enabled:      true,
				Revision:     f.revision,
				Experimental: f.experimental,
			})
			index[f.name] = len(response.Capabilities) - 1
			continue
		}
		state := response.Capabilities[i]
		if f.revision > state.Revision {
			state.Revision = f.revision
		}
		if !f.experimental {
			state.Experimental = false
		}
	}

	for _, c := range response.Capabilities {
		if c.Revision > response.ApiRevision {
			response.ApiRevision = c.Revision
		}
	}
	return response, nil
}
