package livestreams

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// livekitClient talks to the LiveKit server's Twirp/JSON API. The C# service
// used the Livekit.Server.Sdk.Dotnet clients, which are thin Twirp wrappers;
// the request and response shapes here mirror the proto messages they send.
type livekitClient struct {
	endpoint    string
	apiKey      string
	apiSecret   string
	playbackURL string
	http        *http.Client
}

func newLiveKitClient(endpoint, apiKey, apiSecret, playbackURL string) *livekitClient {
	return &livekitClient{
		endpoint:    strings.TrimRight(endpoint, "/"),
		apiKey:      apiKey,
		apiSecret:   apiSecret,
		playbackURL: playbackURL,
		http:        &http.Client{Timeout: 15 * time.Second},
	}
}

// configured reports whether the LiveKit project settings are present. Every
// LiveKit call fails with errLiveKitUnconfigured when they are not.
func (c *livekitClient) configured() bool {
	return c != nil && c.endpoint != "" && c.apiKey != "" && c.apiSecret != ""
}

// host returns the configured endpoint, the value the C# controller echoed as
// the websocket URL.
func (c *livekitClient) host() string { return c.endpoint }

// buildHLSPlaylistURL mirrors LiveKitLivestreamService.BuildHlsPlaylistUrl:
// nil when either side is empty.
func (c *livekitClient) buildHLSPlaylistURL(path *string) *string {
	if c == nil || path == nil || *path == "" || c.playbackURL == "" {
		return nil
	}
	url := strings.TrimRight(c.playbackURL, "/") + "/" + strings.TrimLeft(*path, "/")
	return &url
}

var errLiveKitUnconfigured = errors.New("livestream: LiveKit is not configured")

// ---- tokens ---------------------------------------------------------------

// signJWT renders an HS256 JWT the way LiveKit's AccessToken does.
func signJWT(claims map[string]any, secret string) (string, error) {
	header := map[string]any{"alg": "HS256", "typ": "JWT"}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	payloadJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(headerJSON) + "." + enc.EncodeToString(payloadJSON)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(signingInput))
	return signingInput + "." + enc.EncodeToString(mac.Sum(nil)), nil
}

// serverToken is one server API token scoped to a single call. LiveKit checks
// room-scoped methods with EnsureAdminPermission, which requires roomAdmin
// AND video.room equal to the requested room; the .NET SDK therefore mints a
// fresh token per request with the room baked in, and a room-agnostic admin
// token is rejected with "permissions denied". Same for the create/record/
// ingress grants, which the SDK sets per call rather than globally.
func (c *livekitClient) serverToken(grants map[string]any) (string, error) {
	if !c.configured() {
		return "", errLiveKitUnconfigured
	}
	now := time.Now()
	return signJWT(map[string]any{
		"exp":   now.Add(10 * time.Minute).Unix(),
		"nbf":   now.Add(-10 * time.Second).Unix(),
		"iss":   c.apiKey,
		"sub":   c.apiKey,
		"jti":   uuid.NewString(),
		"video": grants,
	}, c.apiSecret)
}

// roomCreateGrants mirrors VideoGrants{RoomCreate: true}.
func roomCreateGrants() map[string]any { return map[string]any{"roomCreate": true} }

// roomAdminGrants mirrors VideoGrants{RoomAdmin: true, Room: room}.
func roomAdminGrants(room string) map[string]any {
	return map[string]any{"roomAdmin": true, "room": room}
}

// roomRecordGrants mirrors VideoGrants{RoomRecord: true}.
func roomRecordGrants() map[string]any { return map[string]any{"roomRecord": true} }

// ingressAdminGrants mirrors VideoGrants{IngressAdmin: true}.
func ingressAdminGrants() map[string]any { return map[string]any{"ingressAdmin": true} }

// clientToken is the join token handed to browsers. It mirrors
// LiveKitLivestreamService.GenerateToken: video grants for one room plus the
// optional metadata claim, 4h TTL.
func (c *livekitClient) clientToken(room, identity, name string, canPublish, canSubscribe, canPublishData bool, metadata map[string]string, ttl time.Duration) (string, error) {
	if !c.configured() {
		return "", errLiveKitUnconfigured
	}
	if ttl <= 0 {
		ttl = 4 * time.Hour
	}
	now := time.Now()
	claims := map[string]any{
		"exp": now.Add(ttl).Unix(),
		"nbf": now.Add(-10 * time.Second).Unix(),
		"iss": c.apiKey,
		"sub": identity,
		"jti": uuid.NewString(),
		"video": map[string]any{
			"roomJoin":       true,
			"room":           room,
			"canPublish":     canPublish,
			"canSubscribe":   canSubscribe,
			"canPublishData": canPublishData,
		},
	}
	if name != "" {
		claims["name"] = name
	}
	if len(metadata) > 0 {
		raw, err := json.Marshal(metadata)
		if err != nil {
			return "", err
		}
		claims["metadata"] = string(raw)
	}
	return signJWT(claims, c.apiSecret)
}

// ---- Twirp transport ------------------------------------------------------

func (c *livekitClient) call(ctx context.Context, service, method string, grants map[string]any, body any, out any) error {
	if !c.configured() {
		return errLiveKitUnconfigured
	}
	token, err := c.serverToken(grants)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	url := c.endpoint + "/twirp/livekit." + service + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("livekit %s.%s: %s: %s", service, method, resp.Status, strings.TrimSpace(string(payload)))
	}
	if out != nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, out); err != nil {
			return fmt.Errorf("livekit %s.%s decode: %w", service, method, err)
		}
	}
	return nil
}

// ---- rooms ----------------------------------------------------------------

func (c *livekitClient) createRoom(ctx context.Context, name string, metadata map[string]string) error {
	body := map[string]any{
		"name":             name,
		"empty_timeout":    600,
		"max_participants": 10000,
	}
	if len(metadata) > 0 {
		raw, err := json.Marshal(metadata)
		if err != nil {
			return err
		}
		body["metadata"] = string(raw)
	}
	return c.call(ctx, "RoomService", "CreateRoom", roomCreateGrants(), body, nil)
}

func (c *livekitClient) deleteRoom(ctx context.Context, name string) error {
	return c.call(ctx, "RoomService", "DeleteRoom", roomCreateGrants(), map[string]any{"room": name}, nil)
}

func (c *livekitClient) listParticipants(ctx context.Context, room string) (int, error) {
	var resp struct {
		Participants []json.RawMessage `json:"participants"`
	}
	if err := c.call(ctx, "RoomService", "ListParticipants", roomAdminGrants(room), map[string]any{"room": room}, &resp); err != nil {
		return 0, err
	}
	return len(resp.Participants), nil
}

func (c *livekitClient) sendData(ctx context.Context, room string, data []byte) error {
	participants, err := c.listParticipants(ctx, room)
	if err != nil {
		// The C# helper skipped sending when the room could not be listed or
		// had no participants; a missing room is not an error for callers.
		return nil
	}
	if participants == 0 {
		return nil
	}
	body := map[string]any{
		"room": room,
		"data": base64.StdEncoding.EncodeToString(data),
		"kind": 0, // DataPacket.Kind.RELIABLE
	}
	var lastErr error
	for attempt := range 3 {
		if lastErr = c.call(ctx, "RoomService", "SendData", roomAdminGrants(room), body, nil); lastErr == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(100*(attempt+1)) * time.Millisecond):
		}
	}
	return lastErr
}

// broadcast pushes one JSON packet with a `type` field into the room, matching
// LiveKitLivestreamService.BroadcastLivestreamUpdateAsync.
func (c *livekitClient) broadcast(ctx context.Context, room, eventType string, data map[string]any) error {
	payload := map[string]any{"type": eventType}
	for key, value := range data {
		payload[key] = value
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return c.sendData(ctx, room, raw)
}

// ---- ingress --------------------------------------------------------------

type ingressResult struct {
	IngressID string
	URL       string
	StreamKey string
}

func (c *livekitClient) createIngress(ctx context.Context, room, identity, participantName, title string, enableTranscoding, whip bool) (*ingressResult, error) {
	inputType := 0 // RTMP_INPUT
	if whip {
		inputType = 1 // WHIP_INPUT
	}
	if title == "" {
		title = room
	}
	body := map[string]any{
		"input_type":           inputType,
		"name":                 title,
		"room_name":            room,
		"participant_identity": identity,
		"enable_transcoding":   enableTranscoding,
	}
	if participantName != "" {
		body["participant_name"] = participantName
	}
	var resp struct {
		IngressID string `json:"ingress_id"`
		URL       string `json:"url"`
		StreamKey string `json:"stream_key"`
	}
	if err := c.call(ctx, "IngressService", "CreateIngress", ingressAdminGrants(), body, &resp); err != nil {
		return nil, err
	}
	return &ingressResult{IngressID: resp.IngressID, URL: resp.URL, StreamKey: resp.StreamKey}, nil
}

func (c *livekitClient) deleteIngress(ctx context.Context, ingressID string) error {
	return c.call(ctx, "IngressService", "DeleteIngress", ingressAdminGrants(), map[string]any{"ingress_id": ingressID}, nil)
}

// ---- egress ---------------------------------------------------------------

func (c *livekitClient) startRoomCompositeEgress(ctx context.Context, room string, rtmpURLs []string, filePath, layout string) (string, error) {
	if layout == "" {
		layout = "default"
	}
	body := map[string]any{"room_name": room, "layout": layout}
	if len(rtmpURLs) > 0 {
		body["stream_outputs"] = []map[string]any{{
			"protocol": 0, // StreamProtocol.RTMP
			"urls":     rtmpURLs,
		}}
	}
	if filePath != "" {
		body["file_outputs"] = []map[string]any{{"filepath": filePath}}
	}
	var resp struct {
		EgressID string `json:"egress_id"`
	}
	if err := c.call(ctx, "EgressService", "StartRoomCompositeEgress", roomRecordGrants(), body, &resp); err != nil {
		return "", err
	}
	return resp.EgressID, nil
}

type hlsEgressResult struct {
	EgressID       string
	PlaylistName   string
	FilenamePrefix string
}

func (c *livekitClient) startRoomCompositeHLSEgress(ctx context.Context, room, playlistName, filePath string, segmentDuration uint32, layout string) (*hlsEgressResult, error) {
	if layout == "" {
		layout = "default"
	}
	if segmentDuration == 0 {
		segmentDuration = 6
	}
	body := map[string]any{
		"room_name": room,
		"layout":    layout,
		"segment_outputs": []map[string]any{{
			"playlist_name":    playlistName,
			"filename_prefix":  filePath,
			"segment_duration": segmentDuration,
		}},
	}
	var resp struct {
		EgressID string `json:"egress_id"`
	}
	if err := c.call(ctx, "EgressService", "StartRoomCompositeEgress", roomRecordGrants(), body, &resp); err != nil {
		return nil, err
	}
	return &hlsEgressResult{EgressID: resp.EgressID, PlaylistName: playlistName, FilenamePrefix: filePath}, nil
}

func (c *livekitClient) stopEgress(ctx context.Context, egressID string) error {
	return c.call(ctx, "EgressService", "StopEgress", roomRecordGrants(), map[string]any{"egress_id": egressID}, nil)
}
