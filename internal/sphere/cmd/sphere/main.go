// Sphere — the Go port of DysonNetwork.Sphere: posts, publishers, timelines,
// surveys, stickers, livestreams and federation. The process bootstrap lives
// in internal/sphere/app; this command only selects the compiled-in modules.
package main

import (
	"context"
	"log/slog"
	"os"

	platformapp "src.solsynth.dev/sosys/lighthouse/shared/platform/app"
	sphereapp "src.solsynth.dev/sosys/lighthouse/internal/sphere/app"
)

// version and gitCommit are injected at build time (see Dockerfile).
var (
	version   = "dev"
	gitCommit = "unknown"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: platformapp.LogLevel()}))
	slog.SetDefault(log)
	log.Info("starting sphere", "version", version, "commit", gitCommit)

	if err := sphereapp.Run(context.Background(), log); err != nil {
		log.Error("sphere exited with error", "error", err)
		os.Exit(1)
	}
}
