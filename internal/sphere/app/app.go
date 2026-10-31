// Package app owns the Sphere process bootstrap. It builds the shared core
// graph, applies the core schema prerequisites and runs the HTTP and gRPC
// servers through the shared Lighthouse bootstrap.
package app

import (
	"context"
	"fmt"
	"log/slog"

	platformapp "src.solsynth.dev/sosys/lighthouse/shared/platform/app"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/config"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/migrate"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/modules/standard"
)

// Run boots the Sphere service.
func Run(parent context.Context, log *slog.Logger) error {
	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	return RunWith(parent, log, cfg, standard.Modules())
}

// RunWith boots Sphere with an explicit configuration and module set. Tests and
// private builds use it to swap the compiled-in modules.
func RunWith(parent context.Context, log *slog.Logger, cfg *config.Config, factories []module.Factory[core.Services]) error {
	if cfg == nil {
		return fmt.Errorf("sphere: configuration is required")
	}
	return platformapp.Run(parent, log, platformapp.Options[core.Services]{
		Name:      "sphere",
		Base:      &cfg.Base,
		Factories: factories,
		Migrate:   migrate.Run,
		Build: func(_ context.Context, env *platformapp.Env) (*core.Services, error) {
			return &core.Services{Env: env, Config: cfg}, nil
		},
	})
}

// LogLevel is exported for cmd/sphere.
func LogLevel() slog.Level { return platformapp.LogLevel() }
