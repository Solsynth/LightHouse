// Stargate — the Go replacement for DysonNetwork.Padlock plus the Passport
// profile domain. The process bootstrap lives in internal/app; this command
// only selects the compiled-in module set.
package main

import (
	"context"
	"log/slog"
	"os"

	"src.solsynth.dev/sosys/stargate/internal/app"
	"src.solsynth.dev/sosys/stargate/internal/modules/standard"
)

// version and gitCommit are injected at build time (see Dockerfile).
var (
	version   = "dev"
	gitCommit = "unknown"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: app.LogLevel()}))
	slog.SetDefault(log)

	if err := app.Run(context.Background(), log, standard.Modules()); err != nil {
		log.Error("stargate exited with error", "error", err)
		os.Exit(1)
	}
}
