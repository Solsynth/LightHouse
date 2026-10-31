// Package domaintrustmod wires the ported Passport domain-trust domain into
// Stargate as a feature module: the two domain-trust tables, the
// /api/domain-blocks HTTP surface and the DyDomainBlockService gRPC port.
package domaintrustmod

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/stargate/domaintrust"
	"src.solsynth.dev/sosys/stargate/httpserver/domaintrustctl"
	"src.solsynth.dev/sosys/stargate/module"
)

// metricFlushInterval is how often queued validation metrics are written.
const metricFlushInterval = time.Minute

// Factory returns the compiled module descriptor.
func Factory() module.Factory {
	return module.Factory{
		ID:             domaintrust.ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.auth"},
		Capabilities:   []string{"domain.trust", "domain.trust.validate"},
		Models:         domaintrust.Models(),
		SQL:            schemaHooks(),
		New:            func() module.Instance { return &instance{} },
	}
}

func schemaHooks() []module.SchemaHook {
	hooks := domaintrust.Hooks()
	out := make([]module.SchemaHook, 0, len(hooks))
	for _, hook := range hooks {
		out = append(out, module.SchemaHook(hook))
	}
	return out
}

type instance struct {
	stop func()
}

func (i *instance) Configure(ctx context.Context, host *module.Host) error {
	if host == nil || host.Core == nil || host.Core.DB == nil {
		return errors.New("domaintrust: missing host core database")
	}
	log := coreLog(host)
	store := domaintrust.NewStore(host.Core.DB)
	settings := domaintrust.SettingsFromMap(host.ModuleSettings(domaintrust.ModuleID))
	service := domaintrust.NewService(store, settings)

	domaintrustctl.Register(host.API, domaintrustctl.Deps{
		Service: service,
		Perm:    host.Core.Perm,
		Log:     log,
	})
	if host.GRPC != nil {
		gen.RegisterDyDomainBlockServiceServer(host.GRPC, domaintrust.NewGRPCService(service))
	}
	i.stop = startMetricFlush(ctx, service, log)
	return nil
}

func (i *instance) Stop(context.Context) error {
	if i.stop != nil {
		i.stop()
		i.stop = nil
	}
	return nil
}

func coreLog(host *module.Host) *slog.Logger {
	if host != nil && host.Core != nil && host.Core.Log != nil {
		return host.Core.Log
	}
	return slog.Default()
}

// startMetricFlush writes the buffered validation metrics once a minute and
// flushes the remainder on stop, mirroring Passport's buffer flush job.
func startMetricFlush(ctx context.Context, service *domaintrust.Service, log *slog.Logger) func() {
	if service == nil {
		return func() {}
	}
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(metricFlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if err := service.FlushMetrics(runCtx); err != nil {
					log.Warn("domain validation metric flush failed", "error", err)
				}
			}
		}
	}()
	return func() {
		cancel()
		wg.Wait()
		flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer flushCancel()
		if err := service.FlushMetrics(flushCtx); err != nil {
			log.Warn("domain validation metric final flush failed", "error", err)
		}
	}
}
