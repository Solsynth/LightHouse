// Package passkit is the ported Passport Apple Wallet domain: pass records,
// device registrations and .pkpass generation (pass.json + manifest + detached
// PKCS#7 signature).
//
// Entities keep the legacy Passport table and column names, so an existing
// dyson_passport database can be pointed at this module without a data copy.
package passkit

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ModuleID is the feature module identifier.
const ModuleID = "app.solian.id.passkit"

// EntityBase mirrors the C# ModelBase.
type EntityBase struct {
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

// PassEntity is `apple_passes` (SnApplePass).
type PassEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	AccountID           uuid.UUID `gorm:"column:account_id;type:uuid;not null"`
	PassTypeIdentifier  string    `gorm:"column:pass_type_identifier;size:255;not null"`
	SerialNumber        string    `gorm:"column:serial_number;size:255;not null"`
	AuthenticationToken string    `gorm:"column:authentication_token;size:255;not null"`
	LastUpdatedTag      string    `gorm:"column:last_updated_tag;size:128;not null"`

	Registrations []RegistrationEntity `gorm:"foreignKey:PassID;references:ID"`
}

func (PassEntity) TableName() string { return "apple_passes" }

// RegistrationEntity is `apple_pass_registrations` (SnApplePassRegistration).
type RegistrationEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	PassID                  uuid.UUID `gorm:"column:pass_id;type:uuid;not null"`
	DeviceLibraryIdentifier string    `gorm:"column:device_library_identifier;size:255;not null"`
	PushToken               string    `gorm:"column:push_token;size:512;not null"`
}

func (RegistrationEntity) TableName() string { return "apple_pass_registrations" }
