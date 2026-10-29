package publishers

import (
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
)

// subscriptionDTO is the wire shape of SnPublisherSubscription. The model
// type is embedded so every persisted field keeps its name, while the two
// computed properties the C# model exposes are added back.
type subscriptionDTO struct {
	model.PublisherSubscription
	IsActive  bool              `json:"is_active"`
	IsPending bool              `json:"is_pending"`
	Account   *model.AccountRef `json:"account"`
}

// subscriptionStatusResponse mirrors
// PublisherSubscriptionController.SubscriptionStatusResponse.
type subscriptionStatusResponse struct {
	Subscription     *subscriptionDTO `json:"subscription"`
	FollowRequest    *subscriptionDTO `json:"follow_request"`
	RequiresApproval bool             `json:"requires_approval"`
	Status           string           `json:"status"`
	Message          string           `json:"message"`
	IsPending        bool             `json:"is_pending"`
	IsActive         bool             `json:"is_active"`
	Notify           bool             `json:"notify"`
}

// subscriptionReadStatus mirrors SubscriptionReadStatus.
type subscriptionReadStatus struct {
	Subscription    *subscriptionDTO `json:"subscription"`
	LatestContentAt *model.Time      `json:"latest_content_at"`
	HasNewContent   bool             `json:"has_new_content"`
}

// subscriptionWithLiveStatus mirrors SubscriptionWithLiveStatus.
type subscriptionWithLiveStatus struct {
	Subscription    *subscriptionDTO `json:"subscription"`
	IsLive          bool             `json:"is_live"`
	LatestContentAt *model.Time      `json:"latest_content_at"`
	HasNewContent   bool             `json:"has_new_content"`
}

// subscriberResponse mirrors the controller's SubscriberResponse.
type subscriberResponse struct {
	Subscription *subscriptionDTO  `json:"subscription"`
	Account      *model.AccountRef `json:"account"`
}

// relationshipResponse mirrors PublisherRelationshipResponse.
type relationshipResponse struct {
	Subscription *subscriptionDTO `json:"subscription"`
	IsBlocking   bool             `json:"is_blocking"`
	IsMuting     bool             `json:"is_muting"`
	IsSubscribed bool             `json:"is_subscribed"`
}

// publisherQuotaRecord mirrors PublisherQuotaRecord.
type publisherQuotaRecord struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Nick    string  `json:"nick"`
	Type    int     `json:"type"`
	RealmID *string `json:"realm_id"`
}

// publisherQuotaResponse mirrors ResourceQuotaResponse<PublisherQuotaRecord>.
type publisherQuotaResponse struct {
	Total     int                    `json:"total"`
	Used      int                    `json:"used"`
	Remaining int                    `json:"remaining"`
	Level     int                    `json:"level"`
	PerkLevel int                    `json:"perk_level"`
	Records   []publisherQuotaRecord `json:"records"`
}

// rewardPreview mirrors PublisherService.PublisherRewardPreview.
type rewardPreview struct {
	Experience    int `json:"experience"`
	SocialCredits int `json:"social_credits"`
}

// fediverseStatus mirrors PublisherService.FediverseStatus.
type fediverseStatus struct {
	Enabled       bool             `json:"enabled"`
	Actor         *model.Publisher `json:"actor"`
	FollowerCount int              `json:"follower_count"`
	ActorURI      *string          `json:"actor_uri"`
}

// activityHeatmap mirrors the C# ActivityHeatmap DTO.
type activityHeatmap struct {
	Unit        string                `json:"unit"`
	PeriodStart *model.Time           `json:"period_start"`
	PeriodEnd   *model.Time           `json:"period_end"`
	Items       []activityHeatmapItem `json:"items"`
}

// activityHeatmapItem mirrors ActivityHeatmapItem.
type activityHeatmapItem struct {
	Date  *model.Time `json:"date"`
	Count int         `json:"count"`
}

// leaderboardEntry mirrors PublisherLeaderboardService.LeaderboardEntry.
type leaderboardEntry struct {
	PublisherID string  `json:"publisher_id"`
	Name        string  `json:"name"`
	Nick        string  `json:"nick"`
	Picture     any     `json:"picture"`
	Rating      float64 `json:"rating"`
	Rank        int     `json:"rank"`
	Percentile  float64 `json:"percentile"`
	Grade       string  `json:"grade"`
}

// ratingOverview mirrors PublisherLeaderboardService.RatingOverview.
type ratingOverview struct {
	Rating          float64 `json:"rating"`
	Rank            int     `json:"rank"`
	TotalPublishers int     `json:"total_publishers"`
	Percentile      float64 `json:"percentile"`
	Grade           string  `json:"grade"`
}

// advertisingPostStats mirrors Post.SponsorService.AdvertisingPostStats.
type advertisingPostStats struct {
	PostID            string      `json:"post_id"`
	Title             *string     `json:"title"`
	Slug              *string     `json:"slug"`
	ActiveBidTotal    float64     `json:"active_bid_total"`
	BidCount          int         `json:"bid_count"`
	IsCurrentlyPlaced bool        `json:"is_currently_placed"`
	ShownCount        int64       `json:"shown_count"`
	LastShownAt       *model.Time `json:"last_shown_at"`
}

// adminPublisherDetail mirrors PublisherAdminController.AdminPublisherDetail.
type adminPublisherDetail struct {
	Publisher       *model.Publisher `json:"publisher"`
	MemberCount     int              `json:"member_count"`
	PostCount       int              `json:"post_count"`
	CollectionCount int              `json:"collection_count"`
	SubscriberCount int              `json:"subscriber_count"`
}

// publishingSettingsDTO mirrors SnPublishingSettings. The navigation
// properties are always serialized as null because the C# query never loads
// them.
type publishingSettingsDTO struct {
	model.PublishingSettings
	DefaultPostingPublisher   *model.Publisher `json:"default_posting_publisher"`
	DefaultReplyPublisher     *model.Publisher `json:"default_reply_publisher"`
	DefaultFediversePublisher *model.Publisher `json:"default_fediverse_publisher"`
}

// liveStreamDTO mirrors SnLiveStream for the subscription live listing.
type liveStreamDTO struct {
	ID                   string           `json:"id"`
	Title                *string          `json:"title"`
	Description          *string          `json:"description"`
	Slug                 *string          `json:"slug"`
	Type                 int              `json:"type"`
	Visibility           int              `json:"visibility"`
	Status               int              `json:"status"`
	RoomName             string           `json:"room_name"`
	IngressID            *string          `json:"ingress_id"`
	IngressStreamKey     *string          `json:"ingress_stream_key"`
	EgressID             *string          `json:"egress_id"`
	HLSegressID          *string          `json:"hls_egress_id"`
	HLSPlaylistPath      *string          `json:"hls_playlist_path"`
	HLSStartedAt         *model.Time      `json:"hls_started_at"`
	StartedAt            *model.Time      `json:"started_at"`
	EndedAt              *model.Time      `json:"ended_at"`
	TotalDurationSeconds int64            `json:"total_duration_seconds"`
	ViewerCount          int              `json:"viewer_count"`
	PeakViewerCount      int              `json:"peak_viewer_count"`
	TotalAwardScore      float64          `json:"total_award_score"`
	DistributedAward     float64          `json:"distributed_award_amount"`
	Thumbnail            any              `json:"thumbnail"`
	Metadata             any              `json:"metadata"`
	PublisherID          *string          `json:"publisher_id"`
	Publisher            *model.Publisher `json:"publisher"`
	CreatedAt            *model.Time      `json:"created_at"`
	UpdatedAt            *model.Time      `json:"updated_at"`
	DeletedAt            *model.Time      `json:"deleted_at"`
}

// Request payloads.

type publisherRequest struct {
	Name                  *string  `json:"name"`
	Nick                  *string  `json:"nick"`
	Bio                   *string  `json:"bio"`
	PictureID             *string  `json:"picture_id"`
	BackgroundID          *string  `json:"background_id"`
	DefaultPostTags       []string `json:"default_post_tags"`
	DefaultPostCategories []string `json:"default_post_categories"`
	PayoutWalletID        *string  `json:"payout_wallet_id"`
}

type memberInviteRequest struct {
	RelatedUserID string `json:"related_user_id"`
	Role          int    `json:"role"`
}

type publisherFeatureRequest struct {
	Flag      string      `json:"flag"`
	ExpiredAt *model.Time `json:"expired_at"`
}

type updateFediverseActorRequest struct {
	ActorType      *string `json:"actor_type"`
	IsLocked       *bool   `json:"is_locked"`
	IsDiscoverable *bool   `json:"is_discoverable"`
}

type addDomainRequest struct {
	Domain string `json:"domain"`
}

type aggressiveResettleRequest struct {
	DateFrom    *model.Time `json:"date_from"`
	DateTo      *model.Time `json:"date_to"`
	PublisherID *string     `json:"publisher_id"`
}

type updateNotifyRequest struct {
	Notify bool `json:"notify"`
}

type updateSubscriptionReadStateRequest struct {
	LastReadAt *model.Time `json:"last_read_at"`
}

type rejectFollowRequestBody struct {
	Reason *string `json:"reason"`
}

type adminUpdatePublisherRequest struct {
	Name                 *string `json:"name"`
	Nick                 *string `json:"nick"`
	Bio                  *string `json:"bio"`
	GatekeptFollows      *bool   `json:"gatekept_follows"`
	ModerateSubscription *bool   `json:"moderate_subscription"`
}

type setPublisherShadowbanRequest struct {
	Reason int `json:"reason"`
}

type setPublisherVerificationRequest struct {
	Type        int     `json:"type"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	VerifiedBy  *string `json:"verified_by"`
}

type publishingSettingsRequest struct {
	DefaultPostingPublisherID   *string `json:"default_posting_publisher_id"`
	DefaultReplyPublisherID     *string `json:"default_reply_publisher_id"`
	DefaultFediversePublisherID *string `json:"default_fediverse_publisher_id"`
}
