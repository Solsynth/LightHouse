package presenceproviders

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/model"
)

// LastFMPresenceScanResult mirrors LastFmPresenceService.LastFmPresenceScanResult.
type LastFMPresenceScanResult struct {
	Items []LastFMPresenceScanItem `json:"items"`
}

// LastFMPresenceScanItem mirrors LastFmPresenceService.LastFmPresenceScanItem.
type LastFMPresenceScanItem struct {
	AccountID string `json:"account_id"`
	Username  string `json:"username"`
	Status    string `json:"status"`
	Track     string `json:"track,omitempty"`
	Artist    string `json:"artist,omitempty"`
	Error     string `json:"error,omitempty"`
}

const (
	lastFMStatusUpdated       = "updated"
	lastFMStatusRemoved       = "removed"
	lastFMStatusMissingAPIKey = "missing_api_key"
	lastFMStatusError         = "error"

	lastFMRecentTracksMethod = "user.getRecentTracks"
	// Last.fm throttles an IP to five requests per second. The spacing is
	// process-wide (not per scan) because all three stages share the IP.
	lastFMRequestSpacing = 250 * time.Millisecond
)

// lastFMThrottle serializes request starts across the whole process and spaces
// them by lastFMRequestSpacing, like the C# static RequestGate/_nextRequestAt.
var lastFMThrottle struct {
	mu   sync.Mutex
	next time.Time
}

// ScanLastFMUsers is ScanAndUpdatePresencesAsync for the given accounts.
func (s *Service) ScanLastFMUsers(ctx context.Context, accountIDs []uuid.UUID) (LastFMPresenceScanResult, error) {
	connections, err := s.lastFMConnections(ctx, accountIDs)
	if err != nil {
		return LastFMPresenceScanResult{}, err
	}
	result := LastFMPresenceScanResult{Items: []LastFMPresenceScanItem{}}
	if len(connections) == 0 {
		return result, nil
	}

	apiKey := strings.TrimSpace(s.settings.LastFMAPIKey)
	if apiKey == "" {
		s.log.Warn("Last.fm API key not configured, skipping presence update", "count", len(connections))
		for _, connection := range connections {
			result.Items = append(result.Items, LastFMPresenceScanItem{
				AccountID: connection.AccountID.String(),
				Username:  connection.ProvidedIdentifier,
				Status:    lastFMStatusMissingAPIKey,
				Error:     "Last.fm API key not configured",
			})
		}
		return result, nil
	}

	apiSecret := strings.TrimSpace(s.settings.LastFMAPISecret)
	for _, connection := range connections {
		result.Items = append(result.Items, s.scanLastFMConnection(ctx, apiKey, apiSecret, connection))
	}
	return result, nil
}

// scanLastFMConnection is ScanConnectionAsync: one now-playing lookup, then a
// session start or end. A request failure never clears the live session.
func (s *Service) scanLastFMConnection(ctx context.Context, apiKey, apiSecret string, connection providerConnection) LastFMPresenceScanItem {
	username := connection.ProvidedIdentifier
	track, err := s.getNowPlaying(ctx, apiKey, apiSecret, connection)
	if err != nil {
		s.log.Error("failed to update lastfm presence",
			"account", connection.AccountID, "username", username, "error", err)
		return LastFMPresenceScanItem{
			AccountID: connection.AccountID.String(),
			Username:  username,
			Status:    lastFMStatusError,
			Error:     err.Error(),
		}
	}
	if track == nil {
		if err := s.endSession(ctx, connection.AccountID, ServiceLastFM); err != nil {
			s.log.Warn("failed to end lastfm presence session",
				"account", connection.AccountID, "username", username, "error", err)
		}
		return LastFMPresenceScanItem{
			AccountID: connection.AccountID.String(),
			Username:  username,
			Status:    lastFMStatusRemoved,
		}
	}

	if err := s.startSession(ctx, connection.AccountID, ServiceLastFM, lastFMSessionSpec(username, *track, s.now())); err != nil {
		s.log.Error("failed to start lastfm presence session",
			"account", connection.AccountID, "username", username, "error", err)
		return LastFMPresenceScanItem{
			AccountID: connection.AccountID.String(),
			Username:  username,
			Status:    lastFMStatusError,
			Error:     err.Error(),
			Track:     track.Name,
			Artist:    track.Artist,
		}
	}
	return LastFMPresenceScanItem{
		AccountID: connection.AccountID.String(),
		Username:  username,
		Status:    lastFMStatusUpdated,
		Track:     track.Name,
		Artist:    track.Artist,
	}
}

// getNowPlaying tries the public feed and, on Last.fm error 17 (Login
// required), retries with the connection's web-service session key when the
// connection stores one and a shared secret is configured.
func (s *Service) getNowPlaying(ctx context.Context, apiKey, apiSecret string, connection providerConnection) (*lastFMTrack, error) {
	track, err := s.requestNowPlaying(ctx, apiKey, connection.ProvidedIdentifier, "", "")
	var apiErr *lastFMAPIError
	if err != nil && asLastFMAPIError(err, &apiErr) && apiErr.Code == 17 &&
		connection.AccessToken != nil && strings.TrimSpace(*connection.AccessToken) != "" &&
		apiSecret != "" {
		return s.requestNowPlaying(ctx, apiKey, connection.ProvidedIdentifier, *connection.AccessToken, apiSecret)
	}
	return track, err
}

// requestNowPlaying performs one throttled user.getRecentTracks call. A nil
// track means "not now playing".
func (s *Service) requestNowPlaying(ctx context.Context, apiKey, username, sessionKey, apiSecret string) (*lastFMTrack, error) {
	parameters := map[string]string{
		"api_key": apiKey,
		"limit":   "1",
		"method":  lastFMRecentTracksMethod,
		"user":    username,
	}
	if strings.TrimSpace(sessionKey) != "" {
		parameters["sk"] = sessionKey
	}
	if strings.TrimSpace(sessionKey) != "" && apiSecret != "" {
		parameters["api_sig"] = signLastFMParams(apiSecret, parameters)
	}
	values := make(url.Values, len(parameters)+1)
	for key, value := range parameters {
		values.Set(key, value)
	}
	values.Set("format", "json")

	if err := throttleLastFM(ctx); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.lastFMBaseURL+"?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	response, err := s.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	track, apiErr, err := parseLastFMNowPlaying(raw)
	if err != nil {
		return nil, fmt.Errorf("lastfm %s returned HTTP %d: %s", lastFMRecentTracksMethod, response.StatusCode, truncate(string(raw), 512))
	}
	if apiErr != nil {
		return nil, apiErr
	}
	return track, nil
}

func throttleLastFM(ctx context.Context) error {
	lastFMThrottle.mu.Lock()
	defer lastFMThrottle.mu.Unlock()
	if wait := time.Until(lastFMThrottle.next); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	lastFMThrottle.next = time.Now().Add(lastFMRequestSpacing)
	return nil
}

// signLastFMParams is the Last.fm signature: alphabetically ordered
// name+value pairs, shared secret appended, md5 lowercase hex.
func signLastFMParams(secret string, parameters map[string]string) string {
	keys := make([]string, 0, len(parameters))
	for key := range parameters {
		if key == "format" || key == "callback" || key == "api_sig" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(key)
		builder.WriteString(parameters[key])
	}
	builder.WriteString(secret)
	sum := md5.Sum([]byte(builder.String()))
	return hex.EncodeToString(sum[:])
}

// lastFMTrack is the now-playing subset the scanner persists.
type lastFMTrack struct {
	Name     string
	Artist   string
	Album    string
	MBID     string
	URL      string
	ImageURL string
}

// lastFMSessionSpec ports StartListeningSessionAsync.
func lastFMSessionSpec(username string, track lastFMTrack, now time.Time) sessionSpec {
	catalogKey := strings.TrimSpace(track.MBID)
	if catalogKey == "" {
		catalogKey = track.Artist + " - " + track.Name
	}
	profileURL := "https://www.last.fm/user/" + username
	terms := []string{ServiceLastFM, strings.ToLower(track.Artist), strings.ToLower(track.Name)}
	if strings.TrimSpace(track.Album) != "" {
		terms = append(terms, strings.ToLower(track.Album))
	}
	meta := map[string]any{
		"artist":             track.Artist,
		"album":              nilIfEmpty(track.Album),
		"mbid":               nilIfEmpty(track.MBID),
		"url":                nilIfEmpty(track.URL),
		"lastfm_profile_url": profileURL,
		"updated_at":         model.NewTime(now),
	}
	return sessionSpec{
		Type:           "listening",
		Tags:           []model.PresenceTag{{Slug: "music", Name: trimmedPtr("Music")}},
		Provider:       ServiceLastFM,
		ReferenceID:    trimmedPtr(catalogKey),
		Title:          trimmedPtr(track.Name),
		Subtitle:       trimmedPtr(track.Artist),
		Caption:        trimmedPtr(track.Album),
		LargeImage:     trimmedPtr(track.ImageURL),
		TitleURL:       trimmedPtr(track.URL),
		SubtitleURL:    trimmedPtr("https://www.last.fm/music/" + url.PathEscape(track.Artist)),
		QueryableTerms: terms,
		Meta:           meta,
		CatalogKey:     catalogKey,
		CatalogName:    trimmedPtr(track.Name),
	}
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// ─────────────────────────── parsing ───────────────────────────

// lastFMAPIError is LastFmApiException: a Last.fm error object.
type lastFMAPIError struct {
	Code    int
	Message string
}

func (e *lastFMAPIError) Error() string {
	return fmt.Sprintf("lastfm %s error %d: %s", lastFMRecentTracksMethod, e.Code, e.Message)
}

func asLastFMAPIError(err error, target **lastFMAPIError) bool {
	apiErr, ok := err.(*lastFMAPIError)
	if ok {
		*target = apiErr
	}
	return ok
}

type flexText string

// UnmarshalJSON accepts string, number or null, mirroring the C# GetText
// helper (strings verbatim, numbers via their raw text).
func (f *flexText) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*f = ""
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*f = flexText(text)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err == nil {
		*f = flexText(number.String())
		return nil
	}
	return fmt.Errorf("unsupported text value %s", trimmed)
}

type lastFMTrackJSON struct {
	Name   flexText        `json:"name"`
	Artist json.RawMessage `json:"artist"`
	Album  json.RawMessage `json:"album"`
	MBID   flexText        `json:"mbid"`
	URL    flexText        `json:"url"`
	Image  []struct {
		Text flexText `json:"#text"`
		Size string   `json:"size"`
	} `json:"image"`
	Attr *struct {
		NowPlaying any `json:"nowplaying"`
	} `json:"@attr"`
}

// parseLastFMNowPlaying decodes user.getRecentTracks. A nil track with no
// error means the first scrobble is not now playing.
func parseLastFMNowPlaying(raw []byte) (*lastFMTrack, *lastFMAPIError, error) {
	var envelope struct {
		RecentTracks struct {
			Track json.RawMessage `json:"track"`
		} `json:"recenttracks"`
		Error   json.RawMessage `json:"error"`
		Message flexText        `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, nil, err
	}
	if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		return nil, &lastFMAPIError{Code: parseErrorCode(envelope.Error), Message: string(envelope.Message)}, nil
	}

	trackJSON, ok := firstTrackObject(envelope.RecentTracks.Track)
	if !ok {
		return nil, nil, nil
	}
	var element lastFMTrackJSON
	if err := json.Unmarshal(trackJSON, &element); err != nil {
		return nil, nil, err
	}
	if !isNowPlaying(element.Attr) || strings.TrimSpace(string(element.Name)) == "" {
		return nil, nil, nil
	}
	artist := textValue(element.Artist)
	if strings.TrimSpace(artist) == "" {
		artist = "Unknown Artist"
	}
	return &lastFMTrack{
		Name:     string(element.Name),
		Artist:   artist,
		Album:    textValue(element.Album),
		MBID:     string(element.MBID),
		URL:      string(element.URL),
		ImageURL: largestImage(element.Image),
	}, nil, nil
}

func parseErrorCode(raw json.RawMessage) int {
	var number int
	if err := json.Unmarshal(raw, &number); err == nil {
		return number
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if parsed, err := strconv.Atoi(text); err == nil {
			return parsed
		}
	}
	return 0
}

// firstTrackObject handles `limit=1`: Last.fm returns an array normally but a
// bare object when there is a single scrobble.
func firstTrackObject(raw json.RawMessage) ([]byte, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, false
	}
	if strings.HasPrefix(trimmed, "[") {
		var tracks []json.RawMessage
		if err := json.Unmarshal(raw, &tracks); err != nil || len(tracks) == 0 {
			return nil, false
		}
		return tracks[0], true
	}
	return raw, true
}

func isNowPlaying(attr *struct {
	NowPlaying any `json:"nowplaying"`
}) bool {
	if attr == nil {
		return false
	}
	switch value := attr.NowPlaying.(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(value, "true")
	default:
		return false
	}
}

// textValue reads a string or a nested {"#text": ...} / {"name": ...} object
// (Last.fm's artist/album shape), mirroring GetNestedText.
func textValue(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}
	for _, key := range []string{"#text", "name"} {
		if nested, ok := object[key]; ok {
			var value flexText
			if err := value.UnmarshalJSON(nested); err == nil && string(value) != "" {
				return string(value)
			}
		}
	}
	return ""
}

func largestImage(images []struct {
	Text flexText `json:"#text"`
	Size string   `json:"size"`
}) string {
	var largest string
	for _, image := range images {
		if text := strings.TrimSpace(string(image.Text)); text != "" {
			largest = text
		}
	}
	return largest
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
