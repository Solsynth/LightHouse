// Package domaintrustctl ports Passport's DomainTrustController HTTP surface:
// the /api/domain-blocks rule CRUD, the public validate/check endpoints and
// the validation metrics.
package domaintrustctl

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"

	"src.solsynth.dev/sosys/stargate/domaintrust"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/permission"
)

// Deps is the dependency set the controller needs.
type Deps struct {
	Service *domaintrust.Service
	Perm    middleware.PermissionChecker
	Log     *slog.Logger
}

// Register mounts /api/domain-blocks.
func Register(api *gin.RouterGroup, d Deps) {
	group := api.Group("/domain-blocks")
	group.GET("", middleware.RequireAuth(), d.listRules)
	group.GET("/metrics", middleware.RequireAuth(), d.listMetrics)
	group.GET("/metrics/:domain", middleware.RequireAuth(), d.getMetric)
	group.GET("/check", d.checkURL)
	group.POST("/validate", d.validateURL)
	group.POST("", middleware.RequireAuth(), d.createRule)
	group.GET("/:id", middleware.RequireAuth(), d.getRule)
	group.PATCH("/:id", middleware.AskPermission(d.Perm, permission.DomainTrustUpdate), d.updateRule)
	group.DELETE("/:id", middleware.AskPermission(d.Perm, permission.DomainTrustDelete), d.deleteRule)
}

// ─────────────────────────── rules ───────────────────────────

func (d Deps) listRules(c *gin.Context) {
	offset, limit := pagination(c)
	rows, total, err := d.Service.Store.ListBlocks(c.Request.Context(), offset, limit)
	if err != nil {
		d.internal(c, err)
		return
	}
	views := make([]domaintrust.BlockView, 0, len(rows))
	for i := range rows {
		views = append(views, blockView(&rows[i]))
	}
	c.Header("X-Total", strconv.FormatInt(total, 10))
	c.JSON(http.StatusOK, views)
}

func (d Deps) getRule(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	row, err := d.Service.Store.GetBlock(c.Request.Context(), id)
	if domaintrust.IsNotFound(err) {
		d.ruleNotFound(c)
		return
	}
	if err != nil {
		d.internal(c, err)
		return
	}
	c.JSON(http.StatusOK, blockView(row))
}

func (d Deps) createRule(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	var input domaintrust.RuleInput
	if !bindJSON(c, &input) {
		return
	}
	if input.DomainPattern == nil || strings.TrimSpace(*input.DomainPattern) == "" {
		validation(c, "domain_pattern", "The DomainPattern field is required.")
		return
	}
	if len(*input.DomainPattern) > 512 {
		validation(c, "domain_pattern", "The field DomainPattern must be a string with a maximum length of 512.")
		return
	}
	if input.Protocol != nil && len(*input.Protocol) > 16 {
		validation(c, "protocol", "The field Protocol must be a string with a maximum length of 16.")
		return
	}
	if input.Reason != nil && len(*input.Reason) > 256 {
		validation(c, "reason", "The field Reason must be a string with a maximum length of 256.")
		return
	}
	id, err := uuid.Parse(account.Id)
	if err != nil {
		d.internal(c, err)
		return
	}
	row, err := d.Service.CreateRule(c.Request.Context(), input, &id)
	if err != nil {
		d.internal(c, err)
		return
	}
	c.JSON(http.StatusOK, blockView(row))
}

func (d Deps) updateRule(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input domaintrust.RuleInput
	if !bindJSON(c, &input) {
		return
	}
	row, err := d.Service.UpdateRule(c.Request.Context(), id, input)
	if domaintrust.IsNotFound(err) {
		d.ruleNotFound(c)
		return
	}
	if err != nil {
		d.internal(c, err)
		return
	}
	c.JSON(http.StatusOK, blockView(row))
}

func (d Deps) deleteRule(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	deleted, err := d.Service.Store.DeleteBlock(c.Request.Context(), id)
	if err != nil {
		d.internal(c, err)
		return
	}
	if !deleted {
		d.ruleNotFound(c)
		return
	}
	c.Status(http.StatusNoContent)
}

// ─────────────────────────── validation ───────────────────────────

func (d Deps) validateURL(c *gin.Context) {
	var request struct {
		URL string `json:"url"`
	}
	if !bindJSON(c, &request) {
		return
	}
	if strings.TrimSpace(request.URL) == "" {
		validation(c, "url", "The Url field is required.")
		return
	}
	result, err := d.Service.ValidateURL(c.Request.Context(), request.URL)
	if err != nil {
		d.internal(c, err)
		return
	}
	d.Service.RecordValidation(request.URL, result)
	c.JSON(http.StatusOK, result)
}

func (d Deps) checkURL(c *gin.Context) {
	rawURL := strings.TrimSpace(c.Query("url"))
	result, err := d.Service.ValidateURL(c.Request.Context(), rawURL)
	if err != nil {
		d.internal(c, err)
		return
	}
	d.Service.RecordValidation(rawURL, result)
	c.JSON(http.StatusOK, gin.H{
		"is_allowed":   result.IsAllowed,
		"is_verified":  result.IsVerified,
		"block_reason": result.BlockReason,
	})
}

// ─────────────────────────── metrics ───────────────────────────

func (d Deps) listMetrics(c *gin.Context) {
	offset, limit := pagination(c)
	rows, total, err := d.Service.Store.ListMetrics(c.Request.Context(), offset, limit)
	if err != nil {
		d.internal(c, err)
		return
	}
	views := make([]domaintrust.MetricView, 0, len(rows))
	for i := range rows {
		views = append(views, metricView(&rows[i]))
	}
	c.Header("X-Total", strconv.FormatInt(total, 10))
	c.JSON(http.StatusOK, views)
}

func (d Deps) getMetric(c *gin.Context) {
	domain := strings.ToLower(strings.TrimSpace(c.Param("domain")))
	row, err := d.Service.Store.GetMetric(c.Request.Context(), domain)
	if domaintrust.IsNotFound(err) {
		c.JSON(http.StatusNotFound, errs.New("PASSPORT_DOMAIN_METRIC_NOT_FOUND",
			"Domain validation metric not found.", http.StatusNotFound))
		return
	}
	if err != nil {
		d.internal(c, err)
		return
	}
	c.JSON(http.StatusOK, metricView(row))
}

// ─────────────────────────── helpers ───────────────────────────

func currentAccount(c *gin.Context) *model.Account {
	account := middleware.CurrentUser(c.Request.Context())
	if account == nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return nil
	}
	return account
}

func parseID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		// The C# route constraint (:guid) does not match, so the request is a
		// 404 rather than a validation error.
		c.JSON(http.StatusNotFound, errs.New("NOT_FOUND", "The requested resource was not found.", http.StatusNotFound))
		return uuid.Nil, false
	}
	return id, true
}

func pagination(c *gin.Context) (offset, limit int) {
	offset, _ = strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ = strconv.Atoi(c.DefaultQuery("limit", "50"))
	if offset < 0 {
		offset = 0
	}
	if limit < 0 {
		limit = 0
	}
	return offset, limit
}

func (d Deps) ruleNotFound(c *gin.Context) {
	c.JSON(http.StatusNotFound, errs.New("PASSPORT_DOMAIN_RULE_NOT_FOUND",
		"Domain block rule not found.", http.StatusNotFound))
}

func (d Deps) internal(c *gin.Context, err error) {
	if d.Log != nil {
		d.Log.Error("domain trust request failed", "error", err)
	}
	c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Internal server error.", http.StatusInternalServerError))
}

func validation(c *gin.Context, field, message string) {
	c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{field: {message}}))
}

func bindJSON(c *gin.Context, target any) bool {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		validation(c, "body", "Invalid request body.")
		return false
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return true
	}
	if err := json.Unmarshal(body, target); err != nil {
		validation(c, "body", "Invalid request body.")
		return false
	}
	return true
}

// blockView wraps domaintrust.BlockView so the controller can be compiled as
// the single place that decides the wire shape.
func blockView(entity *domaintrust.DomainBlockEntity) domaintrust.BlockView {
	return domaintrust.BlockViewFrom(entity)
}

func metricView(entity *domaintrust.DomainValidationMetricEntity) domaintrust.MetricView {
	return domaintrust.MetricViewFrom(entity)
}
