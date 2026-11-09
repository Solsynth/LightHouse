package metadata

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

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

// SteamProvider fetches game metadata from the official Steam Store and SteamDB.
type SteamProvider struct {
	client    *http.Client
	baseURL   string
	userAgent string
}

func NewSteamProvider(baseURL, userAgent string) *SteamProvider {
	if baseURL == "" {
		baseURL = "https://store.steampowered.com"
	}
	if userAgent == "" {
		userAgent = "Pulsar/1.0 (Solar Network)"
	}
	return &SteamProvider{
		client:    &http.Client{Timeout: 8 * time.Second},
		baseURL:   strings.TrimRight(baseURL, "/"),
		userAgent: userAgent,
	}
}

func (p *SteamProvider) Name() string   { return "steam" }
func (p *SteamProvider) Domain() string { return model.DomainGaming }

type steamAppDetailsResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Name             string   `json:"name"`
		SteamAppID       int      `json:"steam_appid"`
		ShortDescription string   `json:"short_description"`
		HeaderImage      string   `json:"header_image"`
		Background       string   `json:"background"`
		BackgroundRaw    string   `json:"background_raw"`
		Developers       []string `json:"developers"`
		Publishers       []string `json:"publishers"`
		ReleaseDate      struct {
			ComingSoon bool   `json:"coming_soon"`
			Date       string `json:"date"`
		} `json:"release_date"`
		Genres []struct {
			ID          string `json:"id"`
			Description string `json:"description"`
		} `json:"genres"`
	} `json:"data"`
}

func (p *SteamProvider) FetchByProviderKey(ctx context.Context, appIDStr string) (*UnifiedMedia, error) {
	appIDStr = strings.TrimSpace(appIDStr)
	appID, err := strconv.Atoi(appIDStr)
	if err != nil {
		return nil, fmt.Errorf("invalid steam appid: %s", appIDStr)
	}

	reqURL := fmt.Sprintf("%s/api/appdetails?appids=%d&l=english", p.baseURL, appID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", p.userAgent)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("steam api returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var root map[string]steamAppDetailsResponse
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, err
	}

	appResp, ok := root[strconv.Itoa(appID)]
	if !ok || !appResp.Success {
		return nil, fmt.Errorf("steam app %d not found or details unavailable", appID)
	}

	data := appResp.Data
	subtitle := ""
	if len(data.Developers) > 0 {
		subtitle = strings.Join(data.Developers, ", ")
	} else if len(data.Publishers) > 0 {
		subtitle = strings.Join(data.Publishers, ", ")
	}

	banner := data.BackgroundRaw
	if banner == "" {
		banner = data.Background
	}

	genres := make([]string, 0, len(data.Genres))
	for _, g := range data.Genres {
		if g.Description != "" {
			genres = append(genres, g.Description)
		}
	}

	return &UnifiedMedia{
		URN:         fmt.Sprintf("pulsar:gaming:steam:%d", appID),
		Domain:      model.DomainGaming,
		Slug:        fmt.Sprintf("steam-%d", appID),
		Title:       data.Name,
		Subtitle:    subtitle,
		Caption:     "PC / Steam",
		Description: data.ShortDescription,
		ArtworkURL:  data.HeaderImage,
		BannerURL:   banner,
		ReleaseDate: data.ReleaseDate.Date,
		Genres:      genres,
		ExternalIDs: map[string]string{
			"steam": strconv.Itoa(appID),
		},
		ExternalLinks: []ExternalLink{
			{
				Platform: "steam",
				Label:    "Steam Store",
				URL:      fmt.Sprintf("https://store.steampowered.com/app/%d", appID),
			},
			{
				Platform: "steamdb",
				Label:    "SteamDB",
				URL:      fmt.Sprintf("https://steamdb.info/app/%d", appID),
			},
		},
	}, nil
}

type steamStoreSearchResponse struct {
	Total int `json:"total"`
	Items []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
		TinyImage string `json:"tiny_image"`
	} `json:"items"`
}

func (p *SteamProvider) Search(ctx context.Context, query string, limit int) ([]UnifiedMedia, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}

	reqURL := fmt.Sprintf("%s/api/storesearch/?term=%s&l=english&cc=US", p.baseURL, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", p.userAgent)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("steam search returned status %d", resp.StatusCode)
	}

	var searchResp steamStoreSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, err
	}

	if limit <= 0 || limit > 20 {
		limit = 10
	}
	if len(searchResp.Items) < limit {
		limit = len(searchResp.Items)
	}

	results := make([]UnifiedMedia, 0, limit)
	for i := 0; i < limit; i++ {
		item := searchResp.Items[i]
		results = append(results, UnifiedMedia{
			URN:        fmt.Sprintf("pulsar:gaming:steam:%d", item.ID),
			Domain:     model.DomainGaming,
			Slug:       fmt.Sprintf("steam-%d", item.ID),
			Title:      item.Name,
			Caption:    "PC / Steam",
			ArtworkURL: item.TinyImage,
			ExternalIDs: map[string]string{
				"steam": strconv.Itoa(item.ID),
			},
			ExternalLinks: []ExternalLink{
				{
					Platform: "steam",
					Label:    "Steam Store",
					URL:      fmt.Sprintf("https://store.steampowered.com/app/%d", item.ID),
				},
			},
		})
	}
	return results, nil
}
