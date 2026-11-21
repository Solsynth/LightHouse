package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

// Bangumi (bgm.tv) is a Chinese-language database for anime, manga, light
// novels, games and live-action shows. It fills the two gaps the other
// providers leave: a book provider at all, and a second opinion for anime and
// Asian games. The API is public, but its documentation asks every client to
// identify itself with a real User-Agent, so the fleet one is reused here.
const (
	defaultBangumiBase = "https://api.bgm.tv"

	// bangumiInterval spaces calls across every Bangumi provider. The service
	// publishes no hard number; a steady pace keeps a page open from arriving
	// as a burst while still answering a resolve in about a second.
	bangumiInterval = 500 * time.Millisecond

	bangumiTypeBook  = 1
	bangumiTypeAnime = 2
	bangumiTypeMusic = 3
	bangumiTypeGame  = 4
	bangumiTypeReal  = 6
)

// BangumiSet is one provider per media domain Bangumi covers. They share a
// transport, a rate limiter and a User-Agent, and an optional access token
// that raises the account's quotas and unlocks NSFW entries.
type BangumiSet struct {
	// Anime keeps the plain "bangumi" name because it is the provider every
	// "bangumi:123" identifier means; the others are registered for search.
	Anime *BangumiProvider
	Book  *BangumiProvider
	Game  *BangumiProvider
	Video *BangumiProvider
}

// NewBangumiProvider builds the anime provider alone. Most callers want
// NewBangumiSet, which covers every domain Bangumi can answer for.
func NewBangumiProvider(baseURL, userAgent, accessToken string) *BangumiProvider {
	return &BangumiProvider{
		client:      newBangumiClient(baseURL, userAgent, accessToken),
		name:        "bangumi",
		subjectType: bangumiTypeAnime,
		domain:      model.DomainAnime,
	}
}

// NewBangumiSet builds the providers for anime, books, games and live-action
// video over one transport. Empty values select the public API and the fleet
// User-Agent; the access token may stay empty.
func NewBangumiSet(baseURL, userAgent, accessToken string) *BangumiSet {
	c := newBangumiClient(baseURL, userAgent, accessToken)
	return &BangumiSet{
		Anime: &BangumiProvider{client: c, name: "bangumi", subjectType: bangumiTypeAnime, domain: model.DomainAnime},
		Book:  &BangumiProvider{client: c, name: "bangumi-book", subjectType: bangumiTypeBook, domain: model.DomainBook},
		Game:  &BangumiProvider{client: c, name: "bangumi-game", subjectType: bangumiTypeGame, domain: model.DomainGaming},
		Video: &BangumiProvider{client: c, name: "bangumi-video", subjectType: bangumiTypeReal, domain: model.DomainVideo},
	}
}

// BangumiProvider answers for one Bangumi subject type. Search asks only that
// type; a resolve names a subject id, so it derives the domain from the answer
// and is correct even when the id belongs to another type.
type BangumiProvider struct {
	client      *bangumiClient
	name        string
	subjectType int
	domain      string
}

func (p *BangumiProvider) Name() string   { return p.name }
func (p *BangumiProvider) Domain() string { return p.domain }

// bangumiClient is the shared HTTP transport: one client, one limiter, one
// User-Agent and one optional bearer token.
type bangumiClient struct {
	client      *http.Client
	baseURL     string
	userAgent   string
	accessToken string
	limiter     *rateLimiter
}

func newBangumiClient(baseURL, userAgent, accessToken string) *bangumiClient {
	if baseURL == "" {
		baseURL = defaultBangumiBase
	}
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	return &bangumiClient{
		client:      &http.Client{Timeout: 10 * time.Second},
		baseURL:     strings.TrimRight(baseURL, "/"),
		userAgent:   userAgent,
		accessToken: strings.TrimSpace(accessToken),
		limiter:     &rateLimiter{interval: bangumiInterval},
	}
}

// do sends one request and decodes its JSON answer. A 404 is the API saying
// the subject does not exist; 429 and 503 are the service asking for room and
// surface as BusyError so the HTTP layer can answer "try again".
func (c *bangumiClient) do(ctx context.Context, method, path string, payload, dst any) error {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}

	if err := c.limiter.wait(ctx); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
		return BusyError{Service: "bangumi", Status: resp.StatusCode}
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("bangumi returned status %d: %s", resp.StatusCode, path)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bangumi returned status %d", resp.StatusCode)
	}

	if dst == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(dst)
}

// ── Wire shapes ────────────────────────────────────────────────────────────

type bangumiImages struct {
	Large  string `json:"large"`
	Common string `json:"common"`
	Medium string `json:"medium"`
	Small  string `json:"small"`
	Grid   string `json:"grid"`
}

func (i bangumiImages) artwork() string {
	for _, candidate := range []string{i.Large, i.Common, i.Medium, i.Small, i.Grid} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

type bangumiInfobox struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// values reads an infobox value, which Bangumi writes either as one string or
// as a list of {"v": "..."} objects (aliases are the common list case).
func (b bangumiInfobox) values() []string {
	var single string
	if err := json.Unmarshal(b.Value, &single); err == nil {
		if strings.TrimSpace(single) != "" {
			return []string{single}
		}
		return nil
	}
	var many []struct {
		V string `json:"v"`
	}
	if err := json.Unmarshal(b.Value, &many); err == nil {
		out := make([]string, 0, len(many))
		for _, item := range many {
			if strings.TrimSpace(item.V) != "" {
				out = append(out, item.V)
			}
		}
		return out
	}
	return nil
}

type bangumiSubject struct {
	ID       int           `json:"id"`
	Type     int           `json:"type"`
	Name     string        `json:"name"`
	NameCN   string        `json:"name_cn"`
	Summary  string        `json:"summary"`
	Date     string        `json:"date"`
	Platform string        `json:"platform"`
	Images   bangumiImages `json:"images"`
	Eps      int           `json:"eps"`
	Volumes  int           `json:"volumes"`
	// TotalEpisodes is Bangumi's own episode count, which can include specials.
	TotalEpisodes int      `json:"total_episodes"`
	MetaTags      []string `json:"meta_tags"`
	Tags          []struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	} `json:"tags"`
	Infobox []bangumiInfobox `json:"infobox"`
	Rating  struct {
		Rank  int     `json:"rank"`
		Score float64 `json:"score"`
		Total int     `json:"total"`
	} `json:"rating"`
	Collection struct {
		Wish    int `json:"wish"`
		Collect int `json:"collect"`
		Doing   int `json:"doing"`
		OnHold  int `json:"on_hold"`
		Dropped int `json:"dropped"`
	} `json:"collection"`
}

type bangumiPerson struct {
	ID       int      `json:"id"`
	Name     string   `json:"name"`
	Relation string   `json:"relation"`
	Career   []string `json:"career"`
	Type     int      `json:"type"`
}

// ── Provider behaviour ─────────────────────────────────────────────────────

// FetchByProviderKey resolves one subject. The response type decides the
// Pulsar domain, not the provider instance: a "bangumi:123" identifier may
// name an anime, a manga or a game, and the answer should say which.
func (p *BangumiProvider) FetchByProviderKey(ctx context.Context, key string) (*UnifiedMedia, error) {
	key = strings.TrimSpace(key)
	id, err := strconv.Atoi(key)
	if err != nil || id <= 0 {
		return nil, fmt.Errorf("invalid bangumi subject id: %s", key)
	}

	var subject bangumiSubject
	if err := p.client.do(ctx, http.MethodGet, "/v0/subjects/"+strconv.Itoa(id), nil, &subject); err != nil {
		return nil, err
	}
	// Bangumi also indexes music albums (type 3). Pulsar's music domain is
	// track-shaped and answered by MusicBrainz, so an album id is refused
	// rather than stored under the wrong shape.
	if subject.Type != 0 && bangumiDomain(subject.Type) == "" {
		return nil, fmt.Errorf("unsupported bangumi subject type %d", subject.Type)
	}

	// Staff credits live in a second endpoint that some subjects (older games
	// especially) leave empty. Credits are enrichment, never a reason to fail
	// a resolve, so the answer is used when it arrives and dropped otherwise.
	persons, err := p.persons(ctx, id)
	if err != nil {
		persons = nil
	}

	media := p.toUnified(subject, persons)
	return &media, nil
}

func (p *BangumiProvider) persons(ctx context.Context, id int) ([]bangumiPerson, error) {
	var persons []bangumiPerson
	if err := p.client.do(ctx, http.MethodGet, "/v0/subjects/"+strconv.Itoa(id)+"/persons", nil, &persons); err != nil {
		return nil, err
	}
	return persons, nil
}

// Search asks for one subject type. Bangumi's search endpoint is the only
// public way to get more than one name at a time; the subject endpoint answers
// by id.
func (p *BangumiProvider) Search(ctx context.Context, query string, limit int) ([]UnifiedMedia, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 20 {
		limit = 10
	}

	payload := map[string]any{
		"keyword": query,
		"sort":    "match",
		"filter": map[string]any{
			"type": []int{p.subjectType},
		},
	}
	var answer struct {
		Data  []bangumiSubject `json:"data"`
		Total int              `json:"total"`
	}
	path := fmt.Sprintf("/v0/search/subjects?limit=%d&offset=0", limit)
	if err := p.client.do(ctx, http.MethodPost, path, payload, &answer); err != nil {
		return nil, err
	}

	results := make([]UnifiedMedia, 0, len(answer.Data))
	for _, subject := range answer.Data {
		results = append(results, p.toUnified(subject, nil))
	}
	return results, nil
}

// ── Mapping ────────────────────────────────────────────────────────────────

func (p *BangumiProvider) toUnified(subject bangumiSubject, persons []bangumiPerson) UnifiedMedia {
	domain := bangumiDomain(subject.Type)
	if domain == "" {
		domain = p.domain
	}

	title := strings.TrimSpace(subject.NameCN)
	if title == "" {
		title = strings.TrimSpace(subject.Name)
	}
	if title == "" {
		title = fmt.Sprintf("Bangumi %d", subject.ID)
	}

	creators := bangumiCreators(subject, persons)

	externalIDs := map[string]string{"bangumi": strconv.Itoa(subject.ID)}
	links := []ExternalLink{{
		Platform: "bangumi",
		Label:    "Bangumi",
		URL:      fmt.Sprintf("https://bgm.tv/subject/%d", subject.ID),
	}}
	if official := bangumiInfoboxValue(subject.Infobox, "官方网站"); official != "" && strings.HasPrefix(official, "http") {
		links = append(links, ExternalLink{Platform: "official", Label: "Official Site", URL: official})
	}
	if imdb := bangumiInfoboxValue(subject.Infobox, "IMDb"); strings.HasPrefix(imdb, "tt") {
		externalIDs["imdb"] = imdb
		links = append(links, ExternalLink{
			Platform: "imdb",
			Label:    "IMDb",
			URL:      "https://www.imdb.com/title/" + imdb + "/",
		})
	}

	media := UnifiedMedia{
		URN:           fmt.Sprintf("pulsar:%s:bangumi:%d", domain, subject.ID),
		Domain:        domain,
		Slug:          fmt.Sprintf("bangumi-%d", subject.ID),
		Title:         title,
		Subtitle:      bangumiSubtitle(creators, domain),
		Caption:       bangumiCaption(subject, domain),
		Description:   bangumiSummary(subject.Summary),
		ArtworkURL:    subject.Images.artwork(),
		ReleaseDate:   strings.TrimSpace(subject.Date),
		Genres:        bangumiGenres(subject),
		ExternalIDs:   externalIDs,
		ExternalLinks: links,
		Creators:      creators,
	}
	return media
}

func bangumiDomain(subjectType int) string {
	switch subjectType {
	case bangumiTypeBook:
		return model.DomainBook
	case bangumiTypeAnime:
		return model.DomainAnime
	case bangumiTypeGame:
		return model.DomainGaming
	case bangumiTypeReal:
		return model.DomainVideo
	default:
		return ""
	}
}

func bangumiSummary(summary string) string {
	summary = strings.ReplaceAll(summary, "\r\n", "\n")
	summary = strings.ReplaceAll(summary, "\r", "\n")
	return strings.TrimSpace(summary)
}

func bangumiGenres(subject bangumiSubject) []string {
	if len(subject.MetaTags) > 0 {
		return append([]string(nil), subject.MetaTags...)
	}

	genres := make([]string, 0, 8)
	for _, tag := range subject.Tags {
		if tag.Count <= 0 || bangumiTagIsNoise(tag.Name) {
			continue
		}
		genres = append(genres, tag.Name)
		if len(genres) >= 8 {
			break
		}
	}
	return genres
}

// bangumiTagIsNoise drops the "2023年10月" and "2023" style tags that are
// popularity metadata rather than genre.
func bangumiTagIsNoise(name string) bool {
	digits := 0
	for _, r := range name {
		if digits == 4 {
			break
		}
		if r < '0' || r > '9' {
			return false
		}
		digits++
	}
	return digits == 4
}

var bangumiPlatformLabels = map[string]string{
	"漫画":   "Manga",
	"小说":   "Novel",
	"画集":   "Artbook",
	"绘本":   "Picture Book",
	"公式书":  "Guidebook",
	"游戏":   "Game",
	"电影":   "Movie",
	"电视剧":  "TV Series",
	"日剧":   "TV Series",
	"华语剧":  "TV Series",
	"欧美剧":  "TV Series",
	"动画":   "Anime",
	"剧场版":  "Movie",
	"桌面游戏": "Tabletop",
}

func bangumiCaption(subject bangumiSubject, domain string) string {
	platform := strings.TrimSpace(subject.Platform)
	if label, ok := bangumiPlatformLabels[platform]; ok {
		platform = label
	}

	parts := make([]string, 0, 3)
	if platform != "" {
		parts = append(parts, platform)
	}

	switch domain {
	case model.DomainAnime:
		if subject.Eps > 0 {
			parts = append(parts, fmt.Sprintf("%d episodes", subject.Eps))
		}
	case model.DomainBook:
		if subject.Volumes > 0 {
			parts = append(parts, fmt.Sprintf("%d volumes", subject.Volumes))
		} else if subject.Eps > 0 {
			parts = append(parts, fmt.Sprintf("%d chapters", subject.Eps))
		}
	}

	if year := bangumiYear(subject.Date); year != "" {
		parts = append(parts, year)
	}
	return strings.Join(parts, " · ")
}

func bangumiYear(date string) string {
	date = strings.TrimSpace(date)
	if len(date) >= 4 {
		return date[:4]
	}
	return ""
}

// bangumiSubtitle picks the credit a reader expects under the title: the
// studio for an anime, the author for a book, the developer for a game and the
// director for live action.
func bangumiSubtitle(creators []model.CreatorCredit, domain string) string {
	priorities := map[string][]string{
		model.DomainAnime:  {model.RoleStudio, model.RoleAuthor, model.RoleDirector},
		model.DomainBook:   {model.RoleAuthor, model.RolePublisher},
		model.DomainGaming: {model.RoleDeveloper, model.RolePublisher},
		model.DomainVideo:  {model.RoleDirector, model.RoleAuthor, model.RoleStudio},
	}
	for _, role := range priorities[domain] {
		names := make([]string, 0, 3)
		for _, credit := range creators {
			if credit.Role != role || credit.Name == "" {
				continue
			}
			names = append(names, credit.Name)
			if len(names) == 3 {
				break
			}
		}
		if len(names) > 0 {
			return strings.Join(names, ", ")
		}
	}
	if len(creators) > 0 {
		return creators[0].Name
	}
	return ""
}

func bangumiInfoboxValue(items []bangumiInfobox, key string) string {
	for _, item := range items {
		if strings.TrimSpace(item.Key) != key {
			continue
		}
		if values := item.values(); len(values) > 0 {
			return strings.TrimSpace(values[0])
		}
	}
	return ""
}

// bangumiCreators merges the staff endpoint with the infobox. The endpoint is
// more precise when it exists; the infobox covers the games and older subjects
// that have no staff rows.
func bangumiCreators(subject bangumiSubject, persons []bangumiPerson) []model.CreatorCredit {
	creators := make([]model.CreatorCredit, 0, len(persons))
	seen := make(map[string]bool)
	add := func(name, role string) {
		name = strings.TrimSpace(name)
		if name == "" || role == "" {
			return
		}
		key := strings.ToLower(name) + "\x00" + role
		if seen[key] {
			return
		}
		seen[key] = true
		creators = append(creators, model.CreatorCredit{Name: name, Role: role})
	}

	for _, person := range persons {
		add(person.Name, bangumiRelationRole(person.Relation))
	}
	for _, item := range subject.Infobox {
		role := bangumiInfoboxRole(item.Key)
		if role == "" {
			continue
		}
		for _, value := range item.values() {
			for _, name := range splitBangumiCredits(value) {
				add(name, role)
			}
		}
	}
	return creators
}

func bangumiRelationRole(relation string) string {
	relation = strings.TrimSpace(relation)
	switch {
	case relation == "":
		return ""
	case containsAny(relation, "导演", "監督", "监督"):
		return model.RoleDirector
	case containsAny(relation, "动画制作", "制作公司", "动画公司", "製作"):
		return model.RoleStudio
	case containsAny(relation, "开发", "开发商"):
		return model.RoleDeveloper
	case containsAny(relation, "发行", "出版社", "出版", "连载杂志"):
		return model.RolePublisher
	case containsAny(relation, "原作", "作者", "作画", "漫画", "脚本", "剧本", "编剧", "系列构成", "构成"):
		return model.RoleAuthor
	case containsAny(relation, "音乐", "主题曲", "插画", "人物设定", "角色设计", "声优", "演员", "主演"):
		return model.RoleArtist
	default:
		return ""
	}
}

func bangumiInfoboxRole(key string) string {
	switch strings.TrimSpace(key) {
	case "导演", "監督", "监督", "总导演":
		return model.RoleDirector
	case "动画制作", "制作公司":
		return model.RoleStudio
	case "开发", "开发商":
		return model.RoleDeveloper
	case "发行", "发行商", "出版社", "出版", "连载杂志":
		return model.RolePublisher
	case "原作", "作者", "作画", "剧本", "编剧", "系列构成":
		return model.RoleAuthor
	case "音乐", "主题曲", "插画", "人物设定", "角色设计":
		return model.RoleArtist
	default:
		return ""
	}
}

// splitBangumiCredits turns one infobox value into names. Infobox values are
// human prose: several names separated by 、 or ・, sometimes followed by a
// parenthetical publisher note that belongs to the book, not the person.
func splitBangumiCredits(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if idx := strings.IndexAny(value, "（("); idx > 0 {
		value = value[:idx]
	}
	parts := strings.FieldsFunc(value, func(r rune) bool {
		switch r {
		case '、', '，', ',', '／', '/', '・', '·', '&':
			return true
		default:
			return false
		}
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
