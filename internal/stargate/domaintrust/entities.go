// Package domaintrust is the ported Passport domain-trust domain: block/allow
// rules matched against URL hosts and the validation metrics those checks
// produce.
//
// Entities keep the legacy Passport table and column names, so an existing
// dyson_passport database can be pointed at this module without a data copy.
package domaintrust

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ModuleID is the feature module identifier.
const ModuleID = "app.solian.id.domaintrust"

// EntityBase mirrors the C# ModelBase.
type EntityBase struct {
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at"`
}

// TrustLevel is DomainTrustLevel: blocked, neutral, verified.
type TrustLevel int

const (
	TrustBlocked  TrustLevel = 0
	TrustNeutral  TrustLevel = 1
	TrustVerified TrustLevel = 2
)

// DomainBlockEntity is `domain_blocks` (SnDomainBlock).
type DomainBlockEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	DomainPattern      string     `gorm:"column:domain_pattern;size:512;not null"`
	Protocol           *string    `gorm:"column:protocol;size:16"`
	PortRestriction    *int       `gorm:"column:port_restriction"`
	Reason             *string    `gorm:"column:reason;size:256"`
	Priority           int        `gorm:"column:priority"`
	TrustLevel         int        `gorm:"column:trust_level"`
	IsActive           bool       `gorm:"column:is_active"`
	CreatedByAccountID *uuid.UUID `gorm:"column:created_by_account_id;type:uuid"`
}

func (DomainBlockEntity) TableName() string { return "domain_blocks" }

// DomainValidationMetricEntity is `domain_validation_metrics`
// (SnDomainValidationMetric). The domain is unique.
type DomainValidationMetricEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Domain        string     `gorm:"column:domain;size:512;not null"`
	CheckCount    int64      `gorm:"column:check_count"`
	BlockedCount  int64      `gorm:"column:blocked_count"`
	VerifiedCount int64      `gorm:"column:verified_count"`
	LastCheckedAt *time.Time `gorm:"column:last_checked_at"`
}

func (DomainValidationMetricEntity) TableName() string { return "domain_validation_metrics" }
