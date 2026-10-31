// Package nfc is the ported Passport NFC domain: NTAG 424 DNA SDM validation,
// tag registration/claiming and the tag-to-account resolution used by the
// profile scan flow and by NFC login (through the nfcTokens port).
//
// Entities keep the legacy Passport table and column names, so an existing
// dyson_passport database can be pointed at this module without a data copy.
package nfc

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// ModuleID is the feature module identifier.
const ModuleID = "app.solian.id.nfc"

// EntityBase mirrors the C# ModelBase.
type EntityBase struct {
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

// TagEntity is `nfc_tags` (SnNfcTag).
//
// Columns follow SnNfcTag exactly: uid (unique), nullable account_id, label,
// is_active, locked_at, last_seen_at, is_encrypted, sun_key (bytea) and the
// replay-protection counter. Meta is a module-owned jsonb extension column for
// forward-compatible tag metadata; Passport never populated it, so a legacy
// row simply carries NULL.
type TagEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	UID         string         `gorm:"column:uid;size:64;not null"`
	AccountID   *uuid.UUID     `gorm:"column:account_id;type:uuid"`
	Label       *string        `gorm:"column:label;size:64"`
	IsActive    bool           `gorm:"column:is_active;not null;default:true"`
	LockedAt    *time.Time     `gorm:"column:locked_at"`
	LastSeenAt  *time.Time     `gorm:"column:last_seen_at"`
	IsEncrypted bool           `gorm:"column:is_encrypted;not null;default:false"`
	SunKey      []byte         `gorm:"column:sun_key;type:bytea"`
	Counter     *int           `gorm:"column:counter"`
	Meta        datatypes.JSON `gorm:"column:meta;type:jsonb"`
}

func (TagEntity) TableName() string { return "nfc_tags" }
