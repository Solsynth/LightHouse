package publishers

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

func (i *instance) handleListFeatures(c *gin.Context) {
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	httpx.OK(c, map[string]bool{
		flagFollowRequiresApproval: i.hasFollowRequiresApproval(c.Request.Context(), publisher),
		flagPostsRequireFollow:     i.hasPostsRequireFollow(c.Request.Context(), publisher),
	})
}

func (i *instance) handleExpectedReward(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	if !i.isMemberWithRole(c.Request.Context(), publisher.ID, accountID, roleViewer) {
		i.forbidden(c, "You are not allowed to view stats data of this publisher.")
		return
	}
	preview, err := i.expectedReward(c.Request.Context(), publisher.ID)
	if err != nil {
		i.internal(c)
		return
	}
	httpx.OK(c, preview)
}

func (i *instance) handleRatingHistory(c *gin.Context) {
	page, ok := httpx.PageQuery(c, i.maxTake())
	if !ok {
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	ctx := c.Request.Context()
	isMember := false
	if accountID, ok := i.accountID(c); ok {
		isMember = i.isMemberWithRole(ctx, publisher.ID, accountID, roleViewer)
	}
	var total int64
	if err := i.store.DB.WithContext(ctx).Model(&store.PublisherRatingRecordEntity{}).
		Where("publisher_id = ?", publisher.ID).Count(&total).Error; err != nil {
		i.internal(c)
		return
	}
	var rows []store.PublisherRatingRecordEntity
	if err := i.store.DB.WithContext(ctx).
		Where("publisher_id = ?", publisher.ID).
		Order("created_at DESC").
		Offset(page.Offset).Limit(page.Take).
		Find(&rows).Error; err != nil {
		i.internal(c)
		return
	}
	records := make([]model.PublisherRatingRecord, 0, len(rows))
	for index := range rows {
		records = append(records, ratingRecordModel(&rows[index]))
	}
	c.Header("X-Total", strconv.FormatInt(total, 10))
	httpx.OK(c, struct {
		IsMember bool                          `json:"is_member"`
		Records  []model.PublisherRatingRecord `json:"records"`
	}{IsMember: isMember, Records: records})
}

func ratingRecordModel(entity *store.PublisherRatingRecordEntity) model.PublisherRatingRecord {
	return model.PublisherRatingRecord{
		ID:          entity.ID.String(),
		ReasonType:  entity.ReasonType,
		Reason:      entity.Reason,
		Delta:       entity.Delta,
		PublisherID: entity.PublisherID.String(),
		CreatedAt:   model.NewTime(entity.CreatedAt),
		UpdatedAt:   model.NewTime(entity.UpdatedAt),
		DeletedAt:   store.DeletedPtr(entity.DeletedAt),
	}
}

func (i *instance) handleAddFeature(c *gin.Context) {
	var request publisherFeatureRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	if strings.TrimSpace(request.Flag) == "" {
		httpx.Fail(c, validationError("flag", "The Flag field is required."))
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if request.Flag == flagDevelop {
		i.badRequest(c, "PUBLISHER_FLAG_SYSTEM_ONLY", "Flag '"+request.Flag+"' is a system flag and cannot be enabled manually")
		return
	}
	if request.Flag == flagFollowRequiresApproval || request.Flag == flagPostsRequireFollow {
		if publisher.AccountID != nil {
			if account := i.getAccount(ctx, publisher.AccountID.String()); account != nil && int(account.GetPerkLevel()) < minPerkLevel {
				i.forbidden(c, "This feature requires PerkLevel >= "+itoa(minPerkLevel))
				return
			}
		}
		enabled := true
		if request.Flag == flagFollowRequiresApproval {
			publisher.ModerateSubscription = &enabled
		} else {
			publisher.GatekeptFollows = &enabled
		}
		if err := i.store.DB.WithContext(ctx).Save(publisher).Error; err != nil {
			i.internal(c)
			return
		}
		httpx.OK(c, model.PublisherFeature{PublisherID: publisher.ID.String(), Flag: request.Flag})
		return
	}
	entity := store.PublisherFeatureEntity{
		ID:          uuid.New(),
		PublisherID: publisher.ID,
		Flag:        request.Flag,
	}
	if request.ExpiredAt != nil {
		expiredAt := request.ExpiredAt.Time()
		entity.ExpiredAt = &expiredAt
	}
	if err := i.store.DB.WithContext(ctx).Create(&entity).Error; err != nil {
		i.internal(c)
		return
	}
	httpx.OK(c, model.PublisherFeature{
		ID:          entity.ID.String(),
		Flag:        entity.Flag,
		ExpiredAt:   store.TimePtr(entity.ExpiredAt),
		PublisherID: entity.PublisherID.String(),
		CreatedAt:   model.NewTime(entity.CreatedAt),
		UpdatedAt:   model.NewTime(entity.UpdatedAt),
	})
}

func (i *instance) handleRemoveFeature(c *gin.Context) {
	flag := c.Query("flag")
	if flag == "" {
		i.badRequest(c, "PUBLISHER_FLAG_REQUIRED", "Flag is required")
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if flag == flagFollowRequiresApproval || flag == flagPostsRequireFollow {
		if flag == flagFollowRequiresApproval {
			publisher.ModerateSubscription = nil
		} else {
			publisher.GatekeptFollows = nil
		}
		if err := i.store.DB.WithContext(ctx).Save(publisher).Error; err != nil {
			i.internal(c)
			return
		}
		httpx.NoContent(c)
		return
	}
	var entity store.PublisherFeatureEntity
	err := i.store.DB.WithContext(ctx).
		Where("publisher_id = ? AND flag = ?", publisher.ID, flag).
		First(&entity).Error
	if err != nil {
		i.notFound(c)
		return
	}
	if err := i.store.DB.WithContext(ctx).Delete(&entity).Error; err != nil {
		i.internal(c)
		return
	}
	httpx.NoContent(c)
}

func (i *instance) handleSettleRewards(c *gin.Context) {
	ctx := c.Request.Context()
	if err := i.settlePublisherRewards(ctx); err != nil {
		i.internal(c)
		return
	}
	if _, err := i.settlePostAwards(ctx); err != nil {
		if errors.Is(err, ports.ErrUnavailable) {
			i.unavailable(c)
			return
		}
		i.internal(c)
		return
	}
	httpx.NoContent(c)
}

func (i *instance) handleSettleAwards(c *gin.Context) {
	if _, err := i.settlePostAwards(c.Request.Context()); err != nil {
		if errors.Is(err, ports.ErrUnavailable) {
			i.unavailable(c)
			return
		}
		i.internal(c)
		return
	}
	httpx.NoContent(c)
}

func (i *instance) handleResettle(c *gin.Context) {
	var request aggressiveResettleRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	if request.DateFrom == nil || request.DateTo == nil {
		httpx.Fail(c, validationError("date_from", "DateFrom and DateTo are required."))
		return
	}
	var publisherID *uuid.UUID
	if request.PublisherID != nil && *request.PublisherID != "" {
		parsed, err := uuid.Parse(*request.PublisherID)
		if err != nil {
			httpx.Fail(c, validationError("publisher_id", "The value is not a valid UUID."))
			return
		}
		publisherID = &parsed
	}
	results, err := i.aggressiveResettle(c.Request.Context(), request.DateFrom.Time(), request.DateTo.Time(), publisherID)
	if err != nil {
		i.internal(c)
		return
	}
	type publisherResult struct {
		PublisherID string  `json:"publisher_id"`
		Delta       float64 `json:"delta"`
	}
	publishers := make([]publisherResult, 0, len(results))
	for id, delta := range results {
		publishers = append(publishers, publisherResult{PublisherID: id.String(), Delta: delta})
	}
	httpx.OK(c, gin.H{"processed": len(results), "publishers": publishers})
}

func (i *instance) handleFediverseStatus(c *gin.Context) {
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	status, err := i.fediverseStatus(c.Request.Context(), publisher)
	if err != nil {
		i.internal(c)
		return
	}
	httpx.OK(c, status)
}

func (i *instance) handleEnableFediverse(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	if !i.isMemberWithRole(c.Request.Context(), publisher.ID, accountID, roleManager) {
		i.forbidden(c, "You need at least be manager to enable fediverse for this publisher.")
		return
	}
	if err := i.enableFediverse(c.Request.Context(), publisher); err != nil {
		i.badRequest(c, "PUBLISHER_FEDIVERSE_ENABLE_FAILED", err.Error())
		return
	}
	status, err := i.fediverseStatus(c.Request.Context(), publisher)
	if err != nil {
		i.internal(c)
		return
	}
	httpx.OK(c, status)
}

func (i *instance) handleUpdateFediverse(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	var request updateFediverseActorRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	if !i.isMemberWithRole(c.Request.Context(), publisher.ID, accountID, roleManager) {
		i.forbidden(c, "You need at least be manager to change fediverse settings for this publisher.")
		return
	}
	if err := i.updateFediverseActor(c.Request.Context(), publisher, request); err != nil {
		i.badRequest(c, "PUBLISHER_FEDIVERSE_UPDATE_FAILED", err.Error())
		return
	}
	status, err := i.fediverseStatus(c.Request.Context(), publisher)
	if err != nil {
		i.internal(c)
		return
	}
	httpx.OK(c, status)
}

func (i *instance) handleDisableFediverse(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	if !i.isMemberWithRole(c.Request.Context(), publisher.ID, accountID, roleManager) {
		i.forbidden(c, "You need at least be manager to disable fediverse for this publisher.")
		return
	}
	if err := i.disableFediverse(c.Request.Context(), publisher); err != nil {
		i.internal(c)
		return
	}
	httpx.NoContent(c)
}

func domainModel(entity *store.PublisherVerifiedDomainEntity) model.PublisherVerifiedDomain {
	return model.PublisherVerifiedDomain{
		ID:             entity.ID.String(),
		PublisherID:    entity.PublisherID.String(),
		Domain:         entity.Domain,
		Status:         entity.Status,
		VerifiedAt:     store.TimePtr(entity.VerifiedAt),
		LastCheckedAt:  store.TimePtr(entity.LastCheckedAt),
		FailedAttempts: entity.FailedAttempts,
		LastError:      entity.LastError,
		CreatedAt:      model.NewTime(entity.CreatedAt),
		UpdatedAt:      model.NewTime(entity.UpdatedAt),
		DeletedAt:      store.DeletedPtr(entity.DeletedAt),
	}
}

func (i *instance) handleListDomains(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	if !i.isMemberWithRole(c.Request.Context(), publisher.ID, accountID, roleEditor) {
		i.forbidden(c, "You need at least be an editor to view domains.")
		return
	}
	var rows []store.PublisherVerifiedDomainEntity
	if err := i.store.DB.WithContext(c.Request.Context()).
		Where("publisher_id = ?", publisher.ID).
		Order("domain ASC").
		Find(&rows).Error; err != nil {
		i.internal(c)
		return
	}
	domains := make([]model.PublisherVerifiedDomain, 0, len(rows))
	for index := range rows {
		domains = append(domains, domainModel(&rows[index]))
	}
	httpx.OK(c, domains)
}

func (i *instance) handleAddDomain(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	var request addDomainRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	if !i.isMemberWithRole(c.Request.Context(), publisher.ID, accountID, roleManager) {
		i.forbidden(c, "You need at least be a manager to add domains.")
		return
	}
	domain := strings.ToLower(strings.TrimSpace(request.Domain))
	if domain == "" {
		i.badRequest(c, "PUBLISHER_DOMAIN_REQUIRED", "Domain is required.")
		return
	}
	var existing int64
	if err := i.store.DB.WithContext(c.Request.Context()).Model(&store.PublisherVerifiedDomainEntity{}).
		Where("publisher_id = ? AND domain = ?", publisher.ID, domain).
		Count(&existing).Error; err != nil {
		i.internal(c)
		return
	}
	if existing > 0 {
		i.conflict(c, "PUBLISHER_DOMAIN_CONFLICT", "This domain already exists for this publisher.")
		return
	}
	record := store.PublisherVerifiedDomainEntity{
		ID:          uuid.New(),
		PublisherID: publisher.ID,
		Domain:      domain,
		Status:      domainPending,
	}
	if err := i.store.DB.WithContext(c.Request.Context()).Create(&record).Error; err != nil {
		i.internal(c)
		return
	}
	publisherID := publisher.ID
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := i.verifyDomain(ctx, publisherID, domain); err != nil {
			i.log.Warn("domain verification failed", "domain", domain, "error", err)
		}
	}()
	httpx.OK(c, domainModel(&record))
}

func (i *instance) handleRemoveDomain(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	if !i.isMemberWithRole(c.Request.Context(), publisher.ID, accountID, roleManager) {
		i.forbidden(c, "You need at least be a manager to remove domains.")
		return
	}
	domainID, err := uuid.Parse(c.Param("domainId"))
	if err != nil {
		i.notFound(c)
		return
	}
	var record store.PublisherVerifiedDomainEntity
	if err := i.store.DB.WithContext(c.Request.Context()).
		Where("id = ? AND publisher_id = ?", domainID, publisher.ID).
		First(&record).Error; err != nil {
		i.notFound(c)
		return
	}
	if err := i.store.DB.WithContext(c.Request.Context()).Delete(&record).Error; err != nil {
		i.internal(c)
		return
	}
	httpx.NoContent(c)
}

func (i *instance) handleRecheckDomain(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	if !i.isMemberWithRole(c.Request.Context(), publisher.ID, accountID, roleManager) {
		i.forbidden(c, "You need at least be a manager to recheck domains.")
		return
	}
	domainID, err := uuid.Parse(c.Param("domainId"))
	if err != nil {
		i.notFound(c)
		return
	}
	var record store.PublisherVerifiedDomainEntity
	if err := i.store.DB.WithContext(c.Request.Context()).
		Where("id = ? AND publisher_id = ?", domainID, publisher.ID).
		First(&record).Error; err != nil {
		i.notFound(c)
		return
	}
	record.Status = domainPending
	if err := i.store.DB.WithContext(c.Request.Context()).Save(&record).Error; err != nil {
		i.internal(c)
		return
	}
	if _, err := i.verifyDomain(c.Request.Context(), publisher.ID, record.Domain); err != nil {
		i.internal(c)
		return
	}
	if err := i.store.DB.WithContext(c.Request.Context()).
		Where("id = ?", record.ID).First(&record).Error; err != nil {
		i.internal(c)
		return
	}
	httpx.OK(c, domainModel(&record))
}

func (i *instance) handleAdvertisingPosts(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	if !i.isMemberWithRole(c.Request.Context(), publisher.ID, accountID, roleViewer) {
		i.forbidden(c, "You need at least be a viewer to view advertising data for this publisher.")
		return
	}
	page, ok := httpx.PageQuery(c, i.maxTake())
	if !ok {
		return
	}
	rows, err := i.store.AdvertisingPostStats(c.Request.Context(), publisher.ID)
	if err != nil {
		i.internal(c)
		return
	}
	total := int64(len(rows))
	start := page.Offset
	if start > len(rows) {
		start = len(rows)
	}
	end := start + page.Take
	if end > len(rows) {
		end = len(rows)
	}
	items := make([]advertisingPostStats, 0, end-start)
	for _, row := range rows[start:end] {
		items = append(items, advertisingPostStats{
			PostID:            row.PostID.String(),
			Title:             row.Title,
			Slug:              row.Slug,
			ActiveBidTotal:    row.ActiveBidTotal,
			BidCount:          row.BidCount,
			IsCurrentlyPlaced: row.IsCurrentlyPlaced,
			ShownCount:        row.ShownCount,
			LastShownAt:       store.TimePtr(row.LastShownAt),
		})
	}
	httpx.List(c, items, total)
}
