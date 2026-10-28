package livestreams

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"src.solsynth.dev/sosys/go/pkg/errs"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// ---- request bodies -------------------------------------------------------

type createRequest struct {
	Title       *string        `json:"title"`
	Description *string        `json:"description"`
	Slug        *string        `json:"slug"`
	ThumbnailID *string        `json:"thumbnail_id"`
	Type        *int           `json:"type"`
	Visibility  *int           `json:"visibility"`
	Metadata    map[string]any `json:"metadata"`
}

type updateRequest struct {
	Title       *string        `json:"title"`
	Description *string        `json:"description"`
	Slug        *string        `json:"slug"`
	Type        *int           `json:"type"`
	Visibility  *int           `json:"visibility"`
	Metadata    map[string]any `json:"metadata"`
}

type thumbnailRequest struct {
	ThumbnailID *string `json:"thumbnail_id"`
}

type startStreamingRequest struct {
	ParticipantName  *string `json:"participant_name"`
	NoIngress        *bool   `json:"no_ingress"`
	UseWhip          *bool   `json:"use_whip"`
	EnableTranscoding *bool  `json:"enable_transcoding"`
}

type startEgressRequest struct {
	RtmpURLs []string `json:"rtmp_urls"`
	FilePath *string  `json:"file_path"`
}

type startHLSRequest struct {
	PlaylistName    *string `json:"playlist_name"`
	SegmentDuration uint32  `json:"segment_duration"`
	SegmentCount    int     `json:"segment_count"`
	Layout          *string `json:"layout"`
	HLSBaseURL      *string `json:"hls_base_url"`
}

type sendChatRequest struct {
	Content string `json:"content"`
}

type timeoutRequest struct {
	DurationMinutes *int `json:"duration_minutes"`
}

type awardRequest struct {
	Amount  float64 `json:"amount"`
	Message *string `json:"message"`
}

// ---- helpers --------------------------------------------------------------

func forbidden(c *gin.Context, message string) {
	httpx.Fail(c, errs.New("UNAUTHORIZED", message, http.StatusForbidden))
}

func (s *service) accountID(c *gin.Context) (uuid.UUID, bool) {
	user := authn.CurrentUser(c)
	if user == nil {
		httpx.Unauthorized(c)
		return uuid.Nil, false
	}
	id, err := uuid.Parse(user.GetId())
	if err != nil {
		httpx.Unauthorized(c)
		return uuid.Nil, false
	}
	return id, true
}

func parseLimit(c *gin.Context, fallback int) int {
	value, err := strconv.Atoi(c.Query("limit"))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func parseOffset(c *gin.Context) int {
	value, err := strconv.Atoi(c.Query("offset"))
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func (s *service) requireStream(c *gin.Context) (*store.LiveStreamEntity, *store.PublisherEntity, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.Status(http.StatusNotFound)
		return nil, nil, false
	}
	entity, publisher, err := s.getByID(c.Request.Context(), id)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			c.Status(http.StatusNotFound)
			return nil, nil, false
		}
		httpx.Internal(c)
		return nil, nil, false
	}
	return entity, publisher, true
}

func (s *service) resolveThumbnail(ctx context.Context, id *string) (*model.CloudFileReferenceObject, bool) {
	if id == nil {
		return nil, true
	}
	files, ok := s.filesPort()
	if !ok {
		return nil, false
	}
	file, err := files.GetFile(ctx, *id)
	if err != nil || file == nil {
		return nil, false
	}
	return file, true
}

// ---- handlers -------------------------------------------------------------

func (s *service) create(c *gin.Context) {
	accountID, ok := s.accountID(c)
	if !ok {
		return
	}
	var req createRequest
	if !httpx.BindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()

	var publisher *store.PublisherEntity
	if strings.TrimSpace(c.Query("pub")) == "" {
		publishers, err := s.userPublishers(ctx, accountID)
		if err != nil {
			httpx.Internal(c)
			return
		}
		if len(publishers) == 0 {
			httpx.BadRequest(c, "LIVE_STREAM_NO_PUBLISHER_MEMBERSHIP",
				"You need to be a member of a publisher to create live streams.")
			return
		}
		publisher = &publishers[0]
	} else {
		row, err := s.store.GetPublisherByName(ctx, c.Query("pub"))
		if err != nil || row == nil {
			httpx.BadRequest(c, "PUBLISHER_NOT_FOUND", "Publisher not found.")
			return
		}
		if !s.isEditor(ctx, &row.ID, accountID) {
			forbidden(c, "You need at least be an editor to create live streams for this publisher.")
			return
		}
		publisher = row
	}

	thumbnail, found := s.resolveThumbnail(ctx, req.ThumbnailID)
	if !found {
		httpx.BadRequest(c, "LIVE_STREAM_THUMBNAIL_NOT_FOUND", "Thumbnail not found.")
		return
	}

	stream, err := s.createStream(ctx, publisher.ID, req, thumbnail)
	if err != nil {
		s.log.Error("failed to create livestream", "error", err)
		httpx.Internal(c)
		return
	}
	httpx.OK(c, stream)
}

func (s *service) list(c *gin.Context) {
	streams, err := s.getActive(c.Request.Context(), parseLimit(c, 50), parseOffset(c))
	if err != nil {
		httpx.Internal(c)
		return
	}
	for i := range streams {
		s.sanitizeForPublic(&streams[i])
	}
	httpx.OK(c, streams)
}

func (s *service) listByPublisher(c *gin.Context) {
	publisherID, err := uuid.Parse(c.Param("publisherId"))
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	streams, err := s.getByPublisher(c.Request.Context(), publisherID, parseLimit(c, 20), parseOffset(c))
	if err != nil {
		httpx.Internal(c)
		return
	}
	for i := range streams {
		s.sanitizeForPublic(&streams[i])
	}
	httpx.OK(c, streams)
}

func (s *service) get(c *gin.Context) {
	entity, publisher, ok := s.requireStream(c)
	if !ok {
		return
	}
	stream := streamToModel(entity, publisher)
	authorized := false
	if user := authn.CurrentUser(c); user != nil {
		if accountID, err := uuid.Parse(user.GetId()); err == nil {
			authorized = s.isEditor(c.Request.Context(), entity.PublisherID, accountID)
		}
	}
	if !authorized {
		s.sanitizeForPublic(&stream)
	}
	httpx.OK(c, stream)
}

func (s *service) token(c *gin.Context) {
	entity, publisher, ok := s.requireStream(c)
	if !ok {
		return
	}
	if entity.Status != statusActive {
		httpx.BadRequest(c, "LIVE_STREAM_NOT_ACTIVE", "LiveStream is not active")
		return
	}
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
	isStreamer := c.Query("streamer") == "true" || c.Query("streamer") == "1"
	isTool := c.Query("tool") == "true" || c.Query("tool") == "1"
	if s.isEditor(c.Request.Context(), entity.PublisherID, accountID) && isStreamer {
		isStreamer = true
	}
	identity := user.GetName()
	if identity == "" {
		identity = "user_" + strings.ReplaceAll(accountID.String(), "-", "")
	}
	name := user.GetNick()
	if isTool {
		identity += "_tool"
	}
	token, err := s.lk.clientToken(entity.RoomName, identity, name, isStreamer, true, false, map[string]string{
		"livestream_id": entity.ID.String(),
		"is_streamer":   strconv.FormatBool(isStreamer),
		"account_id":    user.GetId(),
	}, 4*time.Hour)
	if err != nil {
		s.log.Error("failed to generate livekit token", "error", err)
		httpx.Internal(c)
		return
	}
	_ = publisher
	httpx.OK(c, gin.H{
		"token":       token,
		"room_name":   entity.RoomName,
		"url":         strings.Replace(s.lk.host(), "http", "ws", 1),
		"is_streamer": isStreamer,
		"identity":    identity,
	})
}

func (s *service) start(c *gin.Context) {
	entity, publisher, ok := s.requireStream(c)
	if !ok {
		return
	}
	accountID, ok := s.accountID(c)
	if !ok {
		return
	}
	if !s.isEditor(c.Request.Context(), entity.PublisherID, accountID) {
		forbidden(c, "You need to be an editor of this publisher to start the live stream.")
		return
	}
	var req startStreamingRequest
	if c.Request.ContentLength != 0 {
		if !httpx.BindJSON(c, &req) {
			return
		}
	}
	identity := "streamer_" + strings.ReplaceAll(accountID.String(), "-", "") + "_ingress"
	name := ""
	if req.ParticipantName != nil {
		name = *req.ParticipantName
	} else if publisher != nil && publisher.Nick != "" {
		name = publisher.Nick
	} else {
		name = "streamer"
	}
	ctx := c.Request.Context()

	if req.NoIngress != nil && *req.NoIngress {
		if err := s.startInAppStreaming(ctx, entity.ID); err != nil {
			s.log.Error("failed to start in-app stream", "error", err)
			httpx.Internal(c)
			return
		}
		httpx.OK(c, gin.H{
			"room_name": entity.RoomName,
			"url":       strings.Replace(s.lk.host(), "http", "ws", 1),
		})
		return
	}

	useWhip := req.UseWhip != nil && *req.UseWhip
	enableTranscoding := req.EnableTranscoding == nil || *req.EnableTranscoding
	result, err := s.startStreaming(ctx, entity.ID, identity, name, true, enableTranscoding, useWhip)
	if err != nil {
		s.log.Error("failed to start stream", "error", err)
		httpx.Internal(c)
		return
	}
	httpx.OK(c, gin.H{
		"url":        result.URL,
		"stream_key": result.StreamKey,
		"room_name":  entity.RoomName,
	})
}

func (s *service) startEgress(c *gin.Context) {
	entity, _, ok := s.requireStream(c)
	if !ok {
		return
	}
	accountID, ok := s.accountID(c)
	if !ok {
		return
	}
	if !s.isEditor(c.Request.Context(), entity.PublisherID, accountID) {
		forbidden(c, "You need to be an editor of this publisher to manage egress.")
		return
	}
	if !authn.IsSuperuser(c) {
		forbidden(c, "Egress settings can only be configured by administrators.")
		return
	}
	var req startEgressRequest
	if c.Request.ContentLength != 0 {
		if !httpx.BindJSON(c, &req) {
			return
		}
	}
	egressID, err := s.startEgressStream(c.Request.Context(), entity.ID, req.RtmpURLs, req.FilePath)
	if err != nil {
		s.log.Error("failed to start egress", "error", err)
		httpx.Internal(c)
		return
	}
	httpx.OK(c, gin.H{"egress_id": egressID})
}

func (s *service) stopEgress(c *gin.Context) {
	entity, _, ok := s.requireStream(c)
	if !ok {
		return
	}
	accountID, ok := s.accountID(c)
	if !ok {
		return
	}
	if !s.isEditor(c.Request.Context(), entity.PublisherID, accountID) {
		forbidden(c, "You need to be an editor of this publisher to manage egress.")
		return
	}
	if err := s.stopStreamEgress(c.Request.Context(), entity.ID); err != nil {
		httpx.Internal(c)
		return
	}
	httpx.NoContent(c)
}

func (s *service) startHLS(c *gin.Context) {
	entity, _, ok := s.requireStream(c)
	if !ok {
		return
	}
	accountID, ok := s.accountID(c)
	if !ok {
		return
	}
	if !s.isEditor(c.Request.Context(), entity.PublisherID, accountID) {
		forbidden(c, "You need to be an editor of this publisher to manage HLS egress.")
		return
	}
	if !authn.IsSuperuser(c) {
		forbidden(c, "HLS egress settings can only be configured by administrators.")
		return
	}
	var req startHLSRequest
	if c.Request.ContentLength != 0 {
		if !httpx.BindJSON(c, &req) {
			return
		}
	}
	playlistName := "playlist.m3u8"
	if req.PlaylistName != nil && *req.PlaylistName != "" {
		playlistName = *req.PlaylistName
	}
	segmentDuration := req.SegmentDuration
	if segmentDuration == 0 {
		segmentDuration = 6
	}
	result, err := s.startHLSEgress(c.Request.Context(), entity.ID, playlistName, segmentDuration, req.SegmentCount, req.Layout)
	if err != nil {
		s.log.Error("failed to start HLS egress", "error", err)
		httpx.Internal(c)
		return
	}
	playlistPath := entity.ID.String() + "/" + playlistName
	entity.HLSPlaylistPath = &playlistPath
	if err := s.db.WithContext(c.Request.Context()).Save(entity).Error; err != nil {
		httpx.Internal(c)
		return
	}
	playlistURL := s.lk.buildHLSPlaylistURL(&playlistPath)
	httpx.OK(c, gin.H{
		"egress_id":    result.EgressID,
		"playlist_url": playlistURL,
		"playlist_name": playlistName,
	})
}

func (s *service) stopHLS(c *gin.Context) {
	entity, _, ok := s.requireStream(c)
	if !ok {
		return
	}
	accountID, ok := s.accountID(c)
	if !ok {
		return
	}
	if !s.isEditor(c.Request.Context(), entity.PublisherID, accountID) {
		forbidden(c, "You need to be an editor of this publisher to manage HLS egress.")
		return
	}
	if err := s.stopHLSStream(c.Request.Context(), entity.ID); err != nil {
		httpx.Internal(c)
		return
	}
	httpx.NoContent(c)
}

func (s *service) end(c *gin.Context) {
	entity, _, ok := s.requireStream(c)
	if !ok {
		return
	}
	accountID, ok := s.accountID(c)
	if !ok {
		return
	}
	if !s.isEditor(c.Request.Context(), entity.PublisherID, accountID) {
		forbidden(c, "You need to be an editor of this publisher to end the live stream.")
		return
	}
	if err := s.endStream(c.Request.Context(), entity.ID); err != nil {
		httpx.Internal(c)
		return
	}
	httpx.NoContent(c)
}

func (s *service) update(c *gin.Context) {
	entity, _, ok := s.requireStream(c)
	if !ok {
		return
	}
	accountID, ok := s.accountID(c)
	if !ok {
		return
	}
	if !s.isEditor(c.Request.Context(), entity.PublisherID, accountID) {
		forbidden(c, "You need to be an editor of this publisher to update the live stream.")
		return
	}
	var req updateRequest
	if !httpx.BindJSON(c, &req) {
		return
	}
	if req.Title != nil {
		entity.Title = req.Title
	}
	if req.Description != nil {
		entity.Description = req.Description
	}
	if req.Slug != nil {
		entity.Slug = req.Slug
	}
	if req.Type != nil {
		entity.Type = *req.Type
	}
	if req.Visibility != nil {
		entity.Visibility = *req.Visibility
	}
	if req.Metadata != nil {
		entity.Metadata = store.ToJSON(req.Metadata)
	}
	if err := s.db.WithContext(c.Request.Context()).Save(entity).Error; err != nil {
		httpx.Internal(c)
		return
	}
	_ = s.lk.broadcast(c.Request.Context(), entity.RoomName, "stream_updated", map[string]any{
		"livestream_id": entity.ID.String(),
		"title":         derefString(entity.Title),
		"description":   derefString(entity.Description),
	})
	stream := streamToModel(entity, nil)
	s.sanitizeForPublic(&stream)
	httpx.OK(c, stream)
}

func (s *service) updateThumbnail(c *gin.Context) {
	entity, _, ok := s.requireStream(c)
	if !ok {
		return
	}
	accountID, ok := s.accountID(c)
	if !ok {
		return
	}
	if !s.isEditor(c.Request.Context(), entity.PublisherID, accountID) {
		forbidden(c, "You need to be an editor of this publisher to update the thumbnail.")
		return
	}
	var req thumbnailRequest
	if !httpx.BindJSON(c, &req) {
		return
	}
	if req.ThumbnailID != nil {
		thumbnail, found := s.resolveThumbnail(c.Request.Context(), req.ThumbnailID)
		if !found {
			httpx.BadRequest(c, "LIVE_STREAM_THUMBNAIL_NOT_FOUND", "Thumbnail not found.")
			return
		}
		entity.Thumbnail = store.ToJSON(thumbnail)
	} else {
		entity.Thumbnail = nil
	}
	if err := s.db.WithContext(c.Request.Context()).Save(entity).Error; err != nil {
		httpx.Internal(c)
		return
	}
	stream := streamToModel(entity, nil)
	s.sanitizeForPublic(&stream)
	httpx.OK(c, stream)
}

func (s *service) delete(c *gin.Context) {
	entity, _, ok := s.requireStream(c)
	if !ok {
		return
	}
	accountID, ok := s.accountID(c)
	if !ok {
		return
	}
	if !s.isEditor(c.Request.Context(), entity.PublisherID, accountID) {
		forbidden(c, "You need to be an editor of this publisher to delete the live stream.")
		return
	}
	if err := s.deleteStream(c.Request.Context(), entity.ID); err != nil {
		httpx.Internal(c)
		return
	}
	httpx.NoContent(c)
}

func (s *service) details(c *gin.Context) {
	entity, _, ok := s.requireStream(c)
	if !ok {
		return
	}
	participants, err := s.lk.listParticipants(c.Request.Context(), entity.RoomName)
	if err != nil {
		httpx.NotFound(c, "LiveStream room not found")
		return
	}
	httpx.OK(c, gin.H{
		"participant_count": participants,
		"viewer_count":      entity.ViewerCount,
		"peak_viewer_count": entity.PeakViewerCount,
	})
}

// ---- chat -----------------------------------------------------------------

func (s *service) listChat(c *gin.Context) {
	entity, _, ok := s.requireStream(c)
	if !ok {
		return
	}
	var rows []store.LiveStreamChatMessageEntity
	err := s.db.WithContext(c.Request.Context()).
		Where("live_stream_id = ? AND deleted_at IS NULL", entity.ID).
		Order("created_at DESC").
		Limit(parseLimit(c, 50)).Offset(parseOffset(c)).
		Find(&rows).Error
	if err != nil {
		httpx.Internal(c)
		return
	}
	messages := make([]model.LiveStreamChatMessage, 0, len(rows))
	for i := range rows {
		messages = append(messages, chatToModel(&rows[i]))
	}
	httpx.OK(c, messages)
}

func (s *service) sendChat(c *gin.Context) {
	entity, _, ok := s.requireStream(c)
	if !ok {
		return
	}
	accountID, ok := s.accountID(c)
	if !ok {
		return
	}
	if entity.Status != statusActive {
		httpx.BadRequest(c, "LIVE_STREAM_NOT_ACTIVE", "Stream is not active")
		return
	}
	var req sendChatRequest
	if !httpx.BindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()

	var timedOut int64
	if err := s.db.WithContext(ctx).Model(&store.LiveStreamChatMessageEntity{}).
		Where("live_stream_id = ? AND sender_id = ? AND timeout_until > ?", entity.ID, accountID, time.Now().UTC()).
		Count(&timedOut).Error; err != nil {
		httpx.Internal(c)
		return
	}
	if timedOut > 0 {
		forbidden(c, "You are temporarily muted")
		return
	}

	senderName := "Anonymous"
	if user := authn.CurrentUser(c); user != nil {
		switch {
		case user.GetNick() != "":
			senderName = user.GetNick()
		case user.GetName() != "":
			senderName = user.GetName()
		}
	}
	message := &store.LiveStreamChatMessageEntity{
		ID:           uuid.New(),
		LiveStreamID: entity.ID,
		SenderID:     accountID,
		SenderName:   senderName,
		Content:      req.Content,
	}
	if err := s.db.WithContext(ctx).Create(message).Error; err != nil {
		httpx.Internal(c)
		return
	}
	wire := chatToModel(message)
	if raw, err := json.Marshal(wire); err == nil {
		_ = s.lk.sendData(ctx, entity.RoomName, raw)
	}
	httpx.OK(c, wire)
}

func (s *service) deleteChat(c *gin.Context) {
	entity, _, ok := s.requireStream(c)
	if !ok {
		return
	}
	accountID, ok := s.accountID(c)
	if !ok {
		return
	}
	if !s.isEditor(c.Request.Context(), entity.PublisherID, accountID) {
		forbidden(c, "You need to be an editor of this publisher to moderate chat.")
		return
	}
	messageID, err := uuid.Parse(c.Param("messageId"))
	if err != nil {
		httpx.NotFound(c, "Message not found")
		return
	}
	var message store.LiveStreamChatMessageEntity
	if err := s.db.WithContext(c.Request.Context()).
		Where("id = ? AND live_stream_id = ?", messageID, entity.ID).
		First(&message).Error; err != nil {
		httpx.NotFound(c, "Message not found")
		return
	}
	if err := s.db.WithContext(c.Request.Context()).
		Model(&message).
		Update("deleted_at", time.Now().UTC()).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.NoContent(c)
}

func (s *service) timeoutChat(c *gin.Context) {
	entity, _, ok := s.requireStream(c)
	if !ok {
		return
	}
	accountID, ok := s.accountID(c)
	if !ok {
		return
	}
	if !s.isEditor(c.Request.Context(), entity.PublisherID, accountID) {
		forbidden(c, "You need to be an editor of this publisher to timeout users.")
		return
	}
	messageID, err := uuid.Parse(c.Param("messageId"))
	if err != nil {
		httpx.NotFound(c, "Message not found")
		return
	}
	var req timeoutRequest
	if c.Request.ContentLength != 0 {
		if !httpx.BindJSON(c, &req) {
			return
		}
	}
	duration := 10
	if req.DurationMinutes != nil {
		duration = *req.DurationMinutes
	}
	var message store.LiveStreamChatMessageEntity
	if err := s.db.WithContext(c.Request.Context()).
		Where("id = ? AND live_stream_id = ?", messageID, entity.ID).
		First(&message).Error; err != nil {
		httpx.NotFound(c, "Message not found")
		return
	}
	until := time.Now().UTC().Add(time.Duration(duration) * time.Minute)
	if err := s.db.WithContext(c.Request.Context()).
		Model(&message).
		Update("timeout_until", until).Error; err != nil {
		httpx.Internal(c)
		return
	}
	payload, _ := json.Marshal(gin.H{
		"type":             "timeout",
		"sender_id":        message.SenderID.String(),
		"duration_minutes": duration,
	})
	_ = s.lk.sendData(c.Request.Context(), entity.RoomName, payload)
	httpx.NoContent(c)
}

// ---- awards ---------------------------------------------------------------

func (s *service) listAwards(c *gin.Context) {
	entity, _, ok := s.requireStream(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	var total int64
	if err := s.db.WithContext(ctx).Model(&store.LiveStreamAwardEntity{}).
		Where("live_stream_id = ?", entity.ID).Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	var rows []store.LiveStreamAwardEntity
	if err := s.db.WithContext(ctx).
		Where("live_stream_id = ?", entity.ID).
		Order("created_at DESC").
		Limit(parseLimit(c, 20)).Offset(parseOffset(c)).
		Find(&rows).Error; err != nil {
		httpx.Internal(c)
		return
	}
	awards := make([]model.LiveStreamAward, 0, len(rows))
	for i := range rows {
		awards = append(awards, awardToModel(&rows[i]))
	}
	httpx.List(c, awards, total)
}

func (s *service) activeAwards(c *gin.Context) {
	entity, _, ok := s.requireStream(c)
	if !ok {
		return
	}
	var rows []store.LiveStreamAwardEntity
	if err := s.db.WithContext(c.Request.Context()).
		Where("live_stream_id = ?", entity.ID).
		Order("amount DESC").
		Find(&rows).Error; err != nil {
		httpx.Internal(c)
		return
	}
	now := time.Now().UTC()
	active := make([]model.LiveStreamAward, 0, len(rows))
	for i := range rows {
		highlight := time.Duration(rows[i].Amount*2) * time.Second
		if rows[i].CreatedAt.Add(highlight).After(now) {
			active = append(active, awardToModel(&rows[i]))
		}
	}
	httpx.OK(c, active)
}

type leaderboardRow struct {
	AccountID   uuid.UUID
	TotalAmount float64
	AwardCount  int64
}

func (s *service) awardLeaderboard(c *gin.Context) {
	entity, _, ok := s.requireStream(c)
	if !ok {
		return
	}
	var rows []leaderboardRow
	err := s.db.WithContext(c.Request.Context()).
		Model(&store.LiveStreamAwardEntity{}).
		Select("account_id, COALESCE(SUM(amount), 0) AS total_amount, COUNT(*) AS award_count").
		Where("live_stream_id = ?", entity.ID).
		Group("account_id").
		Order("total_amount DESC").
		Limit(parseLimit(c, 10)).
		Scan(&rows).Error
	if err != nil {
		httpx.Internal(c)
		return
	}
	result := make([]gin.H, 0, len(rows))
	for index, row := range rows {
		result = append(result, gin.H{
			"rank":         index + 1,
			"account_id":   row.AccountID.String(),
			"total_amount": row.TotalAmount,
			"award_count":  row.AwardCount,
			"account":      nil,
		})
	}
	httpx.OK(c, result)
}

func (s *service) award(c *gin.Context) {
	entity, publisher, ok := s.requireStream(c)
	if !ok {
		return
	}
	accountID, ok := s.accountID(c)
	if !ok {
		return
	}
	var req awardRequest
	if !httpx.BindJSON(c, &req) {
		return
	}
	payments, available := s.paymentsPort()
	if !available {
		httpx.Fail(c, errs.New("SERVICE_UNAVAILABLE", "The payment service is unavailable.", http.StatusServiceUnavailable))
		return
	}
	amount := formatAmount(req.Amount)
	remark := "from @" + entity.RoomName
	if publisher != nil {
		remark = "from @" + publisher.Name
	}
	if entity.Title != nil && strings.TrimSpace(*entity.Title) != "" {
		remark = *entity.Title
	}
	meta := map[string]any{
		"account_id":   accountID.String(),
		"livestream_id": entity.ID.String(),
		"amount":       amount,
		"message":      req.Message,
	}
	_ = remark
	orderID, err := payments.CreateOrder(c.Request.Context(), accountID.String(), "livestreams.award", amount, "points", meta)
	if err != nil {
		s.log.Error("failed to create livestream award order", "error", err)
		httpx.Internal(c)
		return
	}
	parsed, err := uuid.Parse(orderID)
	if err != nil {
		s.log.Error("payment service returned a non-uuid order id", "order_id", orderID)
		httpx.Internal(c)
		return
	}
	httpx.OK(c, gin.H{"order_id": parsed.String()})
}
