// Package seed bootstraps idempotent baseline data for Stargate. It currently
// seeds the permission registry (nodes + default groups), mirroring
// DysonNetwork.Padlock's PermissionSeedService. Runs on boot after migrations.
package seed

import (
	"context"

	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/internal/permission"
)

func Seed(ctx context.Context, database *gorm.DB) error {
	return permission.New(database).EnsureSeeded(ctx)
}
