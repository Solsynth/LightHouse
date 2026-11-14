// Package presencecmd defines the service-to-service commands that move live
// presence sessions into Pulsar. Stargate's provider scanners own the account
// connections and provider polling; Pulsar owns the activity rows and the
// broadcast.
package presencecmd

const (
	// Stream is the JetStream stream used for session commands.
	Stream = "pulsar_presence"
	// Subject carries both start and end commands.
	Subject = "pulsar.presence.session"
	// Consumer is the durable Pulsar consumer name.
	Consumer = "pulsar-presence-ingest"

	ActionStart = "start"
	ActionEnd   = "end"
)

// Command is one provider-driven session change.
type Command struct {
	AccountID string       `json:"account_id"`
	ManualID  string       `json:"manual_id"`
	Action    string       `json:"action"`
	Spec      *SessionSpec `json:"spec,omitempty"`
}

// SessionSpec is the provider payload Pulsar turns into an ActiveSession.
type SessionSpec struct {
	Type           string         `json:"type"`
	Provider       string         `json:"provider,omitempty"`
	ReferenceID    *string        `json:"reference_id,omitempty"`
	Title          *string        `json:"title,omitempty"`
	Subtitle       *string        `json:"subtitle,omitempty"`
	Caption        *string        `json:"caption,omitempty"`
	LargeImage     *string        `json:"large_image,omitempty"`
	SmallImage     *string        `json:"small_image,omitempty"`
	TitleURL       *string        `json:"title_url,omitempty"`
	SubtitleURL    *string        `json:"subtitle_url,omitempty"`
	QueryableTerms []string       `json:"queryable_terms,omitempty"`
	Meta           map[string]any `json:"meta,omitempty"`
	CatalogKey     string         `json:"catalog_key,omitempty"`
	CatalogName    *string        `json:"catalog_name,omitempty"`
	LeaseMinutes   int            `json:"lease_minutes,omitempty"`
}
