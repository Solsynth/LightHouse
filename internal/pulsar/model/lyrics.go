package model

import (
	"time"

	"github.com/google/uuid"
)

// Lyric origins. A contribution says where its words came from, because a
// lyric fetched from LRCLIB and one typed by hand are not the same evidence:
// the first can be re-fetched by anyone, the second only exists because
// somebody did the work.
const (
	LyricOriginLRCLIB = "lrclib"
	LyricOriginManual = "manual"
	LyricOriginFile   = "file"
	LyricOriginOther  = "other"
)

// SharedLyric is one account's contribution of a lyric for one catalog item.
//
// One row per (catalog, account, language): an account has one opinion of the
// words in one language, and re-sending replaces its earlier text instead of
// piling up revisions. Two accounts contributing the same LRCLIB record stay
// two rows — the votes decide which one other listeners get, and the same
// recording read in two languages is two different answers, not duplicates.
type SharedLyric struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	CatalogID uuid.UUID `gorm:"type:uuid;not null;index:ix_shared_lyrics_catalog" json:"catalog_id"`
	AccountID uuid.UUID `gorm:"type:uuid;not null;index:ix_shared_lyrics_account" json:"account_id"`

	// Language is the language the words themselves are in, lowercased BCP-47
	// when the contributor knew it. Empty means "not stated"; it still ranks
	// with the others rather than being invisible.
	Language string `gorm:"type:varchar(35);not null;default:'';index:ix_shared_lyrics_lang" json:"language"`

	// Lines is the client's own JSON line array (time, text, pronunciation,
	// translation), the same shape the library mirror stores, so a device can
	// adopt a contribution without a second conversion.
	Lines  string `gorm:"type:text;not null" json:"lines"`
	Synced bool   `gorm:"not null;default:false" json:"synced"`

	// TranslationLanguage names the language of the translation layer already
	// inside Lines, when the contributor's engine wrote one. Empty means the
	// lines carry no translation.
	TranslationLanguage *string `gorm:"type:varchar(35)" json:"translation_language,omitempty"`
	TranslationEngine   *string `gorm:"type:varchar(32)" json:"translation_engine,omitempty"`

	// Origin names where the words came from, and RemoteID the LRCLIB row when
	// Origin is lrclib: a later reader can fetch the same record and compare.
	Origin   string `gorm:"type:varchar(32);not null;default:'manual'" json:"origin"`
	RemoteID *int64 `json:"remote_id,omitempty"`

	// Score is the standing the contribution earned: +1 per adoption or
	// upvote, -1 per downvote. It is stored rather than summed per request so
	// a suggestion list is one indexed read; every write that changes it
	// recomputes it from the votes in the same transaction.
	//
	// AdoptionCount is the number of accounts that positive-voted the row —
	// adopting is one way to do that, so the two counters read the same rows
	// and an adoption moves both. Upvotes and Downvotes are the same rows
	// split by sign, for a client that draws them apart.
	Score         int `gorm:"not null;default:0;index:ix_shared_lyrics_score" json:"score"`
	AdoptionCount int `gorm:"not null;default:0" json:"adoption_count"`
	Upvotes       int `gorm:"not null;default:0" json:"upvotes"`
	Downvotes     int `gorm:"not null;default:0" json:"downvotes"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (SharedLyric) TableName() string { return "shared_lyrics" }

// SharedLyricVote is one account's thumb on one contribution. One row per
// (contribution, account): a second vote replaces the first, so an account
// cannot stack its own opinion.
type SharedLyricVote struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	LyricID   uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:ux_shared_lyric_vote" json:"lyric_id"`
	AccountID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:ux_shared_lyric_vote" json:"account_id"`
	// Value is +1 or -1.
	Value     int       `gorm:"not null" json:"value"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (SharedLyricVote) TableName() string { return "shared_lyric_votes" }

// Metadata fields an account may suggest for a catalog item. The set is the
// one a library can be wrong about — a mistagged title, a cover instead of the
// real artist, an album the release never had.
const (
	MetadataFieldTitle       = "title"
	MetadataFieldSubtitle    = "subtitle"
	MetadataFieldCaption     = "caption"
	MetadataFieldReleaseDate = "release_date"
	MetadataFieldArtworkURL  = "artwork_url"
	MetadataFieldGenre       = "genre"
)

// MetadataFields is the set a suggestion may name.
var MetadataFields = map[string]bool{
	MetadataFieldTitle:       true,
	MetadataFieldSubtitle:    true,
	MetadataFieldCaption:     true,
	MetadataFieldReleaseDate: true,
	MetadataFieldArtworkURL:  true,
	MetadataFieldGenre:       true,
}

// MetadataSuggestion is one account's proposed value for one field of one
// catalog item. One row per (catalog, account, field): an account's opinion of
// a field is one value; changing its mind replaces it.
//
// The row is a proposal, not an edit: nothing here is written onto the catalog
// item automatically. Agreement is what promotes it — a value several accounts
// independently suggest outranks one nobody else confirms.
type MetadataSuggestion struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	CatalogID uuid.UUID `gorm:"type:uuid;not null;index:ix_meta_sugg_catalog" json:"catalog_id"`
	AccountID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:ux_meta_sugg_account" json:"account_id"`
	Field     string    `gorm:"type:varchar(32);not null;uniqueIndex:ux_meta_sugg_account;index:ix_meta_sugg_field" json:"field"`
	Value     string    `gorm:"type:varchar(4096);not null" json:"value"`

	// Score is the suggestion's standing among the values proposed for the same
	// field: +1 per confirming account, -1 per rejection.
	Score     int `gorm:"not null;default:1;index:ix_meta_sugg_score" json:"score"`
	Upvotes   int `gorm:"not null;default:0" json:"upvotes"`
	Downvotes int `gorm:"not null;default:0" json:"downvotes"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (MetadataSuggestion) TableName() string { return "metadata_suggestions" }

// MetadataSuggestionVote is one account's thumb on a suggestion other than its
// own.
type MetadataSuggestionVote struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	SuggestionID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:ux_meta_sugg_vote" json:"suggestion_id"`
	AccountID    uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:ux_meta_sugg_vote" json:"account_id"`
	Value        int       `gorm:"not null" json:"value"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (MetadataSuggestionVote) TableName() string { return "metadata_suggestion_votes" }

// LyricsContributionModels is every table the lyrics module owns.
func LyricsContributionModels() []any {
	return []any{
		&SharedLyric{},
		&SharedLyricVote{},
		&MetadataSuggestion{},
		&MetadataSuggestionVote{},
	}
}
