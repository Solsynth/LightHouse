package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Kind names one music entity a provider can answer for. The engine keeps one
// provider per kind, so a client asking about an artist never spends a
// request on a recording search.
const (
	KindTrack  = "track"
	KindArtist = "artist"
	KindAlbum  = "album"
)

// TypedProvider is a Provider that also says which music entity it answers
// for by key. [Provider] alone is enough for the generic resolve path; this
// is what the artist and album endpoints need in order to ask the right
// upstream.
type TypedProvider interface {
	Provider
	Kind() string
}

// ── Wikipedia ──────────────────────────────────────────────────────────────

// wikiClient reads article summaries and lead images from Wikipedia. It is
// the description source: MusicBrainz has facts but no prose, and an album
// page that says what the record is beats one that only lists tracks.
type wikiClient struct {
	client    *http.Client
	userAgent string
}

func newWikiClient(userAgent string) *wikiClient {
	if userAgent == "" {
		userAgent = defaultMusicBrainzUA
	}
	return &wikiClient{
		client:    &http.Client{Timeout: 6 * time.Second},
		userAgent: userAgent,
	}
}

// wikiSummary is the REST summary endpoint's answer, trimmed to what a page
// draws.
type wikiSummary struct {
	Title     string `json:"title"`
	Extract   string `json:"extract"`
	Lang      string `json:"lang"`
	Type      string `json:"type"`
	Thumbnail *struct {
		Source string `json:"source"`
	} `json:"thumbnail"`
}

// summary fetches one article summary in [lang]. A missing article is an
// empty answer, not an error: most artists have no article in the reader's
// language, and that must cost the page nothing.
func (w *wikiClient) summary(ctx context.Context, lang, title string) *wikiSummary {
	lang = strings.TrimSpace(lang)
	if lang == "" {
		lang = "en"
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return nil
	}
	u := fmt.Sprintf("https://%s.wikipedia.org/api/rest_v1/page/summary/%s", lang, url.PathEscape(strings.ReplaceAll(title, " ", "_")))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", w.userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	var summary wikiSummary
	if err := json.Unmarshal(body, &summary); err != nil {
		return nil
	}
	// A disambiguation page describes the name, not the act.
	if summary.Type == "disambiguation" || strings.TrimSpace(summary.Extract) == "" {
		return nil
	}
	summary.Lang = lang
	return &summary
}

// articleURL is the human page for a summary, which is what a "read more"
// link opens.
func articleURL(title, lang string) string {
	if lang == "" {
		lang = "en"
	}
	return fmt.Sprintf("https://%s.wikipedia.org/wiki/%s", lang, url.PathEscape(strings.ReplaceAll(title, " ", "_")))
}

// imageOf returns the summary's lead image, if it has one.
func imageOf(summary *wikiSummary) string {
	if summary == nil || summary.Thumbnail == nil {
		return ""
	}
	return summary.Thumbnail.Source
}

// localizedSummary finds the article for a title, in the reader's language
// when it exists and in English otherwise.
//
// The lookup is not always by the same title: an artist canonical in
// Japanese is "多田葵" in MusicBrainz and "Aoi Tada" on English Wikipedia, and
// no summary endpoint bridges the two. So when the direct lookups miss, the
// title is looked up once in each candidate language and its cross-language
// links are followed — one extra request, paid only by the artists whose
// names are not in Latin script.
func (w *wikiClient) localizedSummary(ctx context.Context, title, lang string) *wikiSummary {
	lang = strings.TrimSpace(lang)
	if lang == "" {
		lang = "en"
	}
	// The article may only exist on the wiki for the act's own script: a
	// Japanese artist is "多田葵" on ja.wikipedia and "Aoi Tada" on
	// en.wikipedia. Ask the reader's language first, then the wikis a music
	// page most often needs.
	candidates := make([]string, 0, 4)
	for _, candidate := range []string{lang, "en", "ja", "zh", "ko"} {
		if candidate != "" && !slices.Contains(candidates, candidate) {
			candidates = append(candidates, candidate)
		}
	}

	for _, candidate := range candidates {
		summary := w.summary(ctx, candidate, title)
		if summary == nil {
			// The same title may not resolve there, but the article the other
			// wikis know is one cross-language link away.
			if linked := w.langlink(ctx, lang, title, candidate); linked != "" {
				summary = w.summary(ctx, candidate, linked)
			}
		}
		if summary == nil {
			continue
		}
		if candidate == lang {
			return summary
		}
		// Prose in the reader's own language beats prose in another one, so
		// bridge the hit before accepting it as-is.
		if linked := w.langlink(ctx, candidate, summary.Title, lang); linked != "" {
			if localized := w.summary(ctx, lang, linked); localized != nil {
				return localized
			}
		}
		return summary
	}
	return nil
}

// langlink returns the title of an article in [to], given one in [from].
func (w *wikiClient) langlink(ctx context.Context, from, title, to string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	u := fmt.Sprintf(
		"https://%s.wikipedia.org/w/api.php?action=query&format=json&formatversion=2&redirects=1&prop=langlinks&lllang=%s&titles=%s",
		from, url.QueryEscape(to), url.QueryEscape(title),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", w.userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ""
	}
	var answer struct {
		Query struct {
			Pages []struct {
				Title     string `json:"title"`
				LangLinks []struct {
					Lang  string `json:"lang"`
					Title string `json:"title"`
				} `json:"langlinks"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return ""
	}
	for _, page := range answer.Query.Pages {
		for _, link := range page.LangLinks {
			if strings.EqualFold(link.Lang, to) && strings.TrimSpace(link.Title) != "" {
				return link.Title
			}
		}
	}
	return ""
}

// wikiTitleOf returns the first Wikipedia relation's article title, which is
// the article name the relation points at.
func wikiTitleOf(relations []mbRelation) string {
	for _, relation := range relations {
		if !strings.EqualFold(relation.Type, "wikipedia") {
			continue
		}
		resource := relation.URL.Resource
		idx := strings.Index(resource, "/wiki/")
		if idx < 0 {
			continue
		}
		title, err := url.PathUnescape(resource[idx+len("/wiki/"):])
		if err != nil {
			continue
		}
		return strings.ReplaceAll(title, "_", " ")
	}
	return ""
}
