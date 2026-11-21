package metadata

import (
	"context"

	"github.com/google/uuid"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

// defaultUserAgent identifies Pulsar to upstream providers that do not require
// a contact address. MusicBrainz has its own UA because its policy demands one.
const defaultUserAgent = "Pulsar/1.0 (Solar Network)"

// ExternalLink represents a link to an upstream media service.
type ExternalLink struct {
	Platform string `json:"platform"`
	Label    string `json:"label"`
	URL      string `json:"url"`
}

// UnifiedMedia represents canonical unified metadata across all domains.
type UnifiedMedia struct {
	ID              uuid.UUID             `json:"id"`
	URN             string                `json:"urn"`
	Domain          string                `json:"domain"`
	Slug            string                `json:"slug"`
	Title           string                `json:"title"`
	Subtitle        string                `json:"subtitle,omitempty"`
	Caption         string                `json:"caption,omitempty"`
	Description     string                `json:"description,omitempty"`
	ArtworkURL      string                `json:"artwork_url,omitempty"`
	BannerURL       string                `json:"banner_url,omitempty"`
	DurationSeconds int                   `json:"duration_seconds,omitempty"`
	ReleaseDate     string                `json:"release_date,omitempty"`
	Genres          []string              `json:"genres,omitempty"`
	ExternalIDs     map[string]string     `json:"external_ids,omitempty"`
	ExternalLinks   []ExternalLink        `json:"external_links,omitempty"`
	Creators        []model.CreatorCredit `json:"creators,omitempty"`
	// The appended entity payloads: an artist's profile and album list, or a
	// record's tracklist. A track resolve leaves them empty.
	Artist            *ArtistProfile `json:"artist,omitempty"`
	Tracks            []AlbumTrack   `json:"tracks,omitempty"`
	Albums            []AlbumSummary `json:"albums,omitempty"`
	ReleaseGroupCount int            `json:"release_group_count,omitempty"`
	// The description the app draws under the name.
	Bio                 string `json:"bio,omitempty"`
	BioSource           string `json:"bio_source,omitempty"`
	ImageURL            string `json:"image_url,omitempty"`
	ShareURL            string `json:"share_url,omitempty"`
	ScrobbleCount       int64  `json:"scrobble_count"`
	ActiveListenerCount int64  `json:"active_listener_count"`
}

// ArtistProfile is what MusicBrainz knows about a person or group beyond the
// name: when and where they are from, what they play, and where the page may
// send a reader who wants more. Every field is optional — a thinly documented
// act answers with a name and little else, which still beats a blank page.
//
// It is embedded in UnifiedMedia rather than being its own payload so one
// record — the catalog item — carries everything the client needs for an
// artist page: the same row a share link or a scrobble resolves to.
type ArtistProfile struct {
	MBID string `json:"mbid,omitempty"`
	// "Person", "Group", "Orchestra", "Choir", or empty when untyped.
	Type           string `json:"type,omitempty"`
	SortName       string `json:"sort_name,omitempty"`
	Disambiguation string `json:"disambiguation,omitempty"`
	Country        string `json:"country,omitempty"`
	Area           string `json:"area,omitempty"`
	BeginArea      string `json:"begin_area,omitempty"`
	// Full dates ("1981-07-03") or bare years ("1985"), as MusicBrainz keeps
	// them. A person's span is birth and death, a group's is formation and
	// dissolution; the fields are named for the page's reading, not the API's.
	Born   string   `json:"born,omitempty"`
	Died   string   `json:"died,omitempty"`
	Genres []string `json:"genres,omitempty"`
	URL    string   `json:"url,omitempty"`
}

// AlbumTrack is one track of a release group, in disc and track order.
type AlbumTrack struct {
	RecordingMBID string `json:"recording_mbid,omitempty"`
	Title         string `json:"title"`
	Artist        string `json:"artist,omitempty"`
	Disc          int    `json:"disc,omitempty"`
	Position      int    `json:"position,omitempty"`
	LengthMs      int    `json:"length_ms,omitempty"`
}

// AlbumSummary is one record in an artist's discography: enough to draw a
// tile and open its page, without the tracklist a full album resolve carries.
type AlbumSummary struct {
	MBID       string `json:"mbid"`
	Title      string `json:"title"`
	Year       int    `json:"year,omitempty"`
	Type       string `json:"type,omitempty"`
	ArtworkURL string `json:"artwork_url,omitempty"`
}

// Provider defines the interface for an upstream metadata provider.
type Provider interface {
	Name() string
	Domain() string
	FetchByProviderKey(ctx context.Context, key string) (*UnifiedMedia, error)
	Search(ctx context.Context, query string, limit int) ([]UnifiedMedia, error)
}
