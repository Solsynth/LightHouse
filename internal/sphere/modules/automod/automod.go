// Package automod ports DysonNetwork.Sphere/Automod: the automod_rules table,
// the management API and the matching engine. The posts pipeline can reuse the
// engine through the package-level Evaluate function.
package automod

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/config"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// ModuleID is the automod module's registry ID.
const ModuleID = "app.solian.sphere.automod"

// PermissionRulesManage gates rule creation and updates, mirroring
// PermissionKeys.AutomodRulesManage.
const PermissionRulesManage = "automod.rules.manage"

// Factory returns the automod module factory.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Models:         []any{store.AutomodRuleEntity{}},
		SQL: []module.SchemaHook{module.DDL(
			`CREATE UNIQUE INDEX IF NOT EXISTS ix_automod_rules_name_deleted_at
			 ON automod_rules (name, deleted_at)`,
		)},
		Capabilities: []string{"automod.rules", "automod.evaluate"},
		New:          func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	core *core.Services
	db   *gorm.DB
	log  *slog.Logger
}

func (i *instance) Configure(ctx context.Context, host *module.Host[core.Services]) error {
	i.core = host.Core
	i.db = host.Core.DB
	i.log = host.Log

	api := host.API
	api.GET("/automod/rules", i.listRules)
	api.GET("/automod/rules/:id", i.getRule)
	api.POST("/automod/rules", i.permission(PermissionRulesManage), i.createRule)
	api.PUT("/automod/rules/:id", i.permission(PermissionRulesManage), i.updateRule)
	api.DELETE("/automod/rules/:id", i.deleteRule)
	api.POST("/automod/rules/bulk", i.bulkUpsert)
	api.POST("/automod/rules/:id/test", i.testRule)

	if host.Core.Config != nil {
		if err := i.seedRules(ctx, host.Core.Config.Automod.Rules); err != nil {
			return err
		}
	}
	return i.refreshRules(ctx)
}

func (i *instance) Stop(context.Context) error { return nil }

// permission returns the permission gate for a rule-management route. A
// missing auth service fails closed, matching RequirePermission.
func (i *instance) permission(key string) gin.HandlerFunc {
	if i.core.Auth == nil {
		return func(c *gin.Context) {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable,
				errs.New("SERVICE_UNAVAILABLE", "The permission service is unavailable.", http.StatusServiceUnavailable))
		}
	}
	return i.core.Auth.RequirePermission(key)
}

// seedRules upserts the operator's configuration rules by name. Existing rows
// are updated in place; rows removed from the configuration are never deleted.
func (i *instance) seedRules(ctx context.Context, seeds []config.AutomodRuleSeed) error {
	if len(seeds) == 0 {
		return nil
	}
	return i.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		for _, seed := range seeds {
			name := strings.TrimSpace(seed.Name)
			if name == "" {
				continue
			}
			ruleType := ParseType(seed.Type)
			action := ParseAction(seed.Action)

			var existing store.AutomodRuleEntity
			err := tx.Where("LOWER(name) = ?", strings.ToLower(name)).First(&existing).Error
			switch {
			case errors.Is(err, gorm.ErrRecordNotFound):
				row := store.AutomodRuleEntity{
					ID:            uuid.New(),
					Name:          name,
					Description:   optionalString(seed.Description),
					Type:          ruleType,
					DefaultAction: action,
					Pattern:       seed.Pattern,
					IsRegex:       seed.IsRegex,
					DerankWeight:  seed.DerankWeight,
					IsEnabled:     seed.IsEnabled,
					Priority:      seed.Priority,
				}
				row.CreatedAt = now
				row.UpdatedAt = now
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
			case err != nil:
				return err
			default:
				existing.Description = optionalString(seed.Description)
				existing.Type = ruleType
				existing.DefaultAction = action
				existing.Pattern = seed.Pattern
				existing.IsRegex = seed.IsRegex
				existing.DerankWeight = seed.DerankWeight
				existing.IsEnabled = seed.IsEnabled
				existing.Priority = seed.Priority
				existing.UpdatedAt = now
				if err := tx.Save(&existing).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// refreshRules reloads the enabled, unexpired rules into the package snapshot
// that Evaluate reads.
func (i *instance) refreshRules(ctx context.Context) error {
	var rows []store.AutomodRuleEntity
	if err := i.db.WithContext(ctx).
		Where("is_enabled = ?", true).
		Where("expires_at IS NULL OR expires_at > ?", time.Now().UTC()).
		Order("priority ASC").
		Find(&rows).Error; err != nil {
		return err
	}
	rules := make([]Rule, 0, len(rows))
	for idx := range rows {
		rules = append(rules, compileRule(&rows[idx]))
	}
	SetRules(rules)
	return nil
}

func (i *instance) listRules(c *gin.Context) {
	query := i.db.WithContext(c.Request.Context()).Model(&store.AutomodRuleEntity{})
	if raw, ok := c.GetQuery("enabledOnly"); ok {
		if enabledOnly, err := strconv.ParseBool(raw); err == nil && enabledOnly {
			query = query.Where("is_enabled = ?", true).
				Where("expires_at IS NULL OR expires_at > ?", time.Now().UTC())
		}
	}

	var rows []store.AutomodRuleEntity
	if err := query.Order("priority ASC").Find(&rows).Error; err != nil {
		i.log.Error("list automod rules", "error", err)
		httpx.Internal(c)
		return
	}
	items := make([]model.AutomodRule, 0, len(rows))
	for idx := range rows {
		items = append(items, ruleToModel(&rows[idx]))
	}
	httpx.List(c, items, int64(len(items)))
}

func (i *instance) getRule(c *gin.Context) {
	row, ok := i.findRule(c)
	if !ok {
		return
	}
	httpx.OK(c, ruleToModel(row))
}

func (i *instance) createRule(c *gin.Context) {
	var request createRuleRequest
	if !httpx.BindJSON(c, &request) {
		return
	}

	now := time.Now().UTC()
	row := store.AutomodRuleEntity{
		ID:            uuid.New(),
		Name:          request.Name,
		Description:   request.Description,
		Type:          request.typeValue(),
		DefaultAction: request.actionValue(),
		Pattern:       request.Pattern,
		IsRegex:       request.regexValue(),
		DerankWeight:  request.derankWeightValue(),
		IsEnabled:     request.enabledValue(),
		Priority:      request.priorityValue(),
	}
	row.CreatedAt = now
	row.UpdatedAt = now

	if err := i.db.WithContext(c.Request.Context()).Create(&row).Error; err != nil {
		i.log.Error("create automod rule", "error", err)
		httpx.Internal(c)
		return
	}
	i.invalidate(c.Request.Context())

	c.Header("Location", "/api/automod/rules/"+row.ID.String())
	httpx.Created(c, ruleToModel(&row))
}

func (i *instance) updateRule(c *gin.Context) {
	row, ok := i.findRule(c)
	if !ok {
		return
	}

	var request updateRuleRequest
	if !httpx.BindJSON(c, &request) {
		return
	}

	if request.Name != nil && *request.Name != "" {
		row.Name = *request.Name
	}
	if request.Description != nil {
		row.Description = request.Description
	}
	if request.Type != nil {
		row.Type = *request.Type
	}
	if request.DefaultAction != nil {
		row.DefaultAction = *request.DefaultAction
	}
	if request.Pattern != nil && *request.Pattern != "" {
		row.Pattern = *request.Pattern
	}
	if request.IsRegex != nil {
		row.IsRegex = *request.IsRegex
	}
	if request.DerankWeight != nil {
		row.DerankWeight = *request.DerankWeight
	}
	if request.IsEnabled != nil {
		row.IsEnabled = *request.IsEnabled
	}
	if request.Priority != nil {
		row.Priority = *request.Priority
	}
	row.UpdatedAt = time.Now().UTC()

	if err := i.db.WithContext(c.Request.Context()).Save(row).Error; err != nil {
		i.log.Error("update automod rule", "error", err)
		httpx.Internal(c)
		return
	}
	i.invalidate(c.Request.Context())
	httpx.OK(c, ruleToModel(row))
}

func (i *instance) deleteRule(c *gin.Context) {
	row, ok := i.findRule(c)
	if !ok {
		return
	}
	if err := i.db.WithContext(c.Request.Context()).Delete(row).Error; err != nil {
		i.log.Error("delete automod rule", "error", err)
		httpx.Internal(c)
		return
	}
	i.invalidate(c.Request.Context())
	c.Status(http.StatusNoContent)
}

func (i *instance) bulkUpsert(c *gin.Context) {
	var requests []bulkRuleRequest
	if !httpx.BindJSON(c, &requests) {
		return
	}

	ctx := c.Request.Context()
	results := make([]model.AutomodRule, 0, len(requests))
	err := i.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		for _, request := range requests {
			name := strings.TrimSpace(request.Name)
			if name == "" {
				continue
			}

			var existing store.AutomodRuleEntity
			err := tx.Where("LOWER(name) = ?", strings.ToLower(name)).First(&existing).Error
			switch {
			case errors.Is(err, gorm.ErrRecordNotFound):
				row := store.AutomodRuleEntity{
					ID:            uuid.New(),
					Name:          request.Name,
					Description:   request.Description,
					Type:          request.typeValue(),
					DefaultAction: request.actionValue(),
					Pattern:       request.Pattern,
					IsRegex:       request.regexValue(),
					DerankWeight:  request.derankWeightValue(),
					IsEnabled:     request.enabledValue(),
					Priority:      request.priorityValue(),
				}
				row.CreatedAt = now
				row.UpdatedAt = now
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
				results = append(results, ruleToModel(&row))
			case err != nil:
				return err
			default:
				existing.Description = request.Description
				existing.Type = request.typeValue()
				existing.DefaultAction = request.actionValue()
				existing.Pattern = request.Pattern
				existing.IsRegex = request.regexValue()
				existing.DerankWeight = request.derankWeightValue()
				existing.IsEnabled = request.enabledValue()
				existing.Priority = request.priorityValue()
				existing.UpdatedAt = now
				if err := tx.Save(&existing).Error; err != nil {
					return err
				}
				results = append(results, ruleToModel(&existing))
			}
		}
		return nil
	})
	if err != nil {
		i.log.Error("bulk upsert automod rules", "error", err)
		httpx.Internal(c)
		return
	}

	i.invalidate(ctx)
	httpx.OK(c, results)
}

func (i *instance) testRule(c *gin.Context) {
	row, ok := i.findRule(c)
	if !ok {
		return
	}

	var request testRuleRequest
	if !httpx.BindJSON(c, &request) {
		return
	}

	content := ""
	if request.Content != nil {
		content = *request.Content
	}

	rule := compileRule(row)
	results := make([]model.AutomodRuleResult, 0, 1)
	if matched, ok := matchRule(rule, content); ok {
		text := matched
		results = append(results, model.AutomodRuleResult{
			RuleID:       rule.ID,
			RuleName:     rule.Name,
			Type:         rule.Type,
			Action:       rule.Action,
			DerankWeight: rule.DerankWeight,
			MatchedText:  &text,
		})
	}
	httpx.List(c, results, int64(len(results)))
}

// findRule loads the rule named by the :id path parameter, writing the C#
// AUTOMOD_RULE_NOT_FOUND body when it does not exist.
func (i *instance) findRule(c *gin.Context) (*store.AutomodRuleEntity, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		ruleNotFound(c)
		return nil, false
	}
	var row store.AutomodRuleEntity
	if err := i.db.WithContext(c.Request.Context()).Where("id = ?", id).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			ruleNotFound(c)
			return nil, false
		}
		i.log.Error("load automod rule", "error", err)
		httpx.Internal(c)
		return nil, false
	}
	return &row, true
}

// invalidate reloads the snapshot after a mutation; a failed reload only
// leaves the previous rule set active.
func (i *instance) invalidate(ctx context.Context) {
	if err := i.refreshRules(ctx); err != nil && i.log != nil {
		i.log.Warn("reload automod rules", "error", err)
	}
}

func ruleNotFound(c *gin.Context) {
	httpx.Fail(c, errs.New("AUTOMOD_RULE_NOT_FOUND", "Rule not found.", http.StatusNotFound))
}

func ruleToModel(entity *store.AutomodRuleEntity) model.AutomodRule {
	return model.AutomodRule{
		ID:            entity.ID.String(),
		Name:          entity.Name,
		Description:   entity.Description,
		Type:          entity.Type,
		DefaultAction: entity.DefaultAction,
		Pattern:       entity.Pattern,
		IsRegex:       entity.IsRegex,
		DerankWeight:  entity.DerankWeight,
		IsEnabled:     entity.IsEnabled,
		ExpiresAt:     store.TimePtr(entity.ExpiresAt),
		Priority:      entity.Priority,
		CreatedAt:     model.NewTime(entity.CreatedAt),
		UpdatedAt:     model.NewTime(entity.UpdatedAt),
		DeletedAt:     store.DeletedPtr(entity.DeletedAt),
	}
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// createRuleRequest mirrors CreateAutomodRuleRequest. Optional scalars are
// pointers so an absent field keeps the C# property default (DerankWeight 10,
// IsEnabled true) instead of collapsing to the Go zero value.
type createRuleRequest struct {
	Name          string  `json:"name"`
	Description   *string `json:"description"`
	Type          *int    `json:"type"`
	DefaultAction *int    `json:"default_action"`
	Pattern       string  `json:"pattern"`
	IsRegex       *bool   `json:"is_regex"`
	DerankWeight  *int    `json:"derank_weight"`
	IsEnabled     *bool   `json:"is_enabled"`
	Priority      *int    `json:"priority"`
}

func (r createRuleRequest) typeValue() int {
	if r.Type == nil {
		return 0
	}
	return *r.Type
}

func (r createRuleRequest) actionValue() int {
	if r.DefaultAction == nil {
		return 0
	}
	return *r.DefaultAction
}

func (r createRuleRequest) regexValue() bool {
	return r.IsRegex != nil && *r.IsRegex
}

func (r createRuleRequest) derankWeightValue() int {
	if r.DerankWeight == nil {
		return 10
	}
	return *r.DerankWeight
}

func (r createRuleRequest) enabledValue() bool {
	if r.IsEnabled == nil {
		return true
	}
	return *r.IsEnabled
}

func (r createRuleRequest) priorityValue() int {
	if r.Priority == nil {
		return 0
	}
	return *r.Priority
}

// updateRuleRequest mirrors UpdateAutomodRuleRequest: every field is optional
// and a nil field means "leave unchanged".
type updateRuleRequest struct {
	Name          *string `json:"name"`
	Description   *string `json:"description"`
	Type          *int    `json:"type"`
	DefaultAction *int    `json:"default_action"`
	Pattern       *string `json:"pattern"`
	IsRegex       *bool   `json:"is_regex"`
	DerankWeight  *int    `json:"derank_weight"`
	IsEnabled     *bool   `json:"is_enabled"`
	Priority      *int    `json:"priority"`
}

// bulkRuleRequest mirrors AutomodRuleDto.
type bulkRuleRequest struct {
	Name          string  `json:"name"`
	Description   *string `json:"description"`
	Type          *int    `json:"type"`
	DefaultAction *int    `json:"default_action"`
	Pattern       string  `json:"pattern"`
	IsRegex       *bool   `json:"is_regex"`
	DerankWeight  *int    `json:"derank_weight"`
	IsEnabled     *bool   `json:"is_enabled"`
	Priority      *int    `json:"priority"`
}

func (r bulkRuleRequest) typeValue() int {
	return createRuleRequest{Type: r.Type}.typeValue()
}

func (r bulkRuleRequest) actionValue() int {
	return createRuleRequest{DefaultAction: r.DefaultAction}.actionValue()
}

func (r bulkRuleRequest) regexValue() bool {
	return createRuleRequest{IsRegex: r.IsRegex}.regexValue()
}

func (r bulkRuleRequest) derankWeightValue() int {
	return createRuleRequest{DerankWeight: r.DerankWeight}.derankWeightValue()
}

func (r bulkRuleRequest) enabledValue() bool {
	return createRuleRequest{IsEnabled: r.IsEnabled}.enabledValue()
}

func (r bulkRuleRequest) priorityValue() int {
	return createRuleRequest{Priority: r.Priority}.priorityValue()
}

// testRuleRequest mirrors TestAutomodRequest.
type testRuleRequest struct {
	Content *string `json:"content"`
}
