package realtime

import (
	"src.solsynth.dev/sosys/lighthouse/internal/messager/model"
)

// JoinCallResponse mirrors the C# JoinCallResponse returned by
// GET /api/chat/realtime/{roomId}/join.
type JoinCallResponse struct {
	Provider     string            `json:"provider"`
	Endpoint     string            `json:"endpoint"`
	Token        string            `json:"token"`
	CallID       string            `json:"call_id"`
	RoomName     string            `json:"room_name"`
	RoomTitle    *string           `json:"room_title"`
	IsAdmin      bool              `json:"is_admin"`
	Participants []CallParticipant `json:"participants"`
}

// CallParticipant mirrors the C# CallParticipant.
type CallParticipant struct {
	Identity  string            `json:"identity"`
	Name      string            `json:"name"`
	AccountID *string           `json:"account_id"`
	Profile   *model.ChatMember `json:"profile"`
	JoinedAt  *model.Time       `json:"joined_at"`
	TrackSid  *string           `json:"track_sid"`
}

// KickParticipantRequest mirrors the C# KickParticipantRequest body.
type KickParticipantRequest struct {
	BanDurationMinutes *int    `json:"ban_duration_minutes"`
	Reason             *string `json:"reason"`
}

// accountRef is the subset of SnAccount embedded in call participant
// profiles. The chat module owns the full account wire shape; this module only
// needs the identity fields clients render in a call roster.
type accountRef struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Nick     string `json:"nick"`
	Region   string `json:"region"`
	Language string `json:"language"`
	Picture  any    `json:"picture"`
}
