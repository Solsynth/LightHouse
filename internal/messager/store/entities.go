package store

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// EntityBase is embedded by every soft-deletable Messager entity.
type EntityBase struct {
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

// ChatRoomEntity is the `chat_rooms` table.
type ChatRoomEntity struct {
	ID                   uuid.UUID      `gorm:"column:id;primaryKey"`
	EntityBase
	Name                 *string        `gorm:"column:name"`
	Description          *string        `gorm:"column:description"`
	Slug                 *string        `gorm:"column:slug"`
	Type                 int            `gorm:"column:type"`
	IsCommunity          bool           `gorm:"column:is_community"`
	IsPublic             bool           `gorm:"column:is_public"`
	IsReadReceiptsPublic bool           `gorm:"column:is_read_receipts_public"`
	EncryptionMode       int            `gorm:"column:encryption_mode"`
	MLSGroupID           *string        `gorm:"column:mls_group_id"`
	E2eePolicy           datatypes.JSON `gorm:"column:e2ee_policy;type:jsonb"`
	Picture              datatypes.JSON `gorm:"column:picture;type:jsonb"`
	Background           datatypes.JSON `gorm:"column:background;type:jsonb"`
	AccountID            *uuid.UUID     `gorm:"column:account_id"`
	RealmID              *uuid.UUID     `gorm:"column:realm_id"`
}

func (ChatRoomEntity) TableName() string { return "chat_rooms" }

// ChatMemberEntity is the `chat_members` table.
type ChatMemberEntity struct {
	ID           uuid.UUID      `gorm:"column:id;primaryKey"`
	EntityBase
	ChatRoomID   uuid.UUID      `gorm:"column:chat_room_id"`
	AccountID    uuid.UUID      `gorm:"column:account_id"`
	Username     *string        `gorm:"column:username"`
	Nick         *string        `gorm:"column:nick"`
	ChatGroupID  *uuid.UUID     `gorm:"column:chat_group_id"`
	Notify       int            `gorm:"column:notify"`
	LastReadAt   *time.Time     `gorm:"column:last_read_at"`
	JoinedAt     *time.Time     `gorm:"column:joined_at"`
	LeaveAt      *time.Time     `gorm:"column:leave_at"`
	ConfirmedAt  *time.Time     `gorm:"column:confirmed_at"`
	InvitedByID  *uuid.UUID     `gorm:"column:invited_by_id"`
	BreakUntil   *time.Time     `gorm:"column:break_until"`
	TimeoutUntil *time.Time     `gorm:"column:timeout_until"`
	TimeoutCause datatypes.JSON `gorm:"column:timeout_cause;type:jsonb"`
}

func (ChatMemberEntity) TableName() string { return "chat_members" }

// ChatGroupEntity is the `chat_groups` table.
type ChatGroupEntity struct {
	ID        uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AccountID uuid.UUID `gorm:"column:account_id"`
	Name      string    `gorm:"column:name"`
	Color     *string   `gorm:"column:color"`
	Icon      *string   `gorm:"column:icon"`
	Order     int       `gorm:"column:order"`
}

func (ChatGroupEntity) TableName() string { return "chat_groups" }

// ChatMessageEntity is the `chat_messages` table.
type ChatMessageEntity struct {
	ID                 uuid.UUID      `gorm:"column:id;primaryKey"`
	EntityBase
	RoomSequence       int64          `gorm:"column:room_sequence"`
	Type               string         `gorm:"column:type"`
	Content            *string        `gorm:"column:content"`
	EncryptionMeta     datatypes.JSON `gorm:"column:encryption_meta;type:jsonb"`
	ClientMessageID    *string        `gorm:"column:client_message_id"`
	Meta               datatypes.JSON `gorm:"column:meta;type:jsonb"`
	MembersMentioned   datatypes.JSON `gorm:"column:members_mentioned;type:jsonb"`
	EditedAt           *time.Time     `gorm:"column:edited_at"`
	Attachments        datatypes.JSON `gorm:"column:attachments;type:jsonb"`
	ReactionsCount     datatypes.JSON `gorm:"column:reactions_count;type:jsonb"`
	RepliedMessageID   *uuid.UUID     `gorm:"column:replied_message_id"`
	ThreadID           *uuid.UUID     `gorm:"column:thread_id"`
	ThreadRootID       *uuid.UUID     `gorm:"column:thread_root_id"`
	ForwardedMessageID *uuid.UUID     `gorm:"column:forwarded_message_id"`
	SenderID           uuid.UUID      `gorm:"column:sender_id"`
	ChatRoomID         uuid.UUID      `gorm:"column:chat_room_id"`
}

func (ChatMessageEntity) TableName() string { return "chat_messages" }

// ChatReactionEntity is the `chat_reactions` table.
type ChatReactionEntity struct {
	ID        uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	MessageID uuid.UUID `gorm:"column:message_id"`
	SenderID  uuid.UUID `gorm:"column:sender_id"`
	Symbol    string    `gorm:"column:symbol"`
	Attitude  int       `gorm:"column:attitude"`
}

func (ChatReactionEntity) TableName() string { return "chat_reactions" }

// ChatMessagePinEntity is the `chat_message_pins` table.
type ChatMessagePinEntity struct {
	ID               uuid.UUID  `gorm:"column:id;primaryKey"`
	EntityBase
	MessageID        uuid.UUID  `gorm:"column:message_id"`
	ChatRoomID       uuid.UUID  `gorm:"column:chat_room_id"`
	PinnedByMemberID uuid.UUID  `gorm:"column:pinned_by_member_id"`
	ExpiresAt        *time.Time `gorm:"column:expires_at"`
}

func (ChatMessagePinEntity) TableName() string { return "chat_message_pins" }

// ChatRoomCounterEntity is the `chat_room_counters` table.
type ChatRoomCounterEntity struct {
	ChatRoomID   uuid.UUID `gorm:"column:chat_room_id;primaryKey"`
	LastSequence int64     `gorm:"column:last_sequence"`
}

func (ChatRoomCounterEntity) TableName() string { return "chat_room_counters" }

// RealtimeCallEntity is the `chat_realtime_call` table.
type RealtimeCallEntity struct {
	ID           uuid.UUID  `gorm:"column:id;primaryKey"`
	EntityBase
	EndedAt      *time.Time `gorm:"column:ended_at"`
	SenderID     uuid.UUID  `gorm:"column:sender_id"`
	RoomID       uuid.UUID  `gorm:"column:room_id"`
	ProviderName *string    `gorm:"column:provider_name"`
	SessionID    *string    `gorm:"column:session_id"`
	Upstream     *string    `gorm:"column:upstream;type:jsonb"`
}

func (RealtimeCallEntity) TableName() string { return "chat_realtime_call" }

// ChatVoiceClipEntity is the `chat_voice_clips` table.
type ChatVoiceClipEntity struct {
	ID               uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	ChatRoomID       uuid.UUID `gorm:"column:chat_room_id"`
	SenderID         uuid.UUID `gorm:"column:sender_id"`
	MimeType         string    `gorm:"column:mime_type"`
	StoragePath      string    `gorm:"column:storage_path"`
	OriginalFileName string    `gorm:"column:original_file_name"`
	Size             int64     `gorm:"column:size"`
	DurationMS       *int      `gorm:"column:duration_ms"`
}

func (ChatVoiceClipEntity) TableName() string { return "chat_voice_clips" }
