package accountrecordsctl

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/stargate/accountrecords"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/store"
)

func (d Deps) getPublicBadges(c *gin.Context) {
	account := d.resolvePublicAccount(c)
	if account == nil {
		return
	}
	id := accountUUID(account)
	badges, err := d.Store.ListBadges(c.Request.Context(), id)
	if err != nil {
		d.internalError(c, err)
		return
	}
	if badges == nil {
		badges = []accountrecords.Badge{}
	}
	c.JSON(http.StatusOK, badges)
}

func (d Deps) getPublicBoard(c *gin.Context) {
	account := d.resolvePublicAccount(c)
	if account == nil {
		return
	}
	id := accountUUID(account)
	items, err := d.Store.ListBoard(c.Request.Context(), id)
	if err != nil {
		d.internalError(c, err)
		return
	}
	if items == nil {
		items = []accountrecords.BoardItem{}
	}
	c.JSON(http.StatusOK, items)
}

func (d Deps) getPublicCredits(c *gin.Context) {
	account := d.resolvePublicAccount(c)
	if account == nil {
		return
	}
	id := accountUUID(account)
	credits, err := d.Store.EffectiveSocialCredit(c.Request.Context(), id)
	if err != nil {
		d.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, credits)
}

// resolvePublicAccount mirrors AccountService.LookupAccount: name first, then
// verified email/phone.
func (d Deps) resolvePublicAccount(c *gin.Context) *model.Account {
	name := c.Param("name")
	if d.Accounts == nil {
		d.internalError(c, errors.New("account store unavailable"))
		return nil
	}
	account, err := d.Accounts.LookupAccount(c.Request.Context(), name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, notFound(name))
			return nil
		}
		d.internalError(c, err)
		return nil
	}
	return account
}
