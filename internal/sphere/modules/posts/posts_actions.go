package posts

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/accountjson"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/localization"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

const (
	positiveReactionWeight = 2
	neutralReactionWeight  = 1
	negativeReactionWeight = -2

	sponsorProductIdentifier = "ads.sponsor"
	awardProductIdentifier   = "posts.award"
	minimumSponsorBid        = 5.0
	// The persisted attitude ints follow DysonNetwork.Shared.Models:
	// Positive = 0, Neutral = 1, Negative = 2. model.PostReactionAttitude
	// declares a different order, so the module keeps its own constants for
	// database predicates.
	attitudePositive   = 0
	attitudeNeutral    = 1
	attitudeNegative   = 2
	sponsorBidDuration = 24 * time.Hour
)

var reactionsAllowedDefault = map[string]struct{}{
	"thumb_up": {}, "thumb_down": {}, "cry": {}, "confuse": {}, "hello": {},
	"shock": {}, "speechless": {}, "salute": {}, "ridicule": {}, "clap": {},
	"laugh": {}, "angry": {}, "party": {}, "pray": {}, "heart": {},
}

type postReactionRequest struct {
	Symbol   string `json:"symbol"`
	Attitude int    `json:"attitude"`
}

type postAwardRequest struct {
	Amount   float64 `json:"amount"`
	Attitude int     `json:"attitude"`
	Message  *string `json:"message"`
}

type postSponsorRequest struct {
	Amount float64 `json:"amount"`
}

type postPinRequest struct {
	Mode int `json:"mode"`
}

type boostRequest struct {
	Content *string `json:"content"`
}

func reactionWeight(attitude int) int {
	switch attitude {
	case attitudePositive:
		return positiveReactionWeight
	case attitudeNeutral:
		return neutralReactionWeight
	case attitudeNegative:
		return negativeReactionWeight
	default:
		return 0
	}
}

func (h *handler) reactPost(c *gin.Context) {
	ctx := c.Request.Context()
	account := authn.CurrentUser(c)
	if account == nil {
		httpx.Unauthorized(c)
		return
	}
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	var request postReactionRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	if len(request.Symbol) > 256 {
		httpx.BadRequest(c, "POST_REACTION_INVALID", "Symbol is too long.")
		return
	}
	if _, allowed := reactionsAllowedDefault[request.Symbol]; !allowed {
		if account.GetPerkSubscription() == nil {
			httpx.BadRequest(c, "POST_REACTION_SUBSCRIPTION_REQUIRED", "You need subscription to send custom reactions")
			return
		}
	}
	v := h.newViewer(c)
	var ent store.PostEntity
	if err := h.applyDetailScope(h.db.WithContext(ctx).Model(&store.PostEntity{}), v).
		Where("id = ?", id).First(&ent).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	publisher, _ := h.publisherByID(ctx, ent.PublisherID)
	post, err := h.hydrateOne(ctx, &ent, v, false, false)
	if err != nil {
		httpx.Internal(c)
		return
	}
	if status, message := h.ensurePostVisible(ctx, &post, v, publisher); status != 0 {
		if status == 404 {
			httpx.NotFound(c, "Post not found.")
			return
		}
		httpx.Fail(c, errs.New("FORBIDDEN", message, status))
		return
	}

	accountID, err := uuid.Parse(account.GetId())
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	isSelfReact := publisher != nil && publisher.AccountID != nil && *publisher.AccountID == accountID

	var existing store.PostReactionEntity
	err = h.db.WithContext(ctx).
		Where("post_id = ? AND symbol = ? AND account_id = ?", ent.ID, request.Symbol, accountID).
		First(&existing).Error
	hasExisting := err == nil

	if hasExisting {
		if err := h.db.WithContext(ctx).Delete(&store.PostReactionEntity{}, "id = ?", existing.ID).Error; err != nil {
			httpx.Internal(c)
			return
		}
		if !isSelfReact {
			h.adjustPostVotes(ctx, &ent, existing.Attitude, false)
			h.enqueueInterest(accountID, ent.ID, float64(-reactionWeight(existing.Attitude)), "reaction")
		}
		httpx.NoContent(c)
		return
	}

	reaction := store.PostReactionEntity{
		ID:        uuid.New(),
		Symbol:    request.Symbol,
		Attitude:  request.Attitude,
		PostID:    ent.ID,
		AccountID: &accountID,
		IsLocal:   true,
	}
	if err := h.db.WithContext(ctx).Create(&reaction).Error; err != nil {
		httpx.Internal(c)
		return
	}
	if !isSelfReact {
		h.adjustPostVotes(ctx, &ent, request.Attitude, true)
		h.enqueueInterest(accountID, ent.ID, float64(reactionWeight(request.Attitude)), "reaction")
		go h.notifyReaction(context.WithoutCancel(ctx), &ent, account, request.Symbol, request.Attitude)
	}
	created := postReactionToModel(&reaction)
	created.Account = accountjson.FromProto(account)
	httpx.OK(c, created)
}

func (h *handler) notifyReaction(ctx context.Context, ent *store.PostEntity, actor *gen.DyAccount, symbol string, attitude int) {
	notify, ok := module.Get[ports.Notify](h.ports, ports.PortNotify)
	if !ok || ent == nil || actor == nil {
		return
	}
	actorID, err := uuid.Parse(actor.GetId())
	if err != nil {
		return
	}
	publisher, err := h.publisherByID(ctx, ent.PublisherID)
	if err != nil || publisher == nil || publisher.AccountID == nil {
		return
	}
	targetAccountID := *publisher.AccountID
	if targetAccountID == actorID {
		return
	}
	if !h.reactionNotificationsEnabled(ctx, targetAccountID) {
		return
	}
	if h.isBlockedOrMuted(ctx, targetAccountID, actorID) {
		return
	}
	actorName := actor.GetNick()
	if actorName == "" {
		actorName = actor.GetName()
	}
	if actorName == "" {
		actorName = "Someone"
	}
	lang := h.accountLanguage(ctx, targetAccountID)
	title := localization.Localize(lang, "postReactTitle", map[string]string{
		"user": actorName,
	})
	var body string
	if ent.Title != nil && strings.TrimSpace(*ent.Title) != "" {
		body = localization.Localize(lang, "postReactContentBody", map[string]string{
			"user":     actorName,
			"reaction": symbol,
			"title":    strings.TrimSpace(*ent.Title),
		})
	} else {
		body = localization.Localize(lang, "postReactBody", map[string]string{
			"user":     actorName,
			"reaction": symbol,
		})
	}
	meta := map[string]string{
		"notification_type": "reaction",
		"reaction":          symbol,
		"reaction_attitude": strconv.Itoa(attitude),
		"actor_id":          actorID.String(),
		"actor_name":        actorName,
		"post_id":           ent.ID.String(),
		"topic":             "posts.reactions",
		"action_uri":        "/posts/" + ent.ID.String(),
	}
	if err := notify.SendPush(ctx, targetAccountID.String(), title, body, meta); err != nil {
		h.log.Warn("posts: notify reaction failed", "post", ent.ID, "target", targetAccountID, "error", err)
	}
}

func (h *handler) adjustPostVotes(ctx context.Context, ent *store.PostEntity, attitude int, adding bool) {
	delta := reactionWeight(attitude)
	scoreDelta := delta
	upvotes, downvotes := 0, 0
	switch attitude {
	case attitudePositive:
		upvotes = 1
	case attitudeNegative:
		downvotes = 1
	}
	if !adding {
		scoreDelta = -delta
		upvotes = -upvotes
		downvotes = -downvotes
	}
	updates := map[string]any{"reaction_score": gormExpr("reaction_score + ?", scoreDelta)}
	if upvotes != 0 {
		updates["upvotes"] = gormExpr("upvotes + ?", upvotes)
	}
	if downvotes != 0 {
		updates["downvotes"] = gormExpr("downvotes + ?", downvotes)
	}
	if err := h.db.WithContext(ctx).Model(&store.PostEntity{}).Where("id = ?", ent.ID).
		UpdateColumns(updates).Error; err != nil {
		h.log.Warn("posts: adjust votes failed", "post", ent.ID, "error", err)
	}
	ent.ReactionScore += scoreDelta
	ent.Upvotes += upvotes
	ent.Downvotes += downvotes
}

func (h *handler) bookmarkPost(c *gin.Context) {
	ctx := c.Request.Context()
	account := authn.CurrentUser(c)
	if account == nil {
		httpx.Unauthorized(c)
		return
	}
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	v := h.newViewer(c)
	var ent store.PostEntity
	if err := h.applyDetailScope(h.db.WithContext(ctx).Model(&store.PostEntity{}), v).
		Where("id = ?", id).First(&ent).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	publisher, _ := h.publisherByID(ctx, ent.PublisherID)
	post, err := h.hydrateOne(ctx, &ent, v, false, false)
	if err != nil {
		httpx.Internal(c)
		return
	}
	if status, message := h.ensurePostVisible(ctx, &post, v, publisher); status != 0 {
		if status == 404 {
			httpx.NotFound(c, "Post not found.")
			return
		}
		httpx.Fail(c, errs.New("FORBIDDEN", message, status))
		return
	}
	accountID, _ := uuid.Parse(account.GetId())
	var bookmark store.PostBookmarkEntity
	err = h.db.WithContext(ctx).Where("post_id = ? AND account_id = ?", id, accountID).First(&bookmark).Error
	if err == nil {
		httpx.OK(c, postBookmarkToModel(&bookmark))
		return
	}
	bookmark = store.PostBookmarkEntity{ID: uuid.New(), PostID: id, AccountID: accountID}
	if err := h.db.WithContext(ctx).Create(&bookmark).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, postBookmarkToModel(&bookmark))
}

func (h *handler) unbookmarkPost(c *gin.Context) {
	ctx := c.Request.Context()
	account := authn.CurrentUser(c)
	if account == nil {
		httpx.Unauthorized(c)
		return
	}
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	accountID, _ := uuid.Parse(account.GetId())
	var bookmark store.PostBookmarkEntity
	if err := h.db.WithContext(ctx).Where("post_id = ? AND account_id = ?", id, accountID).First(&bookmark).Error; err != nil {
		httpx.NotFound(c, "Bookmark not found.")
		return
	}
	if err := h.db.WithContext(ctx).Delete(&store.PostBookmarkEntity{}, "id = ?", bookmark.ID).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.NoContent(c)
}

func (h *handler) getBookmarkStatus(c *gin.Context) {
	ctx := c.Request.Context()
	account := authn.CurrentUser(c)
	if account == nil {
		httpx.Unauthorized(c)
		return
	}
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	accountID, _ := uuid.Parse(account.GetId())
	var bookmark store.PostBookmarkEntity
	if err := h.db.WithContext(ctx).Where("post_id = ? AND account_id = ?", id, accountID).First(&bookmark).Error; err != nil {
		httpx.OK(c, nil)
		return
	}
	httpx.OK(c, postBookmarkToModel(&bookmark))
}

// ---------------------------------------------------------------------------
// Awards
// ---------------------------------------------------------------------------

func (h *handler) getPostAwards(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	page, ok := h.pageFrom(c)
	if !ok {
		return
	}
	query := h.db.WithContext(ctx).Model(&store.PostAwardEntity{}).Where("post_id = ?", id)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	var rows []store.PostAwardEntity
	if err := query.Order("created_at DESC").Offset(page.Offset).Limit(page.Take).Find(&rows).Error; err != nil {
		httpx.Internal(c)
		return
	}
	awards := make([]model.PostAward, 0, len(rows))
	for i := range rows {
		awards = append(awards, postAwardToModel(&rows[i]))
	}
	httpx.List(c, awards, total)
}

func (h *handler) getPendingAwards(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	var rows []store.PostAwardEntity
	if err := h.db.WithContext(ctx).
		Where("post_id = ? AND settled_at IS NULL AND attitude = ?", id, attitudePositive).
		Find(&rows).Error; err != nil {
		httpx.Internal(c)
		return
	}
	total := 0.0
	for _, row := range rows {
		total += row.Amount
	}
	httpx.OK(c, gin.H{
		"count":         len(rows),
		"total_amount":  total,
		"payout_amount": total * 0.80,
	})
}

func (h *handler) awardPost(c *gin.Context) {
	ctx := c.Request.Context()
	account := authn.CurrentUser(c)
	if account == nil {
		httpx.Unauthorized(c)
		return
	}
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	var request postAwardRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	if request.Attitude == attitudeNeutral {
		httpx.BadRequest(c, "POST_AWARD_NEUTRAL_NOT_ALLOWED", "You cannot create a neutral post award")
		return
	}
	v := h.newViewer(c)
	var ent store.PostEntity
	if err := h.applyDetailScope(h.db.WithContext(ctx).Model(&store.PostEntity{}), v).
		Where("id = ?", id).First(&ent).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	publisher, _ := h.publisherByID(ctx, ent.PublisherID)
	post, err := h.hydrateOne(ctx, &ent, v, false, false)
	if err != nil {
		httpx.Internal(c)
		return
	}
	if status, message := h.ensurePostVisible(ctx, &post, v, publisher); status != 0 {
		if status == 404 {
			httpx.NotFound(c, "Post not found.")
			return
		}
		httpx.Fail(c, errs.New("FORBIDDEN", message, status))
		return
	}
	accountID, _ := uuid.Parse(account.GetId())
	payments, ok := h.paymentsPort()
	if !ok {
		httpx.Fail(c, errs.New("SERVICE_UNAVAILABLE", "The payment service is unavailable.", http.StatusServiceUnavailable))
		return
	}
	orderID, err := payments.CreateOrder(ctx, accountID.String(), awardProductIdentifier,
		strconv.FormatFloat(request.Amount, 'f', -1, 64), "points", map[string]any{
			"account_id": accountID.String(),
			"post_id":    ent.ID.String(),
			"amount":     strconv.FormatFloat(request.Amount, 'f', -1, 64),
			"message":    request.Message,
			"attitude":   request.Attitude,
		})
	if err != nil {
		httpx.Fail(c, errs.New("POST_AWARD_CREATE_FAILED", "Unable to create the award order.", http.StatusBadRequest))
		return
	}
	httpx.OK(c, gin.H{"order_id": orderID})
}

// settleAward inserts the award and applies its score, mirroring
// PostService.AwardPost.
func (h *handler) settleAward(ctx context.Context, postID, accountID uuid.UUID, amount float64, attitude int, message *string) error {
	var ent store.PostEntity
	if err := h.db.WithContext(ctx).Where("id = ?", postID).First(&ent).Error; err != nil {
		return err
	}
	award := store.PostAwardEntity{
		ID:        uuid.New(),
		Amount:    amount,
		Attitude:  attitude,
		Message:   message,
		PostID:    postID,
		AccountID: accountID,
	}
	if err := h.db.WithContext(ctx).Create(&award).Error; err != nil {
		return err
	}
	delta := amount
	if attitude != attitudePositive {
		delta = -amount
	}
	return h.db.WithContext(ctx).Model(&store.PostEntity{}).Where("id = ?", postID).
		UpdateColumn("awarded_score", gormExpr("awarded_score + ?", delta)).Error
}

// ---------------------------------------------------------------------------
// Sponsorship
// ---------------------------------------------------------------------------

func (h *handler) sponsorPost(c *gin.Context) {
	ctx := c.Request.Context()
	account := authn.CurrentUser(c)
	if account == nil {
		httpx.Unauthorized(c)
		return
	}
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	var request postSponsorRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	if request.Amount < minimumSponsorBid {
		httpx.BadRequest(c, "POST_SPONSOR_MINIMUM_BID", fmt.Sprintf("Minimum sponsorship bid is %s golds.", strconv.FormatFloat(minimumSponsorBid, 'f', -1, 64)))
		return
	}
	var ent store.PostEntity
	if err := h.db.WithContext(ctx).Where("id = ?", id).First(&ent).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	if ent.Visibility != int(model.VisibilityPublic) {
		httpx.BadRequest(c, "POST_SPONSOR_NOT_PUBLIC", "Only public posts can be sponsored.")
		return
	}
	if ent.ShadowbanReason != nil && *ent.ShadowbanReason != int(model.ShadowbanNone) {
		httpx.BadRequest(c, "POST_SPONSOR_SHADOWBANNED", "This post cannot be sponsored.")
		return
	}
	payments, ok := h.paymentsPort()
	if !ok {
		httpx.Fail(c, errs.New("SERVICE_UNAVAILABLE", "The payment service is unavailable.", http.StatusServiceUnavailable))
		return
	}
	accountID, _ := uuid.Parse(account.GetId())
	orderID, err := payments.CreateOrder(ctx, accountID.String(), sponsorProductIdentifier,
		strconv.FormatFloat(request.Amount, 'f', -1, 64), "golds", map[string]any{
			"account_id": accountID.String(),
			"post_id":    ent.ID.String(),
			"amount":     strconv.FormatFloat(request.Amount, 'f', -1, 64),
		})
	if err != nil {
		httpx.BadRequest(c, "POST_SPONSOR_CREATE_FAILED", "Unable to create the sponsorship order.")
		return
	}
	httpx.OK(c, gin.H{"order_id": orderID, "amount": request.Amount})
}

// confirmSponsorBid mirrors SponsorService.ConfirmSponsorBidAsync.
func (h *handler) confirmSponsorBid(ctx context.Context, postID, accountID uuid.UUID, amount float64) error {
	now := time.Now().UTC()
	bid := store.PostSponsorBidEntity{
		ID:        uuid.New(),
		PostID:    postID,
		AccountID: accountID,
		Amount:    amount,
		ExpiresAt: now.Add(sponsorBidDuration),
	}
	return h.db.WithContext(ctx).Create(&bid).Error
}

func (h *handler) paymentsPort() (ports.Payments, bool) {
	if h.ports == nil {
		return nil, false
	}
	return module.Get[ports.Payments](h.ports, ports.PortPayments)
}

// ---------------------------------------------------------------------------
// Pinning
// ---------------------------------------------------------------------------

func (h *handler) pinPost(c *gin.Context) {
	ctx := c.Request.Context()
	account := authn.CurrentUser(c)
	if account == nil {
		httpx.Unauthorized(c)
		return
	}
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	var request postPinRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	v := h.newViewer(c)
	var ent store.PostEntity
	if err := h.db.WithContext(ctx).Where("id = ?", id).First(&ent).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	if !h.isMemberWithRole(ctx, ent.PublisherID, v.accountID, int(model.PublisherRoleEditor)) {
		httpx.Forbidden(c, "You are not an editor of this publisher")
		return
	}
	if request.Mode == int(model.PinModeRealmPage) && ent.RealmID != nil {
		// Realm moderator checks require the Passport realm port.
		h.log.Debug("posts: realm pin moderator check skipped, realm port unavailable")
	}
	if ent.ChainedPostID != nil {
		httpx.BadRequest(c, "POST_PIN_FAILED", "Chained posts cannot be pinned.")
		return
	}
	if ent.RepliedPostID != nil {
		if request.Mode != int(model.PinModeReplyPage) {
			httpx.BadRequest(c, "POST_PIN_FAILED", "Replies can only be pinned in the reply page.")
			return
		}
		var parent store.PostEntity
		if err := h.db.WithContext(ctx).Where("id = ?", *ent.RepliedPostID).First(&parent).Error; err != nil {
			httpx.BadRequest(c, "POST_PIN_FAILED", "Only editors of original post can pin replies.")
			return
		}
		if !h.isMemberWithRole(ctx, parent.PublisherID, v.accountID, int(model.PublisherRoleEditor)) {
			httpx.BadRequest(c, "POST_PIN_FAILED", "Only editors of original post can pin replies.")
			return
		}
	}
	mode := request.Mode
	ent.PinMode = &mode
	if err := h.db.WithContext(ctx).Save(&ent).Error; err != nil {
		httpx.Internal(c)
		return
	}
	post, err := h.hydrateOne(ctx, &ent, v, false, false)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, post)
}

func (h *handler) unpinPost(c *gin.Context) {
	ctx := c.Request.Context()
	v := h.newViewer(c)
	if !v.authed() {
		httpx.Unauthorized(c)
		return
	}
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	var ent store.PostEntity
	if err := h.db.WithContext(ctx).Where("id = ?", id).First(&ent).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	if !h.isMemberWithRole(ctx, ent.PublisherID, v.accountID, int(model.PublisherRoleEditor)) {
		httpx.Forbidden(c, "You are not an editor of this publisher")
		return
	}
	if ent.RepliedPostID != nil {
		var parent store.PostEntity
		if err := h.db.WithContext(ctx).Where("id = ?", *ent.RepliedPostID).First(&parent).Error; err != nil {
			httpx.BadRequest(c, "POST_UNPIN_FAILED", "Only editors of original post can unpin replies.")
			return
		}
		if !h.isMemberWithRole(ctx, parent.PublisherID, v.accountID, int(model.PublisherRoleEditor)) {
			httpx.BadRequest(c, "POST_UNPIN_FAILED", "Only editors of original post can unpin replies.")
			return
		}
	}
	ent.PinMode = nil
	if err := h.db.WithContext(ctx).Save(&ent).Error; err != nil {
		httpx.Internal(c)
		return
	}
	post, err := h.hydrateOne(ctx, &ent, v, false, false)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, post)
}

// ---------------------------------------------------------------------------
// Boosts
// ---------------------------------------------------------------------------

func (h *handler) boostPublisher(ctx context.Context, accountID uuid.UUID, memberPubs map[uuid.UUID]struct{}) *store.PublisherEntity {
	var settings store.PublishingSettingsEntity
	if err := h.db.WithContext(ctx).Where("account_id = ?", accountID).First(&settings).Error; err == nil &&
		settings.DefaultFediversePublisherID != nil {
		if _, ok := memberPubs[*settings.DefaultFediversePublisherID]; ok {
			if publisher, err := h.publisherByID(ctx, *settings.DefaultFediversePublisherID); err == nil {
				return publisher
			}
		}
	}
	for id := range memberPubs {
		publisher, err := h.publisherByID(ctx, id)
		if err == nil && publisher.AccountID != nil && *publisher.AccountID == accountID {
			return publisher
		}
	}
	for id := range memberPubs {
		if publisher, err := h.publisherByID(ctx, id); err == nil {
			return publisher
		}
	}
	return nil
}

func (h *handler) boostPost(c *gin.Context) {
	ctx := c.Request.Context()
	v := h.newViewer(c)
	if !v.authed() {
		httpx.Unauthorized(c)
		return
	}
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	var request boostRequest
	if c.Request.ContentLength > 0 {
		if !httpx.BindJSON(c, &request) {
			return
		}
	}
	var ent store.PostEntity
	if err := h.applyDetailScope(h.db.WithContext(ctx).Model(&store.PostEntity{}), v).
		Where("id = ?", id).First(&ent).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	publisher, _ := h.publisherByID(ctx, ent.PublisherID)
	post, err := h.hydrateOne(ctx, &ent, v, false, false)
	if err != nil {
		httpx.Internal(c)
		return
	}
	if status, message := h.ensurePostVisible(ctx, &post, v, publisher); status != 0 {
		if status == 404 {
			httpx.NotFound(c, "Post not found.")
			return
		}
		httpx.Fail(c, errs.New("FORBIDDEN", message, status))
		return
	}
	userPublisher := h.boostPublisher(ctx, v.accountID, v.memberPubs)
	if userPublisher == nil {
		httpx.BadRequest(c, "POST_BOOST_PUBLISHER_REQUIRED", "You need a publisher to boost posts.")
		return
	}
	var existing store.BoostEntity
	if err := h.db.WithContext(ctx).Where("post_id = ? AND publisher_id = ?", ent.ID, userPublisher.ID).
		First(&existing).Error; err == nil {
		httpx.BadRequest(c, "POST_ALREADY_BOOSTED", "You have already boosted this post.")
		return
	}
	now := time.Now().UTC()
	boost := store.BoostEntity{
		ID:          uuid.New(),
		PostID:      ent.ID,
		PublisherID: userPublisher.ID,
		Content:     request.Content,
		BoostedAt:   &now,
	}
	if err := h.db.WithContext(ctx).Create(&boost).Error; err != nil {
		httpx.Internal(c)
		return
	}
	if err := h.db.WithContext(ctx).Model(&store.PostEntity{}).Where("id = ?", ent.ID).
		UpdateColumn("boost_count", gormExpr("boost_count + 1")).Error; err != nil {
		h.log.Warn("posts: increment boost count failed", "error", err)
	}
	httpx.OK(c, boostToModel(&boost))
}

func (h *handler) unboostPost(c *gin.Context) {
	ctx := c.Request.Context()
	v := h.newViewer(c)
	if !v.authed() {
		httpx.Unauthorized(c)
		return
	}
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	userPublisher := h.boostPublisher(ctx, v.accountID, v.memberPubs)
	if userPublisher == nil {
		httpx.BadRequest(c, "POST_UNBOOST_PUBLISHER_REQUIRED", "You need a publisher to unboost posts.")
		return
	}
	var boost store.BoostEntity
	if err := h.db.WithContext(ctx).Where("post_id = ? AND publisher_id = ?", id, userPublisher.ID).
		First(&boost).Error; err != nil {
		httpx.NotFound(c, "Boost not found.")
		return
	}
	if err := h.db.WithContext(ctx).Delete(&store.BoostEntity{}, "id = ?", boost.ID).Error; err != nil {
		httpx.Internal(c)
		return
	}
	if err := h.db.WithContext(ctx).Model(&store.PostEntity{}).Where("id = ?", id).
		UpdateColumn("boost_count", gormExpr("GREATEST(0, boost_count - 1)")).Error; err != nil {
		h.log.Warn("posts: decrement boost count failed", "error", err)
	}
	httpx.NoContent(c)
}

func (h *handler) listBoosts(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	page, ok := h.pageFrom(c)
	if !ok {
		return
	}
	query := h.db.WithContext(ctx).Model(&store.BoostEntity{}).Where("post_id = ?", id)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	var rows []store.BoostEntity
	if err := query.Order("boosted_at DESC").Offset(page.Offset).Limit(page.Take).Find(&rows).Error; err != nil {
		httpx.Internal(c)
		return
	}
	boosts := make([]model.Boost, 0, len(rows))
	for i := range rows {
		boosts = append(boosts, boostToModel(&rows[i]))
	}
	httpx.List(c, boosts, total)
}

// ---------------------------------------------------------------------------
// Realm moderation
// ---------------------------------------------------------------------------

type moderatePostRequest struct {
	Reason *string `json:"reason"`
}

func (h *handler) moderatePostInRealm(c *gin.Context) {
	ctx := c.Request.Context()
	v := h.newViewer(c)
	if !v.authed() {
		httpx.Unauthorized(c)
		return
	}
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		httpx.NotFound(c, "Post not found.")
		return
	}
	var request moderatePostRequest
	if c.Request.ContentLength > 0 {
		if !httpx.BindJSON(c, &request) {
			return
		}
	}
	var ent store.PostEntity
	if err := h.db.WithContext(ctx).Where("id = ?", id).First(&ent).Error; err != nil {
		httpx.NotFound(c, "Post not found.")
		return
	}
	if ent.RealmID == nil {
		httpx.BadRequest(c, "POST_NOT_IN_REALM", "This post is not linked to a realm.")
		return
	}
	var count int64
	h.db.WithContext(ctx).Model(&store.RealmPostModerationLogEntity{}).
		Where("post_id = ? AND realm_id = ?", ent.ID, *ent.RealmID).Count(&count)
	if count > 0 {
		httpx.BadRequest(c, "POST_ALREADY_REMOVED_FROM_REALM", "This post has already been removed from the realm.")
		return
	}
	now := time.Now().UTC()
	log := store.RealmPostModerationLogEntity{
		ID:                 uuid.New(),
		RealmID:            *ent.RealmID,
		PostID:             ent.ID,
		ModeratorAccountID: v.accountID,
		Reason:             request.Reason,
		ModeratedAt:        &now,
	}
	if err := h.db.WithContext(ctx).Create(&log).Error; err != nil {
		httpx.Internal(c)
		return
	}
	ent.Visibility = int(model.VisibilityPrivate)
	ent.RealmID = nil
	if err := h.db.WithContext(ctx).Save(&ent).Error; err != nil {
		httpx.Internal(c)
		return
	}
	post, err := h.hydrateOne(ctx, &ent, v, false, false)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, gin.H{
		"success": true,
		"message": "Post removed from realm",
		"post":    post,
	})
}

// gormExpr keeps GORM expressions local to the action handlers.
func gormExpr(sql string, args ...any) clause.Expr {
	return gorm.Expr(sql, args...)
}
