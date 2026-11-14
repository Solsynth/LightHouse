package store

import (
	"time"

	"github.com/google/uuid"
)

// AnalyticsEventEntity is the `analytics_events` table. Rows are append-only;
// the analytics module prunes them on a retention schedule. ClientClass maps to
// model.AnalyticsClientUnknown, model.AnalyticsClientHuman and
// model.AnalyticsClientBot.
type AnalyticsEventEntity struct {
	ID          uuid.UUID  `gorm:"column:id;primaryKey;type:uuid"`
	Kind        string     `gorm:"column:kind;size:64;not null"`
	ResourceID  string     `gorm:"column:resource_id;size:128;not null"`
	Event       string     `gorm:"column:event;size:64;not null"`
	AccountID   *uuid.UUID `gorm:"column:account_id;type:uuid"`
	VisitorID   string     `gorm:"column:visitor_id;size:36"`
	IPHash      string     `gorm:"column:ip_hash;size:64"`
	UserAgent   string     `gorm:"column:user_agent;type:text"`
	ClientClass int        `gorm:"column:client_class;not null;default:0"`
	ClientName  string     `gorm:"column:client_name;size:128"`
	Platform    string     `gorm:"column:platform;size:64"`
	IsMobile    bool       `gorm:"column:is_mobile;not null;default:false"`
	Referrer    string     `gorm:"column:referrer;type:text"`
	CreatedAt   time.Time  `gorm:"column:created_at;index"`
}

// TableName is the append-only event table.
func (AnalyticsEventEntity) TableName() string { return "analytics_events" }
