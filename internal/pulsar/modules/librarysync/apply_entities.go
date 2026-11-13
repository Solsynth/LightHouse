package librarysync

import (
	"encoding/json"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func (a *applier) applyTrack(change Change, rev int64) (bool, error) {
	record := change.Track
	key := func(db *gorm.DB) *gorm.DB {
		return db.Model(&model.LibrarySyncTrack{}).
			Where("account_id = ? AND fingerprint = ?", a.accountID, record.Fingerprint)
	}

	if change.Kind == ChangeDelete {
		clock, err := parseTime(ptrString(change.DeletedAt), a.now)
		if err != nil {
			return false, err
		}
		existing := model.LibrarySyncTrack{}
		err = key(a.tx).Take(&existing).Error
		if isNotFound(err) {
			tombstone := model.LibrarySyncTrack{
				AccountID:   a.accountID,
				Fingerprint: record.Fingerprint,
				Title:       "",
				Clock:       clock,
				Rev:         rev,
				DeviceID:    a.deviceID,

				LibrarySyncBase: model.LibrarySyncBase{CreatedAt: clock, UpdatedAt: clock, DeletedAt: &clock},
			}
			return true, a.tx.Create(&tombstone).Error
		}
		if err != nil {
			return false, err
		}
		if clock.Before(existing.Clock) {
			return false, nil
		}
		return true, key(a.tx).Updates(map[string]any{
			"clock":      clock,
			"deleted_at": clock,
			"rev":        rev,
			"device_id":  a.deviceID,
		}).Error
	}

	updatedAt, err := parseTime(record.UpdatedAt, a.now)
	if err != nil {
		return false, err
	}
	createdAt, err := parseTime(record.CreatedAt, updatedAt)
	if err != nil {
		return false, err
	}
	existing := model.LibrarySyncTrack{}
	err = key(a.tx).Take(&existing).Error
	if isNotFound(err) {
		row := model.LibrarySyncTrack{
			AccountID:   a.accountID,
			Fingerprint: record.Fingerprint,
			Title:       record.Title,
			Artist:      record.Artist,
			AlbumArtist: record.AlbumArtist,
			Album:       record.Album,
			Genre:       record.Genre,
			Year:        record.Year,
			TrackNumber: record.TrackNumber,
			DiscNumber:  record.DiscNumber,
			DurationMs:  record.DurationMs,
			Mbid:        record.Mbid,
			Clock:       updatedAt,
			Rev:         rev,
			DeviceID:    a.deviceID,

			LibrarySyncBase: model.LibrarySyncBase{CreatedAt: createdAt, UpdatedAt: updatedAt},
		}
		return true, a.tx.Create(&row).Error
	}
	if err != nil {
		return false, err
	}
	if updatedAt.Before(existing.Clock) {
		return false, nil
	}
	return true, key(a.tx).Updates(map[string]any{
		"title":        record.Title,
		"artist":       record.Artist,
		"album_artist": record.AlbumArtist,
		"album":        record.Album,
		"genre":        record.Genre,
		"year":         record.Year,
		"track_number": record.TrackNumber,
		"disc_number":  record.DiscNumber,
		"duration_ms":  record.DurationMs,
		"mbid":         record.Mbid,
		"created_at":   createdAt,
		"updated_at":   updatedAt,
		"clock":        updatedAt,
		"deleted_at":   nil,
		"rev":          rev,
		"device_id":    a.deviceID,
	}).Error
}

func (a *applier) applyLink(change Change, rev int64) (bool, error) {
	record := change.Link
	// A local-file link is a path on one device; it never syncs.
	if record.SourceID == localSourceID {
		return false, nil
	}
	key := func(db *gorm.DB) *gorm.DB {
		return db.Model(&model.LibrarySyncLink{}).
			Where("account_id = ? AND source_id = ? AND external_id = ?",
				a.accountID, record.SourceID, record.ExternalID)
	}

	if change.Kind == ChangeDelete {
		clock, err := parseTime(ptrString(change.DeletedAt), a.now)
		if err != nil {
			return false, err
		}
		existing := model.LibrarySyncLink{}
		err = key(a.tx).Take(&existing).Error
		if isNotFound(err) {
			tombstone := model.LibrarySyncLink{
				AccountID:  a.accountID,
				SourceID:   record.SourceID,
				ExternalID: record.ExternalID,
				LinkedAt:   clock,
				Clock:      clock,
				Rev:        rev,
				DeviceID:   a.deviceID,

				LibrarySyncBase: model.LibrarySyncBase{CreatedAt: clock, UpdatedAt: clock, DeletedAt: &clock},
			}
			return true, a.tx.Create(&tombstone).Error
		}
		if err != nil {
			return false, err
		}
		if clock.Before(existing.Clock) {
			return false, nil
		}
		return true, key(a.tx).Updates(map[string]any{
			"clock":      clock,
			"deleted_at": clock,
			"rev":        rev,
			"device_id":  a.deviceID,
		}).Error
	}

	clock, err := parseTime(record.LastSeenAt, a.now)
	if err != nil {
		return false, err
	}
	linkedAt, err := parseTime(record.LinkedAt, clock)
	if err != nil {
		return false, err
	}
	existing := model.LibrarySyncLink{}
	err = key(a.tx).Take(&existing).Error
	if isNotFound(err) {
		row := model.LibrarySyncLink{
			AccountID:        a.accountID,
			SourceID:         record.SourceID,
			ExternalID:       record.ExternalID,
			TrackFingerprint: record.TrackFingerprint,
			AlbumExternalID:  record.AlbumExternalID,
			ArtistExternalID: record.ArtistExternalID,
			ArtID:            record.ArtID,
			LinkedAt:         linkedAt,
			Clock:            clock,
			Rev:              rev,
			DeviceID:         a.deviceID,

			LibrarySyncBase: model.LibrarySyncBase{CreatedAt: linkedAt, UpdatedAt: clock},
		}
		return true, a.tx.Create(&row).Error
	}
	if err != nil {
		return false, err
	}
	if clock.Before(existing.Clock) {
		return false, nil
	}
	return true, key(a.tx).Updates(map[string]any{
		"track_fingerprint":  record.TrackFingerprint,
		"album_external_id":  record.AlbumExternalID,
		"artist_external_id": record.ArtistExternalID,
		"art_id":             record.ArtID,
		"linked_at":          linkedAt,
		"updated_at":         clock,
		"clock":              clock,
		"deleted_at":         nil,
		"rev":                rev,
		"device_id":          a.deviceID,
	}).Error
}

func (a *applier) applyLyrics(change Change, rev int64) (bool, error) {
	record := change.Lyrics
	key := func(db *gorm.DB) *gorm.DB {
		return db.Model(&model.LibrarySyncLyrics{}).
			Where("account_id = ? AND track_fingerprint = ?", a.accountID, record.TrackFingerprint)
	}

	if change.Kind == ChangeDelete {
		clock, err := parseTime(ptrString(change.DeletedAt), a.now)
		if err != nil {
			return false, err
		}
		existing := model.LibrarySyncLyrics{}
		err = key(a.tx).Take(&existing).Error
		if isNotFound(err) {
			tombstone := model.LibrarySyncLyrics{
				AccountID:        a.accountID,
				TrackFingerprint: record.TrackFingerprint,
				Lines:            "",
				Origin:           "none",
				FetchedAt:        clock,
				Clock:            clock,
				Rev:              rev,
				DeviceID:         a.deviceID,

				LibrarySyncBase: model.LibrarySyncBase{CreatedAt: clock, UpdatedAt: clock, DeletedAt: &clock},
			}
			return true, a.tx.Create(&tombstone).Error
		}
		if err != nil {
			return false, err
		}
		if clock.Before(existing.Clock) {
			return false, nil
		}
		return true, key(a.tx).Updates(map[string]any{
			"clock":      clock,
			"deleted_at": clock,
			"rev":        rev,
			"device_id":  a.deviceID,
		}).Error
	}

	clock, err := parseTime(record.FetchedAt, a.now)
	if err != nil {
		return false, err
	}
	existing := model.LibrarySyncLyrics{}
	err = key(a.tx).Take(&existing).Error
	if isNotFound(err) {
		row := model.LibrarySyncLyrics{
			AccountID:           a.accountID,
			TrackFingerprint:    record.TrackFingerprint,
			Lines:               record.Lines,
			Synced:              record.Synced,
			Language:            record.Language,
			Origin:              record.Origin,
			RemoteID:            record.RemoteID,
			FetchedAt:           clock,
			TranslationLanguage: record.TranslationLanguage,
			Clock:               clock,
			Rev:                 rev,
			DeviceID:            a.deviceID,

			LibrarySyncBase: model.LibrarySyncBase{CreatedAt: clock, UpdatedAt: clock},
		}
		return true, a.tx.Create(&row).Error
	}
	if err != nil {
		return false, err
	}
	if clock.Before(existing.Clock) {
		return false, nil
	}
	return true, key(a.tx).Updates(map[string]any{
		"lines":                record.Lines,
		"synced":               record.Synced,
		"language":             record.Language,
		"origin":               record.Origin,
		"remote_id":            record.RemoteID,
		"fetched_at":           clock,
		"translation_language": record.TranslationLanguage,
		"updated_at":           clock,
		"clock":                clock,
		"deleted_at":           nil,
		"rev":                  rev,
		"device_id":            a.deviceID,
	}).Error
}

func (a *applier) applyPlay(change Change, rev int64) (bool, error) {
	record := change.Play
	startedAt, err := parseTime(record.StartedAt, a.now)
	if err != nil {
		return false, err
	}
	clock, err := parseTime(record.Clock, startedAt)
	if err != nil {
		return false, err
	}
	if clock.Before(startedAt) {
		clock = startedAt
	}
	key := func(db *gorm.DB) *gorm.DB {
		return db.Model(&model.LibrarySyncPlay{}).
			Where("account_id = ? AND track_fingerprint = ? AND started_at = ?",
				a.accountID, record.TrackFingerprint, startedAt)
	}

	existing := model.LibrarySyncPlay{}
	err = key(a.tx).Take(&existing).Error
	if isNotFound(err) {
		row := model.LibrarySyncPlay{
			AccountID:        a.accountID,
			TrackFingerprint: record.TrackFingerprint,
			StartedAt:        startedAt,
			SourceID:         record.SourceID,
			ListenedMs:       record.ListenedMs,
			PositionMs:       record.PositionMs,
			Completed:        record.Completed,
			Rev:              rev,
			DeviceID:         a.deviceID,

			LibrarySyncBase: model.LibrarySyncBase{CreatedAt: startedAt, UpdatedAt: clock},
		}
		return true, a.tx.Create(&row).Error
	}
	if err != nil {
		return false, err
	}
	// A play is append-only and cannot be stale: fold the two devices' progress
	// into the wider one instead of rejecting.
	listened := existing.ListenedMs
	if record.ListenedMs > listened {
		listened = record.ListenedMs
	}
	position := existing.PositionMs
	if record.PositionMs > position {
		position = record.PositionMs
	}
	updates := map[string]any{
		"listened_ms": listened,
		"position_ms": position,
		"completed":   existing.Completed || record.Completed,
		"updated_at":  clock,
		"rev":         rev,
		"device_id":   a.deviceID,
	}
	if existing.SourceID == "" && record.SourceID != "" {
		updates["source_id"] = record.SourceID
	}
	return true, key(a.tx).Updates(updates).Error
}

func (a *applier) applySession(change Change, rev int64) (bool, error) {
	clock := a.now
	var document []byte
	if change.Kind == ChangeDelete {
		if change.DeletedAt != nil {
			parsed, err := parseTime(*change.DeletedAt, a.now)
			if err != nil {
				return false, err
			}
			clock = parsed
		}
		document = []byte("{}")
	} else {
		parsed, err := parseTime(change.Session.UpdatedAt, a.now)
		if err != nil {
			return false, err
		}
		clock = parsed
		session := struct {
			QueueJSON    string `json:"queue_json"`
			CurrentIndex *int   `json:"current_index"`
			PositionMs   int64  `json:"position_ms"`
			DurationMs   int64  `json:"duration_ms"`
			Shuffle      bool   `json:"shuffle"`
			RepeatMode   string `json:"repeat_mode"`
			UpdatedAt    string `json:"updated_at"`
		}{
			QueueJSON:    change.Session.QueueJSON,
			CurrentIndex: change.Session.CurrentIndex,
			PositionMs:   change.Session.PositionMs,
			DurationMs:   change.Session.DurationMs,
			Shuffle:      change.Session.Shuffle,
			RepeatMode:   change.Session.RepeatMode,
			UpdatedAt:    clock.Format(time.RFC3339),
		}
		document, err = json.Marshal(session)
		if err != nil {
			return false, err
		}
	}
	return a.applyDocument(change.Entity, rev, clock, document)
}

func (a *applier) applySettings(change Change, rev int64) (bool, error) {
	clock := a.now
	if raw, ok := change.Settings[settingsClockKey].(string); ok {
		parsed, err := parseTime(raw, a.now)
		if err != nil {
			return false, err
		}
		clock = parsed
	}
	var document []byte
	if change.Kind == ChangeDelete {
		document = []byte("{}")
	} else {
		values := make(map[string]any, len(change.Settings))
		for key, value := range change.Settings {
			if key == settingsClockKey {
				continue
			}
			values[key] = value
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			return false, err
		}
		document = encoded
	}
	return a.applyDocument(change.Entity, rev, clock, document)
}

// applyDocument writes one per-account singleton document under
// last-write-wins on its clock. A cleared document is the empty object: the row
// stays so later pulls still see the new clock.
func (a *applier) applyDocument(entity string, rev int64, clock time.Time, document []byte) (bool, error) {
	table := documentTable(entity)
	existing := struct {
		Clock     time.Time
		DeletedAt *time.Time
	}{}
	err := a.tx.Table(table).
		Select("clock", "deleted_at").
		Where("account_id = ?", a.accountID).
		Take(&existing).Error
	if isNotFound(err) {
		return true, a.tx.Table(table).Create(map[string]any{
			"account_id": a.accountID,
			"data":       datatypes.JSON(document),
			"clock":      clock,
			"rev":        rev,
			"device_id":  a.deviceID,
			"created_at": a.now,
			"updated_at": clock,
		}).Error
	}
	if err != nil {
		return false, err
	}
	if clock.Before(existing.Clock) {
		return false, nil
	}
	return true, a.tx.Table(table).
		Where("account_id = ?", a.accountID).
		Updates(map[string]any{
			"data":       datatypes.JSON(document),
			"clock":      clock,
			"rev":        rev,
			"device_id":  a.deviceID,
			"updated_at": clock,
		}).Error
}

func documentTable(entity string) string {
	if entity == EntitySettings {
		return "pulsar_library_settings"
	}
	return "pulsar_library_session"
}
