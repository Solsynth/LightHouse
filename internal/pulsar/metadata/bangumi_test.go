package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

// TestBangumiSearch checks the request Bangumi receives and the row Pulsar
// builds from its search answer: the Chinese title, the poster, the domain and
// the URL the client opens.
func TestBangumiSearch(t *testing.T) {
	var gotAuth, gotAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/search/subjects" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		gotAuth = r.Header.Get("Authorization")
		gotAgent = r.Header.Get("User-Agent")

		var payload struct {
			Keyword string `json:"keyword"`
			Filter  struct {
				Type []int `json:"type"`
			} `json:"filter"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if payload.Keyword != "frieren" {
			t.Errorf("expected keyword frieren, got %q", payload.Keyword)
		}
		if len(payload.Filter.Type) != 1 || payload.Filter.Type[0] != bangumiTypeAnime {
			t.Errorf("expected anime filter, got %+v", payload.Filter.Type)
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"data": [{
				"id": 400602,
				"type": 2,
				"name": "葬送のフリーレン",
				"name_cn": "葬送的芙莉莲",
				"summary": "魔法使芙莉莲踏上了旅途。\r\n她想知道人心。",
				"date": "2023-09-29",
				"platform": "TV",
				"eps": 28,
				"images": {"large": "https://lain.bgm.tv/pic/cover/l/frieren.jpg"},
				"tags": [{"name": "2023", "count": 1764}, {"name": "奇幻", "count": 6509}],
				"infobox": [{"key": "动画制作", "value": "MADHOUSE"}]
			}],
			"total": 1
		}`)
	}))
	defer srv.Close()

	set := NewBangumiSet(srv.URL, "Pulsar-Test/1.0", "secret-token")
	results, err := set.Anime.Search(context.Background(), "  frieren  ", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("expected bearer token, got %q", gotAuth)
	}
	if gotAgent != "Pulsar-Test/1.0" {
		t.Errorf("expected configured user agent, got %q", gotAgent)
	}
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}

	got := results[0]
	if got.Title != "葬送的芙莉莲" {
		t.Errorf("expected Chinese title, got %q", got.Title)
	}
	if got.Domain != model.DomainAnime {
		t.Errorf("expected anime domain, got %q", got.Domain)
	}
	if got.URN != "pulsar:anime:bangumi:400602" || got.Slug != "bangumi-400602" {
		t.Errorf("unexpected identifiers: urn=%q slug=%q", got.URN, got.Slug)
	}
	if got.ArtworkURL != "https://lain.bgm.tv/pic/cover/l/frieren.jpg" {
		t.Errorf("unexpected artwork: %q", got.ArtworkURL)
	}
	if got.Subtitle != "MADHOUSE" {
		t.Errorf("expected the studio under the title, got %q", got.Subtitle)
	}
	if got.Description != "魔法使芙莉莲踏上了旅途。\n她想知道人心。" {
		t.Errorf("expected normalized summary, got %q", got.Description)
	}
	if len(got.Genres) != 1 || got.Genres[0] != "奇幻" {
		t.Errorf("expected date tags to be filtered from genres, got %+v", got.Genres)
	}
	if len(got.ExternalLinks) == 0 || got.ExternalLinks[0].URL != "https://bgm.tv/subject/400602" {
		t.Errorf("expected a Bangumi link, got %+v", got.ExternalLinks)
	}
}

// TestBangumiFetchDerivesDomain proves the anime instance can resolve a book:
// the subject type in the answer, not the instance's search filter, decides
// the Pulsar domain. Identifiers produced by a book search are "bangumi:123"
// too, so this is the path every resolve takes.
func TestBangumiFetchDerivesDomain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/subjects/2139":
			fmt.Fprint(w, `{
				"id": 2139,
				"type": 1,
				"name": "MONSTER",
				"name_cn": "怪物",
				"summary": "浦沢直樹的漫画。",
				"date": "1995-06-30",
				"platform": "漫画",
				"volumes": 18,
				"images": {"large": "https://lain.bgm.tv/pic/cover/l/monster.jpg"},
				"infobox": [
					{"key": "作者", "value": "浦沢直樹"},
					{"key": "出版社", "value": "小学館"},
					{"key": "官方网站", "value": "https://example.com/monster"}
				]
			}`)
		case "/v0/subjects/2139/persons":
			fmt.Fprint(w, `[
				{"id": 1, "name": "浦沢直樹", "relation": "作者", "career": ["mangaka"]},
				{"id": 2, "name": "小学館", "relation": "出版社", "career": ["producer"]}
			]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	set := NewBangumiSet(srv.URL, "", "")
	media, err := set.Anime.FetchByProviderKey(context.Background(), "2139")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if media.Domain != model.DomainBook {
		t.Errorf("expected book domain from the subject type, got %q", media.Domain)
	}
	if media.URN != "pulsar:book:bangumi:2139" {
		t.Errorf("unexpected urn: %q", media.URN)
	}
	if media.Title != "怪物" || media.Subtitle != "浦沢直樹" {
		t.Errorf("unexpected title/subtitle: %q / %q", media.Title, media.Subtitle)
	}
	if media.Caption != "Manga · 18 volumes · 1995" {
		t.Errorf("unexpected caption: %q", media.Caption)
	}
	if len(media.Creators) != 2 {
		t.Fatalf("expected deduplicated authors and publisher, got %+v", media.Creators)
	}
	if media.Creators[0].Role != model.RoleAuthor || media.Creators[1].Role != model.RolePublisher {
		t.Errorf("unexpected roles: %+v", media.Creators)
	}

	var official string
	for _, link := range media.ExternalLinks {
		if link.Platform == "official" {
			official = link.URL
		}
	}
	if official != "https://example.com/monster" {
		t.Errorf("expected the infobox official site, got %q", official)
	}
}

// TestBangumiBusyIsNotNotFound keeps a throttled upstream distinguishable from
// a missing subject: the HTTP layer turns this into 503, not 404.
func TestBangumiBusyIsNotNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	set := NewBangumiSet(srv.URL, "", "")
	_, err := set.Book.Search(context.Background(), "monster", 5)
	var busy BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("expected BusyError, got %v", err)
	}
	if busy.Service != "bangumi" || busy.Status != http.StatusTooManyRequests {
		t.Errorf("unexpected busy error: %+v", busy)
	}
}

func TestParseIdentifierBangumi(t *testing.T) {
	for _, tc := range []struct {
		in       string
		domain   string
		provider string
		key      string
	}{
		{"bangumi:400602", model.DomainAnime, "bangumi", "400602"},
		{"bgm:400602", model.DomainAnime, "bangumi", "400602"},
		{"pulsar:book:bangumi:2139", model.DomainBook, "bangumi", "2139"},
		{"bangumi-2139", model.DomainAnime, "bangumi", "2139"},
		{"bangumi-book-2139", model.DomainBook, "bangumi-book", "2139"},
		{"bangumi-game-623", model.DomainGaming, "bangumi-game", "623"},
		{"bangumi-video-2670", model.DomainVideo, "bangumi-video", "2670"},
	} {
		domain, provider, key := parseIdentifier(tc.in)
		if domain != tc.domain || provider != tc.provider || key != tc.key {
			t.Errorf("%s: got (%q, %q, %q), want (%q, %q, %q)",
				tc.in, domain, provider, key, tc.domain, tc.provider, tc.key)
		}
	}
}
