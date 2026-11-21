package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Supported media domains.
const (
	DomainMusic  = "music"
	DomainGaming = "gaming"
	DomainVideo  = "video"
	DomainAnime  = "anime"
	DomainBook   = "book"
	DomainCustom = "custom"
)

// Supported collection kinds.
const (
	KindPlaylist  = "playlist"
	KindWatchlist = "watchlist"
	KindBacklog   = "backlog"
	KindFavorites = "favorites"
	KindCustom    = "custom"
)

// Favourite kinds: what a favourites entry says the account liked. A
// collection is a list of things, and the things are not all recordings — an
// album or a playlist is itself a catalog record, and a heart on one has to
// stay distinguishable from a heart on a track when a client reads the list
// back. Empty is read as [FavouriteTrack], which is what every row written
// before this field existed meant.
const (
	FavouriteTrack    = "track"
	FavouriteAlbum    = "album"
	FavouritePlaylist = "playlist"
)

// FavouriteKinds is the set a request may name. Anything else is a client
// bug, not a new kind the server should quietly store.
var FavouriteKinds = map[string]bool{
	FavouriteTrack:    true,
	FavouriteAlbum:    true,
	FavouritePlaylist: true,
}

// Collection item statuses.
const (
	StatusPlanToWatch = "plan_to_watch"
	StatusWatching    = "watching"
	StatusCompleted   = "completed"
	StatusOnHold      = "on_hold"
	StatusDropped     = "dropped"
)

// Creator roles on catalog items.
const (
	RoleArtist    = "artist"
	RoleAuthor    = "author"
	RoleDirector  = "director"
	RoleDeveloper = "developer"
	RolePublisher = "publisher"
	RoleStudio    = "studio"
)

// CreatorCredit names one creator of a media item and their role, like the
// artist of a track or the director of a film.
type CreatorCredit struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// CatalogItem is a canonical entry across media domains (track, game, anime, video, book).
type CatalogItem struct {
	ID              uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	Domain          string         `gorm:"type:varchar(32);not null;index:ix_catalog_domain" json:"domain"`
	Slug            string         `gorm:"type:varchar(256);index:ix_catalog_slug" json:"slug,omitempty"`
	URN             string         `gorm:"type:varchar(512);index:ix_catalog_urn" json:"urn,omitempty"`
	Provider        string         `gorm:"type:varchar(64);index:ix_catalog_prov" json:"provider"`
	ProviderKey     string         `gorm:"type:varchar(512);index:ix_catalog_provkey" json:"provider_key,omitempty"`
	Title           string         `gorm:"type:varchar(1024);not null" json:"title"`
	Subtitle        *string        `gorm:"type:varchar(1024)" json:"subtitle,omitempty"`
	Caption         *string        `gorm:"type:varchar(2048)" json:"caption,omitempty"`
	Description     *string        `gorm:"type:text" json:"description,omitempty"`
	ArtworkHash     *string        `gorm:"type:varchar(128)" json:"artwork_hash,omitempty"`
	ArtworkURL      *string        `gorm:"type:varchar(4096)" json:"artwork_url,omitempty"`
	BannerURL       *string        `gorm:"type:varchar(4096)" json:"banner_url,omitempty"`
	DurationSeconds int            `gorm:"default:0" json:"duration_seconds"`
	ReleaseDate     *string        `gorm:"type:varchar(64)" json:"release_date,omitempty"`
	Genres          datatypes.JSON `gorm:"type:jsonb" json:"genres,omitempty"`
	ExternalIDs     datatypes.JSON `gorm:"type:jsonb" json:"external_ids,omitempty"`
	ExternalLinks   datatypes.JSON `gorm:"type:jsonb" json:"external_links,omitempty"`
	Metadata        datatypes.JSON `gorm:"type:jsonb" json:"metadata,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `gorm:"index" json:"-"`
}

func (CatalogItem) TableName() string { return "catalog_items" }

// GetGenres decodes the Genres JSON array.
func (c *CatalogItem) GetGenres() []string {
	if len(c.Genres) == 0 {
		return nil
	}
	var genres []string
	_ = json.Unmarshal(c.Genres, &genres)
	return genres
}

// GetExternalIDs decodes ExternalIDs map.
func (c *CatalogItem) GetExternalIDs() map[string]string {
	if len(c.ExternalIDs) == 0 {
		return nil
	}
	var res map[string]string
	_ = json.Unmarshal(c.ExternalIDs, &res)
	return res
}

// ActiveSession represents real-time now-playing or live gaming/media playback.
type ActiveSession struct {
	ID              uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	AccountID       uuid.UUID      `gorm:"type:uuid;not null;uniqueIndex:ix_sess_acc_man" json:"account_id"`
	CatalogID       *uuid.UUID     `gorm:"type:uuid;index:ix_session_catalog" json:"catalog_id,omitempty"`
	Domain          string         `gorm:"type:varchar(32);not null" json:"domain"`
	ManualID        string         `gorm:"type:varchar(256);not null;uniqueIndex:ix_sess_acc_man" json:"manual_id"`
	Title           string         `gorm:"type:varchar(1024);not null" json:"title"`
	Subtitle        *string        `gorm:"type:varchar(1024)" json:"subtitle,omitempty"`
	Caption         *string        `gorm:"type:varchar(2048)" json:"caption,omitempty"`
	ArtworkURL      *string        `gorm:"type:varchar(4096)" json:"artwork_url,omitempty"`
	LargeImage      *string        `gorm:"type:varchar(4096)" json:"large_image,omitempty"`
	SmallImage      *string        `gorm:"type:varchar(4096)" json:"small_image,omitempty"`
	ProgressSeconds int            `gorm:"default:0" json:"progress_seconds"`
	DurationSeconds int            `gorm:"default:0" json:"duration_seconds"`
	IsPaused        bool           `gorm:"default:false" json:"is_paused"`
	LeaseMinutes    int            `gorm:"default:5" json:"lease_minutes"`
	LeaseExpiresAt  time.Time      `gorm:"index:ix_session_lease" json:"lease_expires_at"`
	StartedAt       time.Time      `json:"started_at"`
	EndedAt         *time.Time     `json:"ended_at,omitempty"`
	Meta            datatypes.JSON `gorm:"type:jsonb" json:"meta,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`

	CatalogItem *CatalogItem `gorm:"foreignKey:CatalogID" json:"catalog_item,omitempty"`
}

func (ActiveSession) TableName() string { return "active_sessions" }

// Scrobble records a finalized media listening, watching, or gaming playback event.
type Scrobble struct {
	ID              uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	AccountID       uuid.UUID      `gorm:"type:uuid;not null;index:ix_scrobble_account_played,priority:1" json:"account_id"`
	CatalogID       *uuid.UUID     `gorm:"type:uuid;index:ix_scrobble_catalog" json:"catalog_id,omitempty"`
	Domain          string         `gorm:"type:varchar(32);not null;index:ix_scrobble_domain" json:"domain"`
	ClientName      string         `gorm:"type:varchar(128);not null" json:"client_name"`
	Title           string         `gorm:"type:varchar(1024);not null" json:"title"`
	Subtitle        *string        `gorm:"type:varchar(1024)" json:"subtitle,omitempty"`
	Caption         *string        `gorm:"type:varchar(2048)" json:"caption,omitempty"`
	ArtworkURL      *string        `gorm:"type:varchar(4096)" json:"artwork_url,omitempty"`
	ListenedSeconds int            `gorm:"default:0" json:"listened_seconds"`
	DurationSeconds int            `gorm:"default:0" json:"duration_seconds"`
	PlayedAt        time.Time      `gorm:"not null;index:ix_scrobble_account_played,priority:2;index:ix_scrobble_played" json:"played_at"`
	IsImported      bool           `gorm:"default:false" json:"is_imported"`
	Meta            datatypes.JSON `gorm:"type:jsonb" json:"meta,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`

	CatalogItem *CatalogItem `gorm:"foreignKey:CatalogID" json:"catalog_item,omitempty"`
}

func (Scrobble) TableName() string { return "scrobbles" }

// Collection represents a playlist, backlog, or watchlist.
type Collection struct {
	ID uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	// AccountID participates in the sync-collection unique key so one account
	// cannot end up with two mirrors of the same remote provider and scope.
	AccountID   uuid.UUID `gorm:"type:uuid;not null;index:ix_coll_account;uniqueIndex:ix_coll_sync,priority:1" json:"account_id"`
	Domain      string    `gorm:"type:varchar(32);not null" json:"domain"`
	Kind        string    `gorm:"type:varchar(32);not null" json:"kind"`
	Name        string    `gorm:"type:varchar(512);not null" json:"name"`
	Description *string   `gorm:"type:text" json:"description,omitempty"`
	IsPublic    bool      `gorm:"default:true" json:"is_public"`
	ArtworkURL  *string   `gorm:"type:varchar(4096)" json:"artwork_url,omitempty"`
	// SyncProvider and SyncScope mark a collection as the mirror of one remote
	// provider's list for one domain. NULL keeps ordinary user collections out
	// of the unique key: Postgres treats NULLs as distinct.
	SyncProvider *string        `gorm:"type:varchar(64);uniqueIndex:ix_coll_sync,priority:2" json:"sync_provider,omitempty"`
	SyncScope    *string        `gorm:"type:varchar(64);uniqueIndex:ix_coll_sync,priority:3" json:"sync_scope,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`

	Items []CollectionItem `gorm:"foreignKey:CollectionID" json:"items,omitempty"`
}

func (Collection) TableName() string { return "collections" }

// CollectionItem links a media item to a playlist or watchlist.
type CollectionItem struct {
	ID           uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	CollectionID uuid.UUID  `gorm:"type:uuid;not null;index:ix_coll_item_coll;uniqueIndex:ix_coll_item_remote,priority:1" json:"collection_id"`
	CatalogID    *uuid.UUID `gorm:"type:uuid;index:ix_coll_item_catalog" json:"catalog_id,omitempty"`
	Position     int        `gorm:"not null;default:0" json:"position"`
	Note         *string    `gorm:"type:text" json:"note,omitempty"`
	Rating       *int       `json:"rating,omitempty"`
	Status       *string    `gorm:"type:varchar(32)" json:"status,omitempty"`
	// Progress is the watched/read unit count — episodes for a show, chapters
	// for a book. VolumeProgress is the volume count a book collection tracks
	// separately. Both are nil when the entry has no progress to report.
	Progress       *int `json:"progress,omitempty"`
	VolumeProgress *int `json:"volume_progress,omitempty"`

	// Kind says what the entry is on a favourites collection: a track, an
	// album or a playlist. Empty is a row written before the column existed
	// and reads as a track, the only thing favourites used to hold.
	Kind      string    `gorm:"type:varchar(32);not null;default:''" json:"kind,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// SyncRemoteID ties an item to its counterpart on the provider. The two
	// fingerprints are the last values both sides agreed on, so a later sync
	// can tell a local edit from a remote edit — and an edit on both sides
	// from no edit at all. They are hidden from clients; the values they hash
	// are already in the response.
	SyncRemoteID          *string    `gorm:"type:varchar(128);uniqueIndex:ix_coll_item_remote,priority:2" json:"sync_remote_id,omitempty"`
	SyncLocalFingerprint  *string    `gorm:"type:varchar(64)" json:"-"`
	SyncRemoteFingerprint *string    `gorm:"type:varchar(64)" json:"-"`
	SyncedAt              *time.Time `json:"synced_at,omitempty"`

	CatalogItem *CatalogItem `gorm:"foreignKey:CatalogID" json:"catalog_item,omitempty"`
}

func (CollectionItem) TableName() string { return "collection_items" }

// Review is an account's comment and rating on one catalog item. One review
// per account per item: an update overwrites, it never appends.
type Review struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	AccountID uuid.UUID      `gorm:"type:uuid;not null;uniqueIndex:ix_review_acc_catalog" json:"account_id"`
	CatalogID uuid.UUID      `gorm:"type:uuid;not null;uniqueIndex:ix_review_acc_catalog;index:ix_review_catalog" json:"catalog_id"`
	Rating    *int           `gorm:"type:int" json:"rating,omitempty"`
	Body      *string        `gorm:"type:text" json:"body,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

func (Review) TableName() string { return "reviews" }

// Creator is a person or organisation behind catalog items — an artist,
// author, director, developer, studio, or publisher. Names match per domain,
// so the same name in two domains stays two creators.
type Creator struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	Domain    string         `gorm:"type:varchar(32);not null;uniqueIndex:ix_creator_domain_name" json:"domain"`
	Name      string         `gorm:"type:varchar(512);not null;uniqueIndex:ix_creator_domain_name" json:"name"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

func (Creator) TableName() string { return "creators" }

// CatalogCreator links a catalog item to one of its creators with a role.
type CatalogCreator struct {
	CatalogID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:ix_cat_creator" json:"catalog_id"`
	CreatorID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:ix_cat_creator;index:ix_cat_creator_creator" json:"creator_id"`
	Role      string    `gorm:"type:varchar(32);not null;uniqueIndex:ix_cat_creator" json:"role"`
	Position  int       `gorm:"not null;default:0" json:"position"`
}

func (CatalogCreator) TableName() string { return "catalog_creators" }

// ExternalSyncConnection stores one account's link to a remote tracker as
// Pulsar sees it: remote identity plus local sync preferences. The
// credentials themselves live in Stargate's account_connections and are
// fetched on demand, so no token field belongs here.
type ExternalSyncConnection struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	AccountID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:ix_sync_acc_prov,priority:1" json:"account_id"`
	Provider  string    `gorm:"type:varchar(64);not null;uniqueIndex:ix_sync_acc_prov,priority:2" json:"provider"`
	// RemoteUserID is the provider's stable numeric id. RemoteUsername is the
	// handle collection endpoints address; both are stored because Bangumi
	// lets a user rename the handle once.
	RemoteUserID   string  `gorm:"type:varchar(256);not null" json:"remote_user_id"`
	RemoteUsername *string `gorm:"type:varchar(256)" json:"remote_username,omitempty"`
	SyncEnabled    bool    `gorm:"default:true" json:"sync_enabled"`
	// ConflictPolicy is this account's override: "local", "remote" or
	// "newest". Empty uses the deployment default.
	ConflictPolicy string     `gorm:"type:varchar(16);not null;default:''" json:"conflict_policy,omitempty"`
	LastScannedAt  *time.Time `json:"last_scanned_at,omitempty"`
	LastSyncedAt   *time.Time `json:"last_synced_at,omitempty"`
	LastError      *string    `gorm:"type:text" json:"last_error,omitempty"`
	// Meta carries provider-specific profile facts (nickname, avatar) that do
	// not deserve a column. Never put tokens here.
	Meta      datatypes.JSON `gorm:"type:jsonb" json:"meta,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

func (ExternalSyncConnection) TableName() string { return "sync_connections" }

// ToJSON safely marshals an arbitrary value into datatypes.JSON.
func ToJSON(v any) datatypes.JSON {
	if v == nil {
		return datatypes.JSON([]byte("{}"))
	}
	b, err := json.Marshal(v)
	if err != nil {
		return datatypes.JSON([]byte("{}"))
	}
	return datatypes.JSON(b)
}
