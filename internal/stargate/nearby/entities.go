package nearby

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// EntityBase mirrors the C# ModelBase.
type EntityBase struct {
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at"`
}

// NearbyDeviceEntity is nearby_devices (SnNearbyDevice).
type NearbyDeviceEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	UserID            uuid.UUID  `gorm:"column:user_id;type:uuid;not null"`
	DeviceID          string     `gorm:"column:device_id;size:256;not null"`
	Discoverable      bool       `gorm:"column:discoverable"`
	FriendOnly        bool       `gorm:"column:friend_only"`
	Capabilities      int        `gorm:"column:capabilities"`
	Status            int        `gorm:"column:status"`
	LastHeartbeatAt   *time.Time `gorm:"column:last_heartbeat_at"`
	LastTokenIssuedAt *time.Time `gorm:"column:last_token_issued_at"`
}

func (NearbyDeviceEntity) TableName() string { return "nearby_devices" }

// NearbyPresenceTokenEntity is nearby_presence_tokens (SnNearbyPresenceToken).
type NearbyPresenceTokenEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	DeviceID     uuid.UUID `gorm:"column:device_id;type:uuid;not null"`
	Slot         int64     `gorm:"column:slot;not null"`
	TokenHash    string    `gorm:"column:token_hash;size:128;not null"`
	ValidFrom    time.Time `gorm:"column:valid_from;not null"`
	ValidTo      time.Time `gorm:"column:valid_to;not null"`
	Discoverable bool      `gorm:"column:discoverable"`
	FriendOnly   bool      `gorm:"column:friend_only"`
	Capabilities int       `gorm:"column:capabilities"`
}

func (NearbyPresenceTokenEntity) TableName() string { return "nearby_presence_tokens" }
