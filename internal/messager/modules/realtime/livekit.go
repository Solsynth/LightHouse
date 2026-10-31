package realtime

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/config"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/app"
)

// livekitClient is the LiveKit provider: HS256 access-token issuance plus the
// RoomService Twirp API (create/delete room, list/remove participants, mute
// track). It mirrors DysonNetwork.Messager's LiveKitRealtimeService.
type livekitClient struct {
	endpoint string // endpoint handed to clients (unchanged configuration value)
	baseURL  string // HTTP origin of the Twirp API
	apiKey   string
	secret   string
	http     *http.Client
	redis    app.CacheService
	log      *slog.Logger
}

// participantCacheItem mirrors the C# ParticipantCacheItem cached per room.
type participantCacheItem struct {
	Identity  string            `json:"identity"`
	Name      string            `json:"name"`
	AccountID *string           `json:"account_id"`
	State     string            `json:"state"`
	Metadata  map[string]string `json:"metadata"`
	JoinedAt  time.Time         `json:"joined_at"`
	TrackSid  *string           `json:"track_sid"`
}

// roomInfo is the CreateRoom response.
type roomInfo struct {
	Sid          string `json:"sid"`
	Name         string `json:"name"`
	EmptyTimeout uint32 `json:"emptyTimeout"`
	CreationTime int64  `json:"creationTime"`
}

// participantInfo is one ListParticipants entry.
type participantInfo struct {
	Identity string          `json:"identity"`
	Name     string          `json:"name"`
	Metadata string          `json:"metadata"`
	State    json.RawMessage `json:"state"`
	Tracks   []struct {
		Sid string `json:"sid"`
	} `json:"tracks"`
}

// liveKitError is a non-2xx Twirp response.
type liveKitError struct {
	Status int
	Code   string
	Msg    string
}

func (e *liveKitError) Error() string {
	if e.Msg != "" {
		return fmt.Sprintf("livekit %s (%d): %s", e.Code, e.Status, e.Msg)
	}
	return fmt.Sprintf("livekit request failed with status %d", e.Status)
}

func newLiveKit(cfg *config.Config, redis app.CacheService, log *slog.Logger) *livekitClient {
	endpoint := strings.TrimSpace(cfg.RealtimeChat.Endpoint)
	base := endpoint
	switch {
	case strings.HasPrefix(base, "wss://"):
		base = "https://" + strings.TrimPrefix(base, "wss://")
	case strings.HasPrefix(base, "ws://"):
		base = "http://" + strings.TrimPrefix(base, "ws://")
	case base != "" && !strings.Contains(base, "://"):
		base = "https://" + base
	}
	base = strings.TrimRight(base, "/")

	return &livekitClient{
		endpoint: endpoint,
		baseURL:  base,
		apiKey:   strings.TrimSpace(cfg.RealtimeChat.APIKey),
		secret:   strings.TrimSpace(cfg.RealtimeChat.APISecret),
		http:     &http.Client{Timeout: 15 * time.Second},
		redis:    redis,
		log:      log,
	}
}

// provider returns the provider name persisted on call rows.
func (l *livekitClient) provider() string { return providerName }

// configured reports whether the LiveKit project is usable. Every call route
// answers 400 CHAT_CALL_SESSION_NOT_CONFIGURED while this is false.
func (l *livekitClient) configured() bool {
	return l.endpoint != "" && l.apiKey != "" && l.secret != ""
}

func base64URL(raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(raw)
}

// signJWT builds an HS256 JWT, the only signing algorithm LiveKit accepts.
func (l *livekitClient) signJWT(claims map[string]any) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	body := base64URL(header) + "." + base64URL(payload)
	mac := hmac.New(sha256.New, []byte(l.secret))
	if _, err := mac.Write([]byte(body)); err != nil {
		return "", err
	}
	return body + "." + base64URL(mac.Sum(nil)), nil
}

// serverToken authorizes exactly one RoomService call. LiveKit validates
// room-scoped methods with EnsureAdminPermission, which requires
// video.roomAdmin AND video.room equal to the requested room, so the C# SDK
// mints a fresh token per request with the room baked in; a single
// room-agnostic admin token is rejected with "permissions denied".
func (l *livekitClient) serverToken(grants map[string]any) (string, error) {
	now := time.Now()
	return l.signJWT(map[string]any{
		"iss":   l.apiKey,
		"sub":   l.apiKey,
		"nbf":   now.Add(-5 * time.Second).Unix(),
		"exp":   now.Add(10 * time.Minute).Unix(),
		"video": grants,
	})
}

// roomCreateGrants mirrors VideoGrants{RoomCreate: true}.
func roomCreateGrants() map[string]any {
	return map[string]any{"roomCreate": true}
}

// roomAdminGrants mirrors VideoGrants{RoomAdmin: true, Room: room}.
func roomAdminGrants(room string) map[string]any {
	return map[string]any{"roomAdmin": true, "room": room}
}

// userToken issues the participant access token handed to clients.
func (l *livekitClient) userToken(account *gen.DyAccount, identity, sessionID, tool string, isAdmin bool) (string, error) {
	now := time.Now()
	metadata, err := json.Marshal(map[string]string{
		"account_id": account.GetId(),
		"tool":       tool,
	})
	if err != nil {
		return "", err
	}
	return l.signJWT(map[string]any{
		"iss":      l.apiKey,
		"sub":      identity,
		"nbf":      now.Add(-5 * time.Second).Unix(),
		"exp":      now.Add(time.Hour).Unix(),
		"name":     account.GetNick(),
		"metadata": string(metadata),
		"video": map[string]any{
			"roomJoin":            true,
			"room":                sessionID,
			"canPublish":          true,
			"canPublishData":      true,
			"canSubscribe":        true,
			"canSubscribeMetrics": true,
			"roomAdmin":           isAdmin,
		},
	})
}

// participantIdentity mirrors the C# identity rule: the account name (or
// user_<id>) with an optional tool suffix.
func participantIdentity(account *gen.DyAccount, tool string) string {
	identity := strings.TrimSpace(account.GetName())
	if identity == "" {
		identity = "user_" + strings.ReplaceAll(account.GetId(), "-", "")
	}
	normalized := normalizeToolName(tool)
	switch {
	case normalized == "":
		return identity
	case normalized == "tool":
		return identity + "_tool"
	default:
		return identity + "_tool_" + normalized
	}
}

// normalizeToolName mirrors the C# NormalizeToolName helper.
func normalizeToolName(tool string) string {
	value := strings.TrimSpace(tool)
	if value == "" {
		return ""
	}
	if value == "0" || strings.EqualFold(value, "false") {
		return ""
	}
	if value == "1" || strings.EqualFold(value, "true") {
		return "tool"
	}
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			builder.WriteRune(r)
		}
	}
	sanitized := builder.String()
	if sanitized == "" {
		return "tool"
	}
	if len(sanitized) > 64 {
		sanitized = sanitized[:64]
	}
	return sanitized
}

// twirp posts one RoomService method, authenticated with grants scoped to that
// call.
func (l *livekitClient) twirp(ctx context.Context, method string, grants map[string]any, in any, out any) error {
	token, err := l.serverToken(grants)
	if err != nil {
		return err
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		l.baseURL+"/twirp/livekit.RoomService/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")

	response, err := l.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		failure := struct {
			Code string `json:"code"`
			Msg  string `json:"msg"`
		}{}
		_ = json.Unmarshal(raw, &failure)
		if failure.Msg == "" {
			failure.Msg = strings.TrimSpace(string(raw))
		}
		return &liveKitError{Status: response.StatusCode, Code: failure.Code, Msg: failure.Msg}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// createSession ensures the provider room exists and returns its name plus the
// upstream parameters persisted on the call row.
func (l *livekitClient) createSession(ctx context.Context, roomID uuid.UUID, metadata map[string]any) (string, map[string]any, error) {
	roomName := "Call_" + strings.ReplaceAll(roomID.String(), "-", "")
	roomMetadata := make(map[string]string, len(metadata))
	for key, value := range metadata {
		roomMetadata[key] = fmt.Sprint(value)
	}
	encoded, err := json.Marshal(roomMetadata)
	if err != nil {
		return "", nil, err
	}

	var room roomInfo
	err = l.twirp(ctx, "CreateRoom", roomCreateGrants(), map[string]any{
		"name":          roomName,
		"empty_timeout": 300,
		"metadata":      string(encoded),
	}, &room)
	if err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "already") && strings.Contains(message, "exist") {
			if l.log != nil {
				l.log.Info("livekit room already exists; reusing", "room_id", roomID, "room", roomName)
			}
			return roomName, map[string]any{}, nil
		}
		return "", nil, err
	}

	sessionID := room.Name
	if sessionID == "" {
		sessionID = roomName
	}
	return sessionID, map[string]any{
		"sid":          room.Sid,
		"emptyTimeout": room.EmptyTimeout,
		"creationTime": room.CreationTime,
	}, nil
}

// endSession deletes the provider room, disconnecting every participant.
func (l *livekitClient) endSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	return l.twirp(ctx, "DeleteRoom", roomCreateGrants(), map[string]any{"room": sessionID}, nil)
}

func (l *livekitClient) listParticipants(ctx context.Context, sessionID string) ([]participantInfo, error) {
	var response struct {
		Participants []participantInfo `json:"participants"`
	}
	if err := l.twirp(ctx, "ListParticipants", roomAdminGrants(sessionID), map[string]any{"room": sessionID}, &response); err != nil {
		return nil, err
	}
	return response.Participants, nil
}

func (l *livekitClient) kickParticipant(ctx context.Context, sessionID, identity string) error {
	return l.twirp(ctx, "RemoveParticipant", roomAdminGrants(sessionID), map[string]any{
		"room":     sessionID,
		"identity": identity,
	}, nil)
}

func (l *livekitClient) muteParticipant(ctx context.Context, sessionID, identity, trackSid string, muted bool) error {
	return l.twirp(ctx, "MutePublishedTrack", roomAdminGrants(sessionID), map[string]any{
		"room":      sessionID,
		"identity":  identity,
		"track_sid": trackSid,
		"muted":     muted,
	}, nil)
}

func (l *livekitClient) participantsKey(sessionID string) string {
	return "RoomParticipants_" + sessionID
}

// syncParticipants lists the live roster and refreshes the six-hour cache,
// matching the C# SyncParticipantsAsync behaviour.
func (l *livekitClient) syncParticipants(ctx context.Context, sessionID string) ([]participantCacheItem, error) {
	raw, err := l.listParticipants(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	participants := make([]participantCacheItem, 0, len(raw))
	for _, participant := range raw {
		participants = append(participants, l.cacheItem(participant))
	}
	if l.redis != nil {
		if err := l.redis.Set(ctx, l.participantsKey(sessionID), participants, 6*time.Hour); err != nil && l.log != nil {
			l.log.Warn("failed to cache call participants", "session", sessionID, "error", err)
		}
	}
	return participants, nil
}

func (l *livekitClient) cacheItem(participant participantInfo) participantCacheItem {
	item := participantCacheItem{
		Identity: participant.Identity,
		Name:     participant.Name,
		State:    decodeParticipantState(participant.State),
		Metadata: map[string]string{},
		JoinedAt: time.Now().UTC(),
	}
	if len(participant.Tracks) > 0 && participant.Tracks[0].Sid != "" {
		trackSid := participant.Tracks[0].Sid
		item.TrackSid = &trackSid
	}
	if participant.Metadata != "" {
		metadata := map[string]string{}
		if err := json.Unmarshal([]byte(participant.Metadata), &metadata); err == nil {
			item.Metadata = metadata
			if accountID, ok := metadata["account_id"]; ok {
				if parsed, err := uuid.Parse(accountID); err == nil {
					value := parsed.String()
					item.AccountID = &value
				}
			}
		} else if l.log != nil {
			l.log.Warn("failed to parse livekit participant metadata", "identity", participant.Identity, "error", err)
		}
	}
	return item
}

// decodeParticipantState renders the protojson enum (name or number) as text.
func decodeParticipantState(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var name string
	if err := json.Unmarshal(raw, &name); err == nil {
		return name
	}
	return string(raw)
}
