package timeline

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	models "src.solsynth.dev/sosys/go/pkg/models"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/accountjson"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
)

// Permission nodes the discovery routes ask for, mirroring PermissionKeys.
const (
	permissionTimelinesFeedback      = "timelines.feedback"
	permissionTimelinesWeightsManage = "timelines.weights.manage"
	permissionTimelinesReset         = "timelines.reset"
)

// timelineMode mirrors SnTimelineMode.
type timelineMode int

const (
	modePersonalized timelineMode = iota
	modeTop
	modeLatest
)

func (m timelineMode) String() string {
	switch m {
	case modeTop:
		return "top"
	case modeLatest:
		return "latest"
	default:
		return "personalized"
	}
}

// parseTimelineMode mirrors the controller's parser: empty means personalized,
// anything else outside the three names is invalid.
func parseTimelineMode(raw string) (timelineMode, bool) {
	switch normalizeKind(raw) {
	case "":
		return modePersonalized, true
	case "personalized":
		return modePersonalized, true
	case "top":
		return modeTop, true
	case "latest":
		return modeLatest, true
	default:
		return modePersonalized, false
	}
}

// discoveryTargetKind mirrors DiscoveryTargetKind.
type discoveryTargetKind int

const (
	discoveryTargetPublisher discoveryTargetKind = iota
	discoveryTargetRealm
	discoveryTargetAccount
)

func parseDiscoveryTargetKind(raw string) (discoveryTargetKind, bool) {
	switch normalizeKind(raw) {
	case "publisher":
		return discoveryTargetPublisher, true
	case "realm":
		return discoveryTargetRealm, true
	case "account":
		return discoveryTargetAccount, true
	default:
		return discoveryTargetPublisher, false
	}
}

// postInterestKind mirrors PostInterestKind.
type postInterestKind int

const (
	interestTag postInterestKind = iota
	interestCategory
	interestPublisher
	interestCollection
)

func (k postInterestKind) String() string {
	switch k {
	case interestCategory:
		return "category"
	case interestPublisher:
		return "publisher"
	case interestCollection:
		return "collection"
	default:
		return "tag"
	}
}

func parsePostInterestKind(raw string) (postInterestKind, bool) {
	switch normalizeKind(raw) {
	case "publisher":
		return interestPublisher, true
	case "collection":
		return interestCollection, true
	case "tag":
		return interestTag, true
	case "category":
		return interestCategory, true
	default:
		return interestTag, false
	}
}

// recommendationFeedbackValue mirrors RecommendationFeedbackValue.
type recommendationFeedbackValue int

const (
	feedbackPositive recommendationFeedbackValue = iota
	feedbackNegative
)

func (v recommendationFeedbackValue) String() string {
	if v == feedbackNegative {
		return "negative"
	}
	return "positive"
}

func parseRecommendationFeedback(raw string) (recommendationFeedbackValue, bool) {
	switch normalizeKind(raw) {
	case "positive", "good", "like":
		return feedbackPositive, true
	case "negative", "bad", "dislike":
		return feedbackNegative, true
	default:
		return feedbackPositive, false
	}
}

// discoveryPreferenceState mirrors DiscoveryPreferenceState (only
// "uninterested" exists in the C# enum).
const discoveryPreferenceUninterested = 0

// normalizeKind lower-cases and trims a query/body enum value.
func normalizeKind(raw string) string { return strings.ToLower(strings.TrimSpace(raw)) }

// timelineFeedScope mirrors TimelineService.TimelineFeedScope.
type timelineFeedScope struct {
	requestedPublisherName  *string
	requestedCollectionSlug *string
	publisherID             *uuid.UUID
	collectionID            *uuid.UUID
}

func (s timelineFeedScope) hasExplicitScope() bool {
	return s.publisherID != nil || s.collectionID != nil
}

// discoveryPreferenceRequest mirrors DiscoveryPreferenceRequest.
type discoveryPreferenceRequest struct {
	Kind        string  `json:"kind"`
	ReferenceID string  `json:"reference_id"`
	Reason      *string `json:"reason"`
}

// recommendationFeedbackRequest mirrors RecommendationFeedbackRequest.
type recommendationFeedbackRequest struct {
	Kind        string  `json:"kind"`
	ReferenceID string  `json:"reference_id"`
	Feedback    string  `json:"feedback"`
	Reason      *string `json:"reason"`
	Suppress    bool    `json:"suppress"`
}

// recommendationWeightChangeRequest mirrors RecommendationWeightChangeRequest.
type recommendationWeightChangeRequest struct {
	Kind             string  `json:"kind"`
	ReferenceID      string  `json:"reference_id"`
	ScoreDelta       float64 `json:"score_delta"`
	InteractionCount int     `json:"interaction_count"`
	SignalType       *string `json:"signal_type"`
}

// newTimelineEvent builds a model.TimelineEvent with the C# Meta default of an
// empty object.
func newTimelineEvent(id, eventType, resource string, data any, createdAt, updatedAt time.Time) model.TimelineEvent {
	return model.TimelineEvent{
		ID:                 id,
		Type:               eventType,
		ResourceIdentifier: resource,
		Meta:               map[string]any{},
		Data:               data,
		CreatedAt:          model.NewTime(createdAt),
		UpdatedAt:          model.NewTime(updatedAt),
	}
}

// emptyTimelineEvent mirrors SnTimelineEvent.Empty().
func emptyTimelineEvent(now time.Time) model.TimelineEvent {
	return newTimelineEvent(uuid.NewString(), "empty", "none", nil, now, now)
}

// postTimelineEvent mirrors SnPost.ToActivity().
func postTimelineEvent(post model.Post, createdAt time.Time) model.TimelineEvent {
	eventType := "posts.new"
	if post.RepliedPostID != nil {
		eventType = "posts.new.replies"
	}
	var updated time.Time
	if post.UpdatedAt != nil {
		updated = post.UpdatedAt.Time()
	}
	event := newTimelineEvent(post.ID, eventType, post.ResourceIdentifier, post, createdAt, updated)
	event.DeletedAt = post.DeletedAt
	return event
}

// presenceTag is the wire shape of SnPresenceTag.
type presenceTag struct {
	Slug string  `json:"slug"`
	Name *string `json:"name"`
}

// presenceActivity is the wire shape of SnPresenceActivity.
type presenceActivity struct {
	ID             string         `json:"id"`
	Type           *string        `json:"type"`
	Tags           []presenceTag  `json:"tags"`
	ActivityTags   []any          `json:"activity_tags"`
	Visibility     int            `json:"visibility"`
	Provider       *string        `json:"provider"`
	ReferenceID    *string        `json:"reference_id"`
	ManualID       *string        `json:"manual_id"`
	Title          *string        `json:"title"`
	Subtitle       *string        `json:"subtitle"`
	Caption        *string        `json:"caption"`
	LargeImage     *string        `json:"large_image"`
	SmallImage     *string        `json:"small_image"`
	TitleURL       *string        `json:"title_url"`
	SubtitleURL    *string        `json:"subtitle_url"`
	QueryableTerms []string       `json:"queryable_terms"`
	Meta           map[string]any `json:"meta"`
	LeaseMinutes   int            `json:"lease_minutes"`
	LeaseExpiresAt *model.Time    `json:"lease_expires_at"`
	StartedAt      *model.Time    `json:"started_at"`
	EndedAt        *model.Time    `json:"ended_at"`
	CatalogID      *string        `json:"catalog_id"`
	AccountID      string         `json:"account_id"`
	Account        any            `json:"account"`
	CreatedAt      *model.Time    `json:"created_at"`
	UpdatedAt      *model.Time    `json:"updated_at"`
	DeletedAt      *model.Time    `json:"deleted_at"`
}

// friendPresenceEvent mirrors FriendPresenceEvent.
type friendPresenceEvent struct {
	Activity presenceActivity `json:"activity"`
}

// accountStatus is the wire shape of SnAccountStatus.
type accountStatus struct {
	ID            string         `json:"id"`
	Attitude      int            `json:"attitude"`
	IsOnline      bool           `json:"is_online"`
	IsIdle        bool           `json:"is_idle"`
	IdleSince     *model.Time    `json:"idle_since"`
	IsCustomized  bool           `json:"is_customized"`
	Type          int            `json:"type"`
	Label         *string        `json:"label"`
	Symbol        *string        `json:"symbol"`
	Icon          any            `json:"icon"`
	Background    any            `json:"background"`
	Meta          map[string]any `json:"meta"`
	ClearedAt     *model.Time    `json:"cleared_at"`
	AppIdentifier *string        `json:"app_identifier"`
	IsAutomated   bool           `json:"is_automated"`
	AccountID     string         `json:"account_id"`
	Account       any            `json:"account"`
	CreatedAt     *model.Time    `json:"created_at"`
	UpdatedAt     *model.Time    `json:"updated_at"`
	DeletedAt     *model.Time    `json:"deleted_at"`
}

// friendStatusEvent mirrors FriendStatusEvent.
type friendStatusEvent struct {
	Status accountStatus `json:"status"`
}

// personalizedDiscoveryEvent mirrors PersonalizedTimelineDiscoveryEvent. The
// suggestion payload is the same publisher/account/realm snapshot the profile
// exposes.
type personalizedDiscoveryEvent struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Items []any  `json:"items"`
}

// timestampPtr converts a protobuf timestamp, nil when absent.
func timestampPtr(ts *timestamppb.Timestamp) *model.Time {
	if ts == nil {
		return nil
	}
	return model.NewTime(ts.AsTime())
}

// presenceActivityFromProto maps DyPresenceActivity onto the C# wire shape.
func presenceActivityFromProto(activity *gen.DyPresenceActivity) presenceActivity {
	out := presenceActivity{
		ID:             activity.GetId(),
		Type:           activity.Type,
		Tags:           []presenceTag{},
		ActivityTags:   []any{},
		Visibility:     int(activity.GetVisibility()),
		ManualID:       activity.ManualId,
		Title:          activity.Title,
		Subtitle:       activity.Subtitle,
		Caption:        activity.Caption,
		LargeImage:     activity.LargeImage,
		SmallImage:     activity.SmallImage,
		TitleURL:       activity.TitleUrl,
		SubtitleURL:    activity.SubtitleUrl,
		QueryableTerms: []string{},
		Meta:           map[string]any{},
		LeaseMinutes:   int(activity.GetLeaseMinutes()),
		LeaseExpiresAt: timestampPtr(activity.GetLeaseExpiresAt()),
		StartedAt:      timestampPtr(activity.GetStartedAt()),
		EndedAt:        timestampPtr(activity.GetEndedAt()),
		CatalogID:      activity.CatalogId,
		AccountID:      activity.GetAccountId(),
		CreatedAt:      timestampPtr(activity.GetCreatedAt()),
		UpdatedAt:      timestampPtr(activity.GetUpdatedAt()),
		DeletedAt:      timestampPtr(activity.GetDeletedAt()),
	}
	for _, tag := range activity.GetTags() {
		out.Tags = append(out.Tags, presenceTag{Slug: tag.GetSlug(), Name: tag.Name})
	}
	if meta := activity.GetMeta(); meta != nil {
		out.Meta = meta.AsMap()
	}
	return out
}

// accountStatusFromProto maps DyAccountStatus onto the wire shape.
func accountStatusFromProto(status *gen.DyAccountStatus) accountStatus {
	out := accountStatus{
		ID:            status.GetId(),
		Attitude:      int(status.GetAttitude()),
		IsOnline:      status.GetIsOnline(),
		IsIdle:        status.GetIsIdle(),
		IsCustomized:  status.GetIsCustomized(),
		Type:          int(status.GetType()),
		Label:         status.Label,
		Symbol:        status.Symbol,
		AppIdentifier: status.AppIdentifier,
		IsAutomated:   status.GetIsAutomated(),
		AccountID:     status.GetAccountId(),
		Meta:          map[string]any{},
		IdleSince:     timestampPtr(status.GetIdleSince()),
		ClearedAt:     timestampPtr(status.GetClearedAt()),
		CreatedAt:     timestampPtr(status.GetCreatedAt()),
		UpdatedAt:     timestampPtr(status.GetUpdatedAt()),
		DeletedAt:     timestampPtr(status.GetDeletedAt()),
	}
	if len(status.GetMeta()) > 0 {
		var value structpb.Struct
		if err := value.UnmarshalJSON(status.GetMeta()); err == nil {
			out.Meta = value.AsMap()
		}
	}
	return out
}

// accountRefFromProto embeds the full DyAccount wire shape, matching the C#
// SnAccount hydration on presence activities and statuses.
func accountRefFromProto(account *gen.DyAccount) any {
	return accountjson.FromProto(account)
}

// realmRefFromProto maps a DyRealm onto the realm snapshot embedded in posts.
func realmRefFromProto(realm *gen.DyRealm) *model.RealmRef {
	if realm == nil {
		return nil
	}
	description := realm.GetDescription()
	ref := &model.RealmRef{
		ID:           realm.GetId(),
		Slug:         realm.GetSlug(),
		Name:         realm.GetName(),
		Description:  &description,
		IsCommunity:  realm.GetIsCommunity(),
		IsPublic:     realm.GetIsPublic(),
		Verification: model.VerificationFromProto(realm.GetVerification()),
		AccountID:    realm.GetAccountId(),
		BoostPoints:  model.RealmBoostPoints(realm.GetBoostPoints()),
		BoostLevel:   int(realm.GetBoostLevel()),
	}
	if picture := realm.GetPicture(); picture != nil {
		ref.Picture = models.FromProtoValue(picture)
	}
	if background := realm.GetBackground(); background != nil {
		ref.Background = models.FromProtoValue(background)
	}
	return ref
}
