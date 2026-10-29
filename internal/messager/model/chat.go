// Package model defines the Messager wire types. All JSON is snake_case; enums
// serialize as integers; times are UTC RFC3339 via model.Time. Fields without
// omitempty are always emitted (null when nil), matching System.Text.Json.
package model

import (
	"time"

	models "src.solsynth.dev/sosys/go/pkg/models"
)

// Time is the fleet Instant type (UTC RFC3339, seconds precision).
type Time = models.Time

// NewTime converts a time.Time into a *Time (nil for the zero value).
func NewTime(t time.Time) *Time { return models.NewTime(t) }

// CloudFileReferenceObject is the jsonb file-cache shape shared with Drive.
type CloudFileReferenceObject = models.SnCloudFileReferenceObject

// ChatRoomType mirrors the C# enum: Group 0, DirectMessage 1.
type ChatRoomType int

const (
	ChatRoomGroup         ChatRoomType = 0
	ChatRoomDirectMessage ChatRoomType = 1
)

// ChatRoomEncryptionMode mirrors the C# enum: None 0, E2eeMls 3.
type ChatRoomEncryptionMode int

const (
	ChatEncryptionNone    ChatRoomEncryptionMode = 0
	ChatEncryptionE2eeMls ChatRoomEncryptionMode = 3
)

// ChatMemberNotify mirrors the C# enum: All 0, Mentions 1, None 2.
type ChatMemberNotify int

const (
	ChatNotifyAll      ChatMemberNotify = 0
	ChatNotifyMentions ChatMemberNotify = 1
	ChatNotifyNone     ChatMemberNotify = 2
)

// ChatTimeoutType mirrors the C# enum: ByModerator 0, BySlowMode 1.
type ChatTimeoutType int

const (
	ChatTimeoutModerator ChatTimeoutType = 0
	ChatTimeoutSlowMode  ChatTimeoutType = 1
)

// ChatReactionAttitude mirrors the C# enum: Positive 0, Neutral 1, Negative 2.
type ChatReactionAttitude int

// ChatRoom is the wire shape of a chat room.
type ChatRoom struct {
	ID                   string       `json:"id"`
	Name                 *string      `json:"name"`
	Description          *string      `json:"description"`
	Slug                 *string      `json:"slug"`
	Type                 int          `json:"type"`
	IsCommunity          bool         `json:"is_community"`
	IsPublic             bool         `json:"is_public"`
	IsReadReceiptsPublic bool         `json:"is_read_receipts_public"`
	EncryptionMode       int          `json:"encryption_mode"`
	MLSGroupID           *string      `json:"mls_group_id"`
	E2eePolicy           any          `json:"e2ee_policy"`
	Picture              any          `json:"picture"`
	Background           any          `json:"background"`
	AccountID            *string      `json:"account_id"`
	RealmID              *string      `json:"realm_id"`
	Account              any          `json:"account"`
	Realm                any          `json:"realm"`
	Members              []ChatMember `json:"members"`
	CreatedAt            *Time        `json:"created_at"`
	UpdatedAt            *Time        `json:"updated_at"`
	DeletedAt            *Time        `json:"deleted_at"`
}

// ChatMember is one room membership.
type ChatMember struct {
	ID                    string     `json:"id"`
	ChatRoomID            string     `json:"chat_room_id"`
	AccountID             string     `json:"account_id"`
	Username              *string    `json:"username"`
	Nick                  *string    `json:"nick"`
	ChatGroupID           *string    `json:"chat_group_id"`
	Notify                int        `json:"notify"`
	LastReadAt            *Time      `json:"last_read_at"`
	JoinedAt              *Time      `json:"joined_at"`
	LeaveAt               *Time      `json:"leave_at"`
	ConfirmedAt           *Time      `json:"confirmed_at"`
	InvitedByID           *string    `json:"invited_by_id"`
	BreakUntil            *Time      `json:"break_until"`
	TimeoutUntil          *Time      `json:"timeout_until"`
	TimeoutCause          any        `json:"timeout_cause"`
	Account               any        `json:"account"`
	Status                any        `json:"status"`
	ChatGroup             *ChatGroup `json:"chat_group"`
	RealmNick             *string    `json:"realm_nick"`
	RealmBio              *string    `json:"realm_bio"`
	RealmExperience       *int       `json:"realm_experience"`
	RealmLevel            *int       `json:"realm_level"`
	RealmLevelingProgress *float64   `json:"realm_leveling_progress"`
	RealmLabel            *string    `json:"realm_label"`
	CreatedAt             *Time      `json:"created_at"`
	UpdatedAt             *Time      `json:"updated_at"`
	DeletedAt             *Time      `json:"deleted_at"`
}

// ChatGroup is one account's room folder.
type ChatGroup struct {
	ID        string   `json:"id"`
	AccountID string   `json:"account_id"`
	Name      string   `json:"name"`
	Color     *string  `json:"color"`
	Icon      *string  `json:"icon"`
	Order     int      `json:"order"`
	RoomIDs   []string `json:"room_ids"`
	CreatedAt *Time    `json:"created_at"`
	UpdatedAt *Time    `json:"updated_at"`
	DeletedAt *Time    `json:"deleted_at"`
}

// ChatEncryptionMeta is the E2EE envelope stored beside a message.
type ChatEncryptionMeta struct {
	Ciphertext []byte `json:"ciphertext"`
	Header     []byte `json:"header,omitempty"`
	Signature  []byte `json:"signature,omitempty"`
	Scheme     string `json:"scheme"`
	Epoch      *int64 `json:"epoch,omitempty"`
}

// ChatMessage is the wire shape of a message.
type ChatMessage struct {
	ID                 string                     `json:"id"`
	RoomSequence       int64                      `json:"room_sequence"`
	Type               string                     `json:"type"`
	Content            *string                    `json:"content"`
	EncryptionMeta     *ChatEncryptionMeta        `json:"encryption_meta"`
	ClientMessageID    *string                    `json:"client_message_id"`
	Nonce              *string                    `json:"nonce"`
	Meta               any                        `json:"meta"`
	MembersMentioned   []string                   `json:"members_mentioned"`
	EditedAt           *Time                      `json:"edited_at"`
	Attachments        []CloudFileReferenceObject `json:"attachments"`
	ReactionsCount     map[string]int             `json:"reactions_count"`
	ReactionsMade      map[string]bool            `json:"reactions_made"`
	RepliedMessageID   *string                    `json:"replied_message_id"`
	ThreadID           *string                    `json:"thread_id"`
	ThreadRootID       *string                    `json:"thread_root_id"`
	ForwardedMessageID *string                    `json:"forwarded_message_id"`
	SenderID           string                     `json:"sender_id"`
	ChatRoomID         string                     `json:"chat_room_id"`
	Sender             *ChatMember                `json:"sender"`
	Reactions          []ChatReaction             `json:"reactions"`
	IsThreadRoot       bool                       `json:"is_thread_root"`
	ThreadRepliesCount int                        `json:"thread_replies_count"`
	ThreadReplies      []ThreadReplyNode          `json:"thread_replies"`
	IsEncrypted        bool                       `json:"is_encrypted"`
	CreatedAt          *Time                      `json:"created_at"`
	UpdatedAt          *Time                      `json:"updated_at"`
	DeletedAt          *Time                      `json:"deleted_at"`
}

// ThreadReplyNode is one flattened thread reply.
type ThreadReplyNode struct {
	Message ChatMessage `json:"message"`
	Depth   int         `json:"depth"`
}

// ChatReaction is one reaction on a message.
type ChatReaction struct {
	ID        string      `json:"id"`
	MessageID string      `json:"message_id"`
	SenderID  string      `json:"sender_id"`
	Symbol    string      `json:"symbol"`
	Attitude  int         `json:"attitude"`
	Sender    *ChatMember `json:"sender"`
	CreatedAt *Time       `json:"created_at"`
	UpdatedAt *Time       `json:"updated_at"`
	DeletedAt *Time       `json:"deleted_at"`
}

// ChatMessagePin is one pinned message.
type ChatMessagePin struct {
	ID               string       `json:"id"`
	MessageID        string       `json:"message_id"`
	ChatRoomID       string       `json:"chat_room_id"`
	PinnedByMemberID string       `json:"pinned_by_member_id"`
	ExpiresAt        *Time        `json:"expires_at"`
	Message          *ChatMessage `json:"message"`
	PinnedBy         *ChatMember  `json:"pinned_by"`
	CreatedAt        *Time        `json:"created_at"`
	UpdatedAt        *Time        `json:"updated_at"`
	DeletedAt        *Time        `json:"deleted_at"`
}

// ChatRoomCounter is the per-room sequence allocator.
type ChatRoomCounter struct {
	ChatRoomID   string `gorm:"column:chat_room_id;primaryKey" json:"chat_room_id"`
	LastSequence int64  `gorm:"column:last_sequence" json:"last_sequence"`
}

// RealtimeCall is one voice/video call session.
type RealtimeCall struct {
	ID           string      `json:"id"`
	EndedAt      *Time       `json:"ended_at"`
	SenderID     string      `json:"sender_id"`
	Sender       *ChatMember `json:"sender"`
	RoomID       string      `json:"room_id"`
	Room         *ChatRoom   `json:"room"`
	ProviderName *string     `json:"provider_name"`
	SessionID    *string     `json:"session_id"`
	Upstream     any         `json:"upstream_config"`
	CreatedAt    *Time       `json:"created_at"`
	UpdatedAt    *Time       `json:"updated_at"`
	DeletedAt    *Time       `json:"deleted_at"`
}

// ChatVoiceClip is one uploaded voice message.
type ChatVoiceClip struct {
	ID               string `json:"id"`
	ChatRoomID       string `json:"chat_room_id"`
	SenderID         string `json:"sender_id"`
	MimeType         string `json:"mime_type"`
	StoragePath      string `json:"storage_path"`
	OriginalFileName string `json:"original_file_name"`
	Size             int64  `json:"size"`
	DurationMS       *int   `json:"duration_ms"`
	CreatedAt        *Time  `json:"created_at"`
	UpdatedAt        *Time  `json:"updated_at"`
	DeletedAt        *Time  `json:"deleted_at"`
}

// ChatRoomSummary is one unread summary row.
type ChatRoomSummary struct {
	UnreadCount int          `json:"unread_count"`
	HasUnread   bool         `json:"has_unread"`
	LastMessage *ChatMessage `json:"last_message"`
}

// DeviceToken is one push/websocket device of a member.
type DeviceToken struct {
	DeviceToken          string `json:"device_token"`
	ExpiresAt            *Time  `json:"expires_at"`
	IsWebsocketConnected bool   `json:"is_websocket_connected,omitempty"`
}
