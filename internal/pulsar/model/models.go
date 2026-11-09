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

// Collection item statuses.
const (
	StatusPlanToWatch = "plan_to_watch"
	StatusWatching    = "watching"
	StatusCompleted   = "completed"
	StatusOnHold      = "on_hold"
	StatusDropped     = "dropped"
)

// CatalogItem is a canonical entry across media domains (track, game, anime, video, book).
type CatalogItem struct {
	ID              uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	Domain          string         `gorm:"type:varchar(32);not null;index:ix_pulsar_catalog_domain" json:"domain"`
	Provider        string         `gorm:"type:varchar(64);index:ix_pulsar_catalog_prov" json:"provider"`
	ProviderKey     string         `gorm:"type:varchar(512);index:ix_pulsar_catalog_provkey" json:"provider_key,omitempty"`
	Title           string         `gorm:"type:varchar(1024);not null" json:"title"`
	Subtitle        *string        `gorm:"type:varchar(1024)" json:"subtitle,omitempty"`
	Caption         *string        `gorm:"type:varchar(2048)" json:"caption,omitempty"`
	ArtworkHash     *string        `gorm:"type:varchar(128)" json:"artwork_hash,omitempty"`
	ArtworkURL      *string        `gorm:"type:varchar(4096)" json:"artwork_url,omitempty"`
	DurationSeconds int            `gorm:"default:0" json:"duration_seconds"`
	ExternalIDs     datatypes.JSON `gorm:"type:jsonb" json:"external_ids,omitempty"`
	Metadata        datatypes.JSON `gorm:"type:jsonb" json:"metadata,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `gorm:"index" json:"-"`
}

func (CatalogItem) TableName() string { return "pulsar_catalog_items" }

// ActiveSession represents real-time now-playing or live gaming/media playback.
type ActiveSession struct {
	ID              uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	AccountID       uuid.UUID      `gorm:"type:uuid;not null;uniqueIndex:ix_pulsar_sess_acc_man" json:"account_id"`
	CatalogID       *uuid.UUID     `gorm:"type:uuid;index:ix_pulsar_session_catalog" json:"catalog_id,omitempty"`
	Domain          string         `gorm:"type:varchar(32);not null" json:"domain"`
	ManualID        string         `gorm:"type:varchar(256);not null;uniqueIndex:ix_pulsar_sess_acc_man" json:"manual_id"`
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
	LeaseExpiresAt  time.Time      `gorm:"index:ix_pulsar_session_lease" json:"lease_expires_at"`
	StartedAt       time.Time      `json:"started_at"`
	EndedAt         *time.Time     `json:"ended_at,omitempty"`
	Meta            datatypes.JSON `gorm:"type:jsonb" json:"meta,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`

	CatalogItem *CatalogItem `gorm:"foreignKey:CatalogID" json:"catalog_item,omitempty"`
}

func (ActiveSession) TableName() string { return "pulsar_active_sessions" }

// Scrobble records a finalized media listening, watching, or gaming playback event.
type Scrobble struct {
	ID              uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	AccountID       uuid.UUID      `gorm:"type:uuid;not null;index:ix_pulsar_scrobble_account_played,priority:1" json:"account_id"`
	CatalogID       *uuid.UUID     `gorm:"type:uuid;index:ix_pulsar_scrobble_catalog" json:"catalog_id,omitempty"`
	Domain          string         `gorm:"type:varchar(32);not null;index:ix_pulsar_scrobble_domain" json:"domain"`
	ClientName      string         `gorm:"type:varchar(128);not null" json:"client_name"`
	Title           string         `gorm:"type:varchar(1024);not null" json:"title"`
	Subtitle        *string        `gorm:"type:varchar(1024)" json:"subtitle,omitempty"`
	Caption         *string        `gorm:"type:varchar(2048)" json:"caption,omitempty"`
	ArtworkURL      *string        `gorm:"type:varchar(4096)" json:"artwork_url,omitempty"`
	ListenedSeconds int            `gorm:"default:0" json:"listened_seconds"`
	DurationSeconds int            `gorm:"default:0" json:"duration_seconds"`
	PlayedAt        time.Time      `gorm:"not null;index:ix_pulsar_scrobble_account_played,priority:2;index:ix_pulsar_scrobble_played" json:"played_at"`
	IsImported      bool           `gorm:"default:false" json:"is_imported"`
	Meta            datatypes.JSON `gorm:"type:jsonb" json:"meta,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`

	CatalogItem *CatalogItem `gorm:"foreignKey:CatalogID" json:"catalog_item,omitempty"`
}

func (Scrobble) TableName() string { return "pulsar_scrobbles" }

// Collection represents a playlist, backlog, or watchlist.
type Collection struct {
	ID          uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	AccountID   uuid.UUID      `gorm:"type:uuid;not null;index:ix_pulsar_coll_account" json:"account_id"`
	Domain      string         `gorm:"type:varchar(32);not null" json:"domain"`
	Kind        string         `gorm:"type:varchar(32);not null" json:"kind"`
	Name        string         `gorm:"type:varchar(512);not null" json:"name"`
	Description *string        `gorm:"type:text" json:"description,omitempty"`
	IsPublic    bool           `gorm:"default:true" json:"is_public"`
	ArtworkURL  *string        `gorm:"type:varchar(4096)" json:"artwork_url,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`

	Items []CollectionItem `gorm:"foreignKey:CollectionID" json:"items,omitempty"`
}

func (Collection) TableName() string { return "pulsar_collections" }

// CollectionItem links a media item to a playlist or watchlist.
type CollectionItem struct {
	ID           uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	CollectionID uuid.UUID      `gorm:"type:uuid;not null;index:ix_pulsar_coll_item_coll" json:"collection_id"`
	CatalogID    *uuid.UUID     `gorm:"type:uuid;index:ix_pulsar_coll_item_catalog" json:"catalog_id,omitempty"`
	Position     int            `gorm:"not null;default:0" json:"position"`
	Note         *string        `gorm:"type:text" json:"note,omitempty"`
	Rating       *int           `json:"rating,omitempty"`
	Status       *string        `gorm:"type:varchar(32)" json:"status,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`

	CatalogItem *CatalogItem `gorm:"foreignKey:CatalogID" json:"catalog_item,omitempty"`
}

func (CollectionItem) TableName() string { return "pulsar_collection_items" }

// ExternalSyncConnection configures background syncing with Last.fm, Steam, Spotify, AniList, etc.
type ExternalSyncConnection struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	AccountID      uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:ix_pulsar_sync_acc_prov" json:"account_id"`
	Provider       string     `gorm:"type:varchar(64);not null;uniqueIndex:ix_pulsar_sync_acc_prov" json:"provider"`
	RemoteUserID   string     `gorm:"type:varchar(256);not null" json:"remote_user_id"`
	AccessToken    *string    `gorm:"type:text" json:"-"`
	RefreshToken   *string    `gorm:"type:text" json:"-"`
	TokenExpiresAt *time.Time `json:"token_expires_at,omitempty"`
	SyncEnabled    bool       `gorm:"default:true" json:"sync_enabled"`
	LastScannedAt  *time.Time `json:"last_scanned_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func (ExternalSyncConnection) TableName() string { return "pulsar_sync_connections" }

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

