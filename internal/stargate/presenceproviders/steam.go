package presenceproviders

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/store"
)

// SteamPresenceScanResult mirrors SteamPresenceService.SteamPresenceScanResult.
type SteamPresenceScanResult struct {
	Items []SteamPresenceScanItem `json:"items"`
}

// SteamPresenceScanItem mirrors SteamPresenceService.SteamPresenceScanItem.
type SteamPresenceScanItem struct {
	AccountID string `json:"account_id"`
	SteamID   string `json:"steam_id"`
	Status    string `json:"status"`
	GameID    string `json:"game_id,omitempty"`
	GameName  string `json:"game_name,omitempty"`
	Raw       any    `json:"raw,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Status strings preserved verbatim from the C# service.
const (
	steamStatusUpdated       = "updated"
	steamStatusRemoved       = "removed"
	steamStatusNotFound      = "not_found"
	steamStatusMissingAPIKey = "missing_api_key"
	steamStatusError         = "error"
)

// steamPlayer is the GetPlayerSummaries player object reduced to the fields
// the scanner reads. Raw keeps the original object for the result payload.
type steamPlayer struct {
	SteamID       string         `json:"steamid"`
	PersonaName   string         `json:"personaname"`
	ProfileURL    string         `json:"profileurl"`
	Avatar        string         `json:"avatar"`
	AvatarMedium  string         `json:"avatarmedium"`
	AvatarFull    string         `json:"avatarfull"`
	GameID        string         `json:"gameid"`
	GameExtraInfo string         `json:"gameextrainfo"`
	Raw           map[string]any `json:"-"`
}

type steamPlayerSummariesResponse struct {
	Response struct {
		Players []json.RawMessage `json:"players"`
	} `json:"response"`
}

// ScanAllSteam scans every account with a Steam connection (the
// POST /presences/steam/scan route).
func (s *Service) ScanAllSteam(ctx context.Context) (SteamPresenceScanResult, error) {
	ids, err := s.connectedAccountIDs(ctx, []string{ServiceSteam})
	if err != nil {
		return SteamPresenceScanResult{}, err
	}
	return s.ScanSteamUsers(ctx, ids)
}

// ScanSteamStage scans the Steam-connected accounts selected by stage
// (POST /presences/steam/scan/stages/:stage).
func (s *Service) ScanSteamStage(ctx context.Context, stage Stage) (SteamPresenceScanResult, error) {
	ids, err := s.accountsForStage(ctx, stage, []string{ServiceSteam})
	if err != nil {
		return SteamPresenceScanResult{}, err
	}
	return s.ScanSteamUsers(ctx, ids)
}

// ScanSteamBySteamID resolves the account that owns a Steam connection and
// scans it (POST /presences/steam/scan-by-steam-id/:steamId). ErrAccountNotFound
// when no connection carries the identifier.
func (s *Service) ScanSteamBySteamID(ctx context.Context, steamID string) (SteamPresenceScanResult, error) {
	steamID = strings.TrimSpace(steamID)
	if steamID == "" {
		return SteamPresenceScanResult{}, ErrAccountNotFound
	}
	connection, err := s.store.GetConnectionByProviderAndIdentifier(ctx, ServiceSteam, steamID)
	if err != nil {
		if err == store.ErrNotFound {
			return SteamPresenceScanResult{}, ErrAccountNotFound
		}
		return SteamPresenceScanResult{}, err
	}
	if connection.DeletedAt != nil {
		return SteamPresenceScanResult{}, ErrAccountNotFound
	}
	accountID, err := uuid.Parse(connection.AccountId)
	if err != nil {
		return SteamPresenceScanResult{}, ErrAccountNotFound
	}
	return s.ScanSteamUsers(ctx, []uuid.UUID{accountID})
}

// ScanSteamUsers is ScanAndUpdatePresencesAsync: it reads the Steam
// connections of accountIDs and scans them as one batch, falling back to
// per-connection requests when the batch call fails (exactly like the C#).
func (s *Service) ScanSteamUsers(ctx context.Context, accountIDs []uuid.UUID) (SteamPresenceScanResult, error) {
	connections, err := s.steamConnections(ctx, accountIDs)
	if err != nil {
		return SteamPresenceScanResult{}, err
	}
	result := SteamPresenceScanResult{Items: []SteamPresenceScanItem{}}
	if len(connections) == 0 {
		return result, nil
	}

	apiKey := strings.TrimSpace(s.settings.SteamAPIKey)
	if apiKey == "" {
		s.log.Warn("Steam API key not configured, skipping presence update", "count", len(connections))
		result.Items = append(result.Items, missingSteamKeyItems(connections)...)
		return result, nil
	}

	valid := make([]providerConnection, 0, len(connections))
	for _, connection := range connections {
		if _, err := strconv.ParseUint(strings.TrimSpace(connection.ProvidedIdentifier), 10, 64); err != nil {
			// C# ulong.Parse throws for the whole batch, then each connection
			// retries individually and fails alone.
			result.Items = append(result.Items, s.scanSingleSteam(ctx, apiKey, connection))
			continue
		}
		valid = append(valid, connection)
	}

	if len(valid) > 0 {
		ids := make([]string, 0, len(valid))
		for _, connection := range valid {
			ids = append(ids, strings.TrimSpace(connection.ProvidedIdentifier))
		}
		players, batchErr := s.fetchSteamPlayers(ctx, apiKey, ids)
		if batchErr != nil {
			s.log.Error("failed to batch-update steam presence", "count", len(valid), "error", batchErr)
			for _, connection := range valid {
				result.Items = append(result.Items, s.scanSingleSteam(ctx, apiKey, connection))
			}
		} else {
			for _, connection := range valid {
				player, ok := players[strings.TrimSpace(connection.ProvidedIdentifier)]
				if !ok {
					_ = s.endSession(ctx, connection.AccountID, ServiceSteam)
					result.Items = append(result.Items, SteamPresenceScanItem{
						AccountID: connection.AccountID.String(),
						SteamID:   connection.ProvidedIdentifier,
						Status:    steamStatusNotFound,
					})
					continue
				}
				result.Items = append(result.Items, s.updateSteamPresenceFromData(ctx, connection, player))
			}
		}
	}
	return result, nil
}

func missingSteamKeyItems(connections []providerConnection) []SteamPresenceScanItem {
	items := make([]SteamPresenceScanItem, 0, len(connections))
	for _, connection := range connections {
		items = append(items, SteamPresenceScanItem{
			AccountID: connection.AccountID.String(),
			SteamID:   connection.ProvidedIdentifier,
			Status:    steamStatusMissingAPIKey,
			Error:     "Steam API key not configured",
		})
	}
	return items
}

// scanSingleSteam is UpdateSteamPresenceAsync: one player summary request and
// its isolated error handling.
func (s *Service) scanSingleSteam(ctx context.Context, apiKey string, connection providerConnection) SteamPresenceScanItem {
	player, err := s.fetchSteamPlayer(ctx, apiKey, strings.TrimSpace(connection.ProvidedIdentifier))
	if err != nil {
		_ = s.endSession(ctx, connection.AccountID, ServiceSteam)
		s.log.Error("failed to update steam presence",
			"account", connection.AccountID, "steam_id", connection.ProvidedIdentifier, "error", err)
		return SteamPresenceScanItem{
			AccountID: connection.AccountID.String(),
			SteamID:   connection.ProvidedIdentifier,
			Status:    steamStatusError,
			Error:     err.Error(),
		}
	}
	return s.updateSteamPresenceFromData(ctx, connection, player)
}

// updateSteamPresenceFromData is UpdateSteamPresenceFromDataAsync: an active
// game starts (or refreshes) the "steam" session, otherwise the session ends.
func (s *Service) updateSteamPresenceFromData(ctx context.Context, connection providerConnection, player steamPlayer) SteamPresenceScanItem {
	gameID := strings.TrimSpace(player.GameID)
	gameName := strings.TrimSpace(player.GameExtraInfo)
	item := SteamPresenceScanItem{
		AccountID: connection.AccountID.String(),
		SteamID:   connection.ProvidedIdentifier,
		Raw:       player.Raw,
	}
	if gameID == "" || gameName == "" {
		_ = s.endSession(ctx, connection.AccountID, ServiceSteam)
		item.Status = steamStatusRemoved
		return item
	}

	spec := steamSessionSpec(connection.AccountID, player, gameID, gameName, s.now())
	if err := s.startSession(ctx, connection.AccountID, ServiceSteam, spec); err != nil {
		s.log.Error("failed to start steam presence session",
			"account", connection.AccountID, "steam_id", connection.ProvidedIdentifier, "error", err)
		item.Status = steamStatusError
		item.Error = err.Error()
		item.GameID = gameID
		item.GameName = gameName
		return item
	}
	item.Status = steamStatusUpdated
	item.GameID = gameID
	item.GameName = gameName
	return item
}

// steamSessionSpec ports ParsePlayerSummaryToPresenceActivity plus the
// StartSessionAsync request the C# service builds from it.
func steamSessionSpec(accountID uuid.UUID, player steamPlayer, gameID, gameName string, now time.Time) sessionSpec {
	profileURL := fmt.Sprintf("https://steamcommunity.com/profiles/%s", player.SteamID)
	titleURL := fmt.Sprintf("https://store.steampowered.com/app/%s", gameID)
	meta := map[string]any{
		"game_id":           gameID,
		"steam_profile_url": profileURL,
		"updated_at":        model.NewTime(now),
	}
	return sessionSpec{
		Type:           "gaming",
		Tags:           []model.PresenceTag{{Slug: "gaming", Name: trimmedPtr("Gaming")}},
		Provider:       ServiceSteam,
		ReferenceID:    trimmedPtr(gameID),
		Title:          trimmedPtr(gameName),
		Subtitle:       trimmedPtr("Playing on Steam"),
		TitleURL:       trimmedPtr(titleURL),
		SubtitleURL:    trimmedPtr(profileURL),
		QueryableTerms: []string{"steam", strings.ToLower(strings.TrimSpace(gameName)), strings.ToLower(strings.TrimSpace(gameID))},
		Meta:           meta,
		CatalogKey:     gameID,
		CatalogName:    trimmedPtr(gameName),
	}
}

// fetchSteamPlayers calls ISteamUser/GetPlayerSummaries/v2 for up to 100 ids
// (the API's own cap; the scanners' stage batches stay well below it).
func (s *Service) fetchSteamPlayers(ctx context.Context, apiKey string, steamIDs []string) (map[string]steamPlayer, error) {
	if len(steamIDs) == 0 {
		return map[string]steamPlayer{}, nil
	}
	players := make(map[string]steamPlayer, len(steamIDs))
	for start := 0; start < len(steamIDs); start += 100 {
		end := min(start+100, len(steamIDs))
		batch, err := s.fetchSteamPlayersBatch(ctx, apiKey, steamIDs[start:end])
		if err != nil {
			return nil, err
		}
		for key, player := range batch {
			players[key] = player
		}
	}
	return players, nil
}

func (s *Service) fetchSteamPlayersBatch(ctx context.Context, apiKey string, steamIDs []string) (map[string]steamPlayer, error) {
	endpoint := s.steamBaseURL + "/ISteamUser/GetPlayerSummaries/v2/"
	query := url.Values{}
	query.Set("key", apiKey)
	query.Set("steamids", strings.Join(steamIDs, ","))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	response, err := s.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("steam GetPlayerSummaries returned HTTP %d", response.StatusCode)
	}
	var payload steamPlayerSummariesResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode steam GetPlayerSummaries: %w", err)
	}
	players := make(map[string]steamPlayer, len(payload.Response.Players))
	for _, raw := range payload.Response.Players {
		var player steamPlayer
		if err := json.Unmarshal(raw, &player); err != nil {
			continue
		}
		var rawMap map[string]any
		if err := json.Unmarshal(raw, &rawMap); err == nil {
			player.Raw = rawMap
		}
		players[player.SteamID] = player
	}
	return players, nil
}

// fetchSteamPlayer is the individual API call (GetPlayerSummaryAsync).
func (s *Service) fetchSteamPlayer(ctx context.Context, apiKey, steamID string) (steamPlayer, error) {
	players, err := s.fetchSteamPlayers(ctx, apiKey, []string{steamID})
	if err != nil {
		return steamPlayer{}, err
	}
	player, ok := players[steamID]
	if !ok {
		return steamPlayer{}, fmt.Errorf("steam player %s not found", steamID)
	}
	return player, nil
}
