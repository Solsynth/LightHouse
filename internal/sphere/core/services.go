// Package core holds the Sphere service's shared service graph handed to
// modules. It is built once by package app and never imports the module
// registry, which keeps the dependency direction one-way:
// app -> core + module, modules -> core + module.
package core

import (
	"src.solsynth.dev/sosys/lighthouse/internal/platform/app"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/config"
)

// Services is the shared, already-constructed core graph. Modules must treat
// nil optional services as unavailable rather than assuming they exist.
type Services struct {
	*app.Env
	Config *config.Config
}
