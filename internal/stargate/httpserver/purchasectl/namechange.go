package purchasectl

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"

	"src.solsynth.dev/sosys/stargate/ports"
	"src.solsynth.dev/sosys/stargate/purchases"
)

type purchaseNameChangeCardResponse struct {
	PurchaseID string            `json:"purchase_id"`
	OrderID    string            `json:"order_id"`
	Amount     purchases.Decimal `json:"amount"`
}

type useNameChangeCardRequest struct {
	Target   string     `json:"target"`
	TargetID *uuid.UUID `json:"target_id"`
	NewName  string     `json:"new_name"`
}

// purchaseNameChangeCard mirrors POST /api/accounts/me/name-change-card/order.
func (d Deps) purchaseNameChangeCard(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	purchase, err := d.Service.PurchaseNameChangeCard(c.Request.Context(), account.Id)
	if err != nil {
		switch {
		case errors.Is(err, purchases.ErrNameChangePurchasesDisabled), errors.Is(err, purchases.ErrPurchaseLimit):
			badRequest(c, "NAME_CHANGE_CARD_PURCHASE_DISALLOWED", err.Error())
		case errors.Is(err, ports.ErrUnavailable):
			serviceUnavailable(c, "Payments are not configured; name change cards are unavailable.")
		default:
			internalError(c, d.Log, err)
		}
		return
	}
	c.JSON(http.StatusOK, purchaseNameChangeCardResponse{
		PurchaseID: purchase.Id,
		OrderID:    purchase.OrderId,
		Amount:     purchase.Amount,
	})
}

// listNameChangeCards mirrors GET /api/accounts/me/name-change-card.
func (d Deps) listNameChangeCards(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	id, ok := accountIDOf(account)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	purchasesList, err := d.Service.ListNameChangeCardPurchases(c.Request.Context(), id)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, purchasesList)
}

// useNameChangeCard mirrors POST /api/accounts/me/name-change-card/use.
func (d Deps) useNameChangeCard(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	var request useNameChangeCardRequest
	if !bindJSON(c, &request) {
		return
	}
	if request.Target == "" {
		validation(c, "target")
		return
	}
	if request.NewName == "" {
		validation(c, "new_name")
		return
	}
	id, ok := accountIDOf(account)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	purchase, err := d.Service.UseNameChangeCard(c.Request.Context(), id, request.Target, request.TargetID, request.NewName)
	if err != nil {
		// InvalidOperationException/RpcException both map to this code in the
		// C# controller; the message is the exception detail.
		badRequest(c, "NAME_CHANGE_CARD_USE_FAILED", err.Error())
		return
	}
	c.JSON(http.StatusOK, purchase)
}
