// DyCapabilitiesService serves the static capability registry for the
// Stargate (Padlock) surface. The registry mirrors the [ApiFeature(...)]
// attributes on the Padlock controllers plus the Passport route groups that
// move to Stargate (Phase 8); every feature in the C# fleet ships at
// revision 1, non-experimental. api_revision is the highest revision,
// matching CapabilityGrpcService's fallback when no explicit value is set.
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

// capabilityRegistry is the static registry: Padlock's ApiFeature list plus
// the Passport route groups moved to Stargate (accounts.board, friends,
// relationships*).
var capabilityRegistry = []capability{
	// Padlock — Account/
	{"accounts.registration", 1, false},
	{"accounts.profile", 1, false},
	{"accounts.identity", 1, false},
	{"accounts.factors", 1, false},
	{"accounts.sessions", 1, false},
	{"accounts.devices", 1, false},
	{"accounts.authorized-apps", 1, false},
	{"accounts.contacts", 1, false},
	{"accounts.action-log", 1, false},
	{"accounts.punishments", 1, false},
	// Padlock — Auth/
	{"auth", 1, false},
	{"auth.challenge", 1, false},
	{"auth.passkey", 1, false},
	{"auth.session", 1, false},
	{"auth.token", 1, false},
	{"auth.sudo", 1, false},
	{"auth.captcha", 1, false},
	{"auth.qr-login", 1, false},
	{"auth.api-keys", 1, false},
	// Padlock — E2EE/
	{"e2ee", 1, false},
	{"e2ee.mls", 1, false},
	// Padlock — Permission/
	{"permissions", 1, false},
	{"permissions.groups", 1, false},
	// Padlock — admin
	{"admin.cache", 1, false},
	{"admin.accounts", 1, false},
	{"admin.accounts.devices", 1, false},
	{"admin.accounts.sessions", 1, false},
	{"admin.accounts.contacts", 1, false},
	{"admin.accounts.factors", 1, false},
	{"admin.accounts.punishments", 1, false},
	{"admin.stats.geography", 1, false},
	{"admin.permissions", 1, false},
	{"admin.permissions.groups", 1, false},
	// Passport route groups moved to Stargate (Phase 8)
	{"accounts.board", 1, false},
	{"accounts.connections", 1, false},
	{"friends", 1, false},
	{"relationships", 1, false},
	{"relationships.friends", 1, false},
	{"relationships.block", 1, false},
	{"relationships.mute", 1, false},
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

// GetCapabilities mirrors CapabilityGrpcService.GetCapabilities: features are
// grouped by capability name; each group reports the max revision, enabled
// when it has features, experimental when ALL of its features are
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
