package purchasectl

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	"src.solsynth.dev/sosys/stargate/ports"
	"src.solsynth.dev/sosys/stargate/purchases"
)

// ── DTOs ──────────────────────────────────────────────────────────────────

type createSpellRequest struct {
	Spell     *string `json:"spell"`
	MaxUsages *int    `json:"max_usages"`
	SkipTests *bool   `json:"skip_tests"`
}

type createAffiliationResultRequest struct {
	ResourceIdentifier string `json:"resource_identifier"`
}

type consumeRegistrationInviteRequest struct {
	Spell     string  `json:"spell"`
	AccountID *string `json:"account_id"`
}

type purchaseAffiliationSpellResponse struct {
	PurchaseID string            `json:"purchase_id"`
	OrderID    string            `json:"order_id"`
	Amount     purchases.Decimal `json:"amount"`
}

// activationRequirementState retains the C# ActivationRequirementState fields
// (the Go core has no examination subsystem, so the test list is empty) plus
// the consumed/skips_tests result the module contract exposes.
type activationRequirementState struct {
	Consumed               bool  `json:"consumed"`
	SkipsTests             bool  `json:"skips_tests"`
	IsActivated            bool  `json:"is_activated"`
	TestsEnabled           bool  `json:"tests_enabled"`
	TestsBypassed          bool  `json:"tests_bypassed"`
	RequireVerifiedContact bool  `json:"require_verified_contact"`
	HasVerifiedContact     bool  `json:"has_verified_contact"`
	Tests                  []any `json:"tests"`
}

// ── handlers ──────────────────────────────────────────────────────────────

// consumeInvite mirrors POST /api/affiliations/registration-invites/consume.
func (d Deps) consumeInvite(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	var request consumeRegistrationInviteRequest
	if !bindJSON(c, &request) {
		return
	}
	if strings.TrimSpace(request.Spell) == "" || len([]rune(request.Spell)) > 1024 {
		validation(c, "spell")
		return
	}
	if account.ActivatedAt != nil {
		badRequest(c, "PASSPORT_AFFILIATION_INVITE_ACCOUNT_ACTIVE",
			"Registration invitations can only be used by unactivated accounts.")
		return
	}
	currentID, ok := accountIDOf(account)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	targetID := currentID
	if request.AccountID != nil && strings.TrimSpace(*request.AccountID) != "" {
		parsed, err := uuid.Parse(strings.TrimSpace(*request.AccountID))
		if err != nil {
			validation(c, "account_id")
			return
		}
		if parsed != currentID && !account.IsSuperuser {
			c.JSON(http.StatusForbidden, errs.Forbidden("You do not have permission to perform this action."))
			return
		}
		targetID = parsed
	}
	consumed, skipsTests, err := d.Service.ConsumeRegistrationInvite(c.Request.Context(), request.Spell, targetID.String())
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if !consumed {
		badRequest(c, "PASSPORT_AFFILIATION_INVITE_INVALID",
			"The registration invitation is invalid, expired, or has no remaining uses.")
		return
	}
	c.JSON(http.StatusOK, activationRequirementState{
		Consumed:               true,
		SkipsTests:             skipsTests,
		IsActivated:            false,
		TestsBypassed:          skipsTests,
		RequireVerifiedContact: false,
		HasVerifiedContact:     false,
		Tests:                  []any{},
	})
}

// purchaseInvite mirrors POST /api/affiliations/purchase.
func (d Deps) purchaseInvite(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	purchase, err := d.Service.PurchaseRegistrationInvite(c.Request.Context(), account.Id)
	if err != nil {
		switch {
		case errors.Is(err, purchases.ErrAffiliationPurchasesDisabled), errors.Is(err, purchases.ErrPurchaseLimit):
			badRequest(c, "PASSPORT_AFFILIATION_PURCHASE_FAILED", err.Error())
		case errors.Is(err, ports.ErrUnavailable):
			serviceUnavailable(c, "Payments are not configured; affiliation invites are unavailable.")
		default:
			internalError(c, d.Log, err)
		}
		return
	}
	c.JSON(http.StatusOK, purchaseAffiliationSpellResponse{
		PurchaseID: purchase.Id,
		OrderID:    purchase.OrderId,
		Amount:     purchase.Amount,
	})
}

// createSpell mirrors POST /api/affiliations.
func (d Deps) createSpell(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	var request createSpellRequest
	if !bindJSON(c, &request) {
		return
	}
	word := ""
	if request.Spell != nil {
		word = *request.Spell
	}
	if len([]rune(word)) > 1024 {
		validation(c, "spell")
		return
	}
	if request.MaxUsages != nil && *request.MaxUsages < 1 {
		validation(c, "max_usages")
		return
	}
	skipTests := true
	if request.SkipTests != nil {
		skipTests = *request.SkipTests
	}
	id, ok := accountIDOf(account)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	spell, err := d.Service.CreateAffiliationSpell(c.Request.Context(), id, word, request.MaxUsages, skipTests)
	if err != nil {
		if errors.Is(err, purchases.ErrSpellTaken) {
			badRequest(c, "PASSPORT_AFFILIATION_SPELL_CREATE_FAILED", err.Error())
			return
		}
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, spell)
}

// recordResult mirrors POST /api/affiliations/{spell}/results.
func (d Deps) recordResult(c *gin.Context) {
	var request createAffiliationResultRequest
	if !bindJSON(c, &request) {
		return
	}
	if len([]rune(request.ResourceIdentifier)) > 8192 {
		validation(c, "resource_identifier")
		return
	}
	word := c.Param("spell")
	if decoded, err := url.PathUnescape(word); err == nil {
		word = decoded
	}
	result, err := d.Service.RecordAffiliationResult(c.Request.Context(), word, request.ResourceIdentifier)
	if err != nil {
		switch {
		case errors.Is(err, purchases.ErrInviteRestricted):
			badRequest(c, "PASSPORT_AFFILIATION_INVITE_USE_RESTRICTED",
				"Registration invitations can only be used during account creation.")
		case errors.Is(err, purchases.ErrSpellNotFound):
			badRequest(c, "PASSPORT_AFFILIATION_RESULT_RECORD_FAILED", err.Error())
		default:
			internalError(c, d.Log, err)
		}
		return
	}
	c.JSON(http.StatusOK, result)
}

// listSpells mirrors GET /api/affiliations.
func (d Deps) listSpells(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	id, ok := accountIDOf(account)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	orderBy := c.DefaultQuery("order", "date")
	take := parseInt(c.DefaultQuery("take", "10"), 10)
	offset := parseInt(c.DefaultQuery("offset", "0"), 0)
	spells, total, err := d.Service.ListAffiliationSpells(c.Request.Context(), id, orderBy,
		boolQuery(c, "desc", false), take, offset)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.Header("X-Total", strconv.Itoa(int(total)))
	c.JSON(http.StatusOK, spells)
}

// getSpell mirrors GET /api/affiliations/{id}.
func (d Deps) getSpell(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	id, ok := accountIDOf(account)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	spellID, ok := spellIDParam(c)
	if !ok {
		return
	}
	spell, err := d.Service.GetAffiliationSpell(c.Request.Context(), id, spellID)
	if err != nil {
		if errors.Is(err, purchases.ErrNotFound) {
			spellNotFound(c)
			return
		}
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, spell)
}

// listResults mirrors GET /api/affiliations/{id}/results.
func (d Deps) listResults(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	id, ok := accountIDOf(account)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	spellID, ok := spellIDParam(c)
	if !ok {
		return
	}
	take := parseInt(c.DefaultQuery("take", "10"), 10)
	offset := parseInt(c.DefaultQuery("offset", "0"), 0)
	results, total, err := d.Service.ListAffiliationResults(c.Request.Context(), id, spellID,
		boolQuery(c, "desc", false), take, offset)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.Header("X-Total", strconv.Itoa(int(total)))
	c.JSON(http.StatusOK, results)
}

// deleteSpell mirrors DELETE /api/affiliations/{id}.
func (d Deps) deleteSpell(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	id, ok := accountIDOf(account)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	spellID, ok := spellIDParam(c)
	if !ok {
		return
	}
	deleted, err := d.Service.DeleteAffiliationSpell(c.Request.Context(), id, spellID)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if !deleted {
		spellNotFound(c)
		return
	}
	c.Status(http.StatusOK)
}
