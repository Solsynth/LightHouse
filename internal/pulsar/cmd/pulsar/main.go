// Pulsar — universal multi-domain playback tracking, scrobbling, catalog,
// and media collections (HTTP + gRPC).
package main

import (
	"context"
	"log/slog"
	"os"

	pulsarapp "src.solsynth.dev/sosys/lighthouse/internal/pulsar/app"
	platformapp "src.solsynth.dev/sosys/lighthouse/shared/platform/app"
)

// version and gitCommit are injected at build time (see Dockerfile).
var (
	version   = "dev"
	gitCommit = "unknown"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: platformapp.LogLevel()}))
	slog.SetDefault(log)
	log.Info("starting pulsar", "version", version, "commit", gitCommit)

	if err := pulsarapp.Run(context.Background(), log); err != nil {
		log.Error("pulsar exited with error", "error", err)
		os.Exit(1)
	}
}
