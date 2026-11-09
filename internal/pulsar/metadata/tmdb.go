package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

// TMDbProvider resolves Movie and TV show metadata from TMDb and IMDb.
type TMDbProvider struct {
	client    *http.Client
	apiKey    string
	baseURL   string
	userAgent string
}

func NewTMDbProvider(apiKey, baseURL, userAgent string) *TMDbProvider {
	if baseURL == "" {
		baseURL = "https://api.themoviedb.org/3"
	}
	if userAgent == "" {
		userAgent = "Pulsar/1.0 (Solar Network)"
	}
	return &TMDbProvider{
		client:    &http.Client{Timeout: 8 * time.Second},
		apiKey:    apiKey,
		baseURL:   strings.TrimRight(baseURL, "/"),
		userAgent: userAgent,
	}
}

func (p *TMDbProvider) Name() string   { return "tmdb" }
func (p *TMDbProvider) Domain() string { return model.DomainVideo }

type tmdbFindResponse struct {
	MovieResults []struct {
		ID           int      `json:"id"`
		Title        string   `json:"title"`
		Overview     string   `json:"overview"`
		PosterPath   string   `json:"poster_path"`
		BackdropPath string   `json:"backdrop_path"`
		ReleaseDate  string   `json:"release_date"`
		GenreIDs     []int    `json:"genre_ids"`
	} `json:"movie_results"`
	TVResults []struct {
		ID           int      `json:"id"`
		Name         string   `json:"name"`
		Overview     string   `json:"overview"`
		PosterPath   string   `json:"poster_path"`
		BackdropPath string   `json:"backdrop_path"`
		FirstAirDate string   `json:"first_air_date"`
		GenreIDs     []int    `json:"genre_ids"`
	} `json:"tv_results"`
}

func (p *TMDbProvider) FetchByProviderKey(ctx context.Context, key string) (*UnifiedMedia, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("empty tmdb/imdb key")
	}

	// If it's an IMDb ID (e.g. tt1234567) and TMDb API key is configured
	if p.apiKey != "" && strings.HasPrefix(key, "tt") {
		reqURL := fmt.Sprintf("%s/find/%s?api_key=%s&external_source=imdb_id", p.baseURL, url.PathEscape(key), p.apiKey)
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

		if resp.StatusCode == http.StatusOK {
			var findResp tmdbFindResponse
			if err := json.NewDecoder(resp.Body).Decode(&findResp); err == nil {
				if len(findResp.MovieResults) > 0 {
					m := findResp.MovieResults[0]
					poster := ""
					if m.PosterPath != "" {
						poster = "https://image.tmdb.org/t/p/w500" + m.PosterPath
					}
					backdrop := ""
					if m.BackdropPath != "" {
						backdrop = "https://image.tmdb.org/t/p/original" + m.BackdropPath
					}
					return &UnifiedMedia{
						URN:         fmt.Sprintf("pulsar:video:imdb:%s", key),
						Domain:      model.DomainVideo,
						Slug:        fmt.Sprintf("imdb-%s", key),
						Title:       m.Title,
						Caption:     "Movie",
						Description: m.Overview,
						ArtworkURL:  poster,
						BannerURL:   backdrop,
						ReleaseDate: m.ReleaseDate,
						ExternalIDs: map[string]string{
							"imdb": key,
							"tmdb": fmt.Sprintf("%d", m.ID),
						},
						ExternalLinks: []ExternalLink{
							{Platform: "imdb", Label: "IMDb", URL: fmt.Sprintf("https://www.imdb.com/title/%s/", key)},
							{Platform: "tmdb", Label: "TMDb", URL: fmt.Sprintf("https://www.themoviedb.org/movie/%d", m.ID)},
						},
					}, nil
				}
			}
		}
	}

	// Fallback response for IMDb IDs if API key is not configured or query fails
	if strings.HasPrefix(key, "tt") {
		return &UnifiedMedia{
			URN:     fmt.Sprintf("pulsar:video:imdb:%s", key),
			Domain:  model.DomainVideo,
			Slug:    fmt.Sprintf("imdb-%s", key),
			Title:   fmt.Sprintf("IMDb %s", key),
			Caption: "Cinema / TV",
			ExternalIDs: map[string]string{
				"imdb": key,
			},
			ExternalLinks: []ExternalLink{
				{
					Platform: "imdb",
					Label:    "IMDb",
					URL:      fmt.Sprintf("https://www.imdb.com/title/%s/", key),
				},
			},
		}, nil
	}

	return nil, fmt.Errorf("tmdb key %s not found", key)
}

func (p *TMDbProvider) Search(ctx context.Context, query string, limit int) ([]UnifiedMedia, error) {
	if p.apiKey == "" || strings.TrimSpace(query) == "" {
		return nil, nil
	}

	reqURL := fmt.Sprintf("%s/search/multi?api_key=%s&query=%s", p.baseURL, p.apiKey, url.QueryEscape(query))
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
		return nil, fmt.Errorf("tmdb search returned status %d", resp.StatusCode)
	}

	var searchResp struct {
		Results []struct {
			ID          int    `json:"id"`
			MediaType   string `json:"media_type"`
			Title       string `json:"title"`
			Name        string `json:"name"`
			PosterPath  string `json:"poster_path"`
			ReleaseDate string `json:"release_date"`
			FirstAirDate string `json:"first_air_date"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, err
	}

	results := make([]UnifiedMedia, 0, len(searchResp.Results))
	for _, r := range searchResp.Results {
		title := r.Title
		if title == "" {
			title = r.Name
		}
		date := r.ReleaseDate
		if date == "" {
			date = r.FirstAirDate
		}
		poster := ""
		if r.PosterPath != "" {
			poster = "https://image.tmdb.org/t/p/w500" + r.PosterPath
		}
		kind := "Movie"
		if r.MediaType == "tv" {
			kind = "TV Series"
		}

		results = append(results, UnifiedMedia{
			URN:         fmt.Sprintf("pulsar:video:tmdb:%d", r.ID),
			Domain:      model.DomainVideo,
			Slug:        fmt.Sprintf("tmdb-%d", r.ID),
			Title:       title,
			Caption:     kind,
			ArtworkURL:  poster,
			ReleaseDate: date,
			ExternalIDs: map[string]string{
				"tmdb": fmt.Sprintf("%d", r.ID),
			},
			ExternalLinks: []ExternalLink{
				{
					Platform: "tmdb",
					Label:    "TMDb",
					URL:      fmt.Sprintf("https://www.themoviedb.org/%s/%d", r.MediaType, r.ID),
				},
			},
		})
	}
	return results, nil
}
