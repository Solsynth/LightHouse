// Package app owns the Pulsar process bootstrap. It builds the shared core
// graph, applies core schema prerequisites, and runs the HTTP and gRPC
// servers through the shared Lighthouse bootstrap.
package app

import (
	"context"
	"fmt"
	"log/slog"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/config"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/migrate"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/modules/standard"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/store"
	platformapp "src.solsynth.dev/sosys/lighthouse/shared/platform/app"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

// Run boots the Pulsar service.
func Run(parent context.Context, log *slog.Logger) error {
	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	return RunWith(parent, log, cfg, standard.Modules())
}

// RunWith boots Pulsar with an explicit configuration and module set.
func RunWith(parent context.Context, log *slog.Logger, cfg *config.Config, factories []module.Factory[core.Services]) error {
	if cfg == nil {
		return fmt.Errorf("pulsar: configuration is required")
	}
	return platformapp.Run(parent, log, platformapp.Options[core.Services]{
		Name:      "pulsar",
		Base:      &cfg.Base,
		Factories: factories,
		Migrate:   migrate.Run,
		Build: func(_ context.Context, env *platformapp.Env) (*core.Services, error) {
			st := store.New(env.DB)
			return &core.Services{
				Env:    env,
				Config: cfg,
				Store:  st,
			}, nil
		},
	})
}

// LogLevel is exported for cmd/pulsar.
func LogLevel() slog.Level { return platformapp.LogLevel() }
