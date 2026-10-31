// Package purchasectl mounts Passport's affiliation-spell and name-change-card
// HTTP surfaces: /api/affiliations and /api/accounts/me/name-change-card.
//
// Route paths, permissions, DTO field names and error codes match
// AffiliationSpellController and NameChangeCardController, with snake_case
// JSON like the C# service.
package purchasectl

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/permission"
	"src.solsynth.dev/sosys/stargate/purchases"
)

// Deps is the dependency set the purchase controllers need.
type Deps struct {
	Service *purchases.Service
	Perm    middleware.PermissionChecker
	Log     *slog.Logger
}

// Register mounts both surfaces on the /api group.
func Register(api *gin.RouterGroup, d Deps) {
	affiliations := api.Group("/affiliations")
	affiliations.POST("/registration-invites/consume", middleware.RequireAuth(), d.consumeInvite)
	affiliations.POST("/purchase", middleware.RequireAuth(), d.purchaseInvite)
	affiliations.POST("", middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.AffiliationsManage), d.createSpell)
	affiliations.POST("/:spell/results", middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.AffiliationsResultsManage), d.recordResult)
	affiliations.GET("", middleware.RequireAuth(), d.listSpells)
	affiliations.GET("/:id", middleware.RequireAuth(), d.getSpell)
	affiliations.GET("/:id/results", middleware.RequireAuth(), d.listResults)
	affiliations.DELETE("/:id", middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.AffiliationsManage), d.deleteSpell)

	cards := api.Group("/accounts/me/name-change-card", middleware.RequireAuth())
	cards.POST("/order", d.purchaseNameChangeCard)
	cards.GET("", d.listNameChangeCards)
	cards.POST("/use", d.useNameChangeCard)
}

// ── helpers ───────────────────────────────────────────────────────────────

func currentAccount(c *gin.Context) *model.Account {
	account := middleware.CurrentUser(c.Request.Context())
	if account == nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return nil
	}
	return account
}

func accountIDOf(account *model.Account) (uuid.UUID, bool) {
	id, err := uuid.Parse(account.Id)
	return id, err == nil
}

func badRequest(c *gin.Context, code, message string) {
	c.JSON(http.StatusBadRequest, errs.New(code, message, http.StatusBadRequest))
}

func serviceUnavailable(c *gin.Context, message string) {
	c.JSON(http.StatusServiceUnavailable, errs.New("SERVICE_UNAVAILABLE", message, http.StatusServiceUnavailable))
}

func internalError(c *gin.Context, log *slog.Logger, err error) {
	if log != nil {
		log.Error("purchase request failed", "error", err)
	}
	c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Internal server error.", http.StatusInternalServerError))
}

func validation(c *gin.Context, field string) {
	c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{field: {"The field is invalid."}}))
}

func spellNotFound(c *gin.Context) {
	c.JSON(http.StatusNotFound, errs.New("PASSPORT_AFFILIATION_SPELL_NOT_FOUND",
		"Affiliation spell not found.", http.StatusNotFound))
}

// bindJSON decodes the body; an empty body binds the zero value, matching
// ASP.NET's empty-body behavior for the DTOs.
func bindJSON(c *gin.Context, target any) bool {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Invalid request body."}}))
		return false
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return true
	}
	if err := json.Unmarshal(body, target); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Invalid request body."}}))
		return false
	}
	return true
}

func parseInt(raw string, fallback int) int {
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func boolQuery(c *gin.Context, name string, fallback bool) bool {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return value
}

func spellIDParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		spellNotFound(c)
		return uuid.Nil, false
	}
	return id, true
}
