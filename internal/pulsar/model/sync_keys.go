package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// SyncAPIKey is a per-account, revocable opaque API key used by external sync
// clients (e.g. SyncYomi, Komga/Kavita-compatible layers, webhooks) that cannot
// perform interactive Stargate OAuth flows. Only the SHA-256 hash is stored.
type SyncAPIKey struct {
	ID         uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	AccountID  uuid.UUID      `gorm:"type:uuid;not null;index:ix_sync_api_keys_account" json:"account_id"`
	Name       string         `gorm:"type:varchar(128);not null" json:"name"`
	KeyHash    string         `gorm:"type:varchar(64);uniqueIndex:ux_sync_api_keys_hash;not null" json:"-"`
	KeyPrefix  string         `gorm:"type:varchar(16);not null" json:"key_prefix"`
	Scopes     datatypes.JSON `gorm:"type:jsonb" json:"scopes,omitempty"`
	LastUsedAt *time.Time     `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time     `json:"revoked_at,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

func (SyncAPIKey) TableName() string { return "sync_api_keys" }
