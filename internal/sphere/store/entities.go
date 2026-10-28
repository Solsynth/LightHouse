package store

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// EntityBase is embedded by every soft-deletable persisted entity. The column
// layout matches the C# ModelBase (created_at/updated_at/deleted_at), so the
// legacy tables import without a rewrite.
type EntityBase struct {
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

// TimestampBase is embedded by entities that have timestamps but no soft
// delete.
type TimestampBase struct {
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

// PostEntity is the `posts` table.
type PostEntity struct {
	ID                uuid.UUID      `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Title             *string        `gorm:"column:title"`
	Description       *string        `gorm:"column:description"`
	Slug              *string        `gorm:"column:slug"`
	EditedAt          *time.Time     `gorm:"column:edited_at"`
	DraftedAt         *time.Time     `gorm:"column:drafted_at"`
	PublishedAt       *time.Time     `gorm:"column:published_at"`
	PublishNotifiedAt *time.Time     `gorm:"column:publish_notified_at"`
	Visibility        int            `gorm:"column:visibility"`
	Content           *string        `gorm:"column:content"`
	ContentType       int            `gorm:"column:content_type"`
	Type              int            `gorm:"column:type"`
	PinMode           *int           `gorm:"column:pin_mode"`
	Metadata          datatypes.JSON `gorm:"column:metadata;type:jsonb"`
	SensitiveMarks    datatypes.JSON `gorm:"column:sensitive_marks;type:jsonb"`
	EmbedView         datatypes.JSON `gorm:"column:embed_view;type:jsonb"`
	FediverseURI      *string        `gorm:"column:fediverse_uri"`
	FediverseType     *int           `gorm:"column:fediverse_type"`
	Language          *string        `gorm:"column:language"`
	Mentions          datatypes.JSON `gorm:"column:mentions;type:jsonb"`
	BoostCount        int            `gorm:"column:boost_count"`
	ViewsUnique       int            `gorm:"column:views_unique"`
	ViewsTotal        int            `gorm:"column:views_total"`
	Upvotes           int            `gorm:"column:upvotes"`
	Downvotes         int            `gorm:"column:downvotes"`
	AwardedScore      float64        `gorm:"column:awarded_score"`
	RepliesCount      int            `gorm:"column:replies_count"`
	ReactionScore     int            `gorm:"column:reaction_score"`
	RepliedGone       bool           `gorm:"column:replied_gone"`
	ForwardedGone     bool           `gorm:"column:forwarded_gone"`
	RepliedPostID     *uuid.UUID     `gorm:"column:replied_post_id;type:uuid"`
	ForwardedPostID   *uuid.UUID     `gorm:"column:forwarded_post_id;type:uuid"`
	ChainedPostID     *uuid.UUID     `gorm:"column:chained_post_id;type:uuid"`
	QuoteAuthorizationID *uuid.UUID  `gorm:"column:quote_authorization_id;type:uuid"`
	RealmID           *uuid.UUID     `gorm:"column:realm_id;type:uuid"`
	Attachments       datatypes.JSON `gorm:"column:attachments;type:jsonb"`
	PublisherID       uuid.UUID      `gorm:"column:publisher_id;type:uuid"`
	ShadowbanReason   *int           `gorm:"column:shadowban_reason"`
	ShadowbannedAt    *time.Time     `gorm:"column:shadowbanned_at"`
	LockedAt          *time.Time     `gorm:"column:locked_at"`
}

func (PostEntity) TableName() string { return "posts" }

// PostIndexEntity is the `post_indices` table.
type PostIndexEntity struct {
	ID         uuid.UUID  `gorm:"column:id;primaryKey;type:uuid"`
	PostID     uuid.UUID  `gorm:"column:post_id;type:uuid"`
	SourceHash string     `gorm:"column:source_hash"`
	SourceText string     `gorm:"column:source_text"`
	Embedding  *string    `gorm:"column:embedding"`
	IndexedAt  time.Time  `gorm:"column:indexed_at"`
}

func (PostIndexEntity) TableName() string { return "post_indices" }

// PostReactionEntity is the `post_reactions` table.
type PostReactionEntity struct {
	ID           uuid.UUID  `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Symbol       string     `gorm:"column:symbol"`
	Attitude     int        `gorm:"column:attitude"`
	PostID       uuid.UUID  `gorm:"column:post_id;type:uuid"`
	AccountID    *uuid.UUID `gorm:"column:account_id;type:uuid"`
	FediverseURI *string    `gorm:"column:fediverse_uri"`
	PublisherID  *uuid.UUID `gorm:"column:publisher_id;type:uuid"`
	IsLocal      bool       `gorm:"column:is_local"`
}

func (PostReactionEntity) TableName() string { return "post_reactions" }

// PostBookmarkEntity is the `post_bookmarks` table.
type PostBookmarkEntity struct {
	ID        uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	PostID    uuid.UUID `gorm:"column:post_id;type:uuid"`
	AccountID uuid.UUID `gorm:"column:account_id;type:uuid"`
}

func (PostBookmarkEntity) TableName() string { return "post_bookmarks" }

// PostWatchPreferenceEntity is the `post_watch_preferences` table.
type PostWatchPreferenceEntity struct {
	ID              uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	AccountID       uuid.UUID `gorm:"column:account_id;type:uuid"`
	Source          int       `gorm:"column:source"`
	NotifyReactions bool      `gorm:"column:notify_reactions"`
	NotifyReplies   bool      `gorm:"column:notify_replies"`
	NotifyChains    bool      `gorm:"column:notify_chains"`
	NotifyForwards  bool      `gorm:"column:notify_forwards"`
	NotifyEdits     bool      `gorm:"column:notify_edits"`
}

func (PostWatchPreferenceEntity) TableName() string { return "post_watch_preferences" }

// PostAwardEntity is the `post_awards` table.
type PostAwardEntity struct {
	ID        uuid.UUID  `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Amount    float64    `gorm:"column:amount"`
	Attitude  int        `gorm:"column:attitude"`
	Message   *string    `gorm:"column:message"`
	PostID    uuid.UUID  `gorm:"column:post_id;type:uuid"`
	AccountID uuid.UUID  `gorm:"column:account_id;type:uuid"`
	SettledAt *time.Time `gorm:"column:settled_at"`
}

func (PostAwardEntity) TableName() string { return "post_awards" }

// PostTagEntity is the `post_tags` table.
type PostTagEntity struct {
	ID               uuid.UUID  `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Slug             string     `gorm:"column:slug"`
	Name             *string    `gorm:"column:name"`
	Description      *string    `gorm:"column:description"`
	OwnerPublisherID *uuid.UUID `gorm:"column:owner_publisher_id;type:uuid"`
	IsProtected      bool       `gorm:"column:is_protected"`
	IsEvent          bool       `gorm:"column:is_event"`
	EventEndsAt      *time.Time `gorm:"column:event_ends_at"`
}

func (PostTagEntity) TableName() string { return "post_tags" }

// PostCategoryEntity is the `post_categories` table.
type PostCategoryEntity struct {
	ID        uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Slug      string  `gorm:"column:slug"`
	Name      *string `gorm:"column:name"`
}

func (PostCategoryEntity) TableName() string { return "post_categories" }

// PostTagLink is the `post_tag_links` join table.
type PostTagLink struct {
	PostID uuid.UUID `gorm:"column:posts_id;primaryKey;type:uuid"`
	TagID  uuid.UUID `gorm:"column:tags_id;primaryKey;type:uuid"`
}

func (PostTagLink) TableName() string { return "post_tag_links" }

// PostCategoryLink is the `post_category_links` join table.
type PostCategoryLink struct {
	CategoryID uuid.UUID `gorm:"column:categories_id;primaryKey;type:uuid"`
	PostID     uuid.UUID `gorm:"column:posts_id;primaryKey;type:uuid"`
}

func (PostCategoryLink) TableName() string { return "post_category_links" }

// PostCollectionEntity is the `post_collections` table.
type PostCollectionEntity struct {
	ID          uuid.UUID      `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Slug        string         `gorm:"column:slug"`
	Name        *string        `gorm:"column:name"`
	Description *string        `gorm:"column:description"`
	PublisherID uuid.UUID      `gorm:"column:publisher_id;type:uuid"`
	Background  datatypes.JSON `gorm:"column:background;type:jsonb"`
	Icon        datatypes.JSON `gorm:"column:icon;type:jsonb"`
}

func (PostCollectionEntity) TableName() string { return "post_collections" }

// PostCollectionItemEntity is the `post_collection_items` table.
type PostCollectionItemEntity struct {
	ID           uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	CollectionID uuid.UUID `gorm:"column:collection_id;type:uuid"`
	PostID       uuid.UUID `gorm:"column:post_id;type:uuid"`
	Order        int       `gorm:"column:order"`
}

func (PostCollectionItemEntity) TableName() string { return "post_collection_items" }

// PostFeaturedRecordEntity is the `post_featured_records` table.
type PostFeaturedRecordEntity struct {
	ID            uuid.UUID  `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	PostID        uuid.UUID  `gorm:"column:post_id;type:uuid"`
	FeaturedAt    *time.Time `gorm:"column:featured_at"`
	SocialCredits int        `gorm:"column:social_credits"`
}

func (PostFeaturedRecordEntity) TableName() string { return "post_featured_records" }

// PostSponsorBidEntity is the `post_sponsor_bids` table.
type PostSponsorBidEntity struct {
	ID        uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	PostID    uuid.UUID `gorm:"column:post_id;type:uuid"`
	AccountID uuid.UUID `gorm:"column:account_id;type:uuid"`
	Amount    float64   `gorm:"column:amount"`
	ExpiresAt time.Time `gorm:"column:expires_at"`
}

func (PostSponsorBidEntity) TableName() string { return "post_sponsor_bids" }

// PostSponsorPlacementEntity is the `post_sponsor_placements` table.
type PostSponsorPlacementEntity struct {
	ID          uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	PostID      uuid.UUID `gorm:"column:post_id;type:uuid"`
	TotalAmount float64   `gorm:"column:total_amount"`
	ValidFrom   time.Time `gorm:"column:valid_from"`
	ValidUntil  time.Time `gorm:"column:valid_until"`
}

func (PostSponsorPlacementEntity) TableName() string { return "post_sponsor_placements" }

// PostAggregatedStatsEntity is the `post_aggregated_stats` table.
type PostAggregatedStatsEntity struct {
	ID                uuid.UUID  `gorm:"column:id;primaryKey;type:uuid"`
	PostID            uuid.UUID  `gorm:"column:post_id;type:uuid"`
	ActiveBidTotal    float64    `gorm:"column:active_bid_total"`
	ActiveBidCount    int        `gorm:"column:active_bid_count"`
	IsCurrentlyPlaced bool       `gorm:"column:is_currently_placed"`
	ShownCount        int64      `gorm:"column:shown_count"`
	LastShownAt       *time.Time `gorm:"column:last_shown_at"`
}

func (PostAggregatedStatsEntity) TableName() string { return "post_aggregated_stats" }

// PostCategorySubscriptionEntity is the `post_category_subscriptions` table.
type PostCategorySubscriptionEntity struct {
	ID           uuid.UUID  `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	AccountID    uuid.UUID  `gorm:"column:account_id;type:uuid"`
	CategoryID   *uuid.UUID `gorm:"column:category_id;type:uuid"`
	TagID        *uuid.UUID `gorm:"column:tag_id;type:uuid"`
	CollectionID *uuid.UUID `gorm:"column:collection_id;type:uuid"`
}

func (PostCategorySubscriptionEntity) TableName() string { return "post_category_subscriptions" }

// PostInterestProfileEntity is the `post_interest_profiles` table.
type PostInterestProfileEntity struct {
	ID               uuid.UUID  `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	AccountID        uuid.UUID  `gorm:"column:account_id;type:uuid"`
	Kind             int        `gorm:"column:kind"`
	ReferenceID      uuid.UUID  `gorm:"column:reference_id;type:uuid"`
	Score            float64    `gorm:"column:score"`
	InteractionCount int        `gorm:"column:interaction_count"`
	LastInteractedAt *time.Time `gorm:"column:last_interacted_at"`
	LastSignalType   *string    `gorm:"column:last_signal_type"`
}

func (PostInterestProfileEntity) TableName() string { return "post_interest_profiles" }

// DiscoveryPreferenceEntity is the `discovery_preferences` table.
type DiscoveryPreferenceEntity struct {
	ID          uuid.UUID  `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	AccountID   uuid.UUID  `gorm:"column:account_id;type:uuid"`
	Kind        int        `gorm:"column:kind"`
	ReferenceID uuid.UUID  `gorm:"column:reference_id;type:uuid"`
	State       int        `gorm:"column:state"`
	Reason      *string    `gorm:"column:reason"`
	AppliedAt   *time.Time `gorm:"column:applied_at"`
}

func (DiscoveryPreferenceEntity) TableName() string { return "discovery_preferences" }

// PublishingSettingsEntity is the `publishing_settings` table. Its updated_at
// is nullable, unlike ModelBase.
type PublishingSettingsEntity struct {
	ID                          uuid.UUID  `gorm:"column:id;primaryKey;type:uuid"`
	CreatedAt                   time.Time  `gorm:"column:created_at"`
	UpdatedAt                   *time.Time `gorm:"column:updated_at"`
	DeletedAt                   gorm.DeletedAt `gorm:"column:deleted_at;index"`
	AccountID                   uuid.UUID  `gorm:"column:account_id;type:uuid"`
	DefaultPostingPublisherID   *uuid.UUID `gorm:"column:default_posting_publisher_id;type:uuid"`
	DefaultReplyPublisherID     *uuid.UUID `gorm:"column:default_reply_publisher_id;type:uuid"`
	DefaultFediversePublisherID *uuid.UUID `gorm:"column:default_fediverse_publisher_id;type:uuid"`
}

func (PublishingSettingsEntity) TableName() string { return "publishing_settings" }

// PublisherEntity is the `publishers` table.
type PublisherEntity struct {
	ID              uuid.UUID      `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Type            int            `gorm:"column:type"`
	Name            string         `gorm:"column:name"`
	Nick            string         `gorm:"column:nick"`
	Bio             *string        `gorm:"column:bio"`
	Picture         datatypes.JSON `gorm:"column:picture;type:jsonb"`
	Background      datatypes.JSON `gorm:"column:background;type:jsonb"`
	Verification    datatypes.JSON `gorm:"column:verification;type:jsonb"`
	Meta            datatypes.JSON `gorm:"column:meta;type:jsonb"`
	Metadata        datatypes.JSON `gorm:"column:metadata;type:jsonb"`
	URI             *string        `gorm:"column:uri"`
	ActorType       *string        `gorm:"column:actor_type"`
	Username        *string        `gorm:"column:username"`
	InstanceID      *uuid.UUID     `gorm:"column:instance_id;type:uuid"`
	InstanceDomain  *string        `gorm:"column:instance_domain"`
	InboxURI        *string        `gorm:"column:inbox_uri"`
	OutboxURI       *string        `gorm:"column:outbox_uri"`
	FollowersURI    *string        `gorm:"column:followers_uri"`
	FollowingURI    *string        `gorm:"column:following_uri"`
	FeaturedURI     *string        `gorm:"column:featured_uri"`
	PublicKeyID     *string        `gorm:"column:public_key_id"`
	PublicKey       *string        `gorm:"column:public_key"`
	IsBot           bool           `gorm:"column:is_bot"`
	IsLocked        bool           `gorm:"column:is_locked"`
	IsDiscoverable  bool           `gorm:"column:is_discoverable"`
	IsCommunity     bool           `gorm:"column:is_community"`
	LastFetchedAt   *time.Time     `gorm:"column:last_fetched_at"`
	LastActivityAt  *time.Time     `gorm:"column:last_activity_at"`
	OutboxFetchedAt *time.Time     `gorm:"column:outbox_fetched_at"`
	AccountID       *uuid.UUID     `gorm:"column:account_id;type:uuid"`
	RealmID         *uuid.UUID     `gorm:"column:realm_id;type:uuid"`
	PayoutWalletID  *uuid.UUID     `gorm:"column:payout_wallet_id;type:uuid"`
	ShadowbanReason *int           `gorm:"column:shadowban_reason"`
	ShadowbannedAt  *time.Time     `gorm:"column:shadowbanned_at"`
	GatekeptFollows *bool          `gorm:"column:gatekept_follows"`
	ModerateSubscription *bool     `gorm:"column:moderate_subscription"`
	Rating          float64        `gorm:"column:rating"`
}

func (PublisherEntity) TableName() string { return "publishers" }

// PublisherMemberEntity is the `publisher_members` table (no soft delete).
type PublisherMemberEntity struct {
	PublisherID uuid.UUID  `gorm:"column:publisher_id;primaryKey;type:uuid"`
	AccountID   uuid.UUID  `gorm:"column:account_id;primaryKey;type:uuid"`
	JoinedAt    *time.Time `gorm:"column:joined_at"`
	Role        int        `gorm:"column:role"`
}

func (PublisherMemberEntity) TableName() string { return "publisher_members" }

// PublisherSubscriptionEntity is the `publisher_subscriptions` table.
type PublisherSubscriptionEntity struct {
	ID                  uuid.UUID  `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	AccountID           *uuid.UUID `gorm:"column:account_id;type:uuid"`
	FollowerPublisherID *uuid.UUID `gorm:"column:follower_publisher_id;type:uuid"`
	PublisherID         uuid.UUID  `gorm:"column:publisher_id;type:uuid"`
	State               int        `gorm:"column:state"`
	FollowedAt          *time.Time `gorm:"column:followed_at"`
	ReviewedAt          *time.Time `gorm:"column:reviewed_at"`
	ReviewedByAccountID *uuid.UUID `gorm:"column:reviewed_by_account_id;type:uuid"`
	RejectReason        *string    `gorm:"column:reject_reason"`
	IsMuting            bool       `gorm:"column:is_muting"`
	IsBlocking          bool       `gorm:"column:is_blocking"`
	Notify              bool       `gorm:"column:notify"`
	LastReadAt          *time.Time `gorm:"column:last_read_at"`
	RealmID             *uuid.UUID `gorm:"column:realm_id;type:uuid"`
	EndedAt             *time.Time `gorm:"column:ended_at"`
	EndReason           *int       `gorm:"column:end_reason"`
	EndedByAccountID    *uuid.UUID `gorm:"column:ended_by_account_id;type:uuid"`
}

func (PublisherSubscriptionEntity) TableName() string { return "publisher_subscriptions" }

// PublisherFeatureEntity is the `publisher_features` table.
type PublisherFeatureEntity struct {
	ID          uuid.UUID  `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Flag        string     `gorm:"column:flag"`
	ExpiredAt   *time.Time `gorm:"column:expired_at"`
	PublisherID uuid.UUID  `gorm:"column:publisher_id;type:uuid"`
}

func (PublisherFeatureEntity) TableName() string { return "publisher_features" }

// PublisherRatingRecordEntity is the `publisher_rating_records` table.
type PublisherRatingRecordEntity struct {
	ID          uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	ReasonType  string    `gorm:"column:reason_type"`
	Reason      string    `gorm:"column:reason"`
	Delta       float64   `gorm:"column:delta"`
	PublisherID uuid.UUID `gorm:"column:publisher_id;type:uuid"`
}

func (PublisherRatingRecordEntity) TableName() string { return "publisher_rating_records" }

// PublisherVerifiedDomainEntity is the `publisher_verified_domains` table.
type PublisherVerifiedDomainEntity struct {
	ID             uuid.UUID  `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	PublisherID    uuid.UUID  `gorm:"column:publisher_id;type:uuid"`
	Domain         string     `gorm:"column:domain"`
	Status         int        `gorm:"column:status"`
	VerifiedAt     *time.Time `gorm:"column:verified_at"`
	LastCheckedAt  *time.Time `gorm:"column:last_checked_at"`
	FailedAttempts int        `gorm:"column:failed_attempts"`
	LastError      *string    `gorm:"column:last_error"`
}

func (PublisherVerifiedDomainEntity) TableName() string { return "publisher_verified_domains" }

// SurveyEntity is the `surveys` table.
type SurveyEntity struct {
	ID                uuid.UUID      `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Title             *string        `gorm:"column:title"`
	Description       *string        `gorm:"column:description"`
	EndedAt           *time.Time     `gorm:"column:ended_at"`
	IsAnonymous       bool           `gorm:"column:is_anonymous"`
	Status            int            `gorm:"column:status"`
	PublishedAt       *time.Time     `gorm:"column:published_at"`
	NotifySubscribers bool           `gorm:"column:notify_subscribers"`
	Attachments       datatypes.JSON `gorm:"column:attachments;type:jsonb"`
	HideResults       bool           `gorm:"column:hide_results"`
	PublisherID       uuid.UUID      `gorm:"column:publisher_id;type:uuid"`
}

func (SurveyEntity) TableName() string { return "surveys" }

// SurveyQuestionEntity is the `survey_questions` table.
type SurveyQuestionEntity struct {
	ID            uuid.UUID      `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Type          int            `gorm:"column:type"`
	Options       datatypes.JSON `gorm:"column:options;type:jsonb"`
	Title         string         `gorm:"column:title"`
	Description   *string        `gorm:"column:description"`
	Order         int            `gorm:"column:order"`
	IsRequired    bool           `gorm:"column:is_required"`
	Attachments   datatypes.JSON `gorm:"column:attachments;type:jsonb"`
	MaxSelections *int           `gorm:"column:max_selections"`
	MaxLength     *int           `gorm:"column:max_length"`
	MinValue      *float64       `gorm:"column:min_value"`
	MaxValue      *float64       `gorm:"column:max_value"`
	SurveyID      uuid.UUID      `gorm:"column:survey_id;type:uuid"`
}

func (SurveyQuestionEntity) TableName() string { return "survey_questions" }

// SurveyAnswerEntity is the `survey_answers` table.
type SurveyAnswerEntity struct {
	ID        uuid.UUID      `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Answer    datatypes.JSON `gorm:"column:answer;type:jsonb"`
	AccountID uuid.UUID      `gorm:"column:account_id;type:uuid"`
	SurveyID  uuid.UUID      `gorm:"column:survey_id;type:uuid"`
}

func (SurveyAnswerEntity) TableName() string { return "survey_answers" }

// SurveySubscriptionEntity is the `survey_subscriptions` table.
type SurveySubscriptionEntity struct {
	ID        uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	AccountID uuid.UUID `gorm:"column:account_id;type:uuid"`
	SurveyID  uuid.UUID `gorm:"column:survey_id;type:uuid"`
}

func (SurveySubscriptionEntity) TableName() string { return "survey_subscriptions" }

// StickerEntity is the `stickers` table.
type StickerEntity struct {
	ID        uuid.UUID      `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Slug      string         `gorm:"column:slug"`
	Name      string         `gorm:"column:name"`
	Image     datatypes.JSON `gorm:"column:image;type:jsonb"`
	Size      int            `gorm:"column:size"`
	Mode      int            `gorm:"column:mode"`
	Order     int            `gorm:"column:order"`
	PackID    uuid.UUID      `gorm:"column:pack_id;type:uuid"`
}

func (StickerEntity) TableName() string { return "stickers" }

// StickerPackEntity is the `sticker_packs` table.
type StickerPackEntity struct {
	ID          uuid.UUID      `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Icon        datatypes.JSON `gorm:"column:icon;type:jsonb"`
	Name        string         `gorm:"column:name"`
	Description string         `gorm:"column:description"`
	Prefix      string         `gorm:"column:prefix"`
	Popularity  int            `gorm:"column:popularity"`
	PublisherID uuid.UUID      `gorm:"column:publisher_id;type:uuid"`
}

func (StickerPackEntity) TableName() string { return "sticker_packs" }

// StickerPackOwnershipEntity is the `sticker_pack_ownerships` table.
type StickerPackOwnershipEntity struct {
	ID        uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	PackID    uuid.UUID `gorm:"column:pack_id;type:uuid"`
	AccountID uuid.UUID `gorm:"column:account_id;type:uuid"`
	Order     int       `gorm:"column:order"`
}

func (StickerPackOwnershipEntity) TableName() string { return "sticker_pack_ownerships" }

// RealmPostModerationLogEntity is the `realm_post_moderation_logs` table.
type RealmPostModerationLogEntity struct {
	ID                 uuid.UUID  `gorm:"column:id;primaryKey;type:uuid"`
	RealmID            uuid.UUID  `gorm:"column:realm_id;type:uuid"`
	PostID             uuid.UUID  `gorm:"column:post_id;type:uuid"`
	ModeratorAccountID uuid.UUID  `gorm:"column:moderator_account_id;type:uuid"`
	Reason             *string    `gorm:"column:reason"`
	ModeratedAt        *time.Time `gorm:"column:moderated_at"`
}

func (RealmPostModerationLogEntity) TableName() string { return "realm_post_moderation_logs" }

// FediverseInstanceEntity is the `fediverse_instances` table.
type FediverseInstanceEntity struct {
	ID                     uuid.UUID      `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Domain                 string         `gorm:"column:domain"`
	Name                   *string        `gorm:"column:name"`
	Description            *string        `gorm:"column:description"`
	Software               *string        `gorm:"column:software"`
	Version                *string        `gorm:"column:version"`
	IconURL                *string        `gorm:"column:icon_url"`
	ThumbnailURL           *string        `gorm:"column:thumbnail_url"`
	ContactEmail           *string        `gorm:"column:contact_email"`
	ContactAccountUsername *string        `gorm:"column:contact_account_username"`
	ActiveUsers            *int           `gorm:"column:active_users"`
	Metadata               datatypes.JSON `gorm:"column:metadata;type:jsonb"`
	IsBlocked              bool           `gorm:"column:is_blocked"`
	IsSilenced             bool           `gorm:"column:is_silenced"`
	BlockReason            *string        `gorm:"column:block_reason"`
	LastFetchedAt          *time.Time     `gorm:"column:last_fetched_at"`
	LastActivityAt         *time.Time     `gorm:"column:last_activity_at"`
	MetadataFetchedAt      *time.Time     `gorm:"column:metadata_fetched_at"`
}

func (FediverseInstanceEntity) TableName() string { return "fediverse_instances" }

// FediverseModerationRuleEntity is the `fediverse_moderation_rules` table.
type FediverseModerationRuleEntity struct {
	ID              uuid.UUID  `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Name            string     `gorm:"column:name"`
	Description     *string    `gorm:"column:description"`
	Type            int        `gorm:"column:type"`
	Action          int        `gorm:"column:action"`
	Domain          *string    `gorm:"column:domain"`
	KeywordPattern  *string    `gorm:"column:keyword_pattern"`
	IsRegex         bool       `gorm:"column:is_regex"`
	IsEnabled       bool       `gorm:"column:is_enabled"`
	ReportThreshold *int       `gorm:"column:report_threshold"`
	ExpiresAt       *time.Time `gorm:"column:expires_at"`
	Priority        int        `gorm:"column:priority"`
	IsSystemRule    bool       `gorm:"column:is_system_rule"`
}

func (FediverseModerationRuleEntity) TableName() string { return "fediverse_moderation_rules" }

// FediverseKeyEntity is the `fediverse_keys` table.
type FediverseKeyEntity struct {
	ID            uuid.UUID  `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	KeyID         string     `gorm:"column:key_id"`
	KeyPEM        string     `gorm:"column:key_pem"`
	PrivateKeyPEM *string    `gorm:"column:private_key_pem"`
	Algorithm     string     `gorm:"column:algorithm"`
	PublisherID   *uuid.UUID `gorm:"column:publisher_id;type:uuid"`
	RotatedAt     *time.Time `gorm:"column:rotated_at"`
}

func (FediverseKeyEntity) TableName() string { return "fediverse_keys" }

// ActivityPubDeliveryEntity is the `activity_pub_deliveries` table.
type ActivityPubDeliveryEntity struct {
	ID                 uuid.UUID  `gorm:"column:id;primaryKey;type:uuid"`
	ActivityID         string     `gorm:"column:activity_id"`
	ActivityType       string     `gorm:"column:activity_type"`
	InboxURI           string     `gorm:"column:inbox_uri"`
	ActorURI           string     `gorm:"column:actor_uri"`
	Status             int        `gorm:"column:status"`
	RetryCount         int        `gorm:"column:retry_count"`
	ErrorMessage       *string    `gorm:"column:error_message"`
	LastAttemptAt      *time.Time `gorm:"column:last_attempt_at"`
	NextRetryAt        *time.Time `gorm:"column:next_retry_at"`
	SentAt             *time.Time `gorm:"column:sent_at"`
	ResponseStatusCode *string    `gorm:"column:response_status_code"`
	ActivityPayload    *string    `gorm:"column:activity_payload"`
}

func (ActivityPubDeliveryEntity) TableName() string { return "activity_pub_deliveries" }

// DeliveryDeadLetterEntity is the `delivery_dead_letters` table.
type DeliveryDeadLetterEntity struct {
	ID              uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	DeliveryID      uuid.UUID `gorm:"column:delivery_id;type:uuid"`
	InboxURI        string    `gorm:"column:inbox_uri"`
	ActorURI        string    `gorm:"column:actor_uri"`
	ActivityType    string    `gorm:"column:activity_type"`
	ActivityPayload string    `gorm:"column:activity_payload"`
	ErrorMessage    *string   `gorm:"column:error_message"`
	RetryCount      int       `gorm:"column:retry_count"`
	CreatedAt       time.Time `gorm:"column:created_at"`
	FailedAt        time.Time `gorm:"column:failed_at"`
}

func (DeliveryDeadLetterEntity) TableName() string { return "delivery_dead_letters" }

// BoostEntity is the `boosts` table.
type BoostEntity struct {
	ID             uuid.UUID  `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	PostID         uuid.UUID  `gorm:"column:post_id;type:uuid"`
	PublisherID    uuid.UUID  `gorm:"column:publisher_id;type:uuid"`
	ActivityPubURI *string    `gorm:"column:activity_pub_uri"`
	WebURL         *string    `gorm:"column:web_url"`
	Content        *string    `gorm:"column:content"`
	BoostedAt      *time.Time `gorm:"column:boosted_at"`
}

func (BoostEntity) TableName() string { return "boosts" }

// QuoteAuthorizationEntity is the `quote_authorizations` table.
type QuoteAuthorizationEntity struct {
	ID                   uuid.UUID  `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	FediverseURI         *string    `gorm:"column:fediverse_uri"`
	PublisherID          uuid.UUID  `gorm:"column:publisher_id;type:uuid"`
	InteractingObjectURI string     `gorm:"column:interacting_object_uri"`
	InteractionTargetURI string     `gorm:"column:interaction_target_uri"`
	TargetPostID         *uuid.UUID `gorm:"column:target_post_id;type:uuid"`
	QuotePostID          *uuid.UUID `gorm:"column:quote_post_id;type:uuid"`
	IsValid              bool       `gorm:"column:is_valid"`
	RevokedAt            *time.Time `gorm:"column:revoked_at"`
}

func (QuoteAuthorizationEntity) TableName() string { return "quote_authorizations" }

// LiveStreamEntity is the `live_streams` table.
type LiveStreamEntity struct {
	ID                     uuid.UUID      `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Title                  *string        `gorm:"column:title"`
	Description            *string        `gorm:"column:description"`
	Slug                   *string        `gorm:"column:slug"`
	Type                   int            `gorm:"column:type"`
	Visibility             int            `gorm:"column:visibility"`
	Status                 int            `gorm:"column:status"`
	RoomName               string         `gorm:"column:room_name"`
	IngressID              *string        `gorm:"column:ingress_id"`
	IngressStreamKey       *string        `gorm:"column:ingress_stream_key"`
	EgressID               *string        `gorm:"column:egress_id"`
	HLSEgressID            *string        `gorm:"column:hls_egress_id"`
	HLSPlaylistPath        *string        `gorm:"column:hls_playlist_path"`
	HLSStartedAt           *time.Time     `gorm:"column:hls_started_at"`
	StartedAt              *time.Time     `gorm:"column:started_at"`
	EndedAt                *time.Time     `gorm:"column:ended_at"`
	TotalDurationSeconds   int64          `gorm:"column:total_duration_seconds"`
	ViewerCount            int            `gorm:"column:viewer_count"`
	PeakViewerCount        int            `gorm:"column:peak_viewer_count"`
	TotalAwardScore        float64        `gorm:"column:total_award_score"`
	DistributedAwardAmount float64        `gorm:"column:distributed_award_amount"`
	Thumbnail              datatypes.JSON `gorm:"column:thumbnail;type:jsonb"`
	Metadata               datatypes.JSON `gorm:"column:metadata;type:jsonb"`
	PublisherID            *uuid.UUID     `gorm:"column:publisher_id;type:uuid"`
}

func (LiveStreamEntity) TableName() string { return "live_streams" }

// LiveStreamChatMessageEntity is the `live_stream_chat_messages` table.
type LiveStreamChatMessageEntity struct {
	ID           uuid.UUID  `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	LiveStreamID uuid.UUID  `gorm:"column:live_stream_id;type:uuid"`
	SenderID     uuid.UUID  `gorm:"column:sender_id;type:uuid"`
	SenderName   string     `gorm:"column:sender_name"`
	Content      string     `gorm:"column:content"`
	TimeoutUntil *time.Time `gorm:"column:timeout_until"`
}

func (LiveStreamChatMessageEntity) TableName() string { return "live_stream_chat_messages" }

// LiveStreamAwardEntity is the `live_stream_awards` table.
type LiveStreamAwardEntity struct {
	ID           uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Amount       float64   `gorm:"column:amount"`
	Attitude     int       `gorm:"column:attitude"`
	Message      *string   `gorm:"column:message"`
	LiveStreamID uuid.UUID `gorm:"column:live_stream_id;type:uuid"`
	AccountID    uuid.UUID `gorm:"column:account_id;type:uuid"`
	SenderName   string    `gorm:"column:sender_name"`
}

func (LiveStreamAwardEntity) TableName() string { return "live_stream_awards" }

// AutomodRuleEntity is the `automod_rules` table.
type AutomodRuleEntity struct {
	ID            uuid.UUID  `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Name          string     `gorm:"column:name"`
	Description   *string    `gorm:"column:description"`
	Type          int        `gorm:"column:type"`
	DefaultAction int        `gorm:"column:default_action"`
	Pattern       string     `gorm:"column:pattern"`
	IsRegex       bool       `gorm:"column:is_regex"`
	DerankWeight  int        `gorm:"column:derank_weight"`
	IsEnabled     bool       `gorm:"column:is_enabled"`
	ExpiresAt     *time.Time `gorm:"column:expires_at"`
	Priority      int        `gorm:"column:priority"`
}

func (AutomodRuleEntity) TableName() string { return "automod_rules" }

// RealmRolePermissionEntity is the `sn_realm_role_permission` table.
type RealmRolePermissionEntity struct {
	ID                 uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	RealmID            uuid.UUID `gorm:"column:realm_id;type:uuid"`
	RoleLevel          int       `gorm:"column:role_level"`
	CanChat            bool      `gorm:"column:can_chat"`
	CanComment         bool      `gorm:"column:can_comment"`
	CanManageMembers   bool      `gorm:"column:can_manage_members"`
	CanManageRealm     bool      `gorm:"column:can_manage_realm"`
	CanModerateChat    bool      `gorm:"column:can_moderate_chat"`
	CanModeratePosts   bool      `gorm:"column:can_moderate_posts"`
	CanPost            bool      `gorm:"column:can_post"`
	CanUploadMedia     bool      `gorm:"column:can_upload_media"`
}

func (RealmRolePermissionEntity) TableName() string { return "sn_realm_role_permission" }

// RealmUserPermissionEntity is the `sn_realm_user_permission` table.
type RealmUserPermissionEntity struct {
	ID               uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	RealmID          uuid.UUID `gorm:"column:realm_id;type:uuid"`
	AccountID        uuid.UUID `gorm:"column:account_id;type:uuid"`
	CanChat          *bool     `gorm:"column:can_chat"`
	CanComment       *bool     `gorm:"column:can_comment"`
	CanManageMembers *bool     `gorm:"column:can_manage_members"`
	CanManageRealm   *bool     `gorm:"column:can_manage_realm"`
	CanModerateChat  *bool     `gorm:"column:can_moderate_chat"`
	CanModeratePosts *bool     `gorm:"column:can_moderate_posts"`
	CanPost          *bool     `gorm:"column:can_post"`
	CanUploadMedia   *bool     `gorm:"column:can_upload_media"`
}

func (RealmUserPermissionEntity) TableName() string { return "sn_realm_user_permission" }
