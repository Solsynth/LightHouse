// Package model defines the Sphere wire types. All JSON is snake_case; enums
// serialize as integers; times serialize as UTC RFC3339 via model.Time, the Go
// counterpart of NodaTime's Instant. Fields without omitempty are always
// emitted (null when nil), matching System.Text.Json's default behaviour.
package model

import (
	"time"

	models "src.solsynth.dev/sosys/go/pkg/models"
)

// Time is the fleet Instant type (UTC RFC3339, seconds precision).
type Time = models.Time

// NewTime converts a time.Time into a *Time (nil for the zero value).
func NewTime(t time.Time) *Time { return models.NewTime(t) }

// CloudFileReferenceObject is the jsonb file-cache shape shared with Drive.
type CloudFileReferenceObject = models.SnCloudFileReferenceObject

// PostType mirrors the C# PostType enum.
type PostType int

const (
	PostTypeMoment  PostType = 0
	PostTypeArticle PostType = 1
	PostTypeBlog    PostType = 2
)

// PostVisibility mirrors the C# PostVisibility enum.
type PostVisibility int

const (
	VisibilityPublic          PostVisibility = 0
	VisibilityFriends         PostVisibility = 1
	VisibilityUnlisted        PostVisibility = 2
	VisibilityPrivate         PostVisibility = 3
	VisibilityCloseFriendsOnly PostVisibility = 4
	VisibilityQuietPublic     PostVisibility = 5
)

// PostContentType mirrors the C# PostContentType enum.
type PostContentType int

const (
	ContentTypeMarkdown PostContentType = 0
	ContentTypeHTML     PostContentType = 1
)

// PostPinMode mirrors the C# PostPinMode enum.
type PostPinMode int

const (
	PinModePublisherPage PostPinMode = 0
	PinModeRealmPage     PostPinMode = 1
	PinModeReplyPage     PostPinMode = 2
)

// PostShadowbanReason mirrors the C# PostShadowbanReason enum.
type PostShadowbanReason int

const (
	ShadowbanNone           PostShadowbanReason = 0
	ShadowbanSpam           PostShadowbanReason = 1
	ShadowbanAdvertising    PostShadowbanReason = 2
	ShadowbanHarassment     PostShadowbanReason = 3
	ShadowbanHateSpeech     PostShadowbanReason = 4
	ShadowbanMisinformation PostShadowbanReason = 5
	ShadowbanIllegal        PostShadowbanReason = 6
	ShadowbanOther          PostShadowbanReason = 99
)

// PostReactionAttitude mirrors the C# PostReactionAttitude enum.
type PostReactionAttitude int

const (
	ReactionNeutral  PostReactionAttitude = 0
	ReactionPositive PostReactionAttitude = 1
	ReactionNegative PostReactionAttitude = 2
)

// PublisherType mirrors the C# PublisherType enum.
type PublisherType int

const (
	PublisherIndividual     PublisherType = 0
	PublisherOrganizational PublisherType = 1
	PublisherFediverse      PublisherType = 2
)

// PublisherRole mirrors the C# PublisherMemberRole values.
type PublisherRole int

const (
	PublisherRoleViewer  PublisherRole = 25
	PublisherRoleEditor  PublisherRole = 50
	PublisherRoleManager PublisherRole = 75
	PublisherRoleOwner   PublisherRole = 100
)

// PublisherSubscriptionState mirrors the C# enum.
type PublisherSubscriptionState int

const (
	SubscriptionPending  PublisherSubscriptionState = 0
	SubscriptionAccepted PublisherSubscriptionState = 1
	SubscriptionRejected PublisherSubscriptionState = 2
)

// PublisherSubscriptionEndReason mirrors the C# enum.
type PublisherSubscriptionEndReason int

const (
	SubscriptionEndedUserLeft           PublisherSubscriptionEndReason = 0
	SubscriptionEndedRemovedByPublisher PublisherSubscriptionEndReason = 1
)

// SurveyStatus mirrors the C# SnSurveyStatus enum.
type SurveyStatus int

const (
	SurveyDraft     SurveyStatus = 0
	SurveyPublished SurveyStatus = 1
	SurveyArchived  SurveyStatus = 2
)

// SurveyQuestionType mirrors the C# SnSurveyQuestionType enum.
type SurveyQuestionType int

const (
	QuestionSingleChoice   SurveyQuestionType = 0
	QuestionMultipleChoice SurveyQuestionType = 1
	QuestionYesNo          SurveyQuestionType = 2
	QuestionRating         SurveyQuestionType = 3
	QuestionFreeText       SurveyQuestionType = 4
)

// StickerSize mirrors the C# SnStickerSize enum.
type StickerSize int

const (
	StickerSizeAuto   StickerSize = 0
	StickerSizeSmall  StickerSize = 1
	StickerSizeMedium StickerSize = 2
	StickerSizeLarge  StickerSize = 3
)

// StickerMode mirrors the C# SnStickerMode enum.
type StickerMode int

const (
	StickerModeSticker StickerMode = 0
	StickerModeEmote   StickerMode = 1
)

// PostWatchSource mirrors the C# PostWatchSource enum.
type PostWatchSource int

const (
	WatchSourceBookmark PostWatchSource = 0
	WatchSourceReaction PostWatchSource = 1
	WatchSourceReply    PostWatchSource = 2
)

// VerificationMark is the jsonb verification block shared with Stargate.
type VerificationMark struct {
	Type        int     `json:"type"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	VerifiedBy  *string `json:"verified_by"`
}

// ContentMention mirrors the C# ContentMention.
type ContentMention struct {
	Username *string `json:"username"`
	URL      *string `json:"url"`
	ActorURI *string `json:"actor_uri"`
}

// ContentTag mirrors the C# ContentTag.
type ContentTag struct {
	Name *string `json:"name"`
	URL  *string `json:"url"`
}

// ContentEmoji mirrors the C# ContentEmoji.
type ContentEmoji struct {
	Shortcode *string `json:"shortcode"`
	StaticURL *string `json:"static_url"`
	URL       *string `json:"url"`
}

// SensitiveMark is one content-warning entry.
type SensitiveMark struct {
	Type        int     `json:"type"`
	Description *string `json:"description"`
}

// PublisherActivityPub is the activitypub object embedded in a publisher.
type PublisherActivityPub struct {
	URI             *string `json:"uri"`
	ActorType       *string `json:"actor_type"`
	Username        *string `json:"username"`
	InstanceID      *string `json:"instance_id"`
	InstanceDomain  *string `json:"instance_domain"`
	InboxURI        *string `json:"inbox_uri"`
	OutboxURI       *string `json:"outbox_uri"`
	FollowersURI    *string `json:"followers_uri"`
	FollowingURI    *string `json:"following_uri"`
	FeaturedURI     *string `json:"featured_uri"`
	PublicKeyID     *string `json:"public_key_id"`
	PublicKey       *string `json:"public_key"`
	IsBot           bool    `json:"is_bot"`
	IsLocked        bool    `json:"is_locked"`
	IsDiscoverable  bool    `json:"is_discoverable"`
	IsCommunity     bool    `json:"is_community"`
	LastFetchedAt   *Time   `json:"last_fetched_at"`
	LastActivityAt  *Time   `json:"last_activity_at"`
	OutboxFetchedAt *Time   `json:"outbox_fetched_at"`
	FollowersCount  int     `json:"followers_count"`
	FollowingCount  int     `json:"following_count"`
	PostCount       int     `json:"post_count"`
	TotalPostCount  *int    `json:"total_post_count"`
	FullHandle      string  `json:"full_handle"`
	WebURL          string  `json:"web_url"`
}

// RealmRef is the realm reference embedded in posts and publishers. Realms
// stay in Passport; Sphere only carries the snapshot fields the C# model
// exposed.
type RealmRef struct {
	ID          string  `json:"id"`
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	IsPublic    bool    `json:"is_public"`
	Picture     any     `json:"picture"`
	Background  any     `json:"background"`
}

// AccountRef is the account reference embedded in responses.
type AccountRef struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Nick     string  `json:"nick"`
	Region   string  `json:"region"`
	Language string  `json:"language"`
	Picture  any     `json:"picture"`
}

// SubscriptionRef is the perk-subscription snapshot Sphere embeds in responses
// (mirrors SnSubscriptionReferenceObject).
type SubscriptionRef struct {
	ID          string  `json:"id"`
	Identifier  string  `json:"identifier"`
	DisplayName *string `json:"display_name,omitempty"`
	PerkLevel   int     `json:"perk_level"`
	IsActive    bool    `json:"is_active"`
	IsAvailable bool    `json:"is_available"`
	IsFreeTrial bool    `json:"is_free_trial"`
	Status      int     `json:"status"`
}
