// Scrobble materialization: a play synced from GroovyBox is projected onto the
// canonical scrobble history, so /history, profile pages, catalog scrobble
// counts and the pulsar.scrobble.created event see one merged playback record.
// library_plays stays the device-to-device mirror; this is the user-facing
// half of the same event.
package librarysync

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/config"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/store"
)

// groovyBoxClientName is the ClientName stamped on scrobbles born from a
// library sync play.
const groovyBoxClientName = "GroovyBox"

// normalizeScrobbler restores the deployment defaults for a zero config, so an
// applier built without settings gates the same way config.Load does.
func normalizeScrobbler(cfg config.Scrobbler) config.Scrobbler {
	if cfg.AutoScrobbleThresholdPercent <= 0 {
		cfg.AutoScrobbleThresholdPercent = 50
	}
	if cfg.AutoScrobbleMinSeconds <= 0 {
		cfg.AutoScrobbleMinSeconds = 240
	}
	return cfg
}

// playScrobbleWorthy applies the same bar the scrobbler module uses for
// sessions: the play counts once it is completed, past the minimum listened
// seconds, or past the configured percentage of the track.
func playScrobbleWorthy(cfg config.Scrobbler, listenedMs, durationMs int64, completed bool) bool {
	if completed {
		return true
	}
	listened := listenedMs / 1000
	if listened >= int64(cfg.AutoScrobbleMinSeconds) {
		return true
	}
	duration := durationMs / 1000
	return duration > 0 && listened*100 >= int64(cfg.AutoScrobbleThresholdPercent)*duration
}

// playScrobbleID derives a stable scrobble id from the play's natural key, so
// a re-pushed or folded play lands on the same history row instead of
// duplicating it.
func playScrobbleID(accountID uuid.UUID, fingerprint string, startedAt time.Time) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(
		"pulsar:library-play:"+accountID.String()+":"+fingerprint+":"+startedAt.UTC().Format(time.RFC3339Nano),
	))
}

// syncPlayScrobble projects a play onto the scrobble history. It creates the
// scrobble when the play first crosses the scrobble threshold and folds later
// progress pushes into the existing row. Newly created scrobbles are collected
// on the applier so the push handler can publish their events after the
// transaction commits.
func (a *applier) syncPlayScrobble(fingerprint string, startedAt time.Time, listenedMs int64, completed bool) error {
	cfg := normalizeScrobbler(a.scrobbler)

	track := model.LibrarySyncTrack{}
	err := a.tx.Where("account_id = ? AND fingerprint = ?", a.accountID, fingerprint).Take(&track).Error
	title, artist := track.Title, track.Artist
	durationMs := track.DurationMs
	if isNotFound(err) {
		// The play arrived before its track row: keep the record addressable
		// by fingerprint and let the threshold gate on listened seconds.
		title, artist, durationMs = fingerprint, nil, 0
	} else if err != nil {
		return err
	}

	if !playScrobbleWorthy(cfg, listenedMs, durationMs, completed) {
		return nil
	}

	duration := int(durationMs / 1000)
	catalog, err := store.FindOrCreateCatalogItemDB(a.tx, &model.CatalogItem{
		Domain:          model.DomainMusic,
		Provider:        "groovybox",
		ProviderKey:     fingerprint,
		Title:           title,
		Subtitle:        artist,
		DurationSeconds: duration,
	})
	if err != nil {
		return err
	}

	meta, err := json.Marshal(map[string]any{
		"source":      "library_sync",
		"fingerprint": fingerprint,
	})
	if err != nil {
		return err
	}

	scrobble := model.Scrobble{
		ID:              playScrobbleID(a.accountID, fingerprint, startedAt),
		AccountID:       a.accountID,
		CatalogID:       &catalog.ID,
		Domain:          model.DomainMusic,
		ClientName:      groovyBoxClientName,
		Title:           title,
		Subtitle:        artist,
		ListenedSeconds: int(listenedMs / 1000),
		DurationSeconds: duration,
		PlayedAt:        startedAt.UTC(),
		Meta:            datatypes.JSON(meta),
	}

	existing := model.Scrobble{}
	err = a.tx.Where("id = ?", scrobble.ID).Take(&existing).Error
	if isNotFound(err) {
		a.createdScrobbles = append(a.createdScrobbles, &scrobble)
		return a.tx.Create(&scrobble).Error
	}
	if err != nil {
		return err
	}
	// A later push folded more progress into the same play.
	if scrobble.ListenedSeconds > existing.ListenedSeconds {
		return a.tx.Model(&model.Scrobble{}).Where("id = ?", existing.ID).
			Update("listened_seconds", scrobble.ListenedSeconds).Error
	}
	return nil
}
