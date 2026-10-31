// Package app owns the Messager process bootstrap.
package app

import (
	"context"
	"fmt"
	"log/slog"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/config"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/core"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/migrate"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/modules/standard"
	platformapp "src.solsynth.dev/sosys/lighthouse/shared/platform/app"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

// Run boots the Messager service.
func Run(parent context.Context, log *slog.Logger) error {
	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	return RunWith(parent, log, cfg, standard.Modules())
}

// RunWith boots Messager with an explicit configuration and module set.
func RunWith(parent context.Context, log *slog.Logger, cfg *config.Config, factories []module.Factory[core.Services]) error {
	if cfg == nil {
		return fmt.Errorf("messager: configuration is required")
	}
	return platformapp.Run(parent, log, platformapp.Options[core.Services]{
		Name:      "messager",
		Base:      &cfg.Base,
		Factories: factories,
		Migrate:   migrate.Run,
		Build: func(_ context.Context, env *platformapp.Env) (*core.Services, error) {
			return &core.Services{Env: env, Config: cfg}, nil
		},
	})
}

// LogLevel is exported for cmd/messager.
func LogLevel() slog.Level { return platformapp.LogLevel() }
