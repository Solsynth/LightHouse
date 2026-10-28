// Package core holds the Messager service's shared service graph handed to
// modules. It is built once by package app and never imports the module
// registry, keeping the dependency direction one-way.
package core

import (
	"src.solsynth.dev/sosys/lighthouse/internal/messager/config"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/app"
)

// Services is the shared, already-constructed core graph. Modules must treat
// nil optional services as unavailable rather than assuming they exist.
type Services struct {
	*app.Env
	Config *config.Config
}
