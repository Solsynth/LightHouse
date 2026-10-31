// Package rewind is the ported Passport account-rewind domain: one
// per-account, per-year JSON document that aggregates the year's activity
// across services, optionally exposed under a random sharable code.
//
// The table keeps Passport's `rewind_points` name and columns so an existing
// database can be reused without a copy. The account_check_in_results table
// is owned by the account-events module and is only read here.
package rewind

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// ModuleID is the feature module identifier.
const ModuleID = "app.solian.id.rewind"

// EntityBase mirrors the C# ModelBase.
type EntityBase struct {
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at"`
}

// RewindPointEntity is `rewind_points` (SnRewindPoint).
type RewindPointEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Year          int            `gorm:"column:year;not null"`
	SchemaVersion int            `gorm:"column:schema_version;not null"`
	SharableCode  *string        `gorm:"column:sharable_code;size:4096"`
	Data          datatypes.JSON `gorm:"column:data;type:jsonb;not null"`
	AccountID     uuid.UUID      `gorm:"column:account_id;type:uuid;not null"`
}

func (RewindPointEntity) TableName() string { return "rewind_points" }
