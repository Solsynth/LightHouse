package model

import (
	"time"

	"github.com/google/uuid"
)

// SyncYomiBlob stores the latest opaque SyncYomi library snapshot per account
// along with its optimistic-concurrency ETag.
type SyncYomiBlob struct {
	AccountID   uuid.UUID `gorm:"type:uuid;primaryKey" json:"account_id"`
	ETag        string    `gorm:"type:varchar(128);not null" json:"etag"`
	ContentType string    `gorm:"type:varchar(128);not null" json:"content_type"`
	Payload     []byte    `gorm:"type:bytea;not null" json:"-"`
	SizeBytes   int64     `gorm:"not null" json:"size_bytes"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (SyncYomiBlob) TableName() string { return "syncyomi_blobs" }
