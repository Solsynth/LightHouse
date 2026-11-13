// The library sync wire contract shared by the handlers and their tests. All
// times are RFC3339 UTC strings with second precision; a null field means
// "absent", never "unchanged". Account ids are derived from the authenticated
// session only — no request body carries one.
package librarysync

// ChangeKind is the operation a change represents.
type ChangeKind string

const (
	// ChangeUpsert writes or overwrites a record.
	ChangeUpsert ChangeKind = "upsert"
	// ChangeDelete tombstones a record.
	ChangeDelete ChangeKind = "delete"
)

// Entity names carried on Change.Entity.
const (
	EntityTrack    = "track"
	EntityLink     = "link"
	EntityLyrics   = "lyrics"
	EntityPlay     = "play"
	EntitySession  = "session"
	EntitySettings = "settings"
)

// Change is one entity mutation. Exactly one record field is set for a
// kind=upsert; a kind=delete carries only the record's natural-key fields.
type Change struct {
	Kind ChangeKind `json:"kind"`
	// Entity selects the record type (EntityTrack ... EntitySettings).
	Entity string `json:"entity"`
	// DeletedAt is required for kind=delete: the tombstone clock. It replaces
	// the record's own clock (updated_at / last_seen_at / fetched_at).
	DeletedAt *string `json:"deleted_at,omitempty"`

	Track    *TrackRecord   `json:"track,omitempty"`
	Link     *LinkRecord    `json:"link,omitempty"`
	Lyrics   *LyricsRecord  `json:"lyrics,omitempty"`
	Play     *PlayRecord    `json:"play,omitempty"`
	Session  *SessionRecord `json:"session,omitempty"`
	Settings map[string]any `json:"settings,omitempty"`
}

// TrackRecord mirrors a client track row.
type TrackRecord struct {
	Fingerprint string  `json:"fingerprint"`
	Title       string  `json:"title"`
	Artist      *string `json:"artist"`
	AlbumArtist *string `json:"album_artist"`
	Album       *string `json:"album"`
	Genre       *string `json:"genre"`
	Year        *int    `json:"year"`
	TrackNumber *int    `json:"track_number"`
	DiscNumber  *int    `json:"disc_number"`
	DurationMs  int64   `json:"duration_ms"`
	Mbid        *string `json:"mbid"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// LinkRecord mirrors a client track link: the song's identity on one source.
type LinkRecord struct {
	SourceID         string  `json:"source_id"`
	ExternalID       string  `json:"external_id"`
	TrackFingerprint string  `json:"track_fingerprint"`
	AlbumExternalID  *string `json:"album_external_id"`
	ArtistExternalID *string `json:"artist_external_id"`
	ArtID            *string `json:"art_id"`
	LinkedAt         string  `json:"linked_at"`
	LastSeenAt       string  `json:"last_seen_at"`
}

// LyricsRecord mirrors a client lyrics row. Lines stays the raw JSON string the
// client stores.
type LyricsRecord struct {
	TrackFingerprint    string  `json:"track_fingerprint"`
	Lines               string  `json:"lines"`
	Synced              bool    `json:"synced"`
	Language            *string `json:"language"`
	Origin              string  `json:"origin"`
	RemoteID            *int64  `json:"remote_id"`
	FetchedAt           string  `json:"fetched_at"`
	TranslationLanguage *string `json:"translation_language"`
}

// PlayRecord is one play of one track. It has no delete: a play is append-only.
type PlayRecord struct {
	TrackFingerprint string `json:"track_fingerprint"`
	SourceID         string `json:"source_id"`
	StartedAt        string `json:"started_at"`
	// Clock is max(started_at, the row's last in-place update): the client
	// sends play_events.touched_at here, or started_at when it has none.
	Clock      string `json:"clock"`
	ListenedMs int64  `json:"listened_ms"`
	PositionMs int64  `json:"position_ms"`
	Completed  bool   `json:"completed"`
}

// SessionRecord mirrors the client's singleton playback session.
type SessionRecord struct {
	QueueJSON    string `json:"queue_json"`
	CurrentIndex *int   `json:"current_index"`
	PositionMs   int64  `json:"position_ms"`
	DurationMs   int64  `json:"duration_ms"`
	Shuffle      bool   `json:"shuffle"`
	RepeatMode   string `json:"repeat_mode"`
	UpdatedAt    string `json:"updated_at"`
}

// PushRequest is one batch of local changes.
type PushRequest struct {
	DeviceID string   `json:"device_id"`
	Changes  []Change `json:"changes"`
}

// PushResponse reports the account's cursor after the push and the indices of
// changes whose clock was older than the stored row.
type PushResponse struct {
	Rev      int64 `json:"rev"`
	Rejected []int `json:"rejected"`
}

// PullResponse is one page of the account's mirror. Live rows travel in the
// typed arrays; tombstones travel in Removed.
type PullResponse struct {
	Rev      int64          `json:"rev"`
	HasMore  bool           `json:"has_more"`
	Tracks   []TrackRecord  `json:"tracks"`
	Links    []LinkRecord   `json:"links"`
	Lyrics   []LyricsRecord `json:"lyrics"`
	Plays    []PlayRecord   `json:"plays"`
	Session  *SessionRecord `json:"session"`
	Settings map[string]any `json:"settings"`
	// SessionClock and SettingsClock are the document clocks even when the
	// document itself is empty (cleared), so a client can tell "cleared now"
	// from "unchanged".
	SessionClock  *string `json:"session_clock"`
	SettingsClock *string `json:"settings_clock"`
	// Removed maps an entity name to encoded natural keys carrying the
	// tombstone clock.
	Removed map[string][]string `json:"removed"`
}

// StatusResponse is the account's sync summary.
type StatusResponse struct {
	Rev         int64  `json:"rev"`
	Tracks      int64  `json:"tracks"`
	Plays       int64  `json:"plays"`
	DeviceCount int64  `json:"device_count"`
	LastDevice  string `json:"last_device"`
}
