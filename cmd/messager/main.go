// Messager — the Go port of DysonNetwork.Messager: chat rooms, messages,
// groups, pins, reactions, voice clips and realtime calls. The process
// bootstrap lives in internal/messager/app; this command only selects the
// compiled-in modules.
package main

import (
	"context"
	"log/slog"
	"os"

	messagerapp "src.solsynth.dev/sosys/lighthouse/internal/messager/app"
	platformapp "src.solsynth.dev/sosys/lighthouse/internal/platform/app"
)

// version and gitCommit are injected at build time (see Dockerfile).
var (
	version   = "dev"
	gitCommit = "unknown"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: platformapp.LogLevel()}))
	slog.SetDefault(log)
	log.Info("starting messager", "version", version, "commit", gitCommit)

	if err := messagerapp.Run(context.Background(), log); err != nil {
		log.Error("messager exited with error", "error", err)
		os.Exit(1)
	}
}
