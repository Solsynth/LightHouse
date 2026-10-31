package accountrecordsctl

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	"src.solsynth.dev/sosys/stargate/accountrecords"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
)

// getActions ports GET /api/accounts/me/actions (Passport's own action-log
// page) and keeps the exact wire shape of GET /api/actions: model.ActionLog
// entries plus X-Total.
func (d Deps) getActions(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	accountID, ok := parseAccountID(c, account)
	if !ok {
		return
	}
	if d.Accounts == nil {
		d.internalError(c, errors.New("account store unavailable"))
		return
	}
	take, offset := pagination(c)
	logs, total, err := d.Accounts.AdminListOwnActionLogs(c.Request.Context(), accountID, "", take, offset)
	if err != nil {
		d.internalError(c, err)
		return
	}
	if logs == nil {
		logs = []model.ActionLog{}
	}
	c.Header("X-Total", strconv.Itoa(total))
	c.JSON(http.StatusOK, logs)
}

func (d Deps) getBadges(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	accountID, ok := parseAccountID(c, account)
	if !ok {
		return
	}
	badges, err := d.Store.ListBadges(c.Request.Context(), accountID)
	if err != nil {
		d.internalError(c, err)
		return
	}
	if badges == nil {
		badges = []accountrecords.Badge{}
	}
	c.JSON(http.StatusOK, badges)
}

func (d Deps) activateBadge(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	accountID, ok := parseAccountID(c, account)
	if !ok {
		return
	}
	badgeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("PASSPORT_BADGE_ACTIVATE_FAILED", "Invalid badge id."))
		return
	}
	if err := d.Store.ActivateBadge(c.Request.Context(), accountID, badgeID); err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("PASSPORT_BADGE_ACTIVATE_FAILED", err.Error()))
		return
	}
	c.Status(http.StatusOK)
}

func (d Deps) getLeveling(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	accountID, ok := parseAccountID(c, account)
	if !ok {
		return
	}
	take, offset := pagination(c)
	records, total, err := d.Store.ListExperience(c.Request.Context(), accountID, take, offset)
	if err != nil {
		d.internalError(c, err)
		return
	}
	if records == nil {
		records = []accountrecords.ExperienceRecord{}
	}
	c.Header("X-Total", strconv.Itoa(total))
	c.JSON(http.StatusOK, records)
}

func (d Deps) getCredits(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	accountID, ok := parseAccountID(c, account)
	if !ok {
		return
	}
	credits, err := d.Store.EffectiveSocialCredit(c.Request.Context(), accountID)
	if err != nil {
		d.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, credits)
}

func (d Deps) getCreditsHistory(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	accountID, ok := parseAccountID(c, account)
	if !ok {
		return
	}
	take, offset := pagination(c)
	records, total, err := d.Store.ListSocialCreditHistory(c.Request.Context(), accountID, take, offset)
	if err != nil {
		d.internalError(c, err)
		return
	}
	if records == nil {
		records = []accountrecords.SocialCreditRecord{}
	}
	c.Header("X-Total", strconv.Itoa(total))
	c.JSON(http.StatusOK, records)
}

func (d Deps) getConnections(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	if d.Accounts == nil {
		d.internalError(c, errors.New("account store unavailable"))
		return
	}
	accountID, ok := parseAccountID(c, account)
	if !ok {
		return
	}
	var provider *string
	if value := c.Query("provider"); value != "" {
		provider = &value
	}
	connections, err := d.Accounts.ListConnectionsWithTokens(c.Request.Context(), accountID, provider)
	if err != nil {
		d.internalError(c, err)
		return
	}
	// model.Connection already tags the tokens as json:"-"; clearing them here
	// is a second guard so no other serializer can leak them.
	for i := range connections {
		connections[i].AccessToken = ""
		connections[i].RefreshToken = ""
	}
	if connections == nil {
		connections = []model.Connection{}
	}
	c.JSON(http.StatusOK, connections)
}

func (d Deps) getBoard(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	accountID, ok := parseAccountID(c, account)
	if !ok {
		return
	}
	items, err := d.Store.ListBoard(c.Request.Context(), accountID)
	if err != nil {
		d.internalError(c, err)
		return
	}
	if items == nil {
		items = []accountrecords.BoardItem{}
	}
	c.JSON(http.StatusOK, items)
}

func (d Deps) replaceBoard(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	accountID, ok := parseAccountID(c, account)
	if !ok {
		return
	}
	var request []accountrecords.BoardItemRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("VALIDATION_ERROR", "The request data is invalid."))
		return
	}
	items, err := d.Store.ReplaceBoard(c.Request.Context(), accountID, boardInputs(request))
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

// ───────────────────────────── helpers ─────────────────────────────

func currentAccount(c *gin.Context) *model.Account {
	account := middleware.CurrentUser(c.Request.Context())
	if account == nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return nil
	}
	return account
}

func parseAccountID(c *gin.Context, account *model.Account) (uuid.UUID, bool) {
	id, err := uuid.Parse(account.Id)
	if err != nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return uuid.Nil, false
	}
	return id, true
}

// pagination reads take/offset with the C# defaults (take 20, offset 0).
func pagination(c *gin.Context) (take, offset int) {
	take = 20
	if raw := c.Query("take"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil {
			take = value
		}
	}
	if take < 1 {
		take = 20
	}
	if take > 200 {
		take = 200
	}
	offset = 0
	if raw := c.Query("offset"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			offset = value
		}
	}
	return take, offset
}

func boardInputs(request []accountrecords.BoardItemRequest) []accountrecords.BoardItemInput {
	items := make([]accountrecords.BoardItemInput, 0, len(request))
	for _, entry := range request {
		items = append(items, entry.Input())
	}
	return items
}

func accountUUID(account *model.Account) uuid.UUID {
	id, _ := uuid.Parse(account.Id)
	return id
}
