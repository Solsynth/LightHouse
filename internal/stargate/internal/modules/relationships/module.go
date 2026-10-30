// Package relationships owns the social graph. Its table is migrated by this
// module only, so a deployment that disables relationships never creates it.
package relationships

import (
	"context"

	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/internal/httpserver/profilectl"
	"src.solsynth.dev/sosys/stargate/internal/module"
	"src.solsynth.dev/sosys/stargate/internal/modules/common"
	"src.solsynth.dev/sosys/stargate/internal/ports"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// Factory returns the compiled module descriptor.
func Factory() module.Factory {
	return module.Factory{
		ID:             "app.solian.id.relationships",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.profiles"},
		Capabilities: []string{
			"friends", "relationships", "relationships.friends",
			"relationships.block", "relationships.mute",
		},
		Models:   []any{&store.RelationshipEntity{}},
		SQL:      []module.SchemaHook{relationshipIndexes},
		Importer: func() module.Importer { return &importer{} },
		New:      func() module.Instance { return &instance{} },
	}
}

func relationshipIndexes(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Exec(`
		CREATE INDEX IF NOT EXISTS ix_account_relationships_related_id
		ON account_relationships (related_id)`).Error
}

type instance struct{}

func (i *instance) Configure(_ context.Context, host *module.Host) error {
	deps := common.Profile(host.Core, ports.FromRegistry(host.Ports))
	profilectl.RegisterRelationships(host.API, deps)
	// The anonymous follow totals are part of this module's surface: a
	// deployment without relationships serves no summary at all.
	profilectl.RegisterPublicRelationships(host.API, deps)
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }
