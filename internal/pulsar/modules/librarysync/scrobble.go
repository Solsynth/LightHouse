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
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

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

// projectPlayScrobble builds the scrobble a play projects to and upserts it by
// its deterministic id. It returns the row only when this call created it, so
// the push handler publishes one event per scrobble. A concurrent writer (the
// startup backfill racing a live push) loses the insert to ON CONFLICT and
// falls through to the progress fold instead of aborting its transaction.
func projectPlayScrobble(tx *gorm.DB, accountID uuid.UUID, cfg config.Scrobbler, fingerprint string, startedAt time.Time, listenedMs int64, completed bool) (*model.Scrobble, error) {
	cfg = normalizeScrobbler(cfg)

	track := model.LibrarySyncTrack{}
	err := tx.Where("account_id = ? AND fingerprint = ?", accountID, fingerprint).Take(&track).Error
	title, artist := track.Title, track.Artist
	durationMs := track.DurationMs
	if isNotFound(err) {
		// The play arrived before its track row: keep the record addressable
		// by fingerprint and let the threshold gate on listened seconds.
		title, artist, durationMs = fingerprint, nil, 0
	} else if err != nil {
		return nil, err
	}

	if !playScrobbleWorthy(cfg, listenedMs, durationMs, completed) {
		return nil, nil
	}

	duration := int(durationMs / 1000)
	catalog, err := store.FindOrCreateCatalogItemDB(tx, &model.CatalogItem{
		Domain:          model.DomainMusic,
		Provider:        "groovybox",
		ProviderKey:     fingerprint,
		Title:           title,
		Subtitle:        artist,
		DurationSeconds: duration,
	})
	if err != nil {
		return nil, err
	}

	meta, err := json.Marshal(map[string]any{
		"source":      "library_sync",
		"fingerprint": fingerprint,
	})
	if err != nil {
		return nil, err
	}

	scrobble := model.Scrobble{
		ID:              playScrobbleID(accountID, fingerprint, startedAt),
		AccountID:       accountID,
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

	res := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoNothing: true}).Create(&scrobble)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected > 0 {
		return &scrobble, nil
	}
	// The scrobble already exists: a later push folded more progress into the
	// same play.
	return nil, tx.Model(&model.Scrobble{}).
		Where("id = ? AND listened_seconds < ?", scrobble.ID, scrobble.ListenedSeconds).
		Update("listened_seconds", scrobble.ListenedSeconds).Error
}

// syncPlayScrobble projects a play onto the scrobble history for the live push
// path, collecting rows the handler publishes events for after the commit.
func (a *applier) syncPlayScrobble(fingerprint string, startedAt time.Time, listenedMs int64, completed bool) error {
	created, err := projectPlayScrobble(a.tx, a.accountID, a.scrobbler, fingerprint, startedAt, listenedMs, completed)
	if err != nil {
		return err
	}
	if created != nil {
		a.createdScrobbles = append(a.createdScrobbles, created)
	}
	return nil
}
