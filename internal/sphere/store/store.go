package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/filejson"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps the shared GORM handle. Callers intentionally share this handle
// so service-level transactions can span store and runtime consumer writes.
type Store struct {
	DB *gorm.DB
}

// New builds a store over the shared handle.
func New(database *gorm.DB) *Store { return &Store{DB: database} }

// WithTx returns a store bound to tx.
func (s *Store) WithTx(tx *gorm.DB) *Store { return &Store{DB: tx} }

// mapNotFound normalizes GORM's not-found error.
func mapNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

// ParseUUID parses a UUID string.
func ParseUUID(value string) (uuid.UUID, error) { return uuid.Parse(value) }

// UUIDString renders a UUID.
func UUIDString(id uuid.UUID) string { return id.String() }

// UUIDPtrString renders an optional UUID.
func UUIDPtrString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	value := id.String()
	return &value
}

// TimePtr converts a time.Time into a *model.Time (nil for nil).
func TimePtr(value *time.Time) *model.Time {
	if value == nil {
		return nil
	}
	return model.NewTime(*value)
}

// TimeValue converts a *model.Time back into a time.Time (zero for nil).
func TimeValue(value *model.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.Time()
}

// JSONValue decodes a jsonb column into a generic value. A nil/empty column
// yields nil so the wire shape stays null.
func JSONValue(raw datatypes.JSON) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return value
}

// JSONArray decodes a jsonb array into dst, leaving dst empty when the column
// is null.
func JSONArray[T any](raw datatypes.JSON, dst *[]T) {
	if len(raw) == 0 {
		return
	}
	_ = json.Unmarshal(raw, dst)
}

// JSONMap decodes a jsonb object into dst.
func JSONMap[T any](raw datatypes.JSON, dst *T) {
	if len(raw) == 0 {
		return
	}
	_ = json.Unmarshal(raw, dst)
}

// ToJSON encodes a value for a jsonb column, returning nil for nil input.
func ToJSON(value any) datatypes.JSON {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return datatypes.JSON(raw)
}

// DeletedPtr renders a soft-delete timestamp for the wire.
func DeletedPtr(value gorm.DeletedAt) *model.Time {
	if !value.Valid {
		return nil
	}
	return model.NewTime(value.Time)
}

// PostToModel maps a post row to the wire type. Relations and computed fields
// are filled by the caller.
func PostToModel(entity *PostEntity) model.Post {
	post := model.Post{
		ID:                   entity.ID.String(),
		Title:                entity.Title,
		Description:          entity.Description,
		Slug:                 entity.Slug,
		EditedAt:             TimePtr(entity.EditedAt),
		DraftedAt:            TimePtr(entity.DraftedAt),
		PublishedAt:          TimePtr(entity.PublishedAt),
		Visibility:           entity.Visibility,
		Content:              entity.Content,
		ContentType:          entity.ContentType,
		Type:                 entity.Type,
		PinMode:              entity.PinMode,
		Meta:                 JSONValue(entity.Metadata),
		FediverseURI:         entity.FediverseURI,
		FediverseType:        entity.FediverseType,
		Language:             entity.Language,
		BoostCount:           entity.BoostCount,
		ViewsUnique:          entity.ViewsUnique,
		ViewsTotal:           entity.ViewsTotal,
		Upvotes:              entity.Upvotes,
		Downvotes:            entity.Downvotes,
		AwardedScore:         entity.AwardedScore,
		RepliesCount:         entity.RepliesCount,
		RepliedGone:          entity.RepliedGone,
		ForwardedGone:        entity.ForwardedGone,
		RepliedPostID:        UUIDPtrString(entity.RepliedPostID),
		ForwardedPostID:      UUIDPtrString(entity.ForwardedPostID),
		ChainedPostID:        UUIDPtrString(entity.ChainedPostID),
		QuoteAuthorizationID: UUIDPtrString(entity.QuoteAuthorizationID),
		RealmID:              UUIDPtrString(entity.RealmID),
		PublisherID:          entity.PublisherID.String(),
		ShadowbanReason:      entity.ShadowbanReason,
		ShadowbannedAt:       TimePtr(entity.ShadowbannedAt),
		LockedAt:             TimePtr(entity.LockedAt),
		CreatedAt:            model.NewTime(entity.CreatedAt),
		UpdatedAt:            model.NewTime(entity.UpdatedAt),
		DeletedAt:            DeletedPtr(entity.DeletedAt),
		ResourceIdentifier:   "post:" + entity.ID.String(),
		ChainedPosts:         []model.Post{},
		Tags:                 []model.PostTag{},
		Categories:           []model.PostCategory{},
		PublisherCollections: []model.PostCollection{},
		FeaturedRecords:      []model.PostFeaturedRecord{},
		Awards:               []model.PostAward{},
		ReactionsCount:       map[string]int{},
		ReactionsMade:        map[string]bool{},
		SensitiveMarks:       []model.SensitiveMark{},
		Mentions:             []model.ContentMention{},
		Attachments:          filejson.References(entity.Attachments),
	}
	JSONArray(entity.SensitiveMarks, &post.SensitiveMarks)
	JSONArray(entity.Mentions, &post.Mentions)
	// embed_view is a typed C# jsonb object (PostEmbedView) stored with
	// PascalCase keys; the client strict-casts its snake_case fields.
	post.EmbedView = filejson.Value(entity.EmbedView)
	post.IsShadowbanned = entity.ShadowbanReason != nil && *entity.ShadowbanReason != int(model.ShadowbanNone)
	return post
}

// PublisherToModel maps a publisher row to the wire type.
func PublisherToModel(entity *PublisherEntity) model.Publisher {
	nick := entity.Nick
	publisher := model.Publisher{
		ID:                     entity.ID.String(),
		Type:                   entity.Type,
		Name:                   entity.Name,
		Nick:                   entity.Nick,
		Bio:                    entity.Bio,
		Picture:                filejson.Value(entity.Picture),
		Background:             filejson.Value(entity.Background),
		Meta:                   JSONValue(entity.Meta),
		Metadata:               JSONValue(entity.Metadata),
		AccountID:              UUIDPtrString(entity.AccountID),
		RealmID:                UUIDPtrString(entity.RealmID),
		PayoutWalletID:         UUIDPtrString(entity.PayoutWalletID),
		DisplayName:            &nick,
		ShadowbanReason:        entity.ShadowbanReason,
		ShadowbannedAt:         TimePtr(entity.ShadowbannedAt),
		GatekeptFollows:        entity.GatekeptFollows,
		ModerateSubscription:   entity.ModerateSubscription,
		Rating:                 entity.Rating,
		RatingLevel:            RatingLevel(entity.Rating),
		IsShadowbanned:         entity.ShadowbanReason != nil && *entity.ShadowbanReason != 0,
		IsGatekept:             entity.GatekeptFollows != nil && *entity.GatekeptFollows,
		IsModerateSubscription: entity.ModerateSubscription != nil && *entity.ModerateSubscription,
		CreatedAt:              model.NewTime(entity.CreatedAt),
		UpdatedAt:              model.NewTime(entity.UpdatedAt),
		DeletedAt:              DeletedPtr(entity.DeletedAt),
	}
	var verification model.VerificationMark
	if len(entity.Verification) > 0 {
		if err := filejson.Unmarshal(entity.Verification, &verification); err == nil {
			publisher.Verification = &verification
		}
	}
	if entity.URI != nil || entity.Type == int(model.PublisherFediverse) {
		publisher.ActivityPub = &model.PublisherActivityPub{
			URI: entity.URI, ActorType: entity.ActorType, Username: entity.Username,
			InstanceID: UUIDPtrString(entity.InstanceID), InstanceDomain: entity.InstanceDomain,
			InboxURI: entity.InboxURI, OutboxURI: entity.OutboxURI,
			FollowersURI: entity.FollowersURI, FollowingURI: entity.FollowingURI,
			FeaturedURI: entity.FeaturedURI, PublicKeyID: entity.PublicKeyID,
			PublicKey: entity.PublicKey, IsBot: entity.IsBot, IsLocked: entity.IsLocked,
			IsDiscoverable: entity.IsDiscoverable, IsCommunity: entity.IsCommunity,
			LastFetchedAt: TimePtr(entity.LastFetchedAt), LastActivityAt: TimePtr(entity.LastActivityAt),
			OutboxFetchedAt: TimePtr(entity.OutboxFetchedAt),
			FullHandle:      fullHandle(entity),
			WebURL:          webURL(entity),
		}
	}
	return publisher
}

// RatingLevel mirrors SnPublisher.RatingLevel: <100 → -1, <200 → 0, <300 → 1,
// else 2.
func RatingLevel(rating float64) int {
	switch {
	case rating < 100:
		return -1
	case rating < 200:
		return 0
	case rating < 300:
		return 1
	default:
		return 2
	}
}

func fullHandle(entity *PublisherEntity) string {
	if entity.Username != nil && entity.InstanceDomain != nil && *entity.Username != "" && *entity.InstanceDomain != "" {
		return *entity.Username + "@" + *entity.InstanceDomain
	}
	return entity.Name
}

func webURL(entity *PublisherEntity) string {
	if entity.URI != nil && len(*entity.URI) >= 4 && (*entity.URI)[:4] == "http" {
		return *entity.URI
	}
	domain := "localhost"
	if entity.InstanceDomain != nil && *entity.InstanceDomain != "" {
		domain = *entity.InstanceDomain
	}
	username := entity.Name
	if entity.Username != nil && *entity.Username != "" {
		username = *entity.Username
	}
	return "https://" + domain + "/@" + username
}

// SurveyToModel maps a survey row to the wire type.
func SurveyToModel(entity *SurveyEntity) model.Survey {
	survey := model.Survey{
		ID:                entity.ID.String(),
		Title:             entity.Title,
		Description:       entity.Description,
		EndedAt:           TimePtr(entity.EndedAt),
		IsAnonymous:       entity.IsAnonymous,
		Status:            entity.Status,
		PublishedAt:       TimePtr(entity.PublishedAt),
		NotifySubscribers: entity.NotifySubscribers,
		HideResults:       entity.HideResults,
		PublisherID:       entity.PublisherID.String(),
		Questions:         []model.SurveyQuestion{},
		Attachments:       filejson.References(entity.Attachments),
		CreatedAt:         model.NewTime(entity.CreatedAt),
		UpdatedAt:         model.NewTime(entity.UpdatedAt),
		DeletedAt:         DeletedPtr(entity.DeletedAt),
	}
	return survey
}

// SurveyQuestionToModel maps a question row to the wire type.
func SurveyQuestionToModel(entity *SurveyQuestionEntity) model.SurveyQuestion {
	question := model.SurveyQuestion{
		ID:            entity.ID.String(),
		Type:          entity.Type,
		Title:         entity.Title,
		Description:   entity.Description,
		Order:         entity.Order,
		IsRequired:    entity.IsRequired,
		MaxSelections: entity.MaxSelections,
		MaxLength:     entity.MaxLength,
		MinValue:      entity.MinValue,
		MaxValue:      entity.MaxValue,
		SurveyID:      entity.SurveyID.String(),
		Options:       []model.SurveyOption{},
		Attachments:   filejson.References(entity.Attachments),
		CreatedAt:     model.NewTime(entity.CreatedAt),
		UpdatedAt:     model.NewTime(entity.UpdatedAt),
		DeletedAt:     DeletedPtr(entity.DeletedAt),
	}
	JSONArray(entity.Options, &question.Options)
	return question
}

// StickerToModel maps a sticker row to the wire type.
func StickerToModel(entity *StickerEntity) model.Sticker {
	return model.Sticker{
		ID:                 entity.ID.String(),
		Slug:               entity.Slug,
		Name:               entity.Name,
		Image:              filejson.Value(entity.Image),
		Size:               entity.Size,
		Mode:               entity.Mode,
		Order:              entity.Order,
		PackID:             entity.PackID.String(),
		ResourceIdentifier: "sticker:" + entity.ID.String(),
		CreatedAt:          model.NewTime(entity.CreatedAt),
		UpdatedAt:          model.NewTime(entity.UpdatedAt),
		DeletedAt:          DeletedPtr(entity.DeletedAt),
	}
}

// StickerPackToModel maps a pack row to the wire type.
func StickerPackToModel(entity *StickerPackEntity) model.StickerPack {
	return model.StickerPack{
		ID:          entity.ID.String(),
		Icon:        filejson.Value(entity.Icon),
		Name:        entity.Name,
		Description: entity.Description,
		Prefix:      entity.Prefix,
		Popularity:  entity.Popularity,
		PublisherID: entity.PublisherID.String(),
		CreatedAt:   model.NewTime(entity.CreatedAt),
		UpdatedAt:   model.NewTime(entity.UpdatedAt),
		DeletedAt:   DeletedPtr(entity.DeletedAt),
	}
}

// GetPost loads a post row by id.
func (s *Store) GetPost(ctx context.Context, id uuid.UUID) (*PostEntity, error) {
	var entity PostEntity
	if err := s.DB.WithContext(ctx).First(&entity, "id = ?", id).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &entity, nil
}

// GetPublisher loads a publisher row by id.
func (s *Store) GetPublisher(ctx context.Context, id uuid.UUID) (*PublisherEntity, error) {
	var entity PublisherEntity
	if err := s.DB.WithContext(ctx).First(&entity, "id = ?", id).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &entity, nil
}

// GetPublisherByName loads a publisher row by its unique name.
func (s *Store) GetPublisherByName(ctx context.Context, name string) (*PublisherEntity, error) {
	var entity PublisherEntity
	if err := s.DB.WithContext(ctx).First(&entity, "name = ?", name).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &entity, nil
}
