package realtime

import (
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"src.solsynth.dev/sosys/go/pkg/errs"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/model"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/store"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
)

// Voice error codes mirrored from the C# service.
const (
	codeVoiceInvalid            = "CHAT_VOICE_INVALID"
	codeVoiceStorageUnavailable = "CHAT_VOICE_STORAGE_UNAVAILABLE"
	codeReplyNotFound           = "CHAT_REPLY_MESSAGE_NOT_FOUND"
	codeForwardNotFound         = "CHAT_FORWARD_MESSAGE_NOT_FOUND"
	codeE2EEVoiceUnsupported    = "CHAT_E2EE_VOICE_NOT_SUPPORTED_V1"
)

// voiceError carries the ApiError fields for a voice-message failure.
type voiceError struct {
	Code    string
	Message string
	Status  int
}

func (e *voiceError) Error() string { return e.Message }

var errVoiceStorageUnavailable = &voiceError{
	Code:    codeVoiceStorageUnavailable,
	Message: "Voice message storage is unavailable.",
	Status:  http.StatusServiceUnavailable,
}

func invalidVoice(message string) error {
	return &voiceError{Code: codeVoiceInvalid, Message: message, Status: http.StatusBadRequest}
}

// voiceMimeExtensions is the accepted audio allow-list, mirroring the C#
// ChatVoiceService MimeToExtension table.
var voiceMimeExtensions = map[string]string{
	"audio/webm":  ".webm",
	"audio/ogg":   ".ogg",
	"audio/mpeg":  ".mp3",
	"audio/mp4":   ".m4a",
	"audio/wav":   ".wav",
	"audio/x-wav": ".wav",
	"audio/aac":   ".aac",
	"audio/flac":  ".flac",
}

// voiceService owns voice clip storage, validation and retention.
type voiceService struct {
	srv           *server
	storage       *s3Storage
	maxFileSize   int64
	keyPrefix     string
	retentionDays int
	cleanupCron   string
}

func (v *voiceService) extension(mimeType, originalName string) string {
	if extension, ok := voiceMimeExtensions[mimeType]; ok {
		return extension
	}
	fallback := strings.ToLower(path.Ext(originalName))
	if fallback == "" || len(fallback) > 10 {
		return ".bin"
	}
	return fallback
}

func (v *voiceService) objectKey(roomID, clipID uuid.UUID, extension string) string {
	prefix := strings.Trim(v.keyPrefix, "/")
	if prefix == "" {
		return fmt.Sprintf("%s/%s%s", roomID, clipID, extension)
	}
	return fmt.Sprintf("%s/%s/%s%s", prefix, roomID, clipID, extension)
}

func (v *voiceService) publicURL(clip *store.ChatVoiceClipEntity) string {
	return v.storage.publicURL(clip.StoragePath)
}

// save validates the upload, stores the object and persists the clip row.
func (v *voiceService) save(ctx context.Context, roomID uuid.UUID, senderID uuid.UUID, file *multipart.FileHeader, durationMS *int) (*store.ChatVoiceClipEntity, error) {
	if file == nil || file.Size <= 0 {
		return nil, invalidVoice("Voice file cannot be empty.")
	}
	if file.Size > v.maxFileSize {
		return nil, invalidVoice(fmt.Sprintf("Voice file is too large. Maximum allowed is %d bytes.", v.maxFileSize))
	}
	contentType := strings.TrimSpace(file.Header.Get("Content-Type"))
	mediaType := contentType
	if index := strings.Index(mediaType, ";"); index >= 0 {
		mediaType = strings.TrimSpace(mediaType[:index])
	}
	if mediaType == "" {
		return nil, invalidVoice("Only audio files are supported for voice messages.")
	}
	if _, ok := voiceMimeExtensions[mediaType]; !ok {
		return nil, invalidVoice("Only audio files are supported for voice messages.")
	}
	if !v.storage.configured() {
		return nil, errVoiceStorageUnavailable
	}

	clipID := uuid.New()
	objectKey := v.objectKey(roomID, clipID, v.extension(mediaType, file.Filename))
	stream, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	if err := v.storage.put(ctx, objectKey, contentType, stream, file.Size); err != nil {
		return nil, err
	}

	clip := &store.ChatVoiceClipEntity{
		ID:               clipID,
		ChatRoomID:       roomID,
		SenderID:         senderID,
		MimeType:         contentType,
		StoragePath:      objectKey,
		OriginalFileName: file.Filename,
		Size:             file.Size,
		DurationMS:       durationMS,
	}
	if err := v.srv.core.DB.WithContext(ctx).Create(clip).Error; err != nil {
		if removeErr := v.storage.delete(ctx, objectKey); removeErr != nil && v.srv.log != nil {
			v.srv.log.Warn("failed to roll back voice object", "key", objectKey, "error", removeErr)
		}
		return nil, err
	}
	return clip, nil
}

func (v *voiceService) find(ctx context.Context, roomID, voiceID uuid.UUID) (*store.ChatVoiceClipEntity, error) {
	var clip store.ChatVoiceClipEntity
	err := v.srv.core.DB.WithContext(ctx).
		Where("id = ? AND chat_room_id = ?", voiceID, roomID).
		First(&clip).Error
	if err != nil {
		return nil, err
	}
	return &clip, nil
}

// cleanup deletes expired clips in batches of 200, objects first.
func (v *voiceService) cleanup(ctx context.Context) (int, error) {
	threshold := time.Now().UTC().AddDate(0, 0, -v.retentionDays)
	removed := 0
	for {
		var clips []store.ChatVoiceClipEntity
		err := v.srv.core.DB.WithContext(ctx).
			Where("created_at < ?", threshold).
			Order("created_at").
			Limit(200).
			Find(&clips).Error
		if err != nil {
			return removed, err
		}
		if len(clips) == 0 {
			return removed, nil
		}
		ids := make([]uuid.UUID, 0, len(clips))
		for index := range clips {
			if v.storage.configured() {
				if err := v.storage.delete(ctx, clips[index].StoragePath); err != nil {
					if v.srv.log != nil {
						v.srv.log.Warn("failed to delete expired voice object",
							"clip_id", clips[index].ID, "key", clips[index].StoragePath, "error", err)
					}
				}
			}
			ids = append(ids, clips[index].ID)
		}
		if err := v.srv.core.DB.WithContext(ctx).
			Where("id IN ?", ids).
			Delete(&store.ChatVoiceClipEntity{}).Error; err != nil {
			return removed, err
		}
		removed += len(clips)
		if len(clips) < 200 {
			return removed, nil
		}
	}
}

// voiceCleanupJob is the scheduled retention sweep.
func (v *voiceService) cleanupJob(ctx context.Context) error {
	removed, err := v.cleanup(ctx)
	if removed > 0 && v.srv.log != nil {
		v.srv.log.Info("voice cleanup removed expired clips", "count", removed)
	}
	return err
}

// sendVoiceMessage handles POST /api/chat/{roomId}/messages/voice.
func (s *server) sendVoiceMessage(c *gin.Context) {
	user, ok := s.account(c)
	if !ok {
		return
	}
	roomID, ok := parseRoomID(c)
	if !ok {
		return
	}
	accountID, err := uuid.Parse(user.GetId())
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	ctx := c.Request.Context()
	member, err := s.joinedMember(ctx, roomID, accountID)
	if err != nil {
		httpx.Forbidden(c, "You need to be a member to send messages here.")
		return
	}
	if member.TimeoutUntil != nil && member.TimeoutUntil.After(time.Now()) {
		httpx.Forbidden(c, "You has been timed out in this chat.")
		return
	}
	room, err := s.core.Store.GetRoom(ctx, roomID)
	if err != nil {
		httpx.NotFound(c, "Room not found.")
		return
	}
	if room.EncryptionMode != int(model.ChatEncryptionNone) {
		httpx.Fail(c, errs.New(codeE2EEVoiceUnsupported,
			"Voice endpoint is not supported for E2EE rooms in v1.", http.StatusConflict))
		return
	}

	file, err := c.FormFile("file")
	if err != nil {
		httpx.Fail(c, errs.Validation(map[string][]string{"file": {"The File field is required."}}))
		return
	}

	clientMessageID := formValue(c, "client_message_id", "clientMessageId")
	if clientMessageID == "" {
		clientMessageID = strings.TrimSpace(c.PostForm("nonce"))
	}
	if clientMessageID != "" {
		existing, err := s.existingClientMessage(ctx, roomID, member.ID, clientMessageID, "voice")
		if err == nil && existing != nil {
			s.writeMessage(c, ctx, member, existing, false)
			return
		}
	}

	var durationMS *int
	if raw := formValue(c, "duration_ms", "durationMs"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			httpx.Fail(c, errs.Validation(map[string][]string{"duration_ms": {"The value is not a valid integer."}}))
			return
		}
		durationMS = &parsed
	}

	repliedMessageID, ok := optionalUUID(c, "replied_message_id", "repliedMessageId")
	if !ok {
		return
	}
	forwardedMessageID, ok := optionalUUID(c, "forwarded_message_id", "forwardedMessageId")
	if !ok {
		return
	}

	if repliedMessageID != nil {
		replied, err := s.core.Store.GetMessage(ctx, *repliedMessageID)
		if err != nil || replied.ChatRoomID != roomID {
			httpx.Fail(c, errs.New(codeReplyNotFound,
				"The message you're replying to does not exist.", http.StatusBadRequest))
			return
		}
	}
	if forwardedMessageID != nil {
		if _, err := s.core.Store.GetMessage(ctx, *forwardedMessageID); err != nil {
			httpx.Fail(c, errs.New(codeForwardNotFound,
				"The message you're forwarding does not exist.", http.StatusBadRequest))
			return
		}
	}

	clip, err := s.voice.save(ctx, roomID, member.ID, file, durationMS)
	if err != nil {
		s.writeVoiceError(c, err)
		return
	}

	voiceURL := s.voice.publicURL(clip)
	if voiceURL == "" {
		voiceURL = fmt.Sprintf("/api/chat/%s/voice/%s", roomID, clip.ID)
	}
	meta := map[string]any{
		"voice_clip_id": clip.ID.String(),
		"voice_url":     voiceURL,
		"mime_type":     clip.MimeType,
		"size":          clip.Size,
	}
	if clip.DurationMS != nil {
		meta["duration_ms"] = *clip.DurationMS
	}
	if strings.TrimSpace(clip.OriginalFileName) != "" {
		meta["file_name"] = clip.OriginalFileName
	}

	mentions := s.voiceMentions(ctx, roomID, repliedMessageID, forwardedMessageID)

	entity := &store.ChatMessageEntity{
		ID:                 uuid.New(),
		Type:               "voice",
		ChatRoomID:         roomID,
		SenderID:           member.ID,
		Meta:               store.ToJSON(meta),
		MembersMentioned:   store.ToJSON(mentions),
		RepliedMessageID:   repliedMessageID,
		ForwardedMessageID: forwardedMessageID,
	}
	if clientMessageID != "" {
		entity.ClientMessageID = &clientMessageID
	}
	if err := s.insertMessage(ctx, roomID, entity); err != nil {
		s.log.Error("failed to persist voice message", "room_id", roomID, "error", err)
		httpx.Internal(c)
		return
	}

	// Sending implies read-through for the sender.
	if err := s.core.DB.WithContext(ctx).Model(&store.ChatMemberEntity{}).
		Where("id = ?", member.ID).
		Update("last_read_at", time.Now().UTC()).Error; err != nil {
		s.log.Warn("failed to update sender read marker", "member_id", member.ID, "error", err)
	}

	s.writeMessage(c, ctx, member, entity, true)
}

// writeMessage renders a persisted message with its sender, delivers it to the
// room and acknowledges it to the caller.
func (s *server) writeMessage(c *gin.Context, ctx context.Context, member *store.ChatMemberEntity, entity *store.ChatMessageEntity, notify bool) {
	message := store.MessageToModel(entity)
	sender := s.memberModel(ctx, member)
	message.Sender = &sender
	if notify {
		s.broadcast(ctx, entity.ChatRoomID, message)
	}
	httpx.OK(c, message)
}

// existingClientMessage finds a retried send by its client message id.
func (s *server) existingClientMessage(ctx context.Context, roomID, senderID uuid.UUID, clientMessageID, messageType string) (*store.ChatMessageEntity, error) {
	var entity store.ChatMessageEntity
	err := s.core.DB.WithContext(ctx).
		Where("chat_room_id = ? AND sender_id = ? AND client_message_id = ? AND type = ?",
			roomID, senderID, clientMessageID, messageType).
		First(&entity).Error
	if err != nil {
		return nil, err
	}
	return &entity, nil
}

// formValue reads a multipart field by its documented snake_case name and
// falls back to the legacy camelCase binding name.
func formValue(c *gin.Context, snake, camel string) string {
	if value := strings.TrimSpace(c.PostForm(snake)); value != "" {
		return value
	}
	return strings.TrimSpace(c.PostForm(camel))
}

// voiceMentions reproduces ChatMessageHelpers.ExtractMentionedUsersAsync for a
// content-less voice message: the replied message's author and the forwarded
// message's sender.
func (s *server) voiceMentions(ctx context.Context, roomID uuid.UUID, replied, forwarded *uuid.UUID) []string {
	seen := map[string]bool{}
	mentioned := []string{}
	add := func(value string) {
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		mentioned = append(mentioned, value)
	}
	if replied != nil {
		if message, err := s.core.Store.GetMessage(ctx, *replied); err == nil && message.ChatRoomID == roomID {
			if sender, err := s.memberByID(ctx, message.SenderID); err == nil {
				add(sender.AccountID.String())
			}
		}
	}
	if forwarded != nil {
		if message, err := s.core.Store.GetMessage(ctx, *forwarded); err == nil {
			add(message.SenderID.String())
		}
	}
	return mentioned
}

func (s *server) memberByID(ctx context.Context, id uuid.UUID) (*store.ChatMemberEntity, error) {
	var entity store.ChatMemberEntity
	if err := s.core.DB.WithContext(ctx).First(&entity, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &entity, nil
}

// getVoiceMessage handles GET /api/chat/{roomId}/voice/{voiceId}.
func (s *server) getVoiceMessage(c *gin.Context) {
	roomID, ok := parseRoomID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	room, err := s.core.Store.GetRoom(ctx, roomID)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	if room.EncryptionMode != int(model.ChatEncryptionNone) || !room.IsPublic {
		user := authn.CurrentUser(c)
		if user == nil {
			httpx.Unauthorized(c)
			return
		}
		accountID, err := uuid.Parse(user.GetId())
		if err != nil {
			httpx.Unauthorized(c)
			return
		}
		if _, err := s.joinedMember(ctx, roomID, accountID); err != nil {
			httpx.Forbidden(c, "You are not a member of this chat room.")
			return
		}
	}

	voiceID, err := uuid.Parse(c.Param("voiceId"))
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	clip, err := s.voice.find(ctx, roomID, voiceID)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	body, size, err := s.voice.storage.open(ctx, clip.StoragePath)
	if err != nil {
		switch {
		case errors.Is(err, errObjectNotFound):
			s.log.Warn("voice object missing in storage", "clip_id", clip.ID, "key", clip.StoragePath)
			c.Status(http.StatusNotFound)
		case errors.Is(err, errVoiceStorageUnavailable):
			httpx.Fail(c, errs.New(codeVoiceStorageUnavailable,
				"Voice message storage is unavailable.", http.StatusServiceUnavailable))
		default:
			s.log.Error("failed to open voice object", "clip_id", clip.ID, "key", clip.StoragePath, "error", err)
			httpx.Internal(c)
		}
		return
	}
	defer body.Close()

	length := clip.Size
	if length <= 0 {
		length = size
	}
	if length <= 0 {
		// gin treats any non-negative length as a Content-Length header.
		length = -1
	}
	contentType := clip.MimeType
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/octet-stream"
	}
	c.DataFromReader(http.StatusOK, length, contentType, body, nil)
}

// writeVoiceError maps a voice failure onto the ApiError envelope.
func (s *server) writeVoiceError(c *gin.Context, err error) {
	var failure *voiceError
	if errors.As(err, &failure) {
		httpx.Fail(c, errs.New(failure.Code, failure.Message, failure.Status))
		return
	}
	s.log.Error("failed to save voice clip", "error", err)
	httpx.Internal(c)
}

func optionalUUID(c *gin.Context, snake, camel string) (*uuid.UUID, bool) {
	raw := formValue(c, snake, camel)
	if raw == "" {
		return nil, true
	}
	parsed, err := uuid.Parse(raw)
	if err != nil {
		httpx.Fail(c, errs.Validation(map[string][]string{snake: {"The value is not a valid UUID."}}))
		return nil, false
	}
	return &parsed, true
}
