package accountrecordsctl

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/accountrecords"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/store"
)

type adminBadgeRequest struct {
	Type        string         `json:"type"`
	Label       *string        `json:"label"`
	Caption     *string        `json:"caption"`
	Meta        map[string]any `json:"meta"`
	ActivatedAt *time.Time     `json:"activated_at"`
	ExpiredAt   *time.Time     `json:"expired_at"`
}

type adminBoardPayloadRequest struct {
	Payload map[string]any `json:"payload"`
}

type verificationRequest struct {
	Type        int     `json:"type"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	VerifiedBy  *string `json:"verified_by"`
}

func (d Deps) adminGetBadges(c *gin.Context) {
	account, ok := d.lookupAdminAccount(c)
	if !ok {
		return
	}
	badges, err := d.Store.ListBadges(c.Request.Context(), accountUUID(account))
	if err != nil {
		d.internalError(c, err)
		return
	}
	if badges == nil {
		badges = []accountrecords.Badge{}
	}
	c.JSON(http.StatusOK, badges)
}

func (d Deps) adminGrantBadge(c *gin.Context) {
	account, ok := d.lookupAdminAccount(c)
	if !ok {
		return
	}
	var request adminBadgeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("VALIDATION_ERROR", "The request data is invalid."))
		return
	}
	badge, err := d.Store.GrantBadgeWithDetails(
		c.Request.Context(), accountUUID(account), request.Type, request.Label, request.Caption,
		request.Meta, request.ActivatedAt, request.ExpiredAt,
	)
	if err != nil {
		d.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, badge)
}

func (d Deps) adminActivateBadge(c *gin.Context) {
	account, ok := d.lookupAdminAccount(c)
	if !ok {
		return
	}
	badgeID, err := uuid.Parse(c.Param("badgeId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("PASSPORT_BADGE_ACTIVATE_FAILED", "Invalid badge id."))
		return
	}
	if err := d.Store.ActivateBadge(c.Request.Context(), accountUUID(account), badgeID); err != nil {
		if errors.Is(err, accountrecords.ErrBadgeNotFound) {
			c.JSON(http.StatusNotFound, errs.New("PASSPORT_BADGE_NOT_FOUND", err.Error(), http.StatusNotFound))
			return
		}
		c.JSON(http.StatusBadRequest, errs.BadRequest("PASSPORT_BADGE_ACTIVATE_FAILED", err.Error()))
		return
	}
	c.Status(http.StatusOK)
}

func (d Deps) adminRevokeBadge(c *gin.Context) {
	account, ok := d.lookupAdminAccount(c)
	if !ok {
		return
	}
	badgeID, err := uuid.Parse(c.Param("badgeId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("PASSPORT_BADGE_ACTIVATE_FAILED", "Invalid badge id."))
		return
	}
	if err := d.Store.RevokeBadge(c.Request.Context(), accountUUID(account), badgeID); err != nil {
		if errors.Is(err, accountrecords.ErrBadgeNotFound) {
			c.JSON(http.StatusNotFound, errs.New("PASSPORT_BADGE_NOT_FOUND", err.Error(), http.StatusNotFound))
			return
		}
		d.internalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (d Deps) adminGetBoard(c *gin.Context) {
	account, ok := d.lookupAdminAccount(c)
	if !ok {
		return
	}
	items, err := d.Store.ListBoard(c.Request.Context(), accountUUID(account))
	if err != nil {
		d.internalError(c, err)
		return
	}
	if items == nil {
		items = []accountrecords.BoardItem{}
	}
	c.JSON(http.StatusOK, items)
}

func (d Deps) adminReplaceBoard(c *gin.Context) {
	account, ok := d.lookupAdminAccount(c)
	if !ok {
		return
	}
	var request []accountrecords.BoardItemRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("VALIDATION_ERROR", "The request data is invalid."))
		return
	}
	items, err := d.Store.ReplaceBoard(c.Request.Context(), accountUUID(account), boardInputs(request))
	if err != nil {
		var validation *accountrecords.ValidationError
		if errors.As(err, &validation) {
			c.JSON(http.StatusBadRequest, errs.BadRequest("PASSPORT_BOARD_REPLACE_FAILED", validation.Message))
			return
		}
		d.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, items)
}

func (d Deps) adminUpdateBoardPayload(c *gin.Context) {
	account, ok := d.lookupAdminAccount(c)
	if !ok {
		return
	}
	itemID, err := uuid.Parse(c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusNotFound, errs.New("PASSPORT_BOARD_ITEM_NOT_FOUND", "Board item not found.", http.StatusNotFound))
		return
	}
	var request adminBoardPayloadRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("VALIDATION_ERROR", "The request data is invalid."))
		return
	}
	item, err := d.Store.AdminUpdateBoardItemPayload(c.Request.Context(), accountUUID(account), itemID, request.Payload)
	if err != nil {
		var validation *accountrecords.ValidationError
		switch {
		case errors.Is(err, accountrecords.ErrBoardItemNotFound):
			c.JSON(http.StatusNotFound, errs.New("PASSPORT_BOARD_ITEM_NOT_FOUND", err.Error(), http.StatusNotFound))
		case errors.As(err, &validation):
			c.JSON(http.StatusBadRequest, errs.BadRequest("PASSPORT_BOARD_UPDATE_FAILED", validation.Message))
		default:
			d.internalError(c, err)
		}
		return
	}
	c.JSON(http.StatusOK, item)
}

func (d Deps) adminDeleteBoardItem(c *gin.Context) {
	account, ok := d.lookupAdminAccount(c)
	if !ok {
		return
	}
	itemID, err := uuid.Parse(c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusNotFound, errs.New("PASSPORT_BOARD_ITEM_NOT_FOUND", "Board item not found.", http.StatusNotFound))
		return
	}
	if err := d.Store.DeleteBoardItem(c.Request.Context(), accountUUID(account), itemID); err != nil {
		if errors.Is(err, accountrecords.ErrBoardItemNotFound) {
			c.JSON(http.StatusNotFound, errs.New("PASSPORT_BOARD_ITEM_NOT_FOUND", err.Error(), http.StatusNotFound))
			return
		}
		d.internalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (d Deps) adminSetVerification(c *gin.Context) {
	account, ok := d.lookupAdminAccount(c)
	if !ok {
		return
	}
	var request verificationRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("VALIDATION_ERROR", "The request data is invalid."))
		return
	}
	profiles := d.profileStore()
	if profiles == nil {
		d.internalError(c, errors.New("profile store unavailable"))
		return
	}
	mark := &model.SnVerificationMark{
		Type:        request.Type,
		Title:       request.Title,
		Description: request.Description,
		VerifiedBy:  request.VerifiedBy,
	}
	if err := profiles.ApplyProfileFieldPatch(c.Request.Context(), accountUUID(account), &store.ProfileFieldPatch{
		HasVerification: true,
		Verification:    mark,
	}); err != nil {
		d.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, mark)
}

func (d Deps) adminClearVerification(c *gin.Context) {
	account, ok := d.lookupAdminAccount(c)
	if !ok {
		return
	}
	profiles := d.profileStore()
	if profiles == nil {
		d.internalError(c, errors.New("profile store unavailable"))
		return
	}
	if err := profiles.ApplyProfileFieldPatch(c.Request.Context(), accountUUID(account), &store.ProfileFieldPatch{
		HasVerification: true,
		Verification:    nil,
	}); err != nil {
		d.internalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (d Deps) adminInvalidateCredits(c *gin.Context) {
	if err := d.Store.InvalidateCreditCache(c.Request.Context()); err != nil {
		d.internalError(c, err)
		return
	}
	c.Status(http.StatusOK)
}

// profileStore is the account store used to patch account_profiles; the
// record store carries the same handle under Profiles.
func (d Deps) profileStore() *store.Store {
	if d.Store != nil && d.Store.Profiles != nil {
		return d.Store.Profiles
	}
	return d.Accounts
}
