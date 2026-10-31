// Package nearbymod wires the ported Passport nearby (BLE presence) domain
// into Stargate as a feature module: the two nearby tables, the
// /api/nearby HTTP surface and the HMAC token lifecycle.
package nearbymod

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/httpserver/nearbyctl"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/module"
	"src.solsynth.dev/sosys/stargate/nearby"
	"src.solsynth.dev/sosys/stargate/ports"
	"src.solsynth.dev/sosys/stargate/relationship"
	"src.solsynth.dev/sosys/stargate/store"
)

// Factory returns the compiled module descriptor.
//
// nearby only requires accounts: relationships and files are optional runtime
// dependencies. Without relationships every peer is treated as a non-friend
// outsider (friend-only tokens stay hidden); without files the cached profile
// picture reference is served as-is.
func Factory() module.Factory {
	return module.Factory{
		ID:             nearby.ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.accounts"},
		Capabilities: []string{
			"nearby.presence", "nearby.resolve",
		},
		Ports: []module.PortRequirement{
			{ID: ports.PortFiles, Optional: true},
		},
		Models: nearby.Models(),
		SQL:    schemaHooks(),
		New:    func() module.Instance { return &instance{} },
	}
}

func schemaHooks() []module.SchemaHook {
	hooks := nearby.Hooks()
	out := make([]module.SchemaHook, 0, len(hooks))
	for _, hook := range hooks {
		out = append(out, module.SchemaHook(hook))
	}
	return out
}

type instance struct{}

func (i *instance) Configure(_ context.Context, host *module.Host) error {
	if host == nil || host.Core == nil || host.Core.DB == nil {
		return errors.New("nearby: missing host core database")
	}
	log := coreLog(host)
	set := ports.FromRegistry(host.Ports)
	service := nearby.NewService(
		nearby.NewStore(host.Core.DB),
		host.Core.Store,
		relationshipReader(host),
		fileResolver(set),
		nearby.SettingsFromMap(host.ModuleSettings(nearby.ModuleID)),
		log,
	)
	nearbyctl.Register(host.API, nearbyctl.Deps{
		Service: service,
		Perm:    host.Core.Perm,
		Log:     log,
	})
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

func coreLog(host *module.Host) *slog.Logger {
	if host != nil && host.Core != nil && host.Core.Log != nil {
		return host.Core.Log
	}
	return slog.Default()
}

// relationshipReader adapts relationship.Service to nearby's small read
// contract, keeping the shared cache keys authoritative.
func relationshipReader(host *module.Host) nearby.RelationshipReader {
	if host == nil || host.Core == nil || host.Core.Relationships == nil {
		return nil
	}
	return relationshipLookup{service: host.Core.Relationships}
}

type relationshipLookup struct {
	service *relationship.Service
}

func (r relationshipLookup) IsFriendFamily(ctx context.Context, accountID, relatedID uuid.UUID) (bool, error) {
	ids, err := r.service.ListRelated(ctx, accountID, model.RelationshipFriends, false)
	if err != nil {
		return false, err
	}
	target := relatedID.String()
	for _, id := range ids {
		if id == target {
			return true, nil
		}
	}
	return false, nil
}

func (r relationshipLookup) IsBlockedEitherDirection(ctx context.Context, accountID, relatedID uuid.UUID) (bool, error) {
	return r.service.IsBlockedEitherDirection(ctx, accountID, relatedID)
}

// fileResolver narrows the files port to nearby's resolve-only contract.
func fileResolver(set *ports.Set) nearby.FileResolver {
	if set == nil || set.Files == nil || !set.Files.Available() {
		return nil
	}
	return set.Files
}

// compile-time guards.
var (
	_ nearby.AccountLookup      = (*store.Store)(nil)
	_ nearby.RelationshipReader = relationshipLookup{}
	_ nearby.FileResolver       = ports.Files(nil)
)
