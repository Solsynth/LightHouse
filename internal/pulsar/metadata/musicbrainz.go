package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

// MusicBrainz is a public, rate-limited service: one request per second per
// IP, with a User-Agent that names the software and a contact. Pulsar makes
// every call for every client, so it also caches the answers — on disk as
// catalog items, and in memory for the searches that by design do not create
// one.
const (
	defaultMusicBrainzBase = "https://musicbrainz.org"
	defaultCoverArtBase    = "https://coverartarchive.org"
	defaultMusicBrainzUA   = "Pulsar/1.0 (https://solsynth.dev; lily@solsynth.dev)"

	// musicBrainzInterval is the floor between upstream calls. The public
	// limit is one request per second averaged; a slightly slower pace keeps a
	// burst of page opens from being the request that gets us blocked.
	musicBrainzInterval = 1100 * time.Millisecond
)

// MusicBrainzSet is the three providers that answer for music entities,
// sharing one HTTP client and one rate limiter so the fleet never asks
// MusicBrainz faster than the service allows.
type MusicBrainzSet struct {
	Recording *MusicBrainzProvider
	Artist    *MusicBrainzArtistProvider
	Album     *MusicBrainzAlbumProvider
}

// NewMusicBrainzSet builds the music providers over one transport. Empty
// values select the public MusicBrainz/Cover Art Archive endpoints and the
// contact-bearing User-Agent MusicBrainz requires.
func NewMusicBrainzSet(baseURL, caaURL, userAgent string) *MusicBrainzSet {
	mb := newMBClient(baseURL, caaURL, userAgent)
	wiki := newWikiClient(userAgent)
	return &MusicBrainzSet{
		Recording: &MusicBrainzProvider{mb: mb},
		Artist:    &MusicBrainzArtistProvider{mb: mb, wiki: wiki},
		Album:     &MusicBrainzAlbumProvider{mb: mb, wiki: wiki},
	}
}

// mbClient is the MusicBrainz/Cover Art Archive transport: one client, one
// limiter, one User-Agent.
type mbClient struct {
	client    *http.Client
	baseURL   string
	caaURL    string
	userAgent string
	limiter   *rateLimiter
}

func newMBClient(baseURL, caaURL, userAgent string) *mbClient {
	if baseURL == "" {
		baseURL = defaultMusicBrainzBase
	}
	if caaURL == "" {
		caaURL = defaultCoverArtBase
	}
	if userAgent == "" {
		userAgent = defaultMusicBrainzUA
	}
	return &mbClient{
		client:    &http.Client{Timeout: 8 * time.Second},
		baseURL:   strings.TrimRight(baseURL, "/"),
		caaURL:    strings.TrimRight(caaURL, "/"),
		userAgent: userAgent,
		limiter:   &rateLimiter{interval: musicBrainzInterval},
	}
}

// BusyError is an upstream answering 503 or 429: the service is throttling us,
// or is simply overloaded at this moment. It is explicitly *not* "no such
// entity", and the HTTP layer maps it to 503 so a client retries later rather
// than concluding the artist or album does not exist.
type BusyError struct {
	// Service names the upstream in the message, e.g. "musicbrainz".
	Service string
	Status  int
}

func (e BusyError) Error() string {
	service := e.Service
	if service == "" {
		service = "metadata upstream"
	}
	return fmt.Sprintf("%s is busy (status %d)", service, e.Status)
}

// get decodes one MusicBrainz JSON answer, honouring the shared rate limit.
// A 503 is the service saying "not now"; it is retried a few times, spaced by
// the limiter, before the caller is told the upstream is busy.
func (c *mbClient) get(ctx context.Context, path string, query url.Values, dst any) error {
	u := c.baseURL + path
	if query != nil {
		u += "?" + query.Encode()
	}
	var lastErr error
	for range 4 {
		if err := c.limiter.wait(ctx); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Accept", "application/json")
		resp, err := c.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		// 503 and 429 are the service asking for room, not a verdict on the
		// entity: the limiter spaces the retries, and a caller that still
		// sees BusyError knows to say "try again", never "no such artist".
		if resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusTooManyRequests {
			lastErr = BusyError{Service: "musicbrainz", Status: resp.StatusCode}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("musicbrainz returned status %d", resp.StatusCode)
		}
		if err := json.Unmarshal(body, dst); err != nil {
			return err
		}
		return nil
	}
	return lastErr
}

// coverURL reports the Cover Art Archive front image for one release group,
// or the release itself when the group has none. The archive answers 307 to
// the image when it exists and 404 when it does not, so existence is one
// cheap HEAD — worth paying once, because a broken image URL is worse than no
// image at all and the answer is cached with the catalog row.
func (c *mbClient) coverURL(ctx context.Context, releaseGroupID, releaseID string) string {
	candidates := make([]string, 0, 2)
	if releaseGroupID != "" {
		candidates = append(candidates, c.caaURL+"/release-group/"+url.PathEscape(releaseGroupID)+"/front-500")
	}
	if releaseID != "" {
		candidates = append(candidates, c.caaURL+"/release/"+url.PathEscape(releaseID)+"/front-500")
	}
	for _, u := range candidates {
		if err := c.limiter.wait(ctx); err != nil {
			return ""
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", c.userAgent)
		resp, err := c.client.Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusTemporaryRedirect || resp.StatusCode == http.StatusFound {
			return u
		}
	}
	return ""
}

// rateLimiter spaces calls at least [interval] apart, across goroutines.
type rateLimiter struct {
	mu       sync.Mutex
	next     time.Time
	interval time.Duration
}

func (r *rateLimiter) wait(ctx context.Context) error {
	r.mu.Lock()
	now := time.Now()
	if r.next.Before(now) {
		r.next = now
	}
	at := r.next
	r.next = at.Add(r.interval)
	r.mu.Unlock()

	delay := time.Until(at)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ── MusicBrainz wire shapes ────────────────────────────────────────────────

type mbTag struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type mbRelation struct {
	Type string `json:"type"`
	URL  struct {
		Resource string `json:"resource"`
	} `json:"url"`
}

type mbArtistCredit struct {
	Name   string `json:"name"`
	Artist struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"artist"`
}

type mbArtist struct {
	ID             string                 `json:"id"`
	Name           string                 `json:"name"`
	SortName       string                 `json:"sort-name"`
	Disambiguation string                 `json:"disambiguation"`
	Type           string                 `json:"type"`
	Country        string                 `json:"country"`
	Area           *struct{ Name string } `json:"area"`
	BeginArea      *struct{ Name string } `json:"begin-area"`
	LifeSpan       *struct {
		Begin string `json:"begin"`
		End   string `json:"end"`
		Ended bool   `json:"ended"`
	} `json:"life-span"`
	Genres    []mbTag      `json:"genres"`
	Tags      []mbTag      `json:"tags"`
	Relations []mbRelation `json:"relations"`
	// Present with inc=release-groups: the artist's discography.
	ReleaseGroups []mbReleaseGroup `json:"release-groups"`
}

type mbReleaseGroup struct {
	ID               string           `json:"id"`
	Title            string           `json:"title"`
	PrimaryType      string           `json:"primary-type"`
	SecondaryTypes   []string         `json:"secondary-types"`
	FirstReleaseDate string           `json:"first-release-date"`
	ArtistCredit     []mbArtistCredit `json:"artist-credit"`
	Genres           []mbTag          `json:"genres"`
	Tags             []mbTag          `json:"tags"`
	Relations        []mbRelation     `json:"relations"`
	// Present with inc=releases: the editions of this record.
	Releases []struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Date   string `json:"date"`
		Status string `json:"status"`
	} `json:"releases"`
}

type mbRelease struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Date   string `json:"date"`
	Status string `json:"status"`
	Media  []struct {
		Position int `json:"position"`
		Tracks   []struct {
			ID           string           `json:"id"`
			Title        string           `json:"title"`
			Length       int              `json:"length"`
			Number       string           `json:"number"`
			ArtistCredit []mbArtistCredit `json:"artist-credit"`
		} `json:"tracks"`
	} `json:"media"`
}

// ── Recording provider ─────────────────────────────────────────────────────

// MusicBrainzProvider resolves canonical music metadata from MusicBrainz and Cover Art Archive.
type MusicBrainzProvider struct {
	mb *mbClient
}

func (p *MusicBrainzProvider) Name() string   { return "musicbrainz" }
func (p *MusicBrainzProvider) Domain() string { return model.DomainMusic }

// Kind names the entity this provider answers for; the engine's typed search
// uses it to ask only the provider that can answer.
func (p *MusicBrainzProvider) Kind() string { return KindTrack }

type mbRecordingResponse struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Length         int    `json:"length"` // ms
	Disambiguation string `json:"disambiguation"`
	ArtistCredit   []struct {
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
	Tags  []mbTag  `json:"tags"`
}

func (p *MusicBrainzProvider) FetchByProviderKey(ctx context.Context, mbid string) (*UnifiedMedia, error) {
	mbid = strings.TrimSpace(mbid)
	if mbid == "" {
		return nil, fmt.Errorf("empty musicbrainz mbid")
	}

	var rec mbRecordingResponse
	err := p.mb.get(ctx, "/ws/2/recording/"+url.PathEscape(mbid), url.Values{
		"inc": {"artist-credits+releases+tags+isrcs"},
		"fmt": {"json"},
	}, &rec)
	if err != nil {
		return nil, fmt.Errorf("musicbrainz recording %s: %w", mbid, err)
	}

	// Extract artists, both for the display subtitle and as structured
	// creator credits the engine can organize by.
	artists := make([]model.CreatorCredit, 0, len(rec.ArtistCredit))
	artistNames := make([]string, 0, len(rec.ArtistCredit))
	for _, ac := range rec.ArtistCredit {
		if ac.Name != "" {
			artistNames = append(artistNames, ac.Name)
			artists = append(artists, model.CreatorCredit{Name: ac.Name, Role: model.RoleArtist})
		}
	}
	artistStr := strings.Join(artistNames, ", ")

	// Extract primary album and cover art
	albumTitle := ""
	releaseDate := ""
	var primaryReleaseID string
	if len(rec.Releases) > 0 {
		primaryReleaseID = rec.Releases[0].ID
		albumTitle = rec.Releases[0].Title
		releaseDate = rec.Releases[0].Date
	}

	genres := tagNames(rec.Tags, 0)

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

	artworkURL := ""
	if primaryReleaseID != "" {
		// Cover Art Archive URL convention for front image
		artworkURL = fmt.Sprintf("%s/release/%s/front-500", p.mb.caaURL, primaryReleaseID)
	}

	return &UnifiedMedia{
		URN:             fmt.Sprintf("pulsar:music:mbid:%s", mbid),
		Domain:          model.DomainMusic,
		Slug:            fmt.Sprintf("mbid-%s", mbid),
		Title:           rec.Title,
		Subtitle:        artistStr,
		Caption:         albumTitle,
		ArtworkURL:      artworkURL,
		DurationSeconds: rec.Length / 1000,
		ReleaseDate:     releaseDate,
		Genres:          genres,
		Creators:        artists,
		ExternalIDs:     externalIDs,
		ExternalLinks:   links,
	}, nil
}

type mbSearchResponse struct {
	Count      int `json:"count"`
	Recordings []struct {
		ID           string           `json:"id"`
		Title        string           `json:"title"`
		Length       int              `json:"length"`
		ArtistCredit []mbArtistCredit `json:"artist-credit"`
		Releases     []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"releases"`
	} `json:"recordings"`
}

func (p *MusicBrainzProvider) Search(ctx context.Context, query string, limit int) ([]UnifiedMedia, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}

	var searchResp mbSearchResponse
	err := p.mb.get(ctx, "/ws/2/recording", url.Values{
		"query": {query},
		"fmt":   {"json"},
	}, &searchResp)
	if err != nil {
		return nil, fmt.Errorf("musicbrainz search: %w", err)
	}

	if limit <= 0 || limit > 20 {
		limit = 10
	}
	if len(searchResp.Recordings) < limit {
		limit = len(searchResp.Recordings)
	}

	results := make([]UnifiedMedia, 0, limit)
	for _, rec := range searchResp.Recordings[:limit] {
		credits := make([]model.CreatorCredit, 0, len(rec.ArtistCredit))
		artistNames := make([]string, 0, len(rec.ArtistCredit))
		for _, ac := range rec.ArtistCredit {
			if ac.Name != "" {
				artistNames = append(artistNames, ac.Name)
				credits = append(credits, model.CreatorCredit{Name: ac.Name, Role: model.RoleArtist})
			}
		}
		album := ""
		artURL := ""
		if len(rec.Releases) > 0 {
			album = rec.Releases[0].Title
			artURL = fmt.Sprintf("%s/release/%s/front-500", p.mb.caaURL, rec.Releases[0].ID)
		}

		results = append(results, UnifiedMedia{
			URN:        fmt.Sprintf("pulsar:music:mbid:%s", rec.ID),
			Domain:     model.DomainMusic,
			Slug:       fmt.Sprintf("mbid-%s", rec.ID),
			Title:      rec.Title,
			Subtitle:   strings.Join(artistNames, ", "),
			Caption:    album,
			ArtworkURL: artURL,
			ExternalIDs: map[string]string{
				"musicbrainz": rec.ID,
			},
			Creators: credits,
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

// tagNames turns MusicBrainz genres or tags into plain names, most-used
// first. [limit] <= 0 keeps every name.
func tagNames(tags []mbTag, limit int) []string {
	sorted := make([]mbTag, len(tags))
	copy(sorted, tags)
	// Insertion sort: the arrays are short and the ordering is stable, so
	// equally-used tags keep MusicBrainz's own order.
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Count > sorted[j-1].Count; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	names := make([]string, 0, len(sorted))
	for _, tag := range sorted {
		name := strings.TrimSpace(tag.Name)
		if name == "" {
			continue
		}
		names = append(names, name)
		if limit > 0 && len(names) >= limit {
			break
		}
	}
	return names
}

// relationsOf returns the URLs of one relation type, in MusicBrainz's order.
func relationsOf(relations []mbRelation, want string) []string {
	urls := make([]string, 0, len(relations))
	for _, relation := range relations {
		if strings.EqualFold(relation.Type, want) && relation.URL.Resource != "" {
			urls = append(urls, relation.URL.Resource)
		}
	}
	return urls
}
