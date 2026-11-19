package metadata

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

// MusicBrainzArtistProvider answers for an artist: a person or a group, by
// MusicBrainz artist id, with a biography, a portrait, genres, and the
// release groups the artist recorded.
//
// A name is the only handle a library gives us — Subsonic, Jellyfin, NetEase
// and a local file all name the act and none of them agree on an id — so the
// provider also resolves by name, and the engine caches that answer.
type MusicBrainzArtistProvider struct {
	mb   *mbClient
	wiki *wikiClient
}

func (p *MusicBrainzArtistProvider) Name() string   { return "musicbrainz-artist" }
func (p *MusicBrainzArtistProvider) Domain() string { return model.DomainMusic }
func (p *MusicBrainzArtistProvider) Kind() string   { return KindArtist }

// Release groups beyond this are not listed on a page; the count still says
// how many there are.
const artistReleaseGroupLimit = 40

func (p *MusicBrainzArtistProvider) FetchByProviderKey(ctx context.Context, mbid string) (*UnifiedMedia, error) {
	mbid = strings.TrimSpace(mbid)
	if mbid == "" {
		return nil, fmt.Errorf("empty musicbrainz artist mbid")
	}

	var artist mbArtist
	err := p.mb.get(ctx, "/ws/2/artist/"+url.PathEscape(mbid), url.Values{
		"inc": {"url-rels+genres+tags+release-groups+aliases"},
		"fmt": {"json"},
	}, &artist)
	if err != nil {
		return nil, fmt.Errorf("musicbrainz artist %s: %w", mbid, err)
	}
	if strings.TrimSpace(artist.Name) == "" {
		return nil, fmt.Errorf("musicbrainz artist %s has no name", mbid)
	}

	profile := &ArtistProfile{
		MBID:           mbid,
		Type:           artist.Type,
		SortName:       artist.SortName,
		Disambiguation: artist.Disambiguation,
		Country:        artist.Country,
		Genres:         firstNonEmpty(tagNames(artist.Genres, 0), tagNames(artist.Tags, 0)),
	}
	if artist.Area != nil {
		profile.Area = artist.Area.Name
	}
	if artist.BeginArea != nil {
		profile.BeginArea = artist.BeginArea.Name
	}
	if artist.LifeSpan != nil {
		profile.Born = artist.LifeSpan.Begin
		profile.Died = artist.LifeSpan.End
	}

	// The article MusicBrainz links is the one about this act; the reader's
	// language first, English second.
	bioTitle := wikiTitleOf(artist.Relations)
	var summary *wikiSummary
	if bioTitle != "" {
		summary = p.wiki.localizedSummary(ctx, bioTitle, readerLang(ctx))
	}
	if summary == nil {
		summary = p.wiki.localizedSummary(ctx, artist.Name, readerLang(ctx))
	}

	links := []ExternalLink{{
		Platform: "musicbrainz",
		Label:    "MusicBrainz Artist",
		URL:      "https://musicbrainz.org/artist/" + mbid,
	}}
	if summary != nil {
		profile.URL = articleURL(summary.Title, summary.Lang)
		links = append(links, ExternalLink{
			Platform: "wikipedia",
			Label:    "Wikipedia",
			URL:      profile.URL,
		})
	}
	for _, homepage := range relationsOf(artist.Relations, "official homepage") {
		links = append(links, ExternalLink{
			Platform: "homepage",
			Label:    "Official site",
			URL:      homepage,
		})
		break
	}

	media := &UnifiedMedia{
		URN:      fmt.Sprintf("pulsar:music:musicbrainz-artist:%s", mbid),
		Domain:   model.DomainMusic,
		Slug:     "mbid-artist-" + mbid,
		Title:    artist.Name,
		Subtitle: artistSubtitle(profile),
		Artist:   profile,
		ExternalIDs: map[string]string{
			"musicbrainz": mbid,
		},
		ExternalLinks: links,
		Creators:      []model.CreatorCredit{{Name: artist.Name, Role: model.RoleArtist}},
	}
	if summary != nil {
		media.Description = summary.Extract
		media.Bio = summary.Extract
		media.BioSource = strings.ToUpper(summary.Lang) + " Wikipedia"
		media.ImageURL = imageOf(summary)
		media.ArtworkURL = media.ImageURL
	}
	if profile.Died != "" {
		// A dissolved group or a death date is not a genre, but it is the one
		// fact a reader looks for after the name: keep it out of the prose and
		// in the structured profile, which the client renders as a line.
		media.Artist.Died = profile.Died
	}

	media.Albums = p.albumsOf(artist)
	media.ReleaseGroupCount = len(artistRelations(artist, "release-groups"))
	if media.ReleaseGroupCount == 0 {
		media.ReleaseGroupCount = len(media.Albums)
	}
	return media, nil
}

// albumsOf turns the lookup's release groups into the list an artist page
// draws, newest last so the first record reads first.
func (p *MusicBrainzArtistProvider) albumsOf(artist mbArtist) []AlbumSummary {
	groups := artist.ReleaseGroups
	albums := make([]AlbumSummary, 0, min(len(groups), artistReleaseGroupLimit))
	seen := make(map[string]bool, len(groups))
	for _, group := range groups {
		if group.ID == "" || seen[group.ID] {
			continue
		}
		seen[group.ID] = true
		// Compilations and live records are documented but not what "their
		// albums" means; a page that mixed them in would bury the records.
		if isSecondaryOnly(group) {
			continue
		}
		albums = append(albums, AlbumSummary{
			MBID:       group.ID,
			Title:      group.Title,
			Year:       yearOf(group.FirstReleaseDate),
			Type:       groupType(group),
			ArtworkURL: p.mb.caaURL + "/release-group/" + url.PathEscape(group.ID) + "/front-500",
		})
		if len(albums) >= artistReleaseGroupLimit {
			break
		}
	}
	return albums
}

// Search is the generic search path's view of an artist: a name query,
// answered with artist records rather than recordings. Each result resolves
// lazily, so a search costs one upstream request, not one per row.
func (p *MusicBrainzArtistProvider) Search(ctx context.Context, query string, limit int) ([]UnifiedMedia, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 20 {
		limit = 10
	}
	var answer struct {
		Artists []struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			Disambiguation string `json:"disambiguation"`
			Country        string `json:"country"`
			Type           string `json:"type"`
		} `json:"artists"`
	}
	err := p.mb.get(ctx, "/ws/2/artist", url.Values{
		"query": {query},
		"fmt":   {"json"},
		"limit": {fmt.Sprint(limit)},
	}, &answer)
	if err != nil {
		return nil, fmt.Errorf("musicbrainz artist search: %w", err)
	}
	results := make([]UnifiedMedia, 0, len(answer.Artists))
	for _, artist := range answer.Artists {
		if artist.ID == "" {
			continue
		}
		results = append(results, UnifiedMedia{
			URN:      fmt.Sprintf("pulsar:music:musicbrainz-artist:%s", artist.ID),
			Domain:   model.DomainMusic,
			Slug:     "mbid-artist-" + artist.ID,
			Title:    artist.Name,
			Subtitle: strings.ToLower(artist.Type),
			Artist: &ArtistProfile{
				MBID:           artist.ID,
				Type:           artist.Type,
				Disambiguation: artist.Disambiguation,
				Country:        artist.Country,
			},
			ExternalIDs: map[string]string{"musicbrainz": artist.ID},
			Creators:    []model.CreatorCredit{{Name: artist.Name, Role: model.RoleArtist}},
		})
	}
	return results, nil
}

// ResolveByName finds the artist MusicBrainz means by [name], and returns the
// URN to resolve.
//
// The name a library gives us is usually the act's Latin-script alias, not
// its canonical name: a Japanese artist is "多田葵" in MusicBrainz and "Aoi
// Tada" in every Subsonic tag. So the query is free text — which matches
// names and aliases alike — and the decision is made here: the same string
// as the name or as one of its aliases, at the best score. A near miss is
// not a match, because two acts with one name must not silently become one
// page.
func (p *MusicBrainzArtistProvider) ResolveByName(ctx context.Context, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("empty artist name")
	}

	var answer struct {
		Artists []struct {
			ID      string    `json:"id"`
			Name    string    `json:"name"`
			Score   int       `json:"score"`
			Aliases []mbAlias `json:"aliases"`
		} `json:"artists"`
	}
	err := p.mb.get(ctx, "/ws/2/artist", url.Values{
		"query": {luceneQuote(name)},
		"fmt":   {"json"},
		"limit": {"8"},
	}, &answer)
	if err != nil {
		return "", fmt.Errorf("musicbrainz artist search: %w", err)
	}

	best := ""
	bestScore := -1
	for _, candidate := range answer.Artists {
		if candidate.ID == "" || !matchesArtistName(name, candidate.Name, candidate.Aliases) {
			continue
		}
		score := candidate.Score
		// The canonical name is the one MusicBrainz means when a library
		// happens to spell it; an alias hit is equally valid but loses a tie.
		if strings.EqualFold(candidate.Name, name) {
			score++
		}
		if score > bestScore {
			best, bestScore = candidate.ID, score
		}
	}
	if best == "" {
		return "", fmt.Errorf("no musicbrainz artist named %q", name)
	}
	return fmt.Sprintf("pulsar:music:musicbrainz-artist:%s", best), nil
}

// ── Album ──────────────────────────────────────────────────────────────────

// MusicBrainzAlbumProvider answers for a release group — the record as a
// work, rather than one edition of it: title, release date, artists, cover,
// genre, a description, and the tracklist of a chosen release.
type MusicBrainzAlbumProvider struct {
	mb   *mbClient
	wiki *wikiClient
}

func (p *MusicBrainzAlbumProvider) Name() string   { return "musicbrainz-album" }
func (p *MusicBrainzAlbumProvider) Domain() string { return model.DomainMusic }
func (p *MusicBrainzAlbumProvider) Kind() string   { return KindAlbum }

func (p *MusicBrainzAlbumProvider) FetchByProviderKey(ctx context.Context, mbid string) (*UnifiedMedia, error) {
	mbid = strings.TrimSpace(mbid)
	if mbid == "" {
		return nil, fmt.Errorf("empty musicbrainz release group mbid")
	}

	var group mbReleaseGroup
	err := p.mb.get(ctx, "/ws/2/release-group/"+url.PathEscape(mbid), url.Values{
		"inc": {"artist-credits+releases+genres+tags+url-rels"},
		"fmt": {"json"},
	}, &group)
	if err != nil {
		return nil, fmt.Errorf("musicbrainz release group %s: %w", mbid, err)
	}
	if strings.TrimSpace(group.Title) == "" {
		return nil, fmt.Errorf("musicbrainz release group %s has no title", mbid)
	}

	artistNames, credits := groupCredits(group)
	artistStr := strings.Join(artistNames, ", ")

	release := p.pickRelease(ctx, &group)
	media := &UnifiedMedia{
		URN:         fmt.Sprintf("pulsar:music:musicbrainz-album:%s", mbid),
		Domain:      model.DomainMusic,
		Slug:        "mbid-album-" + mbid,
		Title:       group.Title,
		Subtitle:    artistStr,
		Caption:     groupType(group),
		ReleaseDate: group.FirstReleaseDate,
		Genres:      firstNonEmpty(tagNames(group.Genres, 0), tagNames(group.Tags, 0)),
		Creators:    credits,
		ExternalIDs: map[string]string{
			"musicbrainz_release_group": mbid,
		},
		ExternalLinks: []ExternalLink{{
			Platform: "musicbrainz",
			Label:    "MusicBrainz Release",
			URL:      "https://musicbrainz.org/release-group/" + mbid,
		}},
	}
	if release != nil {
		media.ExternalIDs["musicbrainz_release"] = release.ID
		media.Tracks = tracksOf(release)
		var total int
		for _, track := range media.Tracks {
			total += track.LengthMs
		}
		media.DurationSeconds = total / 1000
	}

	// A cover is checked once: the Cover Art Archive answers 307 to the image
	// when it exists, so a resolved row never carries a URL that 404s.
	media.ArtworkURL = p.mb.coverURL(ctx, mbid, releaseIDOf(release))

	// Wikipedia has no relation for most release groups; the article is
	// usually under the album's own title. An extract that never names the
	// artist is a different record's page, so it is dropped.
	if summary := p.articleOf(ctx, &group, artistNames); summary != nil {
		media.Description = summary.Extract
		media.Bio = summary.Extract
		media.BioSource = strings.ToUpper(summary.Lang) + " Wikipedia"
		media.ExternalLinks = append(media.ExternalLinks, ExternalLink{
			Platform: "wikipedia",
			Label:    "Wikipedia",
			URL:      articleURL(summary.Title, summary.Lang),
		})
	}
	return media, nil
}

// pickRelease chooses the edition whose tracklist stands for the record:
// the earliest official release, because that is the one whose running order
// everyone knows.
func (p *MusicBrainzAlbumProvider) pickRelease(ctx context.Context, group *mbReleaseGroup) *mbRelease {
	candidates := group.Releases
	if len(candidates) == 0 {
		return nil
	}
	chosen := 0
	for i, release := range candidates {
		if strings.EqualFold(release.Status, "Official") {
			chosen = i
			break
		}
	}
	for i := chosen + 1; i < len(candidates); i++ {
		if !strings.EqualFold(candidates[i].Status, "Official") {
			continue
		}
		if candidates[i].Date != "" && (candidates[chosen].Date == "" || candidates[i].Date < candidates[chosen].Date) {
			chosen = i
		}
	}

	var release mbRelease
	err := p.mb.get(ctx, "/ws/2/release/"+url.PathEscape(candidates[chosen].ID), url.Values{
		"inc": {"recordings+artist-credits"},
		"fmt": {"json"},
	}, &release)
	if err != nil {
		return nil
	}
	return &release
}

// articleOf finds the description for one release group, trying the title
// alone and then "title (artist album)".
func (p *MusicBrainzAlbumProvider) articleOf(ctx context.Context, group *mbReleaseGroup, artistNames []string) *wikiSummary {
	lang := readerLang(ctx)
	titles := []string{group.Title}
	if len(artistNames) > 0 {
		titles = append(titles, group.Title+" ("+artistNames[0]+" album)")
	}
	for _, title := range titles {
		summary := p.wiki.localizedSummary(ctx, title, lang)
		if summary == nil {
			continue
		}
		// Without the artist's name the extract describes something else
		// that shares the title.
		if len(artistNames) > 0 && !strings.Contains(strings.ToLower(summary.Extract), strings.ToLower(artistNames[0])) {
			continue
		}
		return summary
	}
	return nil
}

// Search answers the generic search path with records rather than tracks.
func (p *MusicBrainzAlbumProvider) Search(ctx context.Context, query string, limit int) ([]UnifiedMedia, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 20 {
		limit = 10
	}
	var answer struct {
		Groups []struct {
			ID               string           `json:"id"`
			Title            string           `json:"title"`
			Score            int              `json:"score"`
			PrimaryType      string           `json:"primary-type"`
			FirstReleaseDate string           `json:"first-release-date"`
			ArtistCredit     []mbArtistCredit `json:"artist-credit"`
		} `json:"release-groups"`
	}
	err := p.mb.get(ctx, "/ws/2/release-group", url.Values{
		"query": {query},
		"fmt":   {"json"},
		"limit": {fmt.Sprint(limit)},
	}, &answer)
	if err != nil {
		return nil, fmt.Errorf("musicbrainz release group search: %w", err)
	}
	results := make([]UnifiedMedia, 0, len(answer.Groups))
	for _, group := range answer.Groups {
		if group.ID == "" {
			continue
		}
		results = append(results, UnifiedMedia{
			URN:         fmt.Sprintf("pulsar:music:musicbrainz-album:%s", group.ID),
			Domain:      model.DomainMusic,
			Slug:        "mbid-album-" + group.ID,
			Title:       group.Title,
			Subtitle:    creditNames(group.ArtistCredit),
			Caption:     group.PrimaryType,
			ReleaseDate: group.FirstReleaseDate,
			ArtworkURL:  p.mb.caaURL + "/release-group/" + url.PathEscape(group.ID) + "/front-500",
			ExternalIDs: map[string]string{"musicbrainz_release_group": group.ID},
		})
	}
	return results, nil
}

// ResolveByTitle finds the release group MusicBrainz means by one album
// title and its artist.
func (p *MusicBrainzAlbumProvider) ResolveByTitle(ctx context.Context, title, artist string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", fmt.Errorf("empty album title")
	}
	query := "releasegroup:" + luceneQuote(title)
	if artist = strings.TrimSpace(artist); artist != "" {
		query += " AND artist:" + luceneQuote(artist)
	}

	var answer struct {
		Groups []struct {
			ID           string           `json:"id"`
			Title        string           `json:"title"`
			Score        int              `json:"score"`
			PrimaryType  string           `json:"primary-type"`
			ArtistCredit []mbArtistCredit `json:"artist-credit"`
		} `json:"release-groups"`
	}
	err := p.mb.get(ctx, "/ws/2/release-group", url.Values{
		"query": {query},
		"fmt":   {"json"},
		"limit": {"8"},
	}, &answer)
	if err != nil {
		return "", fmt.Errorf("musicbrainz release group search: %w", err)
	}

	best := ""
	bestScore := -1
	for _, group := range answer.Groups {
		if group.ID == "" || !strings.EqualFold(strings.TrimSpace(group.Title), title) {
			continue
		}
		// An EP or a single that shares the album's name is a different
		// record: prefer the album, and only fall back when nothing else
		// matched.
		score := group.Score
		if strings.EqualFold(group.PrimaryType, "Album") {
			score += 10
		}
		if score > bestScore {
			best, bestScore = group.ID, score
		}
	}
	if best == "" {
		return "", fmt.Errorf("no musicbrainz release group titled %q", title)
	}
	return fmt.Sprintf("pulsar:music:musicbrainz-album:%s", best), nil
}

// ── helpers ────────────────────────────────────────────────────────────────

// readerLang is the language the caller asked the description for. The
// client sends its UI language; without one, English is the safe answer.
func readerLang(ctx context.Context) string {
	if lang, ok := ctx.Value(readerLangKey{}).(string); ok && strings.TrimSpace(lang) != "" {
		return lang
	}
	return "en"
}

// readerLangKey carries the caller's language through the resolve path
// without every provider method taking one more parameter.
type readerLangKey struct{}

// ContextWithReaderLang tags [ctx] with the reader's language, so
// descriptions come back in the language the interface is in.
func ContextWithReaderLang(ctx context.Context, lang string) context.Context {
	return context.WithValue(ctx, readerLangKey{}, lang)
}

func groupCredits(group mbReleaseGroup) ([]string, []model.CreatorCredit) {
	names := make([]string, 0, len(group.ArtistCredit))
	credits := make([]model.CreatorCredit, 0, len(group.ArtistCredit))
	for _, credit := range group.ArtistCredit {
		name := strings.TrimSpace(credit.Name)
		if name == "" {
			name = strings.TrimSpace(credit.Artist.Name)
		}
		if name == "" {
			continue
		}
		names = append(names, name)
		credits = append(credits, model.CreatorCredit{Name: name, Role: model.RoleArtist})
	}
	return names, credits
}

func tracksOf(release *mbRelease) []AlbumTrack {
	if release == nil {
		return nil
	}
	tracks := make([]AlbumTrack, 0, 12)
	for discIndex, medium := range release.Media {
		disc := medium.Position
		if disc == 0 {
			disc = discIndex + 1
		}
		for trackIndex, track := range medium.Tracks {
			position := trackIndex + 1
			tracks = append(tracks, AlbumTrack{
				RecordingMBID: track.ID,
				Title:         track.Title,
				Artist:        creditNames(track.ArtistCredit),
				Disc:          disc,
				Position:      position,
				LengthMs:      track.Length,
			})
		}
	}
	return tracks
}

func creditNames(credits []mbArtistCredit) string {
	names := make([]string, 0, len(credits))
	for _, credit := range credits {
		if name := strings.TrimSpace(credit.Name); name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, ", ")
}

// releaseIDOf returns a release's id, for the cover fallback.
func releaseIDOf(release *mbRelease) string {
	if release == nil {
		return ""
	}
	return release.ID
}

// groupType spells a release group's kind: "Album", "EP", "Single", and the
// secondary kinds a listener uses to tell a live record from a studio one.
func groupType(group mbReleaseGroup) string {
	parts := make([]string, 0, 1+len(group.SecondaryTypes))
	if group.PrimaryType != "" {
		parts = append(parts, group.PrimaryType)
	}
	for _, secondary := range group.SecondaryTypes {
		if secondary != "" {
			parts = append(parts, secondary)
		}
	}
	return strings.Join(parts, " + ")
}

// isSecondaryOnly reports whether a release group is a compilation, live
// record, remix or soundtrack rather than a studio record of its own.
func isSecondaryOnly(group mbReleaseGroup) bool {
	if len(group.SecondaryTypes) == 0 {
		return false
	}
	for _, secondary := range group.SecondaryTypes {
		switch strings.ToLower(secondary) {
		case "compilation", "live", "remix", "dj-mix", "mixtape/street", "demo", "interview", "audiobook", "audio drama", "spokenword", "field recording":
			return true
		}
	}
	return false
}

// yr parses the year out of a MusicBrainz date ("1997-05-21", "1997", "").
func yearOf(date string) int {
	if len(date) < 4 {
		return 0
	}
	year := 0
	for _, r := range date[:4] {
		if r < '0' || r > '9' {
			return 0
		}
		year = year*10 + int(r-'0')
	}
	return year
}

// artistSubtitle is the one line under an artist's name: what they are and
// where they are from.
func artistSubtitle(profile *ArtistProfile) string {
	parts := make([]string, 0, 3)
	if profile.Type != "" {
		// "Group" reads better lowercased in a line that also names a country.
		parts = append(parts, strings.ToLower(profile.Type))
	}
	if profile.Area != "" {
		parts = append(parts, profile.Area)
	}
	// An area already names the place; the bare country code would just be
	// "Japan · JP". Keep the code only when there is no area to name.
	if profile.Country != "" && profile.Area == "" {
		parts = append(parts, profile.Country)
	}
	return strings.Join(parts, " · ")
}

// mbAlias is one of an artist's alternate names — the spelling a library
// outside MusicBrainz's own canon uses.
type mbAlias struct {
	Name    string `json:"name"`
	Locale  string `json:"locale"`
	Primary bool   `json:"primary"`
}

// matchesArtistName reports whether [name] is the act's canonical name or one
// of its aliases, ignoring case, spacing and punctuation — the differences
// between "Aoi Tada", "Tada Aoi" and "多田 葵" are formatting, not identity.
func matchesArtistName(name, canonical string, aliases []mbAlias) bool {
	normalized := normalizeName(name)
	if normalized == "" {
		return false
	}
	if normalizeName(canonical) == normalized {
		return true
	}
	for _, alias := range aliases {
		if normalizeName(alias.Name) == normalized {
			return true
		}
	}
	return false
}

// normalizeName folds case, spaces and punctuation, which differ between
// libraries describing the same act.
func normalizeName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch r {
		case ' ', '\t', '\u3000', '-', '‐', '‑', '–', '—', '_', '.', ',', '\'', '’', '(', ')', '・':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// luceneQuote wraps a phrase for a MusicBrainz search field, escaping the
// characters Lucene treats as syntax.
func luceneQuote(term string) string {
	escaped := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		`(`, `\(`,
		`)`, `\)`,
		`[`, `\[`,
		`]`, `\]`,
		`{`, `\{`,
		`}`, `\}`,
		`^`, `\^`,
		`~`, `\~`,
		`:`, `\:`,
		`/`, `\/`,
		`+`, `\+`,
		`-`, `\-`,
	).Replace(term)
	return `"` + escaped + `"`
}

// firstNonEmpty returns the first non-empty slice, so a provider can prefer
// curated genres over free-form tags.
func firstNonEmpty(candidates ...[]string) []string {
	for _, candidate := range candidates {
		if len(candidate) > 0 {
			return candidate
		}
	}
	return nil
}

// artistRelations counts the entities of one relation kind on an artist
// lookup.
func artistRelations(artist mbArtist, kind string) []struct{ ID string } {
	if kind != "release-groups" {
		return nil
	}
	out := make([]struct{ ID string }, 0, len(artist.ReleaseGroups))
	for _, group := range artist.ReleaseGroups {
		out = append(out, struct{ ID string }{ID: group.ID})
	}
	return out
}
