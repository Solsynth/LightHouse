package publishers

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

func (i *instance) registerAdminRoutes(api *gin.RouterGroup) {
	group := api.Group("/admin/publishers", authn.RequireAuth())
	group.GET("", i.requirePermission(permModerate), i.handleAdminList)
	group.GET("/:name", i.requirePermission(permModerate), i.handleAdminGet)
	group.PATCH("/:name", i.requirePermission(permUpdate), i.handleAdminUpdate)
	group.POST("/:name/shadowban", i.requirePermission(permModerate), i.handleAdminShadowban)
	group.DELETE("/:name/shadowban", i.requirePermission(permModerate), i.handleAdminUnshadowban)
	group.POST("/:name/verification", i.requirePermission(permModerate), i.handleAdminVerification)
	group.DELETE("/:name/verification", i.requirePermission(permModerate), i.handleAdminClearVerification)
	group.DELETE("/:name", i.requirePermission(permDelete), i.handleAdminDelete)
}

func (i *instance) handleAdminList(c *gin.Context) {
	take, _ := strconv.Atoi(c.DefaultQuery("take", "50"))
	if take < 1 {
		take = 1
	}
	if take > 200 {
		take = 200
	}
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if offset < 0 {
		offset = 0
	}
	query := i.store.DB.WithContext(c.Request.Context()).Model(&store.PublisherEntity{})

	if probe := strings.TrimSpace(c.Query("query")); probe != "" {
		pattern := "%" + probe + "%"
		query = query.Where("lower(name) LIKE ? OR lower(nick) LIKE ? OR (bio IS NOT NULL AND lower(bio) LIKE ?)",
			pattern, pattern, pattern)
	}
	if raw := c.Query("type"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil {
			query = query.Where("type = ?", value)
		}
	}
	if raw := c.Query("accountId"); raw != "" {
		if id, err := uuid.Parse(raw); err == nil {
			query = query.Where("account_id = ?", id)
		}
	}
	if raw := c.Query("shadowbanReason"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil {
			query = query.Where("shadowban_reason = ?", value)
		}
	}
	if raw := c.Query("shadowbanned"); raw != "" {
		if value, err := strconv.ParseBool(raw); err == nil {
			if value {
				query = query.Where("shadowban_reason IS NOT NULL AND shadowban_reason <> 0")
			} else {
				query = query.Where("shadowban_reason IS NULL OR shadowban_reason = 0")
			}
		}
	}
	if raw := c.Query("gatekept"); raw != "" {
		if value, err := strconv.ParseBool(raw); err == nil {
			if value {
				query = query.Where("gatekept_follows = ?", true)
			} else {
				query = query.Where("gatekept_follows IS NULL OR gatekept_follows = ?", false)
			}
		}
	}
	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		i.internal(c)
		return
	}
	var entities []store.PublisherEntity
	if err := query.Order("updated_at DESC").Offset(offset).Limit(take).Find(&entities).Error; err != nil {
		i.internal(c)
		return
	}
	rows := make([]*store.PublisherEntity, 0, len(entities))
	for index := range entities {
		rows = append(rows, &entities[index])
	}
	publishers := i.publisherModels(c.Request.Context(), rows, true, false)
	httpx.List(c, publishers, total)
}

func (i *instance) handleAdminGet(c *gin.Context) {
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	ctx := c.Request.Context()
	mapped := i.publisherModelFor(ctx, publisher, true)
	detail := adminPublisherDetail{Publisher: &mapped}
	var count int64

	if err := i.store.DB.WithContext(ctx).Model(&store.PublisherMemberRow{}).
		Where("publisher_id = ? AND joined_at IS NOT NULL", publisher.ID).
		Count(&count).Error; err != nil {
		i.internal(c)
		return
	}
	detail.MemberCount = int(count)

	if err := i.store.DB.WithContext(ctx).Model(&store.PostEntity{}).
		Where("publisher_id = ?", publisher.ID).Count(&count).Error; err != nil {
		i.internal(c)
		return
	}
	detail.PostCount = int(count)

	if err := i.store.DB.WithContext(ctx).Model(&store.PostCollectionEntity{}).
		Where("publisher_id = ?", publisher.ID).Count(&count).Error; err != nil {
		i.internal(c)
		return
	}
	detail.CollectionCount = int(count)

	if err := i.store.DB.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
		Where("publisher_id = ? AND ended_at IS NULL AND state = ? AND is_blocking = ?",
			publisher.ID, subscriptionAccepted, false).
		Count(&count).Error; err != nil {
		i.internal(c)
		return
	}
	detail.SubscriberCount = int(count)

	httpx.OK(c, detail)
}

func (i *instance) handleAdminUpdate(c *gin.Context) {
	var request adminUpdatePublisherRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if request.Name != nil {
		normalized := strings.TrimSpace(*request.Name)
		if normalized == "" {
			i.badRequest(c, "PUBLISHER_NAME_REQUIRED", "Name cannot be empty.")
			return
		}
		if !strings.EqualFold(normalized, publisher.Name) {
			exists, err := i.store.PublisherNamesTaken(ctx, normalized, publisher.ID)
			if err != nil {
				i.internal(c)
				return
			}
			if exists {
				i.badRequest(c, "PUBLISHER_NAME_EXISTS", "A publisher with this name already exists.")
				return
			}
			publisher.Name = normalized
		}
	}
	if request.Nick != nil {
		publisher.Nick = *request.Nick
	}
	if request.Bio != nil {
		publisher.Bio = request.Bio
	}
	if request.GatekeptFollows != nil {
		publisher.GatekeptFollows = request.GatekeptFollows
	}
	if request.ModerateSubscription != nil {
		publisher.ModerateSubscription = request.ModerateSubscription
	}
	if err := i.store.DB.WithContext(ctx).Save(publisher).Error; err != nil {
		i.internal(c)
		return
	}
	httpx.OK(c, store.PublisherToModel(publisher))
}

func (i *instance) handleAdminShadowban(c *gin.Context) {
	var request setPublisherShadowbanRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	if request.Reason == 0 {
		i.badRequest(c, "PUBLISHER_SHADOWBAN_USE_DELETE", "Use DELETE to clear a shadowban.")
		return
	}
	now := time.Now().UTC()
	reason := request.Reason
	publisher.ShadowbanReason = &reason
	publisher.ShadowbannedAt = &now
	if err := i.store.DB.WithContext(c.Request.Context()).Save(publisher).Error; err != nil {
		i.internal(c)
		return
	}
	httpx.OK(c, store.PublisherToModel(publisher))
}

func (i *instance) handleAdminUnshadowban(c *gin.Context) {
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	reason := 0
	publisher.ShadowbanReason = &reason
	publisher.ShadowbannedAt = nil
	if err := i.store.DB.WithContext(c.Request.Context()).Save(publisher).Error; err != nil {
		i.internal(c)
		return
	}
	httpx.OK(c, store.PublisherToModel(publisher))
}

func (i *instance) handleAdminVerification(c *gin.Context) {
	var request setPublisherVerificationRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	mark := model.VerificationMark{
		Type:        request.Type,
		Title:       request.Title,
		Description: request.Description,
		VerifiedBy:  request.VerifiedBy,
	}
	publisher.Verification = verificationJSON(&mark)
	if err := i.store.DB.WithContext(c.Request.Context()).Save(publisher).Error; err != nil {
		i.internal(c)
		return
	}
	httpx.OK(c, store.PublisherToModel(publisher))
}

func (i *instance) handleAdminClearVerification(c *gin.Context) {
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	publisher.Verification = nil
	if err := i.store.DB.WithContext(c.Request.Context()).Save(publisher).Error; err != nil {
		i.internal(c)
		return
	}
	httpx.OK(c, store.PublisherToModel(publisher))
}

func (i *instance) handleAdminDelete(c *gin.Context) {
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	if err := i.store.DB.WithContext(c.Request.Context()).Delete(publisher).Error; err != nil && !errors.Is(err, store.ErrNotFound) {
		i.internal(c)
		return
	}
	httpx.NoContent(c)
}
