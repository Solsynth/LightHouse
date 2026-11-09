package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

// AniListProvider resolves Anime and Manga metadata using the AniList GraphQL API.
type AniListProvider struct {
	client    *http.Client
	endpoint  string
	userAgent string
}

func NewAniListProvider(endpoint, userAgent string) *AniListProvider {
	if endpoint == "" {
		endpoint = "https://graphql.anilist.co"
	}
	if userAgent == "" {
		userAgent = "Pulsar/1.0 (Solar Network)"
	}
	return &AniListProvider{
		client:    &http.Client{Timeout: 8 * time.Second},
		endpoint:  endpoint,
		userAgent: userAgent,
	}
}

func (p *AniListProvider) Name() string   { return "anilist" }
func (p *AniListProvider) Domain() string { return model.DomainAnime }

const aniListMediaFragment = `
query ($id: Int, $search: String) {
  Media (id: $id, search: $search, type: ANIME) {
    id
    idMal
    title {
      romaji
      english
      native
    }
    format
    status
    description(asHtml: false)
    seasonYear
    episodes
    duration
    genres
    bannerImage
    coverImage {
      extraLarge
      large
    }
    siteUrl
    studios(isMain: true) {
      nodes {
        name
      }
    }
  }
}
`

type aniListResponse struct {
	Data struct {
		Media *struct {
			ID          int    `json:"id"`
			IDMal       *int   `json:"idMal"`
			Format      string `json:"format"`
			Status      string `json:"status"`
			Description string `json:"description"`
			SeasonYear  *int   `json:"seasonYear"`
			Episodes    *int   `json:"episodes"`
			Duration    *int   `json:"duration"`
			Genres      []string `json:"genres"`
			BannerImage string `json:"bannerImage"`
			CoverImage  struct {
				ExtraLarge string `json:"extraLarge"`
				Large      string `json:"large"`
			} `json:"coverImage"`
			SiteURL string `json:"siteUrl"`
			Title   struct {
				Romaji  string `json:"romaji"`
				English string `json:"english"`
				Native  string `json:"native"`
			} `json:"title"`
			Studios struct {
				Nodes []struct {
					Name string `json:"name"`
				} `json:"nodes"`
			} `json:"studios"`
		} `json:"Media"`
	} `json:"data"`
}

func (p *AniListProvider) FetchByProviderKey(ctx context.Context, idStr string) (*UnifiedMedia, error) {
	idStr = strings.TrimSpace(idStr)
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return nil, fmt.Errorf("invalid anilist id: %s", idStr)
	}

	reqBody := map[string]any{
		"query": aniListMediaFragment,
		"variables": map[string]any{
			"id": id,
		},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", p.userAgent)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anilist api returned status %d", resp.StatusCode)
	}

	var res aniListResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	media := res.Data.Media
	if media == nil {
		return nil, fmt.Errorf("anilist media %d not found", id)
	}

	title := media.Title.English
	if title == "" {
		title = media.Title.Romaji
	}
	if title == "" {
		title = media.Title.Native
	}

	subtitle := ""
	if len(media.Studios.Nodes) > 0 {
		subtitle = media.Studios.Nodes[0].Name
	}

	caption := media.Format
	if media.SeasonYear != nil {
		caption = fmt.Sprintf("%s (%d)", media.Format, *media.SeasonYear)
	}

	coverArt := media.CoverImage.ExtraLarge
	if coverArt == "" {
		coverArt = media.CoverImage.Large
	}

	durationSec := 0
	if media.Duration != nil {
		durationSec = *media.Duration * 60
	}

	extIDs := map[string]string{
		"anilist": strconv.Itoa(media.ID),
	}
	if media.IDMal != nil {
		extIDs["myanimelist"] = strconv.Itoa(*media.IDMal)
	}

	links := []ExternalLink{
		{
			Platform: "anilist",
			Label:    "AniList",
			URL:      media.SiteURL,
		},
	}
	if media.IDMal != nil {
		links = append(links, ExternalLink{
			Platform: "myanimelist",
			Label:    "MyAnimeList",
			URL:      fmt.Sprintf("https://myanimelist.net/anime/%d", *media.IDMal),
		})
	}

	return &UnifiedMedia{
		URN:             fmt.Sprintf("pulsar:anime:anilist:%d", media.ID),
		Domain:          model.DomainAnime,
		Slug:            fmt.Sprintf("anilist-%d", media.ID),
		Title:           title,
		Subtitle:        subtitle,
		Caption:         caption,
		Description:     media.Description,
		ArtworkURL:      coverArt,
		BannerURL:       media.BannerImage,
		DurationSeconds: durationSec,
		Genres:          media.Genres,
		ExternalIDs:     extIDs,
		ExternalLinks:   links,
	}, nil
}

func (p *AniListProvider) Search(ctx context.Context, query string, limit int) ([]UnifiedMedia, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}

	reqBody := map[string]any{
		"query": aniListMediaFragment,
		"variables": map[string]any{
			"search": strings.TrimSpace(query),
		},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", p.userAgent)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anilist api returned status %d", resp.StatusCode)
	}

	var res aniListResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	media := res.Data.Media
	if media == nil {
		return nil, nil
	}

	title := media.Title.English
	if title == "" {
		title = media.Title.Romaji
	}

	coverArt := media.CoverImage.ExtraLarge
	if coverArt == "" {
		coverArt = media.CoverImage.Large
	}

	return []UnifiedMedia{
		{
			URN:        fmt.Sprintf("pulsar:anime:anilist:%d", media.ID),
			Domain:     model.DomainAnime,
			Slug:       fmt.Sprintf("anilist-%d", media.ID),
			Title:      title,
			Caption:    media.Format,
			ArtworkURL: coverArt,
			BannerURL:  media.BannerImage,
			ExternalIDs: map[string]string{
				"anilist": strconv.Itoa(media.ID),
			},
			ExternalLinks: []ExternalLink{
				{
					Platform: "anilist",
					Label:    "AniList",
					URL:      media.SiteURL,
				},
			},
		},
	}, nil
}
