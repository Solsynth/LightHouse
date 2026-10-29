package model

// Post is the wire shape of a Sphere post. Computed members (thread reply
// counts, reaction maps, bookmark state, chained posts) are filled by the read
// path and serialized exactly where the C# model put them.
type Post struct {
	ID             string          `json:"id"`
	Title          *string         `json:"title"`
	Description    *string         `json:"description"`
	Slug           *string         `json:"slug"`
	EditedAt       *Time           `json:"edited_at"`
	DraftedAt      *Time           `json:"drafted_at"`
	PublishedAt    *Time           `json:"published_at"`
	Visibility     int             `json:"visibility"`
	Content        *string         `json:"content"`
	ContentType    int             `json:"content_type"`
	Type           int             `json:"type"`
	PinMode        *int            `json:"pin_mode"`
	Meta           any             `json:"meta"`
	SensitiveMarks []SensitiveMark `json:"sensitive_marks"`
	EmbedView      any             `json:"embed_view"`

	FediverseURI  *string          `json:"fediverse_uri"`
	FediverseType *int             `json:"fediverse_type"`
	Language      *string          `json:"language"`
	Mentions      []ContentMention `json:"mentions"`

	BoostCount   int     `json:"boost_count"`
	ViewsUnique  int     `json:"views_unique"`
	ViewsTotal   int     `json:"views_total"`
	Upvotes      int     `json:"upvotes"`
	Downvotes    int     `json:"downvotes"`
	AwardedScore float64 `json:"awarded_score"`

	RepliesCount       int `json:"replies_count"`
	ThreadRepliesCount int `json:"thread_replies_count"`

	ChainedPosts []Post  `json:"chained_posts"`
	ChainedCount int     `json:"chained_count"`
	DebugRank    float64 `json:"debug_rank"`
	Sponsored    bool    `json:"sponsored"`

	ReactionsCount map[string]int  `json:"reactions_count"`
	ReactionsMade  map[string]bool `json:"reactions_made"`
	IsBookmarked   bool            `json:"is_bookmarked"`

	RepliedGone   bool `json:"replied_gone"`
	ForwardedGone bool `json:"forwarded_gone"`

	RepliedPostID   *string `json:"replied_post_id"`
	RepliedPost     *Post   `json:"replied_post"`
	ForwardedPostID *string `json:"forwarded_post_id"`
	ForwardedPost   *Post   `json:"forwarded_post"`
	ChainedPostID   *string `json:"chained_post_id"`

	QuoteAuthorizationID *string   `json:"quote_authorization_id"`
	RealmID              *string   `json:"realm_id"`
	Realm                *RealmRef `json:"realm"`

	Attachments []CloudFileReferenceObject `json:"attachments"`

	PublisherID string     `json:"publisher_id"`
	Publisher   *Publisher `json:"publisher"`

	ShadowbanReason *int  `json:"shadowban_reason"`
	ShadowbannedAt  *Time `json:"shadowbanned_at"`
	LockedAt        *Time `json:"locked_at"`
	IsShadowbanned  bool  `json:"is_shadowbanned"`

	Awards               []PostAward          `json:"awards"`
	Tags                 []PostTag            `json:"tags"`
	Categories           []PostCategory       `json:"categories"`
	PublisherCollections []PostCollection     `json:"publisher_collections"`
	FeaturedRecords      []PostFeaturedRecord `json:"featured_records"`

	IsTruncated        bool   `json:"is_truncated"`
	ResourceIdentifier string `json:"resource_identifier"`

	CreatedAt *Time `json:"created_at"`
	UpdatedAt *Time `json:"updated_at"`
	DeletedAt *Time `json:"deleted_at"`
}

// PostReaction is one reaction on a post.
type PostReaction struct {
	ID           string  `json:"id"`
	Symbol       string  `json:"symbol"`
	Attitude     int     `json:"attitude"`
	PostID       string  `json:"post_id"`
	AccountID    *string `json:"account_id"`
	FediverseURI *string `json:"fediverse_uri"`
	PublisherID  *string `json:"publisher_id"`
	IsLocal      bool    `json:"is_local"`
	CreatedAt    *Time   `json:"created_at"`
	UpdatedAt    *Time   `json:"updated_at"`
	DeletedAt    *Time   `json:"deleted_at"`
}

// PostBookmark is one bookmark.
type PostBookmark struct {
	ID        string `json:"id"`
	PostID    string `json:"post_id"`
	AccountID string `json:"account_id"`
	CreatedAt *Time  `json:"created_at"`
	UpdatedAt *Time  `json:"updated_at"`
	DeletedAt *Time  `json:"deleted_at"`
}

// PostAward is one award on a post.
type PostAward struct {
	ID        string  `json:"id"`
	Amount    float64 `json:"amount"`
	Attitude  int     `json:"attitude"`
	Message   *string `json:"message"`
	PostID    string  `json:"post_id"`
	AccountID string  `json:"account_id"`
	SettledAt *Time   `json:"settled_at"`
	CreatedAt *Time   `json:"created_at"`
	UpdatedAt *Time   `json:"updated_at"`
	DeletedAt *Time   `json:"deleted_at"`
}

// PostTag is a post tag.
type PostTag struct {
	ID               string  `json:"id"`
	Slug             string  `json:"slug"`
	Name             *string `json:"name"`
	Description      *string `json:"description"`
	OwnerPublisherID *string `json:"owner_publisher_id"`
	IsProtected      bool    `json:"is_protected"`
	IsEvent          bool    `json:"is_event"`
	EventEndsAt      *Time   `json:"event_ends_at"`
	Usage            *int    `json:"usage"`
	CreatedAt        *Time   `json:"created_at"`
	UpdatedAt        *Time   `json:"updated_at"`
	DeletedAt        *Time   `json:"deleted_at"`
}

// PostCategory is a post category.
type PostCategory struct {
	ID        string  `json:"id"`
	Slug      string  `json:"slug"`
	Name      *string `json:"name"`
	Usage     *int    `json:"usage"`
	CreatedAt *Time   `json:"created_at"`
	UpdatedAt *Time   `json:"updated_at"`
	DeletedAt *Time   `json:"deleted_at"`
}

// PostCollection is a publisher-owned post collection.
type PostCollection struct {
	ID          string  `json:"id"`
	Slug        string  `json:"slug"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
	PublisherID string  `json:"publisher_id"`
	Background  any     `json:"background"`
	Icon        any     `json:"icon"`
	CreatedAt   *Time   `json:"created_at"`
	UpdatedAt   *Time   `json:"updated_at"`
	DeletedAt   *Time   `json:"deleted_at"`
}

// PostCollectionItem is one ordered post in a collection.
type PostCollectionItem struct {
	ID           string `json:"id"`
	CollectionID string `json:"collection_id"`
	PostID       string `json:"post_id"`
	Order        int    `json:"order"`
	CreatedAt    *Time  `json:"created_at"`
	UpdatedAt    *Time  `json:"updated_at"`
	DeletedAt    *Time  `json:"deleted_at"`
}

// PostFeaturedRecord is one featured-post record.
type PostFeaturedRecord struct {
	ID            string `json:"id"`
	PostID        string `json:"post_id"`
	FeaturedAt    *Time  `json:"featured_at"`
	SocialCredits int    `json:"social_credits"`
	CreatedAt     *Time  `json:"created_at"`
	UpdatedAt     *Time  `json:"updated_at"`
	DeletedAt     *Time  `json:"deleted_at"`
}

// PostSponsorBid is one sponsor bid.
type PostSponsorBid struct {
	ID        string  `json:"id"`
	PostID    string  `json:"post_id"`
	AccountID string  `json:"account_id"`
	Amount    float64 `json:"amount"`
	ExpiresAt *Time   `json:"expires_at"`
	CreatedAt *Time   `json:"created_at"`
	UpdatedAt *Time   `json:"updated_at"`
	DeletedAt *Time   `json:"deleted_at"`
}

// PostSponsorPlacement is one active sponsor placement.
type PostSponsorPlacement struct {
	ID          string  `json:"id"`
	PostID      string  `json:"post_id"`
	TotalAmount float64 `json:"total_amount"`
	ValidFrom   *Time   `json:"valid_from"`
	ValidUntil  *Time   `json:"valid_until"`
	CreatedAt   *Time   `json:"created_at"`
	UpdatedAt   *Time   `json:"updated_at"`
	DeletedAt   *Time   `json:"deleted_at"`
}

// PostAggregatedStats is the per-post sponsor read model.
type PostAggregatedStats struct {
	ID                string  `json:"id"`
	PostID            string  `json:"post_id"`
	ActiveBidTotal    float64 `json:"active_bid_total"`
	ActiveBidCount    int     `json:"active_bid_count"`
	IsCurrentlyPlaced bool    `json:"is_currently_placed"`
	ShownCount        int64   `json:"shown_count"`
	LastShownAt       *Time   `json:"last_shown_at"`
}

// PostCategorySubscription is a category/tag/collection subscription.
type PostCategorySubscription struct {
	ID           string  `json:"id"`
	AccountID    string  `json:"account_id"`
	CategoryID   *string `json:"category_id"`
	TagID        *string `json:"tag_id"`
	CollectionID *string `json:"collection_id"`
	CreatedAt    *Time   `json:"created_at"`
	UpdatedAt    *Time   `json:"updated_at"`
	DeletedAt    *Time   `json:"deleted_at"`
}

// PostWatchPreference is one watch-preference row.
type PostWatchPreference struct {
	ID              string `json:"id"`
	AccountID       string `json:"account_id"`
	Source          int    `json:"source"`
	NotifyReactions bool   `json:"notify_reactions"`
	NotifyReplies   bool   `json:"notify_replies"`
	NotifyChains    bool   `json:"notify_chains"`
	NotifyForwards  bool   `json:"notify_forwards"`
	NotifyEdits     bool   `json:"notify_edits"`
	CreatedAt       *Time  `json:"created_at"`
	UpdatedAt       *Time  `json:"updated_at"`
	DeletedAt       *Time  `json:"deleted_at"`
}

// PostWatchPreferenceResponse is the normalized watch listing entry.
type PostWatchPreferenceResponse struct {
	Source          string `json:"source"`
	NotifyReactions bool   `json:"notify_reactions"`
	NotifyReplies   bool   `json:"notify_replies"`
	NotifyChains    bool   `json:"notify_chains"`
	NotifyForwards  bool   `json:"notify_forwards"`
	NotifyEdits     bool   `json:"notify_edits"`
}

// Boost is one boost of a post.
type Boost struct {
	ID             string  `json:"id"`
	PostID         string  `json:"post_id"`
	PublisherID    string  `json:"publisher_id"`
	ActivityPubURI *string `json:"activity_pub_uri"`
	WebURL         *string `json:"web_url"`
	Content        *string `json:"content"`
	BoostedAt      *Time   `json:"boosted_at"`
	CreatedAt      *Time   `json:"created_at"`
	UpdatedAt      *Time   `json:"updated_at"`
	DeletedAt      *Time   `json:"deleted_at"`
}

// QuoteAuthorization is one quote authorization.
type QuoteAuthorization struct {
	ID                   string  `json:"id"`
	FediverseURI         *string `json:"fediverse_uri"`
	PublisherID          string  `json:"publisher_id"`
	InteractingObjectURI string  `json:"interacting_object_uri"`
	InteractionTargetURI string  `json:"interaction_target_uri"`
	TargetPostID         *string `json:"target_post_id"`
	QuotePostID          *string `json:"quote_post_id"`
	IsValid              bool    `json:"is_valid"`
	RevokedAt            *Time   `json:"revoked_at"`
	CreatedAt            *Time   `json:"created_at"`
	UpdatedAt            *Time   `json:"updated_at"`
	DeletedAt            *Time   `json:"deleted_at"`
}

// ThreadedReplyNode is one entry of a threaded reply listing.
type ThreadedReplyNode struct {
	Post     Post    `json:"post"`
	Depth    int     `json:"depth"`
	ParentID *string `json:"parentId"`
}

// PostThreadResponse is the thread endpoint's payload.
type PostThreadResponse struct {
	Ancestors []ThreadedReplyNode `json:"ancestors"`
	Current   ThreadedReplyNode   `json:"current"`
	Replies   []ThreadedReplyNode `json:"replies"`
}

// UserReactionListingItem pairs a reaction with its post.
type UserReactionListingItem struct {
	Reaction PostReaction `json:"reaction"`
	Post     Post         `json:"post"`
}

// PostStatsResponse is the admin stats payload.
type PostStatsResponse struct {
	CalculatedAt    *Time `json:"calculated_at"`
	TotalPosts      int64 `json:"total_posts"`
	PublishedPosts  int64 `json:"published_posts"`
	DraftPosts      int64 `json:"draft_posts"`
	PostsLastDay    int64 `json:"posts_last_day"`
	PostsLastWeek   int64 `json:"posts_last_week"`
	PostsLastMonth  int64 `json:"posts_last_month"`
	TotalPublishers int64 `json:"total_publishers"`
	TotalReactions  int64 `json:"total_reactions"`
	TotalBookmarks  int64 `json:"total_bookmarks"`
}

// SponsorLeaderboardEntry is one sponsor leaderboard row.
type SponsorLeaderboardEntry struct {
	PostID      string  `json:"post_id"`
	TotalAmount float64 `json:"total_amount"`
	BidCount    int     `json:"bid_count"`
}

// PublicAdvertisingPostStats is one ads listing row.
type PublicAdvertisingPostStats struct {
	PostID            string  `json:"post_id"`
	Title             *string `json:"title"`
	Slug              *string `json:"slug"`
	ActiveBidTotal    float64 `json:"active_bid_total"`
	BidCount          int     `json:"bid_count"`
	IsCurrentlyPlaced bool    `json:"is_currently_placed"`
	ShownCount        int64   `json:"shown_count"`
	LastShownAt       *Time   `json:"last_shown_at"`
	DisplayChance     float64 `json:"display_chance"`
}
