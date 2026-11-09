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

// MusicBrainzProvider resolves canonical music metadata from MusicBrainz and Cover Art Archive.
type MusicBrainzProvider struct {
	client    *http.Client
	baseURL   string
	caaURL    string
	userAgent string
}

func NewMusicBrainzProvider(baseURL, caaURL, userAgent string) *MusicBrainzProvider {
	if baseURL == "" {
		baseURL = "https://musicbrainz.org"
	}
	if caaURL == "" {
		caaURL = "https://coverartarchive.org"
	}
	if userAgent == "" {
		userAgent = "Pulsar/1.0 (https://solsynth.dev; contact@solsynth.dev)"
	}
	return &MusicBrainzProvider{
		client:    &http.Client{Timeout: 8 * time.Second},
		baseURL:   strings.TrimRight(baseURL, "/"),
		caaURL:    strings.TrimRight(caaURL, "/"),
		userAgent: userAgent,
	}
}

func (p *MusicBrainzProvider) Name() string   { return "musicbrainz" }
func (p *MusicBrainzProvider) Domain() string { return model.DomainMusic }

type mbRecordingResponse struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Length       int    `json:"length"` // ms
	Disambiguation string `json:"disambiguation"`
	ArtistCredit []struct {
		Name   string `json:"name"`
		Artist struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"artist"`
	} `json:"artist-credit"`
	Releases []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Date  string `json:"date"`
	} `json:"releases"`
	ISRCs []string `json:"isrcs"`
	Tags  []struct {
		Count int    `json:"count"`
		Name  string `json:"name"`
	} `json:"tags"`
}

func (p *MusicBrainzProvider) FetchByProviderKey(ctx context.Context, mbid string) (*UnifiedMedia, error) {
	mbid = strings.TrimSpace(mbid)
	if mbid == "" {
		return nil, fmt.Errorf("empty musicbrainz mbid")
	}

	reqURL := fmt.Sprintf("%s/ws/2/recording/%s?inc=artist-credits+releases+tags+isrcs&fmt=json", p.baseURL, url.PathEscape(mbid))
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
		return nil, fmt.Errorf("musicbrainz api returned status %d", resp.StatusCode)
	}

	var rec mbRecordingResponse
	if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
		return nil, err
	}

	// Extract artists
	artistNames := make([]string, 0, len(rec.ArtistCredit))
	for _, ac := range rec.ArtistCredit {
		if ac.Name != "" {
			artistNames = append(artistNames, ac.Name)
		}
	}
	artistStr := strings.Join(artistNames, ", ")

	// Extract primary album and cover art
	albumTitle := ""
	releaseDate := ""
	artworkURL := ""
	var primaryReleaseID string
	if len(rec.Releases) > 0 {
		primaryReleaseID = rec.Releases[0].ID
		albumTitle = rec.Releases[0].Title
		releaseDate = rec.Releases[0].Date
		// Cover Art Archive URL convention for front image
		artworkURL = fmt.Sprintf("%s/release/%s/front-500", p.caaURL, primaryReleaseID)
	}

	genres := make([]string, 0, len(rec.Tags))
	for _, tag := range rec.Tags {
		if tag.Name != "" {
			genres = append(genres, tag.Name)
		}
	}

	externalIDs := map[string]string{
		"musicbrainz": mbid,
	}
	if len(rec.ISRCs) > 0 {
		externalIDs["isrc"] = rec.ISRCs[0]
	}
	if primaryReleaseID != "" {
		externalIDs["musicbrainz_release"] = primaryReleaseID
	}

	links := []ExternalLink{
		{
			Platform: "musicbrainz",
			Label:    "MusicBrainz Track",
			URL:      fmt.Sprintf("https://musicbrainz.org/recording/%s", mbid),
		},
	}
	if primaryReleaseID != "" {
		links = append(links, ExternalLink{
			Platform: "musicbrainz",
			Label:    "MusicBrainz Release",
			URL:      fmt.Sprintf("https://musicbrainz.org/release/%s", primaryReleaseID),
		})
	}

	durationSec := rec.Length / 1000

	return &UnifiedMedia{
		URN:             fmt.Sprintf("pulsar:music:mbid:%s", mbid),
		Domain:          model.DomainMusic,
		Slug:            fmt.Sprintf("mbid-%s", mbid),
		Title:           rec.Title,
		Subtitle:        artistStr,
		Caption:         albumTitle,
		ArtworkURL:      artworkURL,
		DurationSeconds: durationSec,
		ReleaseDate:     releaseDate,
		Genres:          genres,
		ExternalIDs:     externalIDs,
		ExternalLinks:   links,
	}, nil
}

type mbSearchResponse struct {
	Count      int `json:"count"`
	Recordings []struct {
		ID           string `json:"id"`
		Title        string `json:"title"`
		Length       int    `json:"length"`
		ArtistCredit []struct {
			Name string `json:"name"`
		} `json:"artist-credit"`
		Releases []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"releases"`
	} `json:"recordings"`
}

func (p *MusicBrainzProvider) Search(ctx context.Context, query string, limit int) ([]UnifiedMedia, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}

	reqURL := fmt.Sprintf("%s/ws/2/recording/?query=%s&fmt=json", p.baseURL, url.QueryEscape(query))
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
		return nil, fmt.Errorf("musicbrainz search returned status %d", resp.StatusCode)
	}

	var searchResp mbSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, err
	}

	if limit <= 0 || limit > 20 {
		limit = 10
	}
	if len(searchResp.Recordings) < limit {
		limit = len(searchResp.Recordings)
	}

	results := make([]UnifiedMedia, 0, limit)
	for i := 0; i < limit; i++ {
		rec := searchResp.Recordings[i]
		artists := make([]string, 0, len(rec.ArtistCredit))
		for _, ac := range rec.ArtistCredit {
			if ac.Name != "" {
				artists = append(artists, ac.Name)
			}
		}
		album := ""
		artURL := ""
		if len(rec.Releases) > 0 {
			album = rec.Releases[0].Title
			artURL = fmt.Sprintf("%s/release/%s/front-500", p.caaURL, rec.Releases[0].ID)
		}

		results = append(results, UnifiedMedia{
			URN:             fmt.Sprintf("pulsar:music:mbid:%s", rec.ID),
			Domain:          model.DomainMusic,
			Slug:            fmt.Sprintf("mbid-%s", rec.ID),
			Title:           rec.Title,
			Subtitle:        strings.Join(artists, ", "),
			Caption:         album,
			ArtworkURL:      artURL,
			DurationSeconds: rec.Length / 1000,
			ExternalIDs: map[string]string{
				"musicbrainz": rec.ID,
			},
			ExternalLinks: []ExternalLink{
				{
					Platform: "musicbrainz",
					Label:    "MusicBrainz Track",
					URL:      fmt.Sprintf("https://musicbrainz.org/recording/%s", rec.ID),
				},
			},
		})
	}
	return results, nil
}
