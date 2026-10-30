package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/model"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/filejson"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps the shared GORM handle.
type Store struct {
	DB *gorm.DB
}

// New builds a store over the shared handle.
func New(database *gorm.DB) *Store { return &Store{DB: database} }

// WithTx returns a store bound to tx.
func (s *Store) WithTx(tx *gorm.DB) *Store { return &Store{DB: tx} }

func mapNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

// ParseUUID parses a UUID string.
func ParseUUID(value string) (uuid.UUID, error) { return uuid.Parse(value) }

// UUIDPtrString renders an optional UUID.
func UUIDPtrString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	value := id.String()
	return &value
}

// TimePtr converts a time.Time into a *model.Time (nil for nil).
func TimePtr(value *time.Time) *model.Time {
	if value == nil {
		return nil
	}
	return model.NewTime(*value)
}

// JSONValue decodes a jsonb column into a generic value.
func JSONValue(raw datatypes.JSON) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return value
}

// JSONArray decodes a jsonb array into dst.
func JSONArray[T any](raw datatypes.JSON, dst *[]T) {
	if len(raw) == 0 {
		return
	}
	_ = json.Unmarshal(raw, dst)
}

// JSONMap decodes a jsonb object into dst.
func JSONMap[T any](raw datatypes.JSON, dst *T) {
	if len(raw) == 0 {
		return
	}
	_ = json.Unmarshal(raw, dst)
}

// ToJSON encodes a value for a jsonb column.
func ToJSON(value any) datatypes.JSON {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return datatypes.JSON(raw)
}

// DeletedPtr renders a soft-delete timestamp for the wire.
func DeletedPtr(value gorm.DeletedAt) *model.Time {
	if !value.Valid {
		return nil
	}
	return model.NewTime(value.Time)
}

// RoomToModel maps a room row to the wire type.
func RoomToModel(entity *ChatRoomEntity) model.ChatRoom {
	return model.ChatRoom{
		ID:                   entity.ID.String(),
		Name:                 entity.Name,
		Description:          entity.Description,
		Slug:                 entity.Slug,
		Type:                 entity.Type,
		IsCommunity:          entity.IsCommunity,
		IsPublic:             entity.IsPublic,
		IsReadReceiptsPublic: entity.IsReadReceiptsPublic,
		EncryptionMode:       entity.EncryptionMode,
		MLSGroupID:           entity.MLSGroupID,
		E2eePolicy:           JSONValue(entity.E2eePolicy),
		Picture:              filejson.Value(entity.Picture),
		Background:           filejson.Value(entity.Background),
		AccountID:            UUIDPtrString(entity.AccountID),
		RealmID:              UUIDPtrString(entity.RealmID),
		Members:              []model.ChatMember{},
		CreatedAt:            model.NewTime(entity.CreatedAt),
		UpdatedAt:            model.NewTime(entity.UpdatedAt),
		DeletedAt:            DeletedPtr(entity.DeletedAt),
	}
}

// MemberToModel maps a member row to the wire type.
func MemberToModel(entity *ChatMemberEntity) model.ChatMember {
	member := model.ChatMember{
		ID:           entity.ID.String(),
		ChatRoomID:   entity.ChatRoomID.String(),
		AccountID:    entity.AccountID.String(),
		Username:     entity.Username,
		Nick:         entity.Nick,
		ChatGroupID:  UUIDPtrString(entity.ChatGroupID),
		Notify:       entity.Notify,
		LastReadAt:   TimePtr(entity.LastReadAt),
		JoinedAt:     TimePtr(entity.JoinedAt),
		LeaveAt:      TimePtr(entity.LeaveAt),
		ConfirmedAt:  TimePtr(entity.ConfirmedAt),
		InvitedByID:  UUIDPtrString(entity.InvitedByID),
		BreakUntil:   TimePtr(entity.BreakUntil),
		TimeoutUntil: TimePtr(entity.TimeoutUntil),
		TimeoutCause: JSONValue(entity.TimeoutCause),
		CreatedAt:    model.NewTime(entity.CreatedAt),
		UpdatedAt:    model.NewTime(entity.UpdatedAt),
		DeletedAt:    DeletedPtr(entity.DeletedAt),
	}
	return member
}

// MessageToModel maps a message row to the wire type.
func MessageToModel(entity *ChatMessageEntity) model.ChatMessage {
	message := model.ChatMessage{
		ID:                 entity.ID.String(),
		RoomSequence:       entity.RoomSequence,
		Type:               entity.Type,
		Content:            entity.Content,
		ClientMessageID:    entity.ClientMessageID,
		Nonce:              entity.ClientMessageID,
		Meta:               JSONValue(entity.Meta),
		EditedAt:           TimePtr(entity.EditedAt),
		Attachments:        filejson.References(entity.Attachments),
		ReactionsCount:     map[string]int{},
		ReactionsMade:      map[string]bool{},
		RepliedMessageID:   UUIDPtrString(entity.RepliedMessageID),
		ThreadID:           UUIDPtrString(entity.ThreadID),
		ThreadRootID:       UUIDPtrString(entity.ThreadRootID),
		ForwardedMessageID: UUIDPtrString(entity.ForwardedMessageID),
		SenderID:           entity.SenderID.String(),
		ChatRoomID:         entity.ChatRoomID.String(),
		Reactions:          []model.ChatReaction{},
		ThreadReplies:      []model.ThreadReplyNode{},
		CreatedAt:          model.NewTime(entity.CreatedAt),
		UpdatedAt:          model.NewTime(entity.UpdatedAt),
		DeletedAt:          DeletedPtr(entity.DeletedAt),
	}
	if len(entity.EncryptionMeta) > 0 {
		var meta model.ChatEncryptionMeta
		if err := json.Unmarshal(entity.EncryptionMeta, &meta); err == nil {
			message.EncryptionMeta = &meta
			message.IsEncrypted = true
		}
	}
	JSONArray(entity.MembersMentioned, &message.MembersMentioned)
	JSONMap(entity.ReactionsCount, &message.ReactionsCount)
	if message.ReactionsCount == nil {
		message.ReactionsCount = map[string]int{}
	}
	return message
}

// ReactionToModel maps a reaction row to the wire type.
func ReactionToModel(entity *ChatReactionEntity) model.ChatReaction {
	return model.ChatReaction{
		ID:        entity.ID.String(),
		MessageID: entity.MessageID.String(),
		SenderID:  entity.SenderID.String(),
		Symbol:    entity.Symbol,
		Attitude:  entity.Attitude,
		CreatedAt: model.NewTime(entity.CreatedAt),
		UpdatedAt: model.NewTime(entity.UpdatedAt),
		DeletedAt: DeletedPtr(entity.DeletedAt),
	}
}

// PinToModel maps a pin row to the wire type.
func PinToModel(entity *ChatMessagePinEntity) model.ChatMessagePin {
	return model.ChatMessagePin{
		ID:               entity.ID.String(),
		MessageID:        entity.MessageID.String(),
		ChatRoomID:       entity.ChatRoomID.String(),
		PinnedByMemberID: entity.PinnedByMemberID.String(),
		ExpiresAt:        TimePtr(entity.ExpiresAt),
		CreatedAt:        model.NewTime(entity.CreatedAt),
		UpdatedAt:        model.NewTime(entity.UpdatedAt),
		DeletedAt:        DeletedPtr(entity.DeletedAt),
	}
}

// GroupToModel maps a group row to the wire type.
func GroupToModel(entity *ChatGroupEntity) model.ChatGroup {
	return model.ChatGroup{
		ID:        entity.ID.String(),
		AccountID: entity.AccountID.String(),
		Name:      entity.Name,
		Color:     entity.Color,
		Icon:      entity.Icon,
		Order:     entity.Order,
		RoomIDs:   []string{},
		CreatedAt: model.NewTime(entity.CreatedAt),
		UpdatedAt: model.NewTime(entity.UpdatedAt),
		DeletedAt: DeletedPtr(entity.DeletedAt),
	}
}

// CallToModel maps a realtime call row to the wire type.
func CallToModel(entity *RealtimeCallEntity) model.RealtimeCall {
	call := model.RealtimeCall{
		ID:           entity.ID.String(),
		EndedAt:      TimePtr(entity.EndedAt),
		SenderID:     entity.SenderID.String(),
		RoomID:       entity.RoomID.String(),
		ProviderName: entity.ProviderName,
		SessionID:    entity.SessionID,
		CreatedAt:    model.NewTime(entity.CreatedAt),
		UpdatedAt:    model.NewTime(entity.UpdatedAt),
		DeletedAt:    DeletedPtr(entity.DeletedAt),
	}
	if entity.Upstream != nil && *entity.Upstream != "" {
		var upstream any
		if err := json.Unmarshal([]byte(*entity.Upstream), &upstream); err == nil {
			call.Upstream = upstream
		}
	}
	return call
}

// VoiceClipToModel maps a voice clip row to the wire type.
func VoiceClipToModel(entity *ChatVoiceClipEntity) model.ChatVoiceClip {
	return model.ChatVoiceClip{
		ID:               entity.ID.String(),
		ChatRoomID:       entity.ChatRoomID.String(),
		SenderID:         entity.SenderID.String(),
		MimeType:         entity.MimeType,
		StoragePath:      entity.StoragePath,
		OriginalFileName: entity.OriginalFileName,
		Size:             entity.Size,
		DurationMS:       entity.DurationMS,
		CreatedAt:        model.NewTime(entity.CreatedAt),
		UpdatedAt:        model.NewTime(entity.UpdatedAt),
		DeletedAt:        DeletedPtr(entity.DeletedAt),
	}
}

// GetMember loads an active member row for one account in one room.
func (s *Store) GetMember(ctx context.Context, roomID, accountID uuid.UUID) (*ChatMemberEntity, error) {
	var entity ChatMemberEntity
	err := s.DB.WithContext(ctx).
		Where("chat_room_id = ? AND account_id = ?", roomID, accountID).
		First(&entity).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &entity, nil
}

// GetMessage loads one message row by id.
func (s *Store) GetMessage(ctx context.Context, id uuid.UUID) (*ChatMessageEntity, error) {
	var entity ChatMessageEntity
	if err := s.DB.WithContext(ctx).First(&entity, "id = ?", id).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &entity, nil
}

// GetRoom loads one room row by id.
func (s *Store) GetRoom(ctx context.Context, id uuid.UUID) (*ChatRoomEntity, error) {
	var entity ChatRoomEntity
	if err := s.DB.WithContext(ctx).First(&entity, "id = ?", id).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &entity, nil
}

// NextRoomSequence allocates the next per-room message sequence inside tx. The
// counter row is created on first use and locked for update so concurrent
// senders never share a sequence.
func (s *Store) NextRoomSequence(ctx context.Context, tx *gorm.DB, roomID uuid.UUID) (int64, error) {
	if tx == nil {
		tx = s.DB
	}
	var counter ChatRoomCounterEntity
	err := tx.WithContext(ctx).Clauses().Raw(
		`INSERT INTO chat_room_counters (chat_room_id, last_sequence) VALUES (?, 1)
		 ON CONFLICT (chat_room_id) DO UPDATE SET last_sequence = chat_room_counters.last_sequence + 1
		 RETURNING chat_room_id, last_sequence`, roomID).
		Scan(&counter).Error
	if err != nil {
		return 0, err
	}
	return counter.LastSequence, nil
}
