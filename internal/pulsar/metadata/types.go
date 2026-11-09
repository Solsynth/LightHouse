package metadata

import (
	"context"

	"github.com/google/uuid"
)

// ExternalLink represents a link to an upstream media service.
type ExternalLink struct {
	Platform string `json:"platform"`
	Label    string `json:"label"`
	URL      string `json:"url"`
}

// UnifiedMedia represents canonical unified metadata across all domains.
type UnifiedMedia struct {
	ID                  uuid.UUID         `json:"id"`
	URN                 string            `json:"urn"`
	Domain              string            `json:"domain"`
	Slug                string            `json:"slug"`
	Title               string            `json:"title"`
	Subtitle            string            `json:"subtitle,omitempty"`
	Caption             string            `json:"caption,omitempty"`
	Description         string            `json:"description,omitempty"`
	ArtworkURL          string            `json:"artwork_url,omitempty"`
	BannerURL           string            `json:"banner_url,omitempty"`
	DurationSeconds     int               `json:"duration_seconds,omitempty"`
	ReleaseDate         string            `json:"release_date,omitempty"`
	Genres              []string          `json:"genres,omitempty"`
	ExternalIDs         map[string]string `json:"external_ids,omitempty"`
	ExternalLinks       []ExternalLink    `json:"external_links,omitempty"`
	ShareURL            string            `json:"share_url,omitempty"`
	ScrobbleCount       int64             `json:"scrobble_count"`
	ActiveListenerCount int64             `json:"active_listener_count"`
}

// Provider defines the interface for an upstream metadata provider.
type Provider interface {
	Name() string
	Domain() string
	FetchByProviderKey(ctx context.Context, key string) (*UnifiedMedia, error)
	Search(ctx context.Context, query string, limit int) ([]UnifiedMedia, error)
}
