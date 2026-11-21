// Package bangumi is the write-side Bangumi client: OAuth token exchange and
// the per-user collection endpoints. Metadata search lives in the metadata
// package; this package exists so account linking and mirroring share one
// transport, User-Agent and rate limiter.
package bangumi

import "time"

// Subject types as Bangumi numbers them. There is deliberately no 5; music is
// 3, but Pulsar's music domain is track-shaped and served by MusicBrainz.
const (
	SubjectTypeBook  = 1
	SubjectTypeAnime = 2
	SubjectTypeMusic = 3
	SubjectTypeGame  = 4
	SubjectTypeReal  = 6
)

// Collection types on a user's subject collection.
const (
	CollectionWish    = 1
	CollectionDone    = 2
	CollectionDoing   = 3
	CollectionOnHold  = 4
	CollectionDropped = 5
)

// Images is Bangumi's image set. Different endpoints fill different subsets;
// Best picks the one a card should show.
type Images struct {
	Large  string `json:"large"`
	Common string `json:"common"`
	Medium string `json:"medium"`
	Small  string `json:"small"`
	Grid   string `json:"grid"`
}

// Best returns the largest usable image.
func (i Images) Best() string {
	for _, candidate := range []string{i.Large, i.Common, i.Medium, i.Small, i.Grid} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

// Token is the OAuth token response. Bangumi returns the numeric user id on
// the initial exchange and on refresh, but not on every refresh implementation;
// callers must treat zero as "ask /v0/me".
type Token struct {
	AccessToken  string  `json:"access_token"`
	ExpiresIn    int64   `json:"expires_in"`
	TokenType    string  `json:"token_type"`
	Scope        *string `json:"scope"`
	RefreshToken string  `json:"refresh_token"`
	UserID       int64   `json:"user_id"`
}

// ExpiresAt converts ExpiresIn to an absolute UTC instant.
func (t Token) ExpiresAt(now time.Time) time.Time {
	if t.ExpiresIn <= 0 {
		return time.Time{}
	}
	return now.UTC().Add(time.Duration(t.ExpiresIn) * time.Second)
}

// User is the profile /v0/me and /v0/users/{username} return.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	Sign     string `json:"sign"`
	Avatar   Images `json:"avatar"`
}

// SubjectTag is one tag with its usage counts.
type SubjectTag struct {
	Name       string `json:"name"`
	Count      int    `json:"count"`
	TotalCount int    `json:"total_count"`
}

// SlimSubject is the subject summary embedded in a collection row. It is
// enough to create a catalog record without a second request.
type SlimSubject struct {
	ID              int          `json:"id"`
	Type            int          `json:"type"`
	Name            string       `json:"name"`
	NameCN          string       `json:"name_cn"`
	ShortSummary    string       `json:"short_summary"`
	Date            string       `json:"date"`
	Images          Images       `json:"images"`
	Eps             int          `json:"eps"`
	Volumes         int          `json:"volumes"`
	CollectionTotal int          `json:"collection_total"`
	Score           float64      `json:"score"`
	Rank            int          `json:"rank"`
	Tags            []SubjectTag `json:"tags"`
}

// SubjectCollection is one row of a user's list.
type SubjectCollection struct {
	SubjectID   int         `json:"subject_id"`
	SubjectType int         `json:"subject_type"`
	Rate        int         `json:"rate"`
	Type        int         `json:"type"`
	Comment     string      `json:"comment"`
	Tags        []string    `json:"tags"`
	EpStatus    int         `json:"ep_status"`
	VolStatus   int         `json:"vol_status"`
	UpdatedAt   time.Time   `json:"updated_at"`
	Private     bool        `json:"private"`
	Subject     SlimSubject `json:"subject"`
}

// CollectionPage is one page of a user's collections.
type CollectionPage struct {
	Total  int                 `json:"total"`
	Limit  int                 `json:"limit"`
	Offset int                 `json:"offset"`
	Data   []SubjectCollection `json:"data"`
}

// CollectionPayload is the body of a collection create or patch. Nil fields
// are omitted, which Bangumi interprets as "leave alone" — the difference
// between clearing a rating and not touching it.
type CollectionPayload struct {
	Type      *int     `json:"type,omitempty"`
	Rate      *int     `json:"rate,omitempty"`
	EpStatus  *int     `json:"ep_status,omitempty"`
	VolStatus *int     `json:"vol_status,omitempty"`
	Comment   *string  `json:"comment,omitempty"`
	Private   *bool    `json:"private,omitempty"`
	Tags      []string `json:"tags,omitempty"`
}
