package wallet

// Gift HTTP surface. Ported from DysonNetwork.Wallet's
// Payment/SubscriptionGiftController.cs (mounted at /api/subscriptions/gifts).

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/permission"
)

// GiftCheckResponse mirrors GiftCheckResponse.
type GiftCheckResponse struct {
	GiftCode               string  `json:"gift_code"`
	SubscriptionIdentifier string  `json:"subscription_identifier"`
	CanRedeem              bool    `json:"can_redeem"`
	Error                  string  `json:"error"`
	Message                *string `json:"message"`
}

// purchaseGiftRequest mirrors PurchaseGiftRequest.
type purchaseGiftRequest struct {
	SubscriptionIdentifier   string         `json:"subscription_identifier"`
	RecipientID              *uuid.UUID     `json:"recipient_id"`
	PaymentMethod            string         `json:"payment_method"`
	PaymentDetails           PaymentDetails `json:"payment_details"`
	Message                  *string        `json:"message"`
	Coupon                   *string        `json:"coupon"`
	GiftDurationDays         *int           `json:"gift_duration_days"`
	SubscriptionDurationDays *int           `json:"subscription_duration_days"`
}

// redeemGiftRequest mirrors RedeemGiftRequest.
type redeemGiftRequest struct {
	GiftCode string `json:"gift_code"`
}

// RedeemGiftResponse mirrors RedeemGiftResponse.
type RedeemGiftResponse struct {
	Gift         Gift         `json:"gift"`
	Subscription Subscription `json:"subscription"`
}

// registerGiftHTTP mounts SubscriptionGiftController's routes.
func registerGiftHTTP(api *gin.RouterGroup, d *Deps) {
	if api == nil || d == nil || d.Service == nil {
		return
	}
	subs := NewSubscriptions(d.Service)
	subs.Configure(d.Settings, d.Log)

	group := api.Group("/subscriptions/gifts", middleware.RequireAuth())
	group.GET("/sent", subs.listSentGiftsHTTP)
	group.GET("/received", subs.listReceivedGiftsHTTP)
	group.GET("/check/:giftCode", subs.checkGiftCodeHTTP)
	group.GET("/:giftId", subs.getGiftHTTP)
	group.POST("/purchase",
		requireWalletPermission(d, permission.SubscriptionGiftsPurchase),
		subs.purchaseGiftHTTP)
	group.POST("/redeem",
		requireWalletPermission(d, permission.SubscriptionGiftsRedeem),
		subs.redeemGiftHTTP)
	group.POST("/:giftId/send",
		requireWalletPermission(d, permission.SubscriptionGiftsSend),
		subs.sendGiftHTTP)
	group.POST("/:giftId/cancel",
		requireWalletPermission(d, permission.SubscriptionGiftsCancel),
		subs.cancelGiftHTTP)
	group.POST("/:giftId/order",
		requireWalletPermission(d, permission.SubscriptionsOrderManage),
		subs.createGiftOrderHTTP)
}

// giftWires converts gifts to their wire shape with coupons hydrated.
func (s *Subscriptions) giftWires(ctx context.Context, gifts []WalletGiftEntity) ([]Gift, error) {
	ids := make([]uuid.UUID, 0, len(gifts))
	seen := map[uuid.UUID]struct{}{}
	for i := range gifts {
		if gifts[i].CouponID == nil {
			continue
		}
		if _, ok := seen[*gifts[i].CouponID]; ok {
			continue
		}
		seen[*gifts[i].CouponID] = struct{}{}
		ids = append(ids, *gifts[i].CouponID)
	}
	coupons := map[uuid.UUID]*WalletCouponEntity{}
	if len(ids) > 0 {
		var rows []WalletCouponEntity
		if err := s.db(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
			return nil, err
		}
		for i := range rows {
			coupons[rows[i].ID] = &rows[i]
		}
	}
	out := make([]Gift, 0, len(gifts))
	for i := range gifts {
		entity := &gifts[i]
		wire := giftModel(entity)
		if entity.CouponID != nil {
			if coupon, ok := coupons[*entity.CouponID]; ok {
				couponWire := couponModel(coupon)
				wire.Coupon = &couponWire
			}
		}
		out = append(out, wire)
	}
	return out, nil
}

// GET /api/subscriptions/gifts/sent
func (s *Subscriptions) listSentGiftsHTTP(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	gifts, err := s.GetGiftsByGifter(c.Request.Context(), accountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load gifts.", http.StatusInternalServerError))
		return
	}
	offset := maxInt(queryInt(c, "offset", 0), 0)
	take := queryInt(c, "take", 20)
	setTotal(c, int64(len(gifts)))
	page := paginateGifts(gifts, offset, take)
	wires, err := s.giftWires(c.Request.Context(), page)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load gifts.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, wires)
}

// GET /api/subscriptions/gifts/received
func (s *Subscriptions) listReceivedGiftsHTTP(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	gifts, err := s.GetGiftsByRecipient(c.Request.Context(), accountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load gifts.", http.StatusInternalServerError))
		return
	}
	offset := maxInt(queryInt(c, "offset", 0), 0)
	take := queryInt(c, "take", 20)
	setTotal(c, int64(len(gifts)))
	page := paginateGifts(gifts, offset, take)
	wires, err := s.giftWires(c.Request.Context(), page)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load gifts.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, wires)
}

func paginateGifts(gifts []WalletGiftEntity, offset, take int) []WalletGiftEntity {
	if take <= 0 {
		take = 20
	}
	if offset >= len(gifts) {
		return []WalletGiftEntity{}
	}
	end := offset + take
	if end > len(gifts) {
		end = len(gifts)
	}
	return gifts[offset:end]
}

// GET /api/subscriptions/gifts/:giftId
func (s *Subscriptions) getGiftHTTP(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	giftID, err := uuid.Parse(c.Param("giftId"))
	if err != nil {
		c.JSON(http.StatusNotFound, errs.New("WALLET_GIFT_NOT_FOUND", "Gift was not found.", http.StatusNotFound))
		return
	}
	gift, err := s.GetGift(c.Request.Context(), giftID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the gift.", http.StatusInternalServerError))
		return
	}
	accessible := gift != nil && (gift.GifterID == accountID ||
		(gift.RecipientID != nil && *gift.RecipientID == accountID) ||
		(gift.IsOpenGift && gift.RedeemerID != nil && *gift.RedeemerID == accountID))
	if !accessible {
		c.JSON(http.StatusNotFound, errs.New("WALLET_GIFT_NOT_FOUND", "Gift was not found or you do not have access.", http.StatusNotFound))
		return
	}
	wires, err := s.giftWires(c.Request.Context(), []WalletGiftEntity{*gift})
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the gift.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, wires[0])
}

// GET /api/subscriptions/gifts/check/:giftCode
func (s *Subscriptions) checkGiftCodeHTTP(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	giftCode := c.Param("giftCode")
	gift, err := s.GetGiftByCode(c.Request.Context(), giftCode)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the gift.", http.StatusInternalServerError))
		return
	}
	if gift == nil {
		c.JSON(http.StatusNotFound, errs.New("WALLET_GIFT_NOT_FOUND", "Gift code not found.", http.StatusNotFound))
		return
	}
	canRedeem := false
	errorMessage := ""
	switch {
	case gift.Status != GiftSent:
		switch gift.Status {
		case GiftCreated:
			errorMessage = "Gift has not been sent yet."
		case GiftRedeemed:
			errorMessage = "Gift has already been redeemed."
		case GiftExpired:
			errorMessage = "Gift has expired."
		case GiftCancelled:
			errorMessage = "Gift has been cancelled."
		default:
			errorMessage = "Gift is not redeemable."
		}
	case time.Now().UTC().After(gift.ExpiresAt):
		errorMessage = "Gift has expired."
	case !gift.IsOpenGift && (gift.RecipientID == nil || *gift.RecipientID != accountID):
		errorMessage = "This gift is intended for someone else."
	default:
		canRedeem = true
	}
	c.JSON(http.StatusOK, GiftCheckResponse{
		GiftCode:               giftCode,
		SubscriptionIdentifier: gift.SubscriptionIdentifier,
		CanRedeem:              canRedeem,
		Error:                  errorMessage,
		Message:                gift.Message,
	})
}

// POST /api/subscriptions/gifts/purchase
func (s *Subscriptions) purchaseGiftHTTP(c *gin.Context) {
	user, _, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	var request purchaseGiftRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
			"subscription_identifier": {"The request body is invalid."},
		}))
		return
	}
	errorsByField := map[string][]string{}
	if strings.TrimSpace(request.SubscriptionIdentifier) == "" {
		errorsByField["subscription_identifier"] = []string{"The SubscriptionIdentifier field is required."}
	}
	if strings.TrimSpace(request.PaymentMethod) == "" {
		errorsByField["payment_method"] = []string{"The PaymentMethod field is required."}
	}
	if len(errorsByField) > 0 {
		c.JSON(http.StatusBadRequest, errs.Validation(errorsByField))
		return
	}
	giftDays := 30
	if request.GiftDurationDays != nil {
		giftDays = *request.GiftDurationDays
	}
	subscriptionDays := 30
	if request.SubscriptionDurationDays != nil {
		subscriptionDays = *request.SubscriptionDurationDays
	}
	giftDuration := time.Duration(giftDays) * 24 * time.Hour
	cycleDuration := time.Duration(subscriptionDays) * 24 * time.Hour
	gift, err := s.PurchaseGift(c.Request.Context(), user, request.RecipientID, request.SubscriptionIdentifier,
		request.PaymentMethod, request.PaymentDetails, request.Message, request.Coupon, &giftDuration, &cycleDuration)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_GIFT_PURCHASE_FAILED", err.Error(), http.StatusBadRequest))
		return
	}
	wires, err := s.giftWires(c.Request.Context(), []WalletGiftEntity{*gift})
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the gift.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, wires[0])
}

// POST /api/subscriptions/gifts/redeem
func (s *Subscriptions) redeemGiftHTTP(c *gin.Context) {
	user, _, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	var request redeemGiftRequest
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.GiftCode) == "" {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
			"gift_code": {"The GiftCode field is required."},
		}))
		return
	}
	gift, subscription, err := s.RedeemGift(c.Request.Context(), user, request.GiftCode)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_GIFT_REDEEM_FAILED", err.Error(), http.StatusBadRequest))
		return
	}
	giftWires, err := s.giftWires(c.Request.Context(), []WalletGiftEntity{*gift})
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the gift.", http.StatusInternalServerError))
		return
	}
	subscriptionWires, err := s.subscriptionWires(c.Request.Context(), []WalletSubscriptionEntity{*subscription})
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the subscription.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, RedeemGiftResponse{Gift: giftWires[0], Subscription: subscriptionWires[0]})
}

// POST /api/subscriptions/gifts/:giftId/send
func (s *Subscriptions) sendGiftHTTP(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	giftID, err := uuid.Parse(c.Param("giftId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_GIFT_SEND_FAILED", "Gift not found or access denied.", http.StatusBadRequest))
		return
	}
	gift, err := s.MarkGiftAsSent(c.Request.Context(), giftID, accountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_GIFT_SEND_FAILED", err.Error(), http.StatusBadRequest))
		return
	}
	wires, err := s.giftWires(c.Request.Context(), []WalletGiftEntity{*gift})
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the gift.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, wires[0])
}

// POST /api/subscriptions/gifts/:giftId/cancel
func (s *Subscriptions) cancelGiftHTTP(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	giftID, err := uuid.Parse(c.Param("giftId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_GIFT_CANCEL_FAILED", "Gift not found or access denied.", http.StatusBadRequest))
		return
	}
	gift, err := s.CancelGift(c.Request.Context(), giftID, accountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_GIFT_CANCEL_FAILED", err.Error(), http.StatusBadRequest))
		return
	}
	wires, err := s.giftWires(c.Request.Context(), []WalletGiftEntity{*gift})
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the gift.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, wires[0])
}

// POST /api/subscriptions/gifts/:giftId/order
func (s *Subscriptions) createGiftOrderHTTP(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	giftID, err := uuid.Parse(c.Param("giftId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_GIFT_ORDER_CREATE_FAILED", "No matching gift found.", http.StatusBadRequest))
		return
	}
	order, err := s.CreateGiftOrder(c.Request.Context(), accountID, giftID)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("WALLET_GIFT_ORDER_CREATE_FAILED", err.Error(), http.StatusBadRequest))
		return
	}
	c.JSON(http.StatusOK, orderModel(order, nil))
}
