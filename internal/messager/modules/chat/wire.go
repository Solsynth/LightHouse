package chat

import "src.solsynth.dev/sosys/lighthouse/internal/messager/model"

// sendMessageRequest mirrors ChatController.SendMessageRequest.
type sendMessageRequest struct {
	Content            *string                   `json:"content"`
	Nonce              *string                   `json:"nonce"`
	ClientMessageID    *string                   `json:"client_message_id"`
	FundID             *string                   `json:"fund_id"`
	SurveyID           *string                   `json:"survey_id"`
	MeetID             *string                   `json:"meet_id"`
	NotableDayID       *string                   `json:"notable_day_id"`
	CalendarEventID    *string                   `json:"calendar_event_id"`
	LocationName       *string                   `json:"location_name"`
	LocationAddress    *string                   `json:"location_address"`
	LocationWKT        *string                   `json:"location_wkt"`
	AttachmentsID      []string                  `json:"attachments_id"`
	Embeds             []map[string]any          `json:"embeds"`
	Meta               map[string]any            `json:"meta"`
	RepliedMessageID   *string                   `json:"replied_message_id"`
	ForwardedMessageID *string                   `json:"forwarded_message_id"`
	EncryptionMeta     *model.ChatEncryptionMeta `json:"encryption_meta"`
	IsThreadRoot       bool                      `json:"is_thread_root"`
	ThreadID           *string                   `json:"thread_id"`
}

func (r sendMessageRequest) clientID() *string {
	if r.ClientMessageID != nil && *r.ClientMessageID != "" {
		return r.ClientMessageID
	}
	return r.Nonce
}

type deleteMessageRequest struct {
	Nonce           *string                   `json:"nonce"`
	ClientMessageID *string                   `json:"client_message_id"`
	EncryptionMeta  *model.ChatEncryptionMeta `json:"encryption_meta"`
}

type messageReactionRequest struct {
	Symbol   string `json:"symbol"`
	Attitude int    `json:"attitude"`
}

type threadReplyListResponse struct {
	Root    model.ChatMessage       `json:"root"`
	Replies []model.ThreadReplyNode `json:"replies"`
}

type syncRequest struct {
	LastSyncTimestamp     int64                      `json:"last_sync_timestamp"`
	LastSyncMessageID     *string                    `json:"last_sync_message_id"`
	MissingSequences      []int64                    `json:"missing_sequences"`
	MissingSequenceRanges []syncSequenceRangeRequest `json:"missing_sequence_ranges"`
}

type syncSequenceRangeRequest struct {
	StartSequence int64 `json:"start_sequence"`
	EndSequence   int64 `json:"end_sequence"`
}

type syncResponse struct {
	Messages         []model.ChatMessage `json:"messages"`
	CurrentTimestamp *model.Time         `json:"current_timestamp"`
	TotalCount       int                 `json:"total_count"`
}

type globalSyncResponse struct {
	Messages         []model.ChatMessage `json:"messages"`
	CurrentTimestamp *model.Time         `json:"current_timestamp"`
	CurrentMessageID *string             `json:"current_message_id"`
	TotalCount       int                 `json:"total_count"`
}

type autocompletion struct {
	Type    string `json:"type"`
	Keyword string `json:"keyword"`
	Data    any    `json:"data"`
}

type deviceSubscription struct {
	DeviceToken string     `json:"device_token"`
	ExpiresAt   *model.Time `json:"expires_at"`
}

type roomSubscriptionEntry struct {
	RoomID    string               `json:"room_id"`
	MemberID  string               `json:"member_id"`
	AccountID string               `json:"account_id"`
	Member    model.ChatMember     `json:"member"`
	Devices   []deviceSubscription `json:"devices"`
}

type accountSubscriptionEntry struct {
	RoomID   string               `json:"room_id"`
	MemberID string               `json:"member_id"`
	Room     model.ChatRoom       `json:"room"`
	Devices  []deviceSubscription `json:"devices"`
}

type subscriptionDeviceStatus struct {
	DeviceToken          string     `json:"device_token"`
	ExpiresAt            *model.Time `json:"expires_at"`
	IsWebSocketConnected bool       `json:"is_web_socket_connected"`
}

type subscriptionRoomStatus struct {
	RoomID                    string                      `json:"room_id"`
	MemberID                  string                      `json:"member_id"`
	Room                      model.ChatRoom              `json:"room"`
	IsSubscribed              bool                        `json:"is_subscribed"`
	PushNotificationsSuppressed bool                      `json:"push_notifications_suppressed"`
	Devices                   []subscriptionDeviceStatus  `json:"devices"`
}

type accountStatusResponse struct {
	AccountID                              string                   `json:"account_id"`
	HasActiveSubscriptions                 bool                     `json:"has_active_subscriptions"`
	HasAnyWebSocketConnection              bool                     `json:"has_any_web_socket_connection"`
	PushNotificationsMaySendForUnsubscribed bool                   `json:"push_notifications_may_send_for_unsubscribed_rooms"`
	Subscriptions                          []subscriptionRoomStatus `json:"subscriptions"`
}

type messageSearchRoomResponse struct {
	Room     model.ChatRoom      `json:"room"`
	Messages []model.ChatMessage `json:"messages"`
}

type roomSyncChange struct {
	RoomID    string             `json:"room_id"`
	Type      string             `json:"type"`
	Room      *model.ChatRoom    `json:"room"`
	Member    *model.ChatMember  `json:"member"`
	ChangedAt *model.Time        `json:"changed_at"`
}

type roomSummarySyncChange struct {
	RoomID      string             `json:"room_id"`
	UnreadCount int                `json:"unread_count"`
	HasUnread   bool               `json:"has_unread"`
	LastMessage *model.ChatMessage `json:"last_message"`
	ChangedAt   *model.Time        `json:"changed_at"`
}

type roomSyncResponse struct {
	Changes           []roomSyncChange        `json:"changes"`
	Summaries         []roomSummarySyncChange `json:"summaries"`
	Groups            []model.ChatGroup       `json:"groups"`
	CurrentTimestamp  *model.Time             `json:"current_timestamp"`
	TotalCount        int                     `json:"total_count"`
	SummaryTotalCount int                     `json:"summary_total_count"`
}

type chatRoomRequest struct {
	Name                 *string        `json:"name"`
	Description          *string        `json:"description"`
	Slug                 *string        `json:"slug"`
	PictureID            *string        `json:"picture_id"`
	BackgroundID         *string        `json:"background_id"`
	RealmID              *string        `json:"realm_id"`
	IsCommunity          *bool          `json:"is_community"`
	IsPublic             *bool          `json:"is_public"`
	IsReadReceiptsPublic *bool          `json:"is_read_receipts_public"`
	EncryptionMode       *int           `json:"encryption_mode"`
	E2eePolicy           map[string]any `json:"e2ee_policy"`
}

type directMessageRequest struct {
	RelatedUserID  string `json:"related_user_id"`
	EncryptionMode *int   `json:"encryption_mode"`
}

type enableE2eeRequest struct {
	EncryptionMode *int `json:"encryption_mode"`
}

type enableMlsRequest struct {
	MlsGroupID *string        `json:"mls_group_id"`
	E2eePolicy map[string]any `json:"e2ee_policy"`
}

type chatMemberProfileRequest struct {
	Nick *string `json:"nick"`
}

type chatMemberRequest struct {
	RelatedUserID string `json:"related_user_id"`
	Role          int    `json:"role"`
}

type chatMemberNotifyRequest struct {
	NotifyLevel *int       `json:"notify_level"`
	BreakUntil  *model.Time `json:"break_until"`
}

type chatTimeoutRequest struct {
	Reason       *string     `json:"reason"`
	TimeoutUntil *model.Time `json:"timeout_until"`
}

type pinMessageRequest struct {
	MessageID string      `json:"message_id"`
	ExpiresAt *model.Time `json:"expires_at"`
}

type createGroupRequest struct {
	Name  string  `json:"name"`
	Color *string `json:"color"`
	Icon  *string `json:"icon"`
	Order *int    `json:"order"`
}

type updateGroupRequest struct {
	Name  *string `json:"name"`
	Color *string `json:"color"`
	Icon  *string `json:"icon"`
	Order *int    `json:"order"`
}

type moveToGroupRequest struct {
	GroupID *string `json:"group_id"`
}

type onlineMembersResponse struct {
	OnlineCount        int      `json:"online_count"`
	DirectMessageStatus any     `json:"direct_message_status"`
	OnlineUserNames    []string `json:"online_user_names"`
	OnlineAccounts     []any    `json:"online_accounts"`
}

type deviceJoinedRequest struct {
	MlsDeviceID string `json:"mls_device_id"`
	Epoch       int64  `json:"epoch"`
}

type timeoutUserRequest struct {
	DurationMinutes int     `json:"duration_minutes"`
	Reason          *string `json:"reason"`
}

type deleteRoomMessageRequest struct {
	Reason *string `json:"reason"`
}
