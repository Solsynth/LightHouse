// Package module implements Lighthouse's compile-time module system, the same
// Caddy-style registry Stargate uses: modules are linked into a service binary
// and the registry decides which of them are mounted at runtime.
//
// Each Lighthouse service instantiates the registry with its own core service
// graph (module.Factory[*spherecore.Services] and
// module.Factory[*messagercore.Services]), so the two services share the
// machinery but never share a process, a database or a module set.
package module

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"gorm.io/gorm"
)

// Kind separates domain features from ecosystem adapters.
type Kind string

const (
	KindFeature     Kind = "feature"
	KindIntegration Kind = "integration"
)

// PortID names an interface a module consumes or provides.
type PortID string

// PortRequirement declares a port a module needs. Optional ports may be
// absent; the module must then gate the dependent feature.
type PortRequirement struct {
	ID       PortID
	Optional bool
}

// SchemaHook applies schema changes AutoMigrate cannot express (partial
// indexes, extensions, custom constraints, data backfills). Hooks must be
// idempotent and must never drop data.
type SchemaHook func(ctx context.Context, db *gorm.DB) error

// Factory describes one compiled-in module. Everything except New is static so
// the resolver can make decisions before any module is constructed.
type Factory[C any] struct {
	ID             string
	Kind           Kind
	DefaultEnabled bool
	Required       bool

	// Requires lists feature modules that must be enabled for this module to
	// be meaningful. Disabling a dependency cascades to its dependents.
	Requires []string

	// Ports are runtime interfaces (local or remote). Provides advertises the
	// ports this module can publish during ProvidePorts.
	Ports    []PortRequirement
	Provides []PortID

	// Models and SQL own the module's schema. They are applied only while the
	// module is enabled. Existing legacy tables are declared through SQL hooks
	// so their column layout stays byte-compatible with the C# services.
	Models []any
	SQL    []SchemaHook

	// Capabilities are exposed through DyCapabilitiesService and
	// GET /api/modules.
	Capabilities []string

	// Importer optionally migrates legacy data into a module whose table
	// layout changed during the port.
	Importer func() Importer

	New func() Instance[C]
}

// Instance is a constructed module.
type Instance[C any] interface {
	// Configure wires HTTP routes, gRPC services, consumers and jobs. It runs
	// after core migrations and module schema hooks.
	Configure(ctx context.Context, host *Host[C]) error
	// Stop releases resources. It runs in reverse order during shutdown.
	Stop(ctx context.Context) error
}

// PortProvider is an optional instance interface. All providers are called
// before any module's Configure, so consumers can resolve ports regardless of
// module order.
type PortProvider[C any] interface {
	ProvidePorts(ctx context.Context, host *Host[C]) error
}

// Host is the module's view of the running process.
type Host[C any] struct {
	Core *C

	Engine *gin.Engine
	API    *gin.RouterGroup
	GRPC   *grpc.Server

	Ports   *PortRegistry
	Enabled map[string]bool
	// Settings is the decoded [modules.settings."<id>"] subtree.
	Settings map[string]map[string]any

	Log *slog.Logger
}

// ModuleSettings returns the decoded settings for a module ID.
func (h *Host[C]) ModuleSettings(id string) map[string]any {
	if h == nil {
		return nil
	}
	return h.Settings[id]
}

// IsEnabled reports whether a module is part of the resolved set.
func (h *Host[C]) IsEnabled(id string) bool {
	if h == nil {
		return false
	}
	return h.Enabled[id]
}

// BuildError wraps a module configuration failure with its module ID.
type BuildError struct {
	Module string
	Err    error
}

func (e *BuildError) Error() string {
	return fmt.Sprintf("module %s: %v", e.Module, e.Err)
}

func (e *BuildError) Unwrap() error { return e.Err }
