package librarysync

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/google/uuid"
)

// localSourceID is the client's local-file source. Its links carry absolute
// paths on one device, so they never sync.
const localSourceID = "local"

// settingsClockKey is the client-supplied push time inside the settings map.
const settingsClockKey = "_updated_at"

// changeError is a validation failure attributed to one batch index.
type changeError struct {
	index   int
	message string
}

func (e changeError) Error() string { return fmt.Sprintf("changes[%d]: %s", e.index, e.message) }

// validateBatch rejects any change the applier could not interpret, before a
// single row is written: a malformed request never leaves a partial push
// behind.
func validateBatch(changes []Change) error {
	for i, change := range changes {
		if message := validateChange(change); message != "" {
			return changeError{index: i, message: message}
		}
	}
	return nil
}

// validateChange returns an empty string when the change is well formed.
func validateChange(change Change) string {
	if change.Kind != ChangeUpsert && change.Kind != ChangeDelete {
		return fmt.Sprintf("unknown kind %q", change.Kind)
	}
	switch change.Entity {
	case EntityTrack:
		if change.Track == nil || strings.TrimSpace(change.Track.Fingerprint) == "" {
			return "track requires track.fingerprint"
		}
		if change.Kind == ChangeUpsert && strings.TrimSpace(change.Track.UpdatedAt) == "" {
			return "track requires track.updated_at"
		}
	case EntityLink:
		if change.Link == nil || strings.TrimSpace(change.Link.SourceID) == "" || strings.TrimSpace(change.Link.ExternalID) == "" {
			return "link requires link.source_id and link.external_id"
		}
	case EntityLyrics:
		if change.Lyrics == nil || strings.TrimSpace(change.Lyrics.TrackFingerprint) == "" {
			return "lyrics requires lyrics.track_fingerprint"
		}
		if change.Kind == ChangeUpsert && strings.TrimSpace(change.Lyrics.FetchedAt) == "" {
			return "lyrics requires lyrics.fetched_at"
		}
	case EntityPlay:
		if change.Kind != ChangeUpsert {
			return fmt.Sprintf("play does not support %q", change.Kind)
		}
		if change.Play == nil || strings.TrimSpace(change.Play.TrackFingerprint) == "" {
			return "play requires play.track_fingerprint"
		}
		if strings.TrimSpace(change.Play.StartedAt) == "" {
			return "play requires play.started_at"
		}
	case EntitySession:
		if change.Kind == ChangeUpsert {
			if change.Session == nil || strings.TrimSpace(change.Session.UpdatedAt) == "" {
				return "session requires session.updated_at"
			}
		}
	case EntitySettings:
	case "":
		return "entity is required"
	default:
		return fmt.Sprintf("unknown entity %q", change.Entity)
	}
	if change.Kind == ChangeDelete &&
		change.Entity != EntitySession && change.Entity != EntitySettings {
		if change.DeletedAt == nil || strings.TrimSpace(*change.DeletedAt) == "" {
			return "delete requires deleted_at"
		}
	}
	return ""
}

// parseTime reads an RFC3339 clock. An empty value falls back to fallback so a
// client that omits a redundant timestamp still produces a usable row.
func parseTime(value string, fallback time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback.UTC(), nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable time %q", value)
}

// applier applies one change inside a push transaction.
type applier struct {
	tx        *gorm.DB
	accountID uuid.UUID
	deviceID  string
	now       time.Time
}

// applyOne writes change at revision rev. It reports false when the change lost
// the last-write-wins comparison, or when it is a rule-driven skip (a local
// link); the caller records the index as rejected either way.
func (a *applier) applyOne(change Change, rev int64) (bool, error) {
	switch change.Entity {
	case EntityTrack:
		return a.applyTrack(change, rev)
	case EntityLink:
		return a.applyLink(change, rev)
	case EntityLyrics:
		return a.applyLyrics(change, rev)
	case EntityPlay:
		return a.applyPlay(change, rev)
	case EntitySession:
		return a.applySession(change, rev)
	case EntitySettings:
		return a.applySettings(change, rev)
	}
	return false, nil
}

func isNotFound(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) }

func ptrString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
