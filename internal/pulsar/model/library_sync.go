package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// The library sync mirror: one account's music library as GroovyBox knows it.
//
// Every table carries a live row or a tombstone. DeletedAt is a plain nullable
// timestamp, deliberately not gorm.DeletedAt: tombstones must stay visible to
// SELECT so a pull can ship them, and only the purge job deletes them for real.

// LibrarySyncBase carries the row lifecycle columns every mirror table shares.
// DeletedAt carries no index tag: the purge job's partial indexes are declared
// once in the module's schema hook, where their names are explicit.
type LibrarySyncBase struct {
	// CreatedAt and UpdatedAt mirror the client row's own created_at and
	// updated_at; the apply code fills them explicitly.
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

// LibrarySyncCursor is one row per account: the monotone revision source.
type LibrarySyncCursor struct {
	AccountID uuid.UUID `gorm:"type:uuid;primaryKey" json:"account_id"`
	Rev       int64     `gorm:"not null;default:0" json:"rev"`
	LibrarySyncBase
}

func (LibrarySyncCursor) TableName() string { return "pulsar_library_cursors" }

// LibrarySyncTrack is one row per (account, fingerprint).
type LibrarySyncTrack struct {
	AccountID   uuid.UUID `gorm:"type:uuid;primaryKey" json:"account_id"`
	Fingerprint string    `gorm:"type:varchar(1024);primaryKey;not null" json:"fingerprint"`
	Title       string    `gorm:"type:varchar(1024);not null" json:"title"`
	Artist      *string   `gorm:"type:varchar(1024)" json:"artist,omitempty"`
	AlbumArtist *string   `gorm:"type:varchar(1024)" json:"album_artist,omitempty"`
	Album       *string   `gorm:"type:varchar(1024)" json:"album,omitempty"`
	Genre       *string   `gorm:"type:varchar(1024)" json:"genre,omitempty"`
	Year        *int      `json:"year,omitempty"`
	TrackNumber *int      `json:"track_number,omitempty"`
	DiscNumber  *int      `json:"disc_number,omitempty"`
	DurationMs  int64     `gorm:"not null;default:0" json:"duration_ms"`
	Mbid        *string   `gorm:"type:varchar(1024)" json:"mbid,omitempty"`
	// Clock is the last-write-wins key: UpdatedAt for a live row, DeletedAt for
	// a tombstone.
	Clock    time.Time `gorm:"not null;index:ix_pulsar_library_tracks_clock" json:"clock"`
	Rev      int64     `gorm:"not null;index:ix_pulsar_library_tracks_rev" json:"rev"`
	DeviceID string    `gorm:"type:varchar(64);not null" json:"device_id"`
	LibrarySyncBase
}

func (LibrarySyncTrack) TableName() string { return "pulsar_library_tracks" }

// LibrarySyncLink is one row per (account, source_id, external_id). Local-file
// links never reach this table: they name a path on one device.
type LibrarySyncLink struct {
	AccountID        uuid.UUID `gorm:"type:uuid;primaryKey" json:"account_id"`
	SourceID         string    `gorm:"type:varchar(1024);primaryKey;not null" json:"source_id"`
	ExternalID       string    `gorm:"type:varchar(1024);primaryKey;not null" json:"external_id"`
	TrackFingerprint string    `gorm:"type:varchar(1024);not null" json:"track_fingerprint"`
	AlbumExternalID  *string   `gorm:"type:varchar(1024)" json:"album_external_id,omitempty"`
	ArtistExternalID *string   `gorm:"type:varchar(1024)" json:"artist_external_id,omitempty"`
	ArtID            *string   `gorm:"type:varchar(1024)" json:"art_id,omitempty"`
	LinkedAt         time.Time `gorm:"not null" json:"linked_at"`
	// Clock is LastSeenAt for a live row, the tombstone time for a deletion.
	Clock    time.Time `gorm:"not null;index:ix_pulsar_library_links_rev" json:"clock"`
	Rev      int64     `gorm:"not null" json:"rev"`
	DeviceID string    `gorm:"type:varchar(64);not null" json:"device_id"`
	LibrarySyncBase
}

func (LibrarySyncLink) TableName() string { return "pulsar_library_links" }

// LibrarySyncLyrics is one row per (account, track_fingerprint).
type LibrarySyncLyrics struct {
	AccountID           uuid.UUID `gorm:"type:uuid;primaryKey" json:"account_id"`
	TrackFingerprint    string    `gorm:"type:varchar(1024);primaryKey;not null" json:"track_fingerprint"`
	Lines               string    `gorm:"type:text;not null" json:"lines"`
	Synced              bool      `gorm:"not null;default:false" json:"synced"`
	Language            *string   `gorm:"type:varchar(64)" json:"language,omitempty"`
	Origin              string    `gorm:"type:varchar(64);not null" json:"origin"`
	RemoteID            *int64    `json:"remote_id,omitempty"`
	FetchedAt           time.Time `gorm:"not null" json:"fetched_at"`
	TranslationLanguage *string   `gorm:"type:varchar(64)" json:"translation_language,omitempty"`
	// Clock is FetchedAt for a live row, the tombstone time for a deletion.
	Clock    time.Time `gorm:"not null;index:ix_pulsar_library_lyrics_rev" json:"clock"`
	Rev      int64     `gorm:"not null" json:"rev"`
	DeviceID string    `gorm:"type:varchar(64);not null" json:"device_id"`
	LibrarySyncBase
}

func (LibrarySyncLyrics) TableName() string { return "pulsar_library_lyrics" }

// LibrarySyncPlay is one row per (account, track_fingerprint, started_at).
// A play is append-only: it has no tombstone.
type LibrarySyncPlay struct {
	AccountID        uuid.UUID `gorm:"type:uuid;primaryKey" json:"account_id"`
	TrackFingerprint string    `gorm:"type:varchar(1024);primaryKey;not null" json:"track_fingerprint"`
	StartedAt        time.Time `gorm:"primaryKey" json:"started_at"`
	SourceID         string    `gorm:"type:varchar(1024);not null" json:"source_id"`
	ListenedMs       int64     `gorm:"not null;default:0" json:"listened_ms"`
	PositionMs       int64     `gorm:"not null;default:0" json:"position_ms"`
	Completed        bool      `gorm:"not null;default:false" json:"completed"`
	Rev              int64     `gorm:"not null;index:ix_pulsar_library_plays_rev" json:"rev"`
	DeviceID         string    `gorm:"type:varchar(64);not null" json:"device_id"`
	LibrarySyncBase
}

func (LibrarySyncPlay) TableName() string { return "pulsar_library_plays" }

// LibrarySyncSession is the account's playback-session document: one row per
// account, last-write-wins on Clock.
type LibrarySyncSession struct {
	AccountID uuid.UUID      `gorm:"type:uuid;primaryKey" json:"account_id"`
	Data      datatypes.JSON `gorm:"type:jsonb;not null" json:"data"`
	Clock     time.Time      `gorm:"not null;index:ix_pulsar_library_session_rev" json:"clock"`
	Rev       int64          `gorm:"not null" json:"rev"`
	DeviceID  string         `gorm:"type:varchar(64);not null" json:"device_id"`
	LibrarySyncBase
}

func (LibrarySyncSession) TableName() string { return "pulsar_library_session" }

// LibrarySyncSettings is the account's settings document: one row per account,
// last-write-wins on Clock.
type LibrarySyncSettings struct {
	AccountID uuid.UUID      `gorm:"type:uuid;primaryKey" json:"account_id"`
	Data      datatypes.JSON `gorm:"type:jsonb;not null" json:"data"`
	Clock     time.Time      `gorm:"not null;index:ix_pulsar_library_settings_rev" json:"clock"`
	Rev       int64          `gorm:"not null" json:"rev"`
	DeviceID  string         `gorm:"type:varchar(64);not null" json:"device_id"`
	LibrarySyncBase
}

func (LibrarySyncSettings) TableName() string { return "pulsar_library_settings" }

// LibrarySyncModels is every table the library sync module owns, for
// AutoMigrate.
func LibrarySyncModels() []any {
	return []any{
		&LibrarySyncCursor{},
		&LibrarySyncTrack{},
		&LibrarySyncLink{},
		&LibrarySyncLyrics{},
		&LibrarySyncPlay{},
		&LibrarySyncSession{},
		&LibrarySyncSettings{},
	}
}
