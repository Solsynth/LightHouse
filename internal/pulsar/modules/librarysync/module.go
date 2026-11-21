// Package librarysync mirrors one account's GroovyBox music library: canonical
// tracks, per-source links, stored lyrics, play events, and the singleton
// session and settings documents. It is the device-to-device half of Pulsar.
// A play that crosses the scrobbler thresholds is also projected onto the
// canonical scrobble history, so GroovyBox playback shows up in the same
// history API as every other scrobble (see scrobble.go).
package librarysync

import (
	"context"
	"log/slog"
	"time"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/store"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

// Factory returns the library sync module factory.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             "app.solian.pulsar.librarysync",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Models:         model.LibrarySyncModels(),
		Capabilities:   []string{"library", "library.sync"},
		SQL: []module.SchemaHook{module.DDL(
			// Partial indexes keep tombstones cheap to find for the purge job.
			`CREATE INDEX IF NOT EXISTS ix_library_tracks_deleted
				ON library_tracks (account_id, deleted_at) WHERE deleted_at IS NOT NULL`,
			`CREATE INDEX IF NOT EXISTS ix_library_links_deleted
				ON library_links (account_id, deleted_at) WHERE deleted_at IS NOT NULL`,
			`CREATE INDEX IF NOT EXISTS ix_library_lyrics_deleted
				ON library_lyrics (account_id, deleted_at) WHERE deleted_at IS NOT NULL`,
		)},
		New: func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	core *core.Services
	log  *slog.Logger
}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	i.core = host.Core
	i.log = host.Log
	if i.log == nil {
		i.log = slog.Default()
	}
	i.registerRoutes(host)

	retention := time.Duration(host.Core.Config.LibrarySync.RetentionDays) * 24 * time.Hour
	if retention <= 0 {
		retention = 90 * 24 * time.Hour
	}
	host.Core.Jobs.Every("pulsar.librarysync.purge", 24*time.Hour, func(ctx context.Context) error {
		return store.PurgeLibrarySyncTombstones(ctx, host.Core.DB, retention)
	})
	// One-shot: plays that predate the play-to-scrobble projection are folded
	// into the history. Idempotent per account via its marker row.
	host.Core.Jobs.Now("pulsar.librarysync.scrobble-backfill", func(ctx context.Context) error {
		created, err := BackfillPlayScrobbles(ctx, host.Core.DB, host.Core.Config.Scrobbler)
		if err != nil {
			return err
		}
		if created > 0 {
			i.log.Info("backfilled historical plays into scrobble history", "scrobbles", created)
		}
		return nil
	})
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }
