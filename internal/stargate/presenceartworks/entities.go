// Package presenceartworks is the ported Passport presence-artwork domain:
// content-addressed image storage for presence activity artwork, backed by an
// S3/MinIO bucket and a small metadata table.
//
// Entities keep the legacy Passport table and column names, so an existing
// Passport database can be pointed at this module without a data copy.
package presenceartworks

import (
	"time"

	"gorm.io/gorm"
)

// ModuleID is the feature module identifier.
const ModuleID = "app.solian.id.presenceartworks"

// EntityBase mirrors the C# ModelBase.
type EntityBase struct {
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at"`
}

// PresenceArtworkEntity is `presence_artworks` (SnPresenceArtwork). The hash
// is the primary key, so an identical upload is a metadata touch, never a
// second row.
type PresenceArtworkEntity struct {
	Hash string `gorm:"column:hash;primaryKey;size:71"`
	EntityBase
	MimeType         string    `gorm:"column:mime_type;size:255;not null"`
	Size             int64     `gorm:"column:size"`
	StoragePath      string    `gorm:"column:storage_path;size:1024;not null"`
	LastReferencedAt time.Time `gorm:"column:last_referenced_at;not null"`
}

func (PresenceArtworkEntity) TableName() string { return "presence_artworks" }
