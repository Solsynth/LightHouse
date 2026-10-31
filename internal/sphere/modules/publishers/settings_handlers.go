package publishers

import (
	"errors"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

func (i *instance) registerSettingsRoutes(api *gin.RouterGroup) {
	api.GET("/account/publishing", authn.RequireAuth(), i.handleGetPublishingSettings)
	api.PATCH("/account/publishing", authn.RequireAuth(), i.handleUpdatePublishingSettings)
}

func publishingSettingsModel(entity *store.PublishingSettingsEntity) publishingSettingsDTO {
	return publishingSettingsDTO{
		PublishingSettings: model.PublishingSettings{
			ID:                          entity.ID.String(),
			AccountID:                   entity.AccountID.String(),
			DefaultPostingPublisherID:   store.UUIDPtrString(entity.DefaultPostingPublisherID),
			DefaultReplyPublisherID:     store.UUIDPtrString(entity.DefaultReplyPublisherID),
			DefaultFediversePublisherID: store.UUIDPtrString(entity.DefaultFediversePublisherID),
			CreatedAt:                   model.NewTime(entity.CreatedAt),
			UpdatedAt:                   store.TimePtr(entity.UpdatedAt),
			DeletedAt:                   store.DeletedPtr(entity.DeletedAt),
		},
	}
}

func (i *instance) getOrCreatePublishingSettings(c *gin.Context, accountID uuid.UUID) (*store.PublishingSettingsEntity, error) {
	var settings store.PublishingSettingsEntity
	err := i.store.DB.WithContext(c.Request.Context()).
		Where("account_id = ?", accountID).
		First(&settings).Error
	if err == nil {
		return &settings, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	settings = store.PublishingSettingsEntity{ID: uuid.New(), AccountID: accountID}
	if err := i.store.DB.WithContext(c.Request.Context()).Create(&settings).Error; err != nil {
		return nil, err
	}
	return &settings, nil
}

func (i *instance) handleGetPublishingSettings(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	settings, err := i.getOrCreatePublishingSettings(c, accountID)
	if err != nil {
		i.internal(c)
		return
	}
	httpx.OK(c, publishingSettingsModel(settings))
}

func (i *instance) handleUpdatePublishingSettings(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	var request publishingSettingsRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	settings, err := i.getOrCreatePublishingSettings(c, accountID)
	if err != nil {
		i.internal(c)
		return
	}
	ctx := c.Request.Context()

	validate := func(raw string) (*store.PublisherEntity, bool) {
		publisherID, err := uuid.Parse(raw)
		if err != nil {
			i.badRequest(c, "PUBLISHER_NOT_FOUND", "Publisher not found")
			return nil, false
		}
		publisher, err := i.store.GetPublisher(ctx, publisherID)
		if err != nil {
			i.badRequest(c, "PUBLISHER_NOT_FOUND", "Publisher not found")
			return nil, false
		}
		member, err := i.memberRow(ctx, publisher.ID, accountID, true)
		if err != nil {
			i.internal(c)
			return nil, false
		}
		if member == nil {
			i.badRequest(c, "PUBLISHER_NOT_A_MEMBER", "You are not a member of this publisher")
			return nil, false
		}
		return publisher, true
	}

	if request.DefaultPostingPublisherID != nil {
		publisher, ok := validate(*request.DefaultPostingPublisherID)
		if !ok {
			return
		}
		settings.DefaultPostingPublisherID = &publisher.ID
	}
	if request.DefaultReplyPublisherID != nil {
		publisher, ok := validate(*request.DefaultReplyPublisherID)
		if !ok {
			return
		}
		settings.DefaultReplyPublisherID = &publisher.ID
	}
	if request.DefaultFediversePublisherID != nil {
		publisher, ok := validate(*request.DefaultFediversePublisherID)
		if !ok {
			return
		}
		if publisher.URI == nil {
			i.badRequest(c, "PUBLISHER_FEDIVERSE_NOT_ENABLED", "Fediverse is not enabled for this publisher")
			return
		}
		settings.DefaultFediversePublisherID = &publisher.ID
	}

	now := time.Now().UTC()
	settings.UpdatedAt = &now
	if err := i.store.DB.WithContext(ctx).Save(settings).Error; err != nil {
		i.internal(c)
		return
	}
	httpx.OK(c, publishingSettingsModel(settings))
}
