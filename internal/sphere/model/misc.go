package model

// AutomodRule is one automoderation rule.
type AutomodRule struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Description   *string `json:"description"`
	Type          int     `json:"type"`
	DefaultAction int     `json:"default_action"`
	Pattern       string  `json:"pattern"`
	IsRegex       bool    `json:"is_regex"`
	DerankWeight  int     `json:"derank_weight"`
	IsEnabled     bool    `json:"is_enabled"`
	ExpiresAt     *Time   `json:"expires_at"`
	Priority      int     `json:"priority"`
	CreatedAt     *Time   `json:"created_at"`
	UpdatedAt     *Time   `json:"updated_at"`
	DeletedAt     *Time   `json:"deleted_at"`
}

// AutomodRuleResult is one rule evaluation hit.
type AutomodRuleResult struct {
	RuleID       string  `json:"rule_id"`
	RuleName     string  `json:"rule_name"`
	Type         int     `json:"type"`
	Action       int     `json:"action"`
	DerankWeight int     `json:"derank_weight"`
	MatchedText  *string `json:"matched_text"`
}

// LiveStream is one live stream.
type LiveStream struct {
	ID                     string     `json:"id"`
	Title                  *string    `json:"title"`
	Description            *string    `json:"description"`
	Slug                   *string    `json:"slug"`
	Type                   int        `json:"type"`
	Visibility             int        `json:"visibility"`
	Status                 int        `json:"status"`
	RoomName               string     `json:"room_name"`
	IngressID              *string    `json:"ingress_id"`
	IngressStreamKey       *string    `json:"ingress_stream_key"`
	EgressID               *string    `json:"egress_id"`
	HLSEgressID            *string    `json:"hls_egress_id"`
	HLSPlaylistPath        *string    `json:"hls_playlist_path"`
	HLSStartedAt           *Time      `json:"hls_started_at"`
	StartedAt              *Time      `json:"started_at"`
	EndedAt                *Time      `json:"ended_at"`
	TotalDurationSeconds   int64      `json:"total_duration_seconds"`
	ViewerCount            int        `json:"viewer_count"`
	PeakViewerCount        int        `json:"peak_viewer_count"`
	TotalAwardScore        float64    `json:"total_award_score"`
	DistributedAwardAmount float64    `json:"distributed_award_amount"`
	Thumbnail              any        `json:"thumbnail"`
	Metadata               any        `json:"metadata"`
	PublisherID            *string    `json:"publisher_id"`
	Publisher              *Publisher `json:"publisher"`
	CreatedAt              *Time      `json:"created_at"`
	UpdatedAt              *Time      `json:"updated_at"`
	DeletedAt              *Time      `json:"deleted_at"`
}

// LiveStreamChatMessage is one live chat message.
type LiveStreamChatMessage struct {
	ID           string `json:"id"`
	LiveStreamID string `json:"live_stream_id"`
	SenderID     string `json:"sender_id"`
	SenderName   string `json:"sender_name"`
	Content      string `json:"content"`
	TimeoutUntil *Time  `json:"timeout_until"`
	CreatedAt    *Time  `json:"created_at"`
	UpdatedAt    *Time  `json:"updated_at"`
	DeletedAt    *Time  `json:"deleted_at"`
}

// LiveStreamAward is one award on a live stream.
type LiveStreamAward struct {
	ID           string  `json:"id"`
	Amount       float64 `json:"amount"`
	Attitude     int     `json:"attitude"`
	Message      *string `json:"message"`
	LiveStreamID string  `json:"live_stream_id"`
	AccountID    string  `json:"account_id"`
	SenderName   string  `json:"sender_name"`
	CreatedAt    *Time   `json:"created_at"`
	UpdatedAt    *Time   `json:"updated_at"`
	DeletedAt    *Time   `json:"deleted_at"`
}

// FediverseInstance is one federated instance.
type FediverseInstance struct {
	ID                     string  `json:"id"`
	Domain                 string  `json:"domain"`
	Name                   *string `json:"name"`
	Description            *string `json:"description"`
	Software               *string `json:"software"`
	Version                *string `json:"version"`
	IconURL                *string `json:"icon_url"`
	ThumbnailURL           *string `json:"thumbnail_url"`
	ContactEmail           *string `json:"contact_email"`
	ContactAccountUsername *string `json:"contact_account_username"`
	ActiveUsers            *int    `json:"active_users"`
	Metadata               any     `json:"metadata"`
	IsBlocked              bool    `json:"is_blocked"`
	IsSilenced             bool    `json:"is_silenced"`
	BlockReason            *string `json:"block_reason"`
	LastFetchedAt          *Time   `json:"last_fetched_at"`
	LastActivityAt         *Time   `json:"last_activity_at"`
	MetadataFetchedAt      *Time   `json:"metadata_fetched_at"`
	CreatedAt              *Time   `json:"created_at"`
	UpdatedAt              *Time   `json:"updated_at"`
	DeletedAt              *Time   `json:"deleted_at"`
}

// FediverseModerationRule is one federation moderation rule.
type FediverseModerationRule struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Description     *string `json:"description"`
	Type            int     `json:"type"`
	Action          int     `json:"action"`
	Domain          *string `json:"domain"`
	KeywordPattern  *string `json:"keyword_pattern"`
	IsRegex         bool    `json:"is_regex"`
	IsEnabled       bool    `json:"is_enabled"`
	ReportThreshold *int    `json:"report_threshold"`
	ExpiresAt       *Time   `json:"expires_at"`
	Priority        int     `json:"priority"`
	IsSystemRule    bool    `json:"is_system_rule"`
	CreatedAt       *Time   `json:"created_at"`
	UpdatedAt       *Time   `json:"updated_at"`
	DeletedAt       *Time   `json:"deleted_at"`
}

// TimelineEvent is one entry in a timeline page.
type TimelineEvent struct {
	ID                 string         `json:"id"`
	Type               string         `json:"type"`
	ResourceIdentifier string         `json:"resource_identifier"`
	Meta               map[string]any `json:"meta"`
	Data               any            `json:"data"`
	CreatedAt          *Time          `json:"created_at"`
	UpdatedAt          *Time          `json:"updated_at"`
	DeletedAt          *Time          `json:"deleted_at"`
}

// TimelinePage is the timeline response.
type TimelinePage struct {
	Items      []TimelineEvent `json:"items"`
	NextCursor *string         `json:"next_cursor"`
	Mode       string          `json:"mode"`
}

// DiscoveryProfile is the recommendation profile payload.
type DiscoveryProfile struct {
	GeneratedAt         *Time                 `json:"generated_at"`
	Interests           []InterestEntry       `json:"interests"`
	SuggestedPublishers []Publisher           `json:"suggested_publishers"`
	SuggestedAccounts   []any                 `json:"suggested_accounts"`
	SuggestedRealms     []RealmRef            `json:"suggested_realms"`
	Suppressed          []DiscoveryPreference `json:"suppressed"`
}

// InterestEntry is one interest profile entry.
type InterestEntry struct {
	Kind             int     `json:"kind"`
	ReferenceID      string  `json:"reference_id"`
	Score            float64 `json:"score"`
	InteractionCount int     `json:"interaction_count"`
	LastInteractedAt *Time   `json:"last_interacted_at"`
	LastSignalType   *string `json:"last_signal_type"`
}

// DiscoveryPreference is one suppressed recommendation.
type DiscoveryPreference struct {
	ID          string  `json:"id"`
	AccountID   string  `json:"account_id"`
	Kind        int     `json:"kind"`
	ReferenceID string  `json:"reference_id"`
	State       int     `json:"state"`
	Reason      *string `json:"reason"`
	AppliedAt   *Time   `json:"applied_at"`
	CreatedAt   *Time   `json:"created_at"`
	UpdatedAt   *Time   `json:"updated_at"`
	DeletedAt   *Time   `json:"deleted_at"`
}

// RecommendationFeedbackResult is the feedback endpoint payload.
type RecommendationFeedbackResult struct {
	UpdatedProfiles []InterestEntry      `json:"updated_profiles"`
	Preference      *DiscoveryPreference `json:"preference"`
}

// LinkEmbed is the scraped link preview payload.
type LinkEmbed struct {
	URL         string  `json:"url"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	SiteName    *string `json:"site_name"`
	Image       *string `json:"image"`
	Icon        *string `json:"icon"`
	Type        *string `json:"type"`
}

// Autocompletion is one autocomplete suggestion.
type Autocompletion struct {
	Type       string  `json:"type"`
	Value      string  `json:"value"`
	Label      *string `json:"label"`
	Identifier *string `json:"identifier"`
	Icon       *string `json:"icon"`
}
