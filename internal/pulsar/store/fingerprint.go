package store

import (
	"regexp"
	"strings"
	"unicode"
)

// Catalog fingerprints.
//
// A catalog write names a recording two ways: the writer's own key (provider +
// provider_key) and the song's metadata. Keys are per-device facts — two
// phones holding the same NetEase or Apple Music account mint different ones —
// so the key probe alone leaves one recording as two rows, and a heart set on
// one device never matches the other's list. The fingerprint is the metadata
// half: artist and title folded to one spelling, matching the client's own
// `trackBaseFingerprint`, so the same song lands on the same row whichever
// device wrote it.
//
// The length is deliberately not part of it. Two services time one master a
// few seconds apart, and a length mismatch must not split a heart in half —
// the exact-title probe that already existed has the same rule.

// Bracketed credits vary by source (`(feat. X)`, `[Remastered 2011]`,
// `{Live}`) and say nothing about which song it is.
var (
	catalogBracketed    = regexp.MustCompile(`[\(\[\{][^\)\]\}]*[\)\]\}]`)
	catalogTitleSuffix  = regexp.MustCompile(`(?i)\s*[-–—/]\s*(?:(?:\d{4}\s+)?remaster(?:ed)?(?:\s*\d{4})?|live(?:\s+.*)?|radio\s+edit|single\s+version|album\s+version|original\s+mix|mono|stereo|deluxe(?:\s+edition)?).*$`)
	catalogFeaturedTail = regexp.MustCompile(`(?i)\s*(?:[-–—/]\s*)?(?:feat\.?|ft\.?|featuring)\s+.*$`)
)

// catalogFingerprint is the normalized artist+title identity of one record.
//
// Two writes of the same recording agree on it even when the artist credit
// carries a featured act, the title carries an edition suffix, or the two
// sources differ in case, diacritics and punctuation. Empty when there is no
// title to normalize — a record the caller cannot identify must not match
// every other unidentified one.
func catalogFingerprint(title string, subtitle *string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	artist := ""
	if subtitle != nil {
		artist = *subtitle
	}
	return normalizeCatalogName(artist, true) + "|" + normalizeCatalogName(title, false)
}

// normalizeCatalogName folds one name: bracketed credits and edition
// suffixes stripped, diacritics removed, lowercase, letters and digits only.
// A value that normalizes to nothing falls back to its trimmed lowercase
// form so it can never collide with every other empty name.
func normalizeCatalogName(value string, isArtist bool) string {
	stripped := catalogBracketed.ReplaceAllString(value, " ")
	if !isArtist {
		stripped = catalogTitleSuffix.ReplaceAllString(stripped, " ")
	}
	stripped = catalogFeaturedTail.ReplaceAllString(stripped, " ")

	var b strings.Builder
	b.Grow(len(stripped))
	for _, r := range stripped {
		if r < unicode.MaxASCII {
			r = foldDiacritic(r)
		}
		r = unicode.ToLower(r)
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	if normalized := b.String(); normalized != "" {
		return normalized
	}
	return strings.ToLower(strings.TrimSpace(value))
}

// foldDiacritic maps the Latin-1 accents two sources spell differently onto
// their base letters. The set mirrors the client's own table, so both sides of
// the wire normalize the same way.
func foldDiacritic(r rune) rune {
	switch r {
	case 'À', 'Á', 'Â', 'Ã', 'Ä', 'Å', 'à', 'á', 'â', 'ã', 'ä', 'å':
		return 'a'
	case 'Ò', 'Ó', 'Ô', 'Õ', 'Ö', 'Ø', 'ò', 'ó', 'ô', 'õ', 'ö', 'ø':
		return 'o'
	case 'È', 'É', 'Ê', 'Ë', 'è', 'é', 'ê', 'ë':
		return 'e'
	case 'Ç', 'ç':
		return 'c'
	case 'Ì', 'Í', 'Î', 'Ï', 'ì', 'í', 'î', 'ï':
		return 'i'
	case 'Ù', 'Ú', 'Û', 'Ü', 'ù', 'ú', 'û', 'ü':
		return 'u'
	case 'Ý', 'ý', 'ÿ':
		return 'y'
	case 'Ñ', 'ñ':
		return 'n'
	}
	return r
}
