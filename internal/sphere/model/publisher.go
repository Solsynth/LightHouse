package model

// Publisher is the wire shape of a Sphere publisher.
type Publisher struct {
	ID           string            `json:"id"`
	Type         int               `json:"type"`
	Name         string            `json:"name"`
	Nick         string            `json:"nick"`
	Bio          *string           `json:"bio"`
	Picture      any               `json:"picture"`
	Background   any               `json:"background"`
	Verification *VerificationMark `json:"verification"`
	Meta         any               `json:"meta"`
	Metadata     any               `json:"metadata"`

	ActivityPub *PublisherActivityPub `json:"activitypub"`

	AccountID      *string `json:"account_id"`
	RealmID        *string `json:"realm_id"`
	PayoutWalletID *string `json:"payout_wallet_id"`

	Realm                 *RealmRef `json:"realm"`
	Account               any       `json:"account"`
	RealmNick             *string   `json:"realm_nick"`
	RealmBio              *string   `json:"realm_bio"`
	RealmExperience       *int      `json:"realm_experience"`
	RealmLevel            *int      `json:"realm_level"`
	RealmLevelingProgress *float64  `json:"realm_leveling_progress"`
	RealmLabel            *string   `json:"realm_label"`

	DisplayName *string `json:"display_name"`

	ShadowbanReason      *int    `json:"shadowban_reason"`
	ShadowbannedAt       *Time   `json:"shadowbanned_at"`
	GatekeptFollows      *bool   `json:"gatekept_follows"`
	ModerateSubscription *bool   `json:"moderate_subscription"`
	Rating               float64 `json:"rating"`
	RatingLevel          int     `json:"rating_level"`

	IsShadowbanned         bool `json:"is_shadowbanned"`
	IsGatekept             bool `json:"is_gatekept"`
	IsModerateSubscription bool `json:"is_moderate_subscription"`

	CreatedAt *Time `json:"created_at"`
	UpdatedAt *Time `json:"updated_at"`
	DeletedAt *Time `json:"deleted_at"`
}

// PublisherMember is one membership row.
type PublisherMember struct {
	PublisherID string     `json:"publisher_id"`
	AccountID   string     `json:"account_id"`
	JoinedAt    *Time      `json:"joined_at"`
	Role        int        `json:"role"`
	Account     any        `json:"account"`
	Publisher   *Publisher `json:"publisher"`
}

// PublisherSubscription is one follow subscription.
type PublisherSubscription struct {
	ID                  string     `json:"id"`
	AccountID           *string    `json:"account_id"`
	FollowerPublisherID *string    `json:"follower_publisher_id"`
	PublisherID         string     `json:"publisher_id"`
	State               int        `json:"state"`
	FollowedAt          *Time      `json:"followed_at"`
	ReviewedAt          *Time      `json:"reviewed_at"`
	ReviewedByAccountID *string    `json:"reviewed_by_account_id"`
	RejectReason        *string    `json:"reject_reason"`
	IsMuting            bool       `json:"is_muting"`
	IsBlocking          bool       `json:"is_blocking"`
	Notify              bool       `json:"notify"`
	LastReadAt          *Time      `json:"last_read_at"`
	RealmID             *string    `json:"realm_id"`
	EndedAt             *Time      `json:"ended_at"`
	EndReason           *int       `json:"end_reason"`
	EndedByAccountID    *string    `json:"ended_by_account_id"`
	Publisher           *Publisher `json:"publisher"`
	CreatedAt           *Time      `json:"created_at"`
	UpdatedAt           *Time      `json:"updated_at"`
	DeletedAt           *Time      `json:"deleted_at"`
}

// PublisherFeature is one feature flag grant.
type PublisherFeature struct {
	ID          string `json:"id"`
	Flag        string `json:"flag"`
	ExpiredAt   *Time  `json:"expired_at"`
	PublisherID string `json:"publisher_id"`
	CreatedAt   *Time  `json:"created_at"`
	UpdatedAt   *Time  `json:"updated_at"`
	DeletedAt   *Time  `json:"deleted_at"`
}

// PublisherRatingRecord is one rating change.
type PublisherRatingRecord struct {
	ID          string  `json:"id"`
	ReasonType  string  `json:"reason_type"`
	Reason      string  `json:"reason"`
	Delta       float64 `json:"delta"`
	PublisherID string  `json:"publisher_id"`
	CreatedAt   *Time   `json:"created_at"`
	UpdatedAt   *Time   `json:"updated_at"`
	DeletedAt   *Time   `json:"deleted_at"`
}

// PublisherVerifiedDomain is one verified-domain record.
type PublisherVerifiedDomain struct {
	ID             string  `json:"id"`
	PublisherID    string  `json:"publisher_id"`
	Domain         string  `json:"domain"`
	Status         int     `json:"status"`
	VerifiedAt     *Time   `json:"verified_at"`
	LastCheckedAt  *Time   `json:"last_checked_at"`
	FailedAttempts int     `json:"failed_attempts"`
	LastError      *string `json:"last_error"`
	CreatedAt      *Time   `json:"created_at"`
	UpdatedAt      *Time   `json:"updated_at"`
	DeletedAt      *Time   `json:"deleted_at"`
}

// PublishingSettings is the per-account default publisher configuration.
type PublishingSettings struct {
	ID                          string  `json:"id"`
	AccountID                   string  `json:"account_id"`
	DefaultPostingPublisherID   *string `json:"default_posting_publisher_id"`
	DefaultReplyPublisherID     *string `json:"default_reply_publisher_id"`
	DefaultFediversePublisherID *string `json:"default_fediverse_publisher_id"`
	CreatedAt                   *Time   `json:"created_at"`
	UpdatedAt                   *Time   `json:"updated_at"`
	DeletedAt                   *Time   `json:"deleted_at"`
}

// PublisherStats is the public publisher statistics payload.
type PublisherStats struct {
	PostsCreated        int `json:"posts_created"`
	StickerPacksCreated int `json:"sticker_packs_created"`
	StickersCreated     int `json:"stickers_created"`
	UpvoteReceived      int `json:"upvote_received"`
	DownvoteReceived    int `json:"downvote_received"`
	SubscribersCount    int `json:"subscribers_count"`
}

// PublisherContentStats is the publisher writing statistics payload.
type PublisherContentStats struct {
	PostsCount        int   `json:"posts_count"`
	WordsCount        int64 `json:"words_count"`
	AttachmentsCount  int   `json:"attachments_count"`
	DaysPostedCount   int   `json:"days_posted_count"`
	LongestStreakDays int   `json:"longest_streak_days"`
	FirstPostedAt     *Time `json:"first_posted_at"`
	LastPostedAt      *Time `json:"last_posted_at"`
}

// LeaderboardEntry is one publisher leaderboard row.
type LeaderboardEntry struct {
	PublisherID string  `json:"publisher_id"`
	Name        string  `json:"name"`
	Nick        string  `json:"nick"`
	Picture     any     `json:"picture"`
	Rating      float64 `json:"rating"`
	Rank        int     `json:"rank"`
}

// RatingOverview is one publisher's rating rank.
type RatingOverview struct {
	Rating float64 `json:"rating"`
	Rank   int     `json:"rank"`
	Total  int     `json:"total"`
}

// ActivityHeatmap is the post-activity heatmap payload.
type ActivityHeatmap struct {
	Unit        string         `json:"unit"`
	PeriodStart *Time          `json:"period_start"`
	PeriodEnd   *Time          `json:"period_end"`
	Items       []HeatmapEntry `json:"items"`
}

// HeatmapEntry is one heatmap day.
type HeatmapEntry struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

// SubscriptionStatusResponse is the follow status payload.
type SubscriptionStatusResponse struct {
	Subscription  *PublisherSubscription `json:"subscription"`
	FollowRequest *PublisherSubscription `json:"follow_request"`
	IsSubscribed  bool                   `json:"is_subscribed"`
	IsPending     bool                   `json:"is_pending"`
	IsBlocked     bool                   `json:"is_blocked"`
	IsMuted       bool                   `json:"is_muted"`
}

// PublisherRelationshipResponse is the relationship summary payload.
type PublisherRelationshipResponse struct {
	IsSubscribed bool  `json:"is_subscribed"`
	IsBlocked    bool  `json:"is_blocked"`
	IsMuted      bool  `json:"is_muted"`
	Notify       bool  `json:"notify"`
	LastReadAt   *Time `json:"last_read_at"`
}

// PublisherRewardPreview is the reward preview payload.
type PublisherRewardPreview struct {
	PublisherID string  `json:"publisher_id"`
	Amount      float64 `json:"amount"`
	Currency    string  `json:"currency"`
	PeriodStart *Time   `json:"period_start"`
	PeriodEnd   *Time   `json:"period_end"`
}

// SubscriberResponse is one subscriber listing row.
type SubscriberResponse struct {
	Subscription *PublisherSubscription `json:"subscription"`
	Account      any                    `json:"account"`
}
