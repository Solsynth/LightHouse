// Package seed bootstraps idempotent baseline data for Stargate. It currently
// seeds the permission registry (nodes + default groups), mirroring
// DysonNetwork.Padlock's PermissionSeedService. Runs on boot after migrations.
package seed

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/permission"
)

// Seed ensures the permission nodes and the default/verified/moderator/
// developer groups (with their member enrollments) exist. It is idempotent:
// missing keys and members are inserted, existing rows are preserved.
func Seed(ctx context.Context, db *pgxpool.Pool) error {
	return permission.New(db).EnsureSeeded(ctx)
}
