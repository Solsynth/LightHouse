package bangumisync

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/bangumi"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

// Factory returns the Bangumi collection-mirror module. Account linking is
// owned by Stargate; this module reads the grant on demand.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             "app.solian.pulsar.bangumi",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Models:         []any{&model.ExternalSyncConnection{}},
		Capabilities:   []string{"bangumi", "bangumi.sync"},
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	core   *core.Services
	client *bangumi.Client
	log    *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	syncing map[uuid.UUID]bool

	// disableAutoSync lets tests exercise the read path without a background
	// reconciliation racing their assertions and cleanup.
	disableAutoSync bool
	// linkOverride lets tests supply a Stargate link without a live gRPC
	// server. Production leaves it nil.
	linkOverride func(context.Context, uuid.UUID) (*stargateLink, error)
}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	i.core = host.Core
	i.log = host.Log
	if i.log == nil {
		i.log = slog.Default()
	}
	i.syncing = make(map[uuid.UUID]bool)
	i.ctx, i.cancel = context.WithCancel(context.Background())

	cfg := host.Core.Config.Bangumi
	i.client = bangumi.NewClient(bangumi.Config{
		APIBaseURL: cfg.APIBaseURL,
		UserAgent:  cfg.UserAgent,
	})
	i.registerRoutes(host.API)

	interval := time.Duration(cfg.SyncIntervalMinutes) * time.Minute
	if interval > 0 {
		host.Core.Jobs.Every("pulsar.bangumi.sync", interval, i.syncAll)
	}
	return nil
}

func (i *instance) Stop(context.Context) error {
	if i.cancel != nil {
		i.cancel()
	}
	i.wg.Wait()
	return nil
}

// syncAll reconciles every enabled link. Accounts run one at a time so a
// deployment with many links does not turn one scheduler tick into a burst of
// Bangumi requests.
func (i *instance) syncAll(ctx context.Context) error {
	connections, err := i.core.Store.ListSyncConnections(ctx, provider, true)
	if err != nil {
		return err
	}
	for index := range connections {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn := &connections[index]
		if !i.tryLock(conn.ID) {
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		summary, syncErr := i.syncConnection(callCtx, conn)
		cancel()
		i.unlock(conn.ID)
		if errors.Is(syncErr, errNotLinked) {
			continue
		}
		now := time.Now().UTC()
		_ = i.core.Store.UpdateSyncConnectionResult(context.Background(), conn.ID, &now, syncErr)
		if syncErr != nil {
			i.log.Warn("bangumi sync failed", "account_id", conn.AccountID, "error", syncErr)
			continue
		}
		i.log.Info("bangumi sync finished",
			"account_id", conn.AccountID,
			"pulled", summary.Pulled,
			"pushed", summary.Pushed,
			"removed", summary.Removed,
			"conflicts", summary.Conflicts,
			"failed", summary.Failed)
	}
	return nil
}

// tryLock refuses a second reconciliation of the same account. A manual
// "sync now" during the scheduled run should answer immediately rather than
// race two writers over the same rows.
func (i *instance) tryLock(connectionID uuid.UUID) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.syncing[connectionID] {
		return false
	}
	i.syncing[connectionID] = true
	return true
}

func (i *instance) unlock(connectionID uuid.UUID) {
	i.mu.Lock()
	delete(i.syncing, connectionID)
	i.mu.Unlock()
}
